package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factorybench/internal/store"
)

const sid = "b2053705-3194-4266-a97a-4f0a39bd5593"

// fixture: one running attempt with an unpriced session, one failed tool call and no human wait events.
func fixture(t *testing.T) (*server, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fb.db")
	rw, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ts := func(ago time.Duration) string { return store.FormatTS(now.Add(-ago)) }
	for _, q := range []string{
		`INSERT INTO factories VALUES ('f', 'h', NULL, NULL, 'x')`,
		`INSERT INTO work_items (repo, issue, title, first_seen, outcome) VALUES ('o/r', 7, '<b>t</b>', 'x', 'open')`,
		`INSERT INTO attempts (factory_id, attempt_id, repo, issue, config_version, state, started_at) VALUES ('f', 'a1', 'o/r', 7, 'v1', 'running', '` + ts(30*time.Minute) + `')`,
		`INSERT INTO usage (factory_id, attempt_id, model, ts, input, output, cost_usd, phase) VALUES ('f', 'a1', 'm', '` + ts(20*time.Minute) + `', 10, 5, 0.5, 'implement')`,
		`INSERT INTO usage (factory_id, attempt_id, model, ts, input, output) VALUES ('f', 'a1', 'm', '` + ts(19*time.Minute) + `', 1, 1)`,
		`INSERT INTO tool_calls VALUES ('f', 'a1', NULL, '` + ts(18*time.Minute) + `', 'Bash', 0, 12, 'ShellError')`,
		`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('contract', 'f', 'a1:started', 'attempt.started', '` + ts(25*time.Minute) + `', 'x', '{"attempt_id":"a1"}')`,
		`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('otlp', 'f', 'e1', 'otlp.api_request', '` + ts(20*time.Minute) + `', 'x', '{"factorybench.attempt":"a1","session.id":"` + sid + `","model":"m","query_source":"q1","cost_usd":0.5}')`,
		`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('otlp', 'f', 'e2', 'otlp.tool_decision', '` + ts(18*time.Minute) + `', 'x', '{"factorybench.attempt":"a1","session.id":"` + sid + `","tool_name":"T9","decision":"reject","source":"cfg"}')`,
	} {
		if _, err := rw.Exec(q); err != nil {
			t.Fatal(err, q)
		}
	}
	rw.Close()
	db, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	runs, tr := filepath.Join(dir, "runs"), filepath.Join(dir, "tr")
	os.MkdirAll(filepath.Join(runs, "2026-11-12-x-workers"), 0o755)
	os.MkdirAll(tr, 0o755)
	os.WriteFile(filepath.Join(runs, "2026-11-12-x.md"), []byte("[10:00:00] dispatch lane=build issue=#7 pid=1\n[10:05:00] dispatch lane=review issue=#7 pid=2\n[10:09:00] dispatch lane=build issue=#7 pid=3\n[10:10:00] reaped lock on #7\n[10:11:00] worker on #7 (pid=3) — card moved to 'Hold'; killing\n"), 0o644)
	os.WriteFile(filepath.Join(tr, sid+".jsonl"), []byte(`{"type":"user","timestamp":"t","message":{"content":"do it"}}
{"type":"assistant","timestamp":"t","message":{"content":[{"type":"thinking","thinking":"secret"},{"type":"text","text":"ok"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","timestamp":"t","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"file.go"}]}]}}
`), 0o644)
	o := Options{Addr: "127.0.0.1:8081", Runs: runs, Transcripts: tr, Idle: DefaultIdleTimeout}
	return &server{db: db, o: o, local: true}, Handler(db, o), dir
}

func get(t *testing.T, h http.Handler, url string, out any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req.Host = "127.0.0.1:8081"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if out != nil && w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v: %s", url, err, w.Body)
		}
	}
	return w
}

