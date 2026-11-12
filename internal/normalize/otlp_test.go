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
	"strings"
	"testing"
	"time"

	"factorybench/internal/intake"
	"factorybench/internal/report"
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

// Subagent runs, tool calls and replay are pinned by the otlp_subagents golden case; this checks usage against the run's result.
func TestOTLP(t *testing.T) {
	db := otlpDB(t, map[string]string{"subagent": "completed"},
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
	// a contract activity with a tool lands in the same tables
	var contractCall string
	db.QueryRow(`SELECT c.tool_name || ':' || c.success || ':' || c.duration_ms || ':' || r.status FROM tool_calls c
		JOIN agent_runs r USING (factory_id, attempt_id, agent_id) WHERE c.agent_id = 'x1'`).Scan(&contractCall)
	if contractCall != "run_command:0:7:running" {
		t.Errorf("contract tool call %q", contractCall)
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

// factorybench.phase from OTEL_RESOURCE_ATTRIBUTES survives intake and tags the usage row, so the scorecard
// gets verification tokens; a value outside the contract's phases leaves the row unphased.
func TestOTLPPhase(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.AddFactory(db, "fa", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range [][2]string{
		{"attempt.started", `{"work_item":{"repo":"acme/app","issue":1},"factory_config_version":"v1"}`},
		{"pr.linked", `{"pr_url":"https://github.com/acme/app/pull/1"}`},
	} {
		raw := fmt.Sprintf(`{"spec":"factorybench/v1","id":%q,"type":%q,"ts":"2026-11-12T06:00:00.000Z","attempt_id":"a","payload":%s}`, e[0], e[0], e[1])
		if _, err := store.InsertEvent(db, "contract", "fa", e[0], e[0], "2026-11-12T06:00:00.000Z", raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.InsertEvent(db, "github", "", "pr:1", "pr", "2026-11-12T06:10:00Z",
		`{"repo":"acme/app","pr":{"number":1,"html_url":"https://github.com/acme/app/pull/1","state":"open","head":{"ref":"b"}}}`, nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(intake.LogsHandler(db, nil))
	defer srv.Close()
	for i, phase := range []string{"review", "testing"} {
		kv := func(k, v string) string { return fmt.Sprintf(`{"key":%q,"value":{"stringValue":%q}}`, k, v) }
		body := fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[%s,%s]},"scopeLogs":[{"logRecords":[{"attributes":[%s,%s,%s,%s,%s,%s,%s]}]}]}]}`,
			kv("factorybench.attempt", "a"), kv("factorybench.phase", phase), kv("event.name", "api_request"), kv("session.id", "s"),
			kv("event.sequence", fmt.Sprint(i)), kv("event.timestamp", fmt.Sprintf("2026-11-12T06:0%d:00Z", i+1)),
			kv("model", "claude-sonnet-4-5"), kv("input_tokens", "100"), kv("output_tokens", "50"))
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v %v", resp.Status, err)
		}
		resp.Body.Close()
	}
	if err := Run(db); err != nil {
		t.Fatal(err)
	}
	var phases string
	db.QueryRow(`SELECT group_concat(coalesce(phase, '-'), ',') FROM (SELECT phase FROM usage ORDER BY ts)`).Scan(&phases)
	if phases != "review,-" {
		t.Errorf("phases %q, want review,-", phases)
	}
	var out strings.Builder
	if err := report.Factories(db, &out, false, time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "150 (150 / 1 PRs)") {
		t.Errorf("scorecard:\n%s", out.String())
	}
}
