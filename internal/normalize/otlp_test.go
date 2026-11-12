package normalize

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"factorybench/internal/intake"
	"factorybench/internal/store"
)

// otlpDB posts the Phase 0 captures over /v1/logs as factory fa, each with its attempt started (and finished
// if finished is set), plus contract events, then normalizes.
func otlpDB(t *testing.T, finished map[string]string, contract ...string) *sql.DB {
	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.AddFactory(db, "fa", "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(intake.LogsHandler(db, nil))
	defer srv.Close()
	ev := func(id, typ, attempt, ts, payload string) {
		raw := fmt.Sprintf(`{"spec":"factorybench/v1","id":%q,"type":%q,"ts":%q,"attempt_id":%q,"payload":%s}`, id, typ, ts, attempt, payload)
		if _, err := store.InsertEvent(db, "contract", "fa", id, typ, ts, raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i, run := range []string{"subagent", "killed-sigterm", "killed-sigkill"} {
		attempt := "cc-phase0-" + run
		ev(attempt+":start", "attempt.started", attempt, "2026-11-12T06:00:00.000Z",
			fmt.Sprintf(`{"work_item":{"repo":"acme/app","issue":%d},"factory_config_version":"v1"}`, i+1))
		if st := finished[run]; st != "" {
			ev(attempt+":end", "attempt.finished", attempt, "2026-11-12T07:00:00.000Z", `{"status":"`+st+`"}`)
		}
		b, err := os.ReadFile("../../testdata/captured/claude-code-2.1.322-otlp/" + run + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Requests []struct{ Body json.RawMessage }
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		for _, r := range c.Requests {
			req, _ := http.NewRequest("POST", srv.URL, bytes.NewReader(r.Body))
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("%s: %v %v", run, resp.Status, err)
			}
			resp.Body.Close()
		}
	}
	for i, p := range contract {
		ev(fmt.Sprint("c:", i), "activity", "cc-phase0-killed-sigkill", "2026-11-12T06:30:00.000Z", p)
	}
	if err := Run(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOTLP(t *testing.T) {
	db := otlpDB(t, map[string]string{"subagent": "completed", "killed-sigterm": "failed"},
		`{"tool":"run_command","failed":true,"duration_ms":7,"agent_id":"x1"}`)

	// usage matches the run's final result.modelUsage exactly, subagent requests included
	var in, out, cr, cw int64
	var cost float64
	if err := db.QueryRow(`SELECT sum(input), sum(output), sum(cache_read), sum(cache_write), sum(cost_usd) FROM usage
		WHERE attempt_id = 'cc-phase0-subagent' AND cost_provenance = 'reported'`).Scan(&in, &out, &cr, &cw, &cost); err != nil {
		t.Fatal(err)
	}
	if in != 1338 || out != 2629 || cr != 166488 || cw != 36410 || math.Abs(cost-0.009003855) > 1e-12 {
		t.Errorf("usage %d %d %d %d %v", in, out, cr, cw, cost)
	}
	var sub int
	db.QueryRow(`SELECT count(*) FROM usage WHERE agent_id = 'a78dbae5c3a9cb7ea'`).Scan(&sub)
	if sub != 2 {
		t.Errorf("subagent usage rows %d, want 2", sub)
	}

	runs := map[string]string{}
	store.Each(db, `SELECT agent_id, coalesce(parent_agent_id, '-'), agent_type, is_async, model, tool_calls, coalesce(duration_ms, -1), status
		FROM agent_runs`, func(r *sql.Rows) error {
		var id, parent, typ, model, status string
		var async sql.NullBool
		var tools, dur int64
		err := r.Scan(&id, &parent, &typ, &async, &model, &tools, &dur, &status)
		runs[id] = fmt.Sprint(parent, typ, async.Valid && async.Bool, model, tools, dur, status)
		return err
	})
	want := map[string]string{
		"a78dbae5c3a9cb7ea": fmt.Sprint("-", "general-purpose", true, "claude-haiku-5-5", 1, 4630, "completed"),
		"a659980bca3fb01e4": fmt.Sprint("-", "general-purpose", false, "claude-haiku-5-5", 5, -1, "killed"), // SIGTERM
		"a3bea183083d509c1": fmt.Sprint("a659980bca3fb01e4", "general-purpose", false, "claude-haiku-5-5", 1, 2852, "completed"),
	}
	for id, w := range want {
		if runs[id] != w {
			t.Errorf("agent_runs %s = %q, want %q", id, runs[id], w)
		}
	}

	var failed string
	db.QueryRow(`SELECT tool_name || ':' || success || ':' || duration_ms || ':' || error_type FROM tool_calls
		WHERE attempt_id = 'cc-phase0-subagent' AND success = 0`).Scan(&failed)
	if failed != "Bash:0:3852:ShellError" {
		t.Errorf("failed tool call %q", failed)
	}
	var calls int
	db.QueryRow(`SELECT count(*) FROM tool_calls WHERE attempt_id = 'cc-phase0-subagent'`).Scan(&calls)
	if calls != 7 {
		t.Errorf("tool calls %d, want 7", calls)
	}

	// a contract activity with a tool lands in the same tables
	var contractCall string
	db.QueryRow(`SELECT c.tool_name || ':' || c.success || ':' || c.duration_ms || ':' || r.status FROM tool_calls c
		JOIN agent_runs r USING (factory_id, attempt_id, agent_id) WHERE c.agent_id = 'x1'`).Scan(&contractCall)
	if contractCall != "run_command:0:7:running" {
		t.Errorf("contract tool call %q", contractCall)
	}

	// replay rebuilds identical derived tables
	d1, err := dump(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(db); err != nil {
		t.Fatal(err)
	}
	d2, _ := dump(db)
	j1, _ := json.Marshal(d1)
	j2, _ := json.Marshal(d2)
	if !bytes.Equal(j1, j2) {
		t.Error("replay changed derived tables")
	}
}

// A subagent of an attempt that hasn't finished is running, not killed.
func TestOTLPRunning(t *testing.T) {
	db := otlpDB(t, nil)
	var st string
	db.QueryRow(`SELECT status FROM agent_runs WHERE agent_id = 'a659980bca3fb01e4'`).Scan(&st)
	if st != "running" {
		t.Errorf("status %q, want running", st)
	}
}

// api_error has no capture yet: numbers may arrive as strings and fields may be missing.
func TestOTLPAPIError(t *testing.T) {
	d := derive([]event{
		{Source: "contract", Factory: "f", ID: "1", Type: "attempt.started", TS: "2026-11-12T06:00:00.000Z", AttemptID: "a",
			Payload: []byte(`{"work_item":{"repo":"o/r","issue":1},"factory_config_version":"v"}`)},
		{Source: "otlp", Factory: "f", ID: "s:1", Type: "otlp.api_error", TS: "2026-11-12T06:00:01.000Z", AttemptID: "a",
			Payload: []byte(`{"model":"m","status_code":"429","error":"rate_limit_error","attempt_id":"a"}`)},
		{Source: "otlp", Factory: "f", ID: "s:2", Type: "otlp.api_retries_exhausted", TS: "2026-11-12T06:00:02.000Z", AttemptID: "a",
			Payload: []byte(`{"attempt_id":"a"}`)},
	}, nil, nil)
	if len(d.APIErrors) != 2 || *d.APIErrors[0].Status != 429 || *d.APIErrors[0].Error != "rate_limit_error" ||
		d.APIErrors[1].Kind != "api_retries_exhausted" || d.APIErrors[1].Status != nil {
		t.Fatalf("%+v", d.APIErrors)
	}
}