func TestEndpoints(t *testing.T) {
	_, h, _ := fixture(t)

	var live []map[string]any
	get(t, h, "/api/live", &live)
	if len(live) != 1 || live[0]["Status"] != "idle" || live[0]["Idle"] != true || live[0]["Phase"] != "implement" || live[0]["HasWaitEvents"] != false {
		t.Errorf("live: %v", live)
	}
	if live[0]["Lane"] != nil { // dispatches are for another time window than this attempt
		t.Errorf("lane %v", live[0]["Lane"])
	}
	if live[0]["Unpriced"].(float64) != 1 || live[0]["LastToolAgeS"] == nil {
		t.Errorf("live cost/tool: %v", live[0])
	}

	var items []map[string]any
	get(t, h, "/api/items", &items)
	if len(items) != 1 || items[0]["Cost"] != "$0.50 +?" || items[0]["ToPR"] != "n/a" {
		t.Errorf("items: %v", items)
	}

	var item map[string]any
	get(t, h, "/api/item?repo=o/r&issue=7", &item)
	if a := item["attempts"].([]any)[0].(map[string]any); a["status"] != "idle" {
		t.Errorf("item attempt status: %v", a)
	}
	if tl := item["timeline"].([]any); len(tl) == 0 || tl[0].(map[string]any)["Text"] != "attempt a1 started" || len(item["attempts"].([]any)) != 1 || item["title"] != "<b>t</b>" {
		t.Errorf("item: %v", item)
	}
	if u := item["units"].([]any); len(u) != 1 || u[0].(map[string]any)["CIFirstPush"] != "n/a" {
		t.Errorf("units: %v", u)
	}
	if w := get(t, h, "/api/item?repo=o/r&issue=99", nil); w.Code != 404 {
		t.Errorf("missing item: %d", w.Code)
	}
	if w := get(t, h, "/api/item?repo=o/r&issue=x", nil); w.Code != 400 {
		t.Errorf("bad issue: %d", w.Code)
	}

	var at map[string]any
	get(t, h, "/api/attempt?factory=f&id=a1", &at)
	tools := at["tools"].([]any)[0].(map[string]any)
	if tools["tool_name"] != "Bash" || tools["failed"].(float64) != 1 {
		t.Errorf("tools: %v", tools)
	}
	if at["usage"].([]any)[1].(map[string]any)["cost_usd"] != nil { // unpriced stays null
		t.Errorf("unpriced session shown as a number: %v", at["usage"])
	}
	if s := at["sessions"].([]any); len(s) != 1 || s[0].(map[string]any)["session"] != sid {
		t.Errorf("sessions: %v", s)
	}
	if a := at["attempt"].(map[string]any); a["status"] != "idle" || a["has_wait_events"].(float64) != 0 || a["last_event"] == nil {
		t.Errorf("attempt status: %v", a)
	}
	if u := at["usage"].([]any)[0].(map[string]any); u["lane"] != "" || at["dispatches"] == nil { // the fixture's dispatches are hours away
		t.Errorf("lanes: %v %v", u, at["dispatches"])
	}
	if tl := at["tools_by_lane"].([]any); len(tl) != 1 || tl[0].(map[string]any)["calls"].(float64) != 1 {
		t.Errorf("tools by lane: %v", tl)
	}
	if td := at["tool_decisions"].([]any); len(td) != 1 || td[0].(map[string]any)["decision"] != "reject" || td[0].(map[string]any)["n"].(float64) != 1 {
		t.Errorf("tool decisions: %v", td)
	}
	if mp := at["model_purposes"].([]any); len(mp) != 1 || mp[0].(map[string]any)["purpose"] != "q1" || mp[0].(map[string]any)["calls"].(float64) != 1 {
		t.Errorf("model purposes: %v", mp)
	}
	if get(t, h, "/api/attempt?factory=f&id=nope", nil).Code != 404 {
		t.Error("missing attempt")
	}

	var se map[string][]map[string]any
	get(t, h, "/api/series", &se)
	if a := se["attempts"]; len(a) != 1 || a[0]["unpriced"].(float64) != 1 || a[0]["title"] != "<b>t</b>" || a[0]["status"] != "idle" {
		t.Errorf("series attempts: %v", a)
	}
	calls, cost := 0.0, 0.0 // the two calls may fall into one hourly bucket or two, depending on the clock
	for _, u := range se["usage"] {
		calls += u["sessions"].(float64)
		if c, ok := u["cost_usd"].(float64); ok {
			cost += c
		}
	}
	if calls != 2 || cost != 0.5 {
		t.Errorf("series usage: %v", se["usage"])
	}
	if tl := se["tools"]; len(tl) != 1 || tl[0]["failed"].(float64) != 1 {
		t.Errorf("series tools: %v", tl)
	}

	var sm summary
	get(t, h, "/api/summary", &sm)
	if sm.Issues != 1 || sm.WithPR != 0 || sm.CostPerMerged != nil || sm.Bounces != 1 || *sm.ReworkPerIssue != 1 { // fixture lanes go build, review, build
		t.Errorf("summary: %+v", sm)
	}
	if u := se["usage"]; u[0]["phase"] == nil && u[len(u)-1]["phase"] == nil {
		t.Errorf("usage phase: %v", u)
	}
	var sc struct {
		Groups []string
		Rows   []struct {
			Label  string
			Values []string
		}
	}
	get(t, h, "/api/scorecard?by=config", &sc)
	if len(sc.Groups) != 1 || sc.Groups[0] != "f v1" || sc.Rows[0].Label != "work items" || sc.Rows[0].Values[0] != "1" {
		t.Errorf("scorecard: %+v", sc)
	}
}

