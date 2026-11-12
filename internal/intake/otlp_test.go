package intake

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factorybench/internal/store"
)

// capture is one Phase 0 file: raw OTLP bodies as Claude Code sent them; attempts > 1 means byte-identical retries.
type capture struct {
	Requests []struct {
		Attempts []int           `json:"attempts"`
		Body     json.RawMessage `json:"body"`
	} `json:"requests"`
}

func loadCapture(t *testing.T, name string) capture {
	b, err := os.ReadFile(filepath.Join("../../testdata/captured/claude-code-2.1.322-otlp", name))
	if err != nil {
		t.Fatal(err)
	}
	var c capture
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func logsServer(t *testing.T) (*sql.DB, string, func(token, body string) (int, string)) {
	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.AddFactory(db, "fa", "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(LogsHandler(db, nil))
	t.Cleanup(srv.Close)
	return db, tok, func(token, body string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(rb))
	}
}

func TestLogsCaptures(t *testing.T) {
	db, tok, post := logsServer(t)
	records := 0
	for _, f := range []string{"subagent.json", "killed-sigterm.json", "killed-sigkill.json", "endpoint-down.json"} {
		for _, r := range loadCapture(t, f).Requests {
			body := r.Body
			records += strings.Count(string(body), `"event.name"`)
			for range max(1, len(r.Attempts)) {
				if code, resp := post(tok, string(body)); code != 200 || resp != "{}" {
					t.Fatalf("%s: %d %s", f, code, resp)
				}
			}
		}
	}
	var n, bad int
	db.QueryRow(`SELECT count(*), sum(source != 'otlp' OR factory_id != 'fa' OR type NOT LIKE 'otlp.%' OR id NOT LIKE '%-%:%'
		OR json_extract(payload, '$.attempt_id') NOT LIKE 'cc-phase0-%') FROM events`).Scan(&n, &bad)
	if n != records || bad != 0 { // endpoint-down retries are stored once
		t.Fatalf("stored %d events (%d malformed), want %d", n, bad, records)
	}
	// none of the personal data or content attributes reach the events table
	rows, err := db.Query(`SELECT payload FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		rows.Scan(&p)
		for _, s := range []string{`"user.email"`, `"user.account_uuid"`, `"user.account_id"`, `"user.id"`, `"organization.id"`,
			`"prompt"`, `"prompt_text"`, `"response"`, `"anthropic.custom_base_url"`, "@example.com", "<REDACTED>"} {
			if strings.Contains(p, s) {
				t.Fatalf("stored %s: %s", s, p)
			}
		}
	}
}

func TestLogsRejects(t *testing.T) {
	db, tok, post := logsServer(t)
	rec := func(attempt string) string {
		a := `{"key":"session.id","value":{"stringValue":"s"}},{"key":"event.sequence","value":{"intValue":"1"}},` +
			`{"key":"event.name","value":{"stringValue":"tool_result"}}`
		if attempt != "" {
			a += `,{"key":"factorybench.attempt","value":{"stringValue":"` + attempt + `"}}`
		}
		return `{"timeUnixNano":"1794464593795000000","attributes":[` + a + `]}`
	}
	body := `{"resourceLogs":[{"resource":{"attributes":[]},"scopeLogs":[{"logRecords":[` + rec("") + `,` + rec("a1") + `]}]}]}`
	if code, _ := post("fbt_wrong", body); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	if code, _ := post(tok, "not json"); code != 400 {
		t.Fatalf("bad body: %d", code)
	}
	code, resp := post(tok, body)
	if code != 200 || !strings.Contains(resp, `"rejectedLogRecords":1`) {
		t.Fatalf("missing attempt: %d %s", code, resp)
	}
	var id, ts string
	if err := db.QueryRow(`SELECT id, ts FROM events`).Scan(&id, &ts); err != nil || id != "s:1" || ts != "2026-11-12T06:23:13.795Z" {
		t.Fatalf("stored %s %s %v, want only s:1", id, ts, err)
	}
}