func TestReadOnlyAndLocalOnly(t *testing.T) {
	s, h, dir := fixture(t)
	if _, err := s.db.Exec(`DELETE FROM events`); err == nil {
		t.Error("write through the read-only handle succeeded")
	}
	if w := httptest.NewRecorder(); true {
		req := httptest.NewRequest("POST", "/api/live", nil)
		req.Host = "127.0.0.1:8081"
		h.ServeHTTP(w, req)
		if w.Code != 405 {
			t.Errorf("POST: %d", w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/live", nil)
	req.Host = "evil.example:8081"
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, req); return w.Code }() != 403 {
		t.Error("foreign Host accepted on a loopback listener")
	}
	// a shared listener serves no local files
	shared := Handler(s.db, Options{Addr: ":8081", Runs: filepath.Join(dir, "runs"), Transcripts: filepath.Join(dir, "tr")})
	for _, u := range []string{"/api/local/transcript?session=" + sid, "/api/local/manifest?file=2026-11-12-x.md"} {
		req := httptest.NewRequest("GET", u, nil)
		w := httptest.NewRecorder()
		shared.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Errorf("%s on shared listener: %d", u, w.Code)
		}
	}
}

func TestLocalFiles(t *testing.T) {
	_, h, _ := fixture(t)
	var fl map[string]any
	get(t, h, "/api/flow?repo=o/r&issue=7", &fl)
	var d []any
	for _, r := range fl["dispatches"].([]any) {
		if r.(map[string]any)["lane"] != "" {
			d = append(d, r)
		}
	}
	if len(d) != 3 || d[1].(map[string]any)["lane"] != "review" || len(fl["notes"].([]any)) != 2 {
		t.Errorf("flow: %v", fl)
	}
	if mv := fl["moves"].([]any); len(mv) != 1 || mv[0].(map[string]any)["State"] != "Hold" {
		t.Errorf("moves: %v", fl["moves"])
	}
	if a := fl["attempts"].([]any)[0].(map[string]any); a["bar_state"] != "idle" || a["bar_end"] == nil {
		t.Errorf("bar end: %v", a)
	}
	var m map[string]any
	get(t, h, "/api/local/manifest?file=2026-11-12-x.md", &m)
	if !strings.Contains(m["text"].(string), "lane=review") {
		t.Errorf("manifest: %v", m)
	}
	for _, u := range []string{"/api/local/manifest?file=../fb.db", "/api/local/worker?file=../../etc/passwd", "/api/local/transcript?session=../x", "/api/local/worker?file=a-workers/worker-build-1.log/../../x"} {
		if w := get(t, h, u, nil); w.Code != 400 {
			t.Errorf("%s: %d, want 400", u, w.Code)
		}
	}
	var tr struct {
		Entries []entry
	}
	get(t, h, "/api/local/transcript?session="+sid, &tr)
	if len(tr.Entries) != 4 || tr.Entries[1].Text != "ok" || tr.Entries[2].Kind != "tool_use" || tr.Entries[3].Text != "file.go" {
		t.Errorf("transcript: %+v", tr.Entries)
	}
}

func TestStatus(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for _, c := range []struct {
		state string
		last  *time.Time
		want  string
	}{
		{"running", ago(time.Minute), "running"},
		{"running", ago(DefaultIdleTimeout + time.Second), "idle"},
		{"running", nil, "idle"},                                        // never sent an event
		{"waiting_for_human", ago(24 * time.Hour), "waiting_for_human"}, // a human wait is not idle
		{"failed", ago(24 * time.Hour), "failed"},
	} {
		if got := status(c.state, c.last, now, DefaultIdleTimeout); got != c.want {
			t.Errorf("status(%s, %v) = %s, want %s", c.state, c.last, got, c.want)
		}
	}
}
