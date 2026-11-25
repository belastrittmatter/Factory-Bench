package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factorybench/internal/store"
)

const sid = "00000000-0000-4000-8000-000000000001"

// fixture: one running attempt with an unpriced session, two subagent runs (one without model calls), one failed tool call and no human wait events.
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
		`INSERT INTO usage (factory_id, attempt_id, model, ts, input, output, cost_usd, phase) VALUES ('f', 'a1', 'm', '` + ts(20*time.Minute) + `', 10, 5, 0.5, 'p1')`,
		`INSERT INTO usage (factory_id, attempt_id, model, ts, input, output, agent_id) VALUES ('f', 'a1', 'm', '` + ts(19*time.Minute) + `', 1, 1, 'sub1')`,
		`INSERT INTO agent_runs (factory_id, attempt_id, agent_id, tool_calls, status, started_at) VALUES ('f', 'a1', 'sub1', 0, 'completed', '` + ts(19*time.Minute) + `'), ('f', 'a1', 'sub2', 0, 'killed', '` + ts(19*time.Minute) + `')`, // sub2 made no model call
		`INSERT INTO tool_calls VALUES ('f', 'a1', NULL, '` + ts(18*time.Minute) + `', 't1', 0, 12, 'E1', NULL)`,
		`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('contract', 'f', 'a1:started', 'attempt.started', '` + ts(25*time.Minute) + `', 'x', '{"attempt_id":"a1"}')`,
		`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('otlp', 'f', 'e1', 'otlp.api_request', '` + ts(20*time.Minute) + `', 'x', '{"factorybench.attempt":"a1","session.id":"` + sid + `","factorybench.phase":"p1","model":"m","query_source":"q1","cost_usd":0.5}')`,
		`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('otlp', 'f', 'e3', 'otlp.user_prompt', '` + ts(19*time.Minute) + `', 'x', '{"factorybench.attempt":"a1","session.id":"` + sid + `","factorybench.phase":"a0"}')`,
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
	os.MkdirAll(filepath.Join(runs, "2026-01-02-x-workers"), 0o755)
	os.MkdirAll(tr, 0o755)
	os.WriteFile(filepath.Join(runs, "2026-01-02-x.md"), []byte("[10:00:00] dispatch lane=alpha issue=#7 pid=1\n[10:05:00] dispatch lane=beta issue=#7 pid=2\n[10:09:00] dispatch lane=alpha issue=#7 pid=3\n[10:10:00] reaped lock on #7\n[10:11:00] worker on #7 (pid=3) — card moved to 'Hold'; killing\n"), 0o644)
	os.WriteFile(filepath.Join(tr, sid+".jsonl"), []byte(`{"type":"user","timestamp":"t","message":{"content":"do it"}}
{"type":"assistant","timestamp":"t","message":{"content":[{"type":"thinking","thinking":"secret"},{"type":"text","text":"ok"},{"type":"tool_use","name":"t1","input":{"command":"ls"}}]}}
{"type":"user","timestamp":"t","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"file.go"}]}]}}
`), 0o644)
	o := Options{Addr: "127.0.0.1:8081", Runs: runs, Transcripts: tr, Idle: DefaultIdleTimeout}
	return &server{db: db, o: o, local: true}, Handler(db, o), dir
}

// do sends one request with the given Host header.
func do(h http.Handler, method, host, url string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, nil)
	req.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func get(t *testing.T, h http.Handler, url string, out any) *httptest.ResponseRecorder {
	t.Helper()
	w := do(h, "GET", "127.0.0.1:8081", url)
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
	if len(live) != 1 || live[0]["Status"] != "idle" || live[0]["Idle"] != true || live[0]["Phase"] != "p1" || live[0]["HasWaitEvents"] != false {
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
	if len(item["attempts"].([]any)) != 1 || item["title"] != "<b>t</b>" {
		t.Errorf("item: %v", item)
	}
	if a := item["attempts"].([]any)[0].(map[string]any); a["subagents"].(float64) != 2 { // subagent runs, not distinct agent ids in usage
		t.Errorf("item attempt subagents: %v", a)
	}
	if s := item["sessions"].([]any); len(s) != 1 || s[0].(map[string]any)["session"] != sid || s[0].(map[string]any)["attempt_id"] != "a1" ||
		s[0].(map[string]any)["phase"] != "a0,p1" || s[0].(map[string]any)["first_ts"] == nil {
		t.Errorf("item sessions: %v", s)
	}
	if a := item["attempts"].([]any)[0].(map[string]any); a["input"] == nil || a["tool_calls"] == nil { // per-run token mix and tool calls
		t.Errorf("item attempt sums: %v", a)
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
	if tools["tool_name"] != "t1" || tools["failed"].(float64) != 1 {
		t.Errorf("tools: %v", tools)
	}
	if at["usage"].([]any)[1].(map[string]any)["cost_usd"] != nil { // unpriced stays null
		t.Errorf("unpriced session shown as a number: %v", at["usage"])
	}
	if s := at["sessions"].([]any); len(s) != 1 || s[0].(map[string]any)["session"] != sid || s[0].(map[string]any)["phase"] != "a0,p1" { // phases sorted, not in arrival order
		t.Errorf("sessions: %v", s)
	}
	// the run format covers every attempt of the run, not only this one
	if r := at["run"].([]any); len(r) != 1 || r[0].(map[string]any)["attempt_id"] != "a1" || r[0].(map[string]any)["staged"] != false || r[0].(map[string]any)["subagents"].(float64) != 2 {
		t.Errorf("run: %v", r)
	}
	if s := at["run_sessions"].([]any); len(s) != 1 || s[0].(map[string]any)["session"] != sid || s[0].(map[string]any)["attempt_id"] != "a1" {
		t.Errorf("run_sessions: %v", s)
	}
	if a := at["attempt"].(map[string]any); a["status"] != "idle" || a["has_wait_events"].(float64) != 0 || a["last_event"] == nil {
		t.Errorf("attempt status: %v", a)
	}
	if u := at["usage"].([]any)[0].(map[string]any); u["lane"] != "" || at["dispatches"] == nil { // the fixture's dispatches are from 2026-01-02, outside this attempt
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
	if a := se["attempts"]; len(a) != 1 || a[0]["unpriced"].(float64) != 1 || a[0]["title"] != "<b>t</b>" || a[0]["status"] != "idle" || a[0]["subagents"].(float64) != 2 {
		t.Errorf("series attempts: %v", a)
	}
	if _, ok := se["sessions"]; ok {
		t.Error("series carries sessions: Overview polls it every 5 s")
	}
	var ss []map[string]any
	get(t, h, "/api/sessions", &ss)
	if len(ss) != 1 || ss[0]["session"] != sid || ss[0]["phase"] != "a0,p1" {
		t.Errorf("sessions: %v", ss)
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
	pc := 0.0 // per-attempt phase cost adds up to the usage cost
	for _, x := range se["attempt_phases"] {
		if c, ok := x["cost_usd"].(float64); ok {
			pc += c
		}
	}
	if pc != cost || len(se["attempt_phases"]) == 0 {
		t.Errorf("attempt phases: %v", se["attempt_phases"])
	}
	if _, ok := se["attempt_steps"]; !ok {
		t.Errorf("series has no attempt_steps")
	}

	var sm summary
	get(t, h, "/api/summary", &sm)
	if sm.Issues != 1 || sm.WithPR != 0 || sm.CostPerMerged != nil || sm.Bounces != 1 || *sm.ReworkPerIssue != 1 { // fixture lanes go alpha, beta, alpha
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
	if len(sc.Groups) != 1 || sc.Groups[0] != "v1" || sc.Rows[0].Label != "work items" || sc.Rows[0].Values[0] != "1" {
		t.Errorf("scorecard: %+v", sc)
	}
}

// The issue, attempt and run lookups keep to their own issue, factory and run: another issue of the same factory and
// another factory on the same issue, both with telemetry, stay out of them.
func TestScopes(t *testing.T) {
	_, h, dir := fixture(t)
	rw, err := OpenWritable(filepath.Join(dir, "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	ts := func(t time.Time) string { return store.FormatTS(t) }
	now, day := time.Now().UTC(), time.Date(2026, 1, 2, 9, 59, 0, 0, time.Local) // the fixture's dispatches start at 10:00 local that day
	for _, q := range []string{
		`INSERT INTO factories VALUES ('g', 'h2', NULL, NULL, 'x')`,
		`INSERT INTO work_items (repo, issue, title, first_seen, outcome) VALUES ('o/r', 8, 'other', 'x', 'open')`,
		// b1: factory f on another issue; c1: factory g on the same issue, during the fixture's dispatches
		`INSERT INTO attempts (factory_id, attempt_id, repo, issue, config_version, state, started_at, ended_at) VALUES ('f', 'b1', 'o/r', 8, 'v1', 'completed', '` + ts(now.Add(-time.Hour)) + `', '` + ts(now.Add(-50*time.Minute)) + `')`,
		`INSERT INTO attempts (factory_id, attempt_id, repo, issue, config_version, state, started_at, ended_at) VALUES ('g', 'c1', 'o/r', 7, 'v1', 'completed', '` + ts(day) + `', '` + ts(day.Add(13*time.Minute)) + `')`,
		`INSERT INTO usage (factory_id, attempt_id, model, ts, input, output, cost_usd, phase) VALUES ('f', 'b1', 'm', '` + ts(now.Add(-55*time.Minute)) + `', 1, 1, 0.1, 'p1'), ('g', 'c1', 'm', '` + ts(day.Add(time.Minute)) + `', 1, 1, 0.2, 'p1')`,
		`INSERT INTO agent_runs (factory_id, attempt_id, agent_id, tool_calls, status, started_at) VALUES ('f', 'b1', 's', 0, 'completed', 'x'), ('g', 'c1', 's', 0, 'completed', 'x')`,
		`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('otlp', 'f', 'b1e', 'otlp.api_request', '` + ts(now.Add(-55*time.Minute)) + `', 'x', '{"factorybench.attempt":"b1","session.id":"sb1"}'),
			('otlp', 'g', 'c1e', 'otlp.api_request', '` + ts(day.Add(time.Minute)) + `', 'x', '{"factorybench.attempt":"c1","session.id":"sc1"}'),
			('otlp', 'f', 'xe', 'otlp.api_request', '` + ts(day) + `', 'x', '{"factorybench.attempt":"c1","session.id":"sx"}')`, // c1's id under factory f: no such attempt
	} {
		if _, err := rw.Exec(q); err != nil {
			t.Fatal(err, q)
		}
	}
	rw.Close()
	ids := func(rs []any, k string) string {
		var out []string
		for _, r := range rs {
			out = append(out, fmt.Sprint(r.(map[string]any)[k]))
		}
		return strings.Join(out, " ")
	}
	var item map[string]any
	get(t, h, "/api/item?repo=o/r&issue=7", &item)
	if g := ids(item["sessions"].([]any), "session"); g != "sc1 "+sid {
		t.Errorf("item sessions: %s", g)
	}
	if g := ids(item["attempts"].([]any), "attempt_id"); g != "c1 a1" {
		t.Errorf("item attempts: %s", g)
	}
	if a := item["attempts"].([]any)[0].(map[string]any); a["attempt_id"] != "c1" || a["input"] != 1.0 { // c1's one call, not a1's or b1's
		t.Errorf("item attempt sums: %v", a)
	}
	var at map[string]any
	get(t, h, "/api/attempt?factory=f&id=a1", &at)
	if g := ids(at["run"].([]any), "attempt_id"); g != "a1" {
		t.Errorf("run: %s", g)
	}
	if g := ids(at["run_sessions"].([]any), "session"); g != sid {
		t.Errorf("run sessions: %s", g)
	}
	if l := at["run_lanes"].([]any); len(l) != 0 { // the dispatches ran in c1, another factory's run
		t.Errorf("run lanes: %v", l)
	}
	if g := ids(at["timeline"].([]any), "Who"); g == "" || strings.Trim(strings.ReplaceAll(g, "github", ""), "f ") != "" { // the run's log: f's lines, not g's on the same issue
		t.Errorf("run timeline: %s", g)
	}
	get(t, h, "/api/attempt?factory=g&id=c1", &at)
	if g := ids(at["run"].([]any), "attempt_id") + " | " + ids(at["run_sessions"].([]any), "session") + " | " + fmt.Sprint(at["run_lanes"]); g != "c1 | sc1 | [alpha beta alpha]" {
		t.Errorf("c1 run: %s", g)
	}
	var ss []map[string]any
	get(t, h, "/api/sessions", &ss)
	if len(ss) != 3 { // every attempt's sessions; sx belongs to no attempt
		t.Errorf("sessions: %v", ss)
	}
}

func TestReadOnlyAndLocalOnly(t *testing.T) {
	s, h, dir := fixture(t)
	if _, err := s.db.Exec(`DELETE FROM events`); err == nil {
		t.Error("write through the read-only handle succeeded")
	}
	if w := do(h, "POST", "127.0.0.1:8081", "/api/live"); w.Code != 405 {
		t.Errorf("POST: %d", w.Code)
	}
	if w := do(h, "GET", "evil.example:8081", "/api/live"); w.Code != 403 {
		t.Errorf("foreign Host on a loopback listener: %d", w.Code)
	}
	// a shared listener serves no local files
	shared := Handler(s.db, Options{Addr: ":8081", Runs: filepath.Join(dir, "runs"), Transcripts: filepath.Join(dir, "tr")})
	for _, u := range []string{"/api/local/transcript?session=" + sid, "/api/local/manifest?file=2026-01-02-x.md"} {
		if w := do(shared, "GET", "example.com", u); w.Code != 403 {
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
	if len(d) != 3 || d[1].(map[string]any)["lane"] != "beta" || len(fl["notes"].([]any)) != 2 {
		t.Errorf("flow: %v", fl)
	}
	if mv := fl["moves"].([]any); len(mv) != 1 || mv[0].(map[string]any)["State"] != "Hold" {
		t.Errorf("moves: %v", fl["moves"])
	}
	if a := fl["attempts"].([]any)[0].(map[string]any); a["bar_state"] != "idle" || a["bar_end"] == nil {
		t.Errorf("bar end: %v", a)
	}
	var m map[string]any
	get(t, h, "/api/local/manifest?file=2026-01-02-x.md", &m)
	if !strings.Contains(m["text"].(string), "lane=beta") {
		t.Errorf("manifest: %v", m)
	}
	for _, u := range []string{"/api/local/manifest?file=../fb.db", "/api/local/worker?file=../../etc/passwd", "/api/local/transcript?session=../x", "/api/local/worker?file=a-workers/worker-build-1.log/../../x"} {
		if w := get(t, h, u, nil); w.Code != 400 {
			t.Errorf("%s: %d, want 400", u, w.Code)
		}
	}
	if w := get(t, h, "/api/local/transcript?session="+sid, nil); strings.Contains(w.Body.String(), `\"ls\"`) || strings.Contains(w.Body.String(), `"ls"`) {
		t.Errorf("tool input value leaked: %s", w.Body)
	}
	var tr struct {
		Entries []entry
	}
	get(t, h, "/api/local/transcript?session="+sid, &tr)
	if len(tr.Entries) != 4 || tr.Entries[1].Text != "ok" || tr.Entries[2].Kind != "tool_use" || tr.Entries[2].Text != "input not shown · command: 4 bytes" || tr.Entries[3].Text != "file.go" {
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

// An empty database: every list endpoint answers an empty list (never null) and every rate stays unknown.
func TestEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fb.db")
	rw, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rw.Close()
	db, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := Handler(db, Options{Addr: "127.0.0.1:8081"})
	for _, u := range []string{"/api/live", "/api/items"} {
		if w := get(t, h, u, nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
			t.Errorf("%s: %d %s", u, w.Code, w.Body)
		}
	}
	var se map[string][]any
	get(t, h, "/api/series", &se)
	for k, v := range se {
		if v == nil {
			t.Errorf("series %s is null", k)
		}
	}
	var sm summary
	if w := get(t, h, "/api/summary", &sm); w.Code != 200 || sm.Issues != 0 || sm.AvgToPRSeconds != nil || sm.CostPerMerged != nil || sm.ReworkPerIssue != nil {
		t.Errorf("summary: %d %+v", w.Code, sm)
	}
	if w := get(t, h, "/api/scorecard", nil); w.Code != 200 {
		t.Errorf("scorecard: %d", w.Code)
	}
}

// A truncated file keeps invalid bytes that are not at the cut and drops only a rune the cut split.
func TestReadTextTruncates(t *testing.T) {
	dir := t.TempDir()
	body := append([]byte("a\xffb"), strings.Repeat("x", maxFile-4)...)
	body = append(body, "é tail"...) // é straddles the maxFile limit
	if err := os.WriteFile(filepath.Join(dir, "f"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := readText(dir, "f")
	if err != nil {
		t.Fatal(err)
	}
	text := r["text"].(string)
	if r["truncated"] != true || len(text) != maxFile-1 || !strings.HasPrefix(text, "a\xffb") || strings.HasSuffix(text, "\xc3") {
		t.Errorf("truncated %v, len %d, head %q, tail %q", r["truncated"], len(text), text[:3], text[len(text)-3:])
	}
}

// Live shows the lane of the newest dispatch that ran in the attempt itself.
func TestLiveLane(t *testing.T) {
	_, h, dir := fixture(t)
	at := time.Now().Add(-29 * time.Minute).Local() // a1 started 30 minutes ago
	line := "[" + at.Format("15:04:05") + "] dispatch lane=delta issue=#7 pid=9\n"
	if err := os.WriteFile(filepath.Join(dir, "runs", at.Format("2006-01-02")+"-y.md"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	var live []map[string]any
	get(t, h, "/api/live", &live)
	if len(live) != 1 || live[0]["Lane"] != "delta" {
		t.Errorf("live lane: %v", live)
	}
}

func TestFactories(t *testing.T) {
	s, _, dir := fixture(t)
	admin, err := OpenWritable(filepath.Join(dir, "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	o := s.o
	o.Admin = admin
	h := Handler(s.db, o)
	post := func(h http.Handler, host, path, body string, header bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Host = host
		if header {
			req.Header.Set("X-FactoryBench", "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	add := `{"id":"fb2","commit_email":"Bot@x.io","github_login":"Bot"}`
	if w := post(h, "127.0.0.1:8081", "/api/factories/add", add, false); w.Code != 403 {
		t.Errorf("add without header: %d", w.Code)
	}
	if w := post(h, "evil.example", "/api/factories/add", add, true); w.Code != 403 {
		t.Errorf("add from a foreign Host: %d", w.Code)
	}
	shared := Handler(s.db, Options{Addr: ":8081", Admin: admin}) // Admin is ignored off loopback
	if w := post(shared, "example.com", "/api/factories/add", add, true); w.Code != 403 {
		t.Errorf("add on a shared listener: %d", w.Code)
	}
	var tok struct{ Token string }
	w := post(h, "127.0.0.1:8081", "/api/factories/add", add, true)
	if json.Unmarshal(w.Body.Bytes(), &tok); w.Code != 200 || !strings.HasPrefix(tok.Token, "fbt_") {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if w := post(h, "127.0.0.1:8081", "/api/factories/add", add, true); w.Code != 400 {
		t.Errorf("duplicate add: %d", w.Code)
	}
	if w := post(h, "127.0.0.1:8081", "/api/factories/update", `{"id":"fb2","commit_email":"nope"}`, true); w.Code != 400 {
		t.Errorf("bad email: %d", w.Code)
	}
	if w := post(h, "127.0.0.1:8081", "/api/factories/revoke", `{"id":"nope"}`, true); w.Code != 404 {
		t.Errorf("revoke unknown: %d", w.Code)
	}
	if w := post(h, "127.0.0.1:8081", "/api/factories/revoke", `{"id":"fb2"}`, true); w.Code != 200 {
		t.Errorf("revoke: %d %s", w.Code, w.Body)
	}
	if id, _ := store.FactoryByToken(admin, tok.Token); id != "" {
		t.Error("revoked token still works")
	}
	var list []Factory
	get(t, h, "/api/factories", &list)
	if len(list) != 2 || list[1].ID != "fb2" || list[1].Active || list[1].CommitEmail != "bot@x.io" || list[0].Events != 1 || list[0].Attempts != 1 {
		t.Errorf("list: %+v", list)
	}
	var meta struct{ Manage bool }
	if get(t, h, "/api/meta", &meta); !meta.Manage {
		t.Error("meta.manage off on loopback with Admin")
	}
}

func TestWithStages(t *testing.T) {
	row := func(f, id, role, state string, parent any) map[string]any {
		return map[string]any{"factory_id": f, "attempt_id": id, "repo": "o/r", "issue": int64(1), "role": role, "state": state, "parent_attempt_id": parent}
	}
	rs := []map[string]any{
		row("s", "p", "plan", "completed", nil), row("s", "b1", "build", "failed", "p"), row("s", "b2", "build", "failed", "b1"),
		row("x", "a1", "build", "failed", nil), row("x", "a2", "build", "completed", "a1"), // single session: a retry, not staged
	}
	withStages(rs)
	var got []string
	for _, r := range rs {
		got = append(got, fmt.Sprint(r["attempt_id"], ":", r["stage"], ":", r["staged"]))
	}
	if g := strings.Join(got, " "); g != "p::true b1:handoff:true b2:retry:true a1::false a2:retry:false" {
		t.Errorf("got %s", g)
	}
}

// Named steps: sessions analyzer → green → reviewer → green in one attempt (green is sent back to once), gates, and a
// session of another issue that the issue's filter keeps out. Gate red fails then passes in a1 and passes at once in a2.
func TestSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fb.db")
	rw, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ev := func(id, att, session, step, ts string) string {
		st := ""
		if step != "" {
			st = `,"factorybench.step":"` + step + `"`
		}
		return `INSERT INTO events (source, factory_id, id, type, ts, received_at, payload) VALUES ('otlp', 'f', '` + id + `', 'otlp.api_request', '` + ts +
			`', 'x', '{"factorybench.attempt":"` + att + `","session.id":"` + session + `","factorybench.phase":"implement"` + st + `}')`
	}
	use := func(att, step, ts, cost string) string {
		return `INSERT INTO usage (factory_id, attempt_id, model, ts, input, output, cost_usd, step) VALUES ('f', '` + att + `', 'm', '` + ts + `', 1, 1, ` + cost + `, '` + step + `')`
	}
	for _, q := range []string{
		`INSERT INTO work_items (repo, issue, first_seen) VALUES ('o/r', 7, 'x'), ('o/r', 8, 'x')`,
		`INSERT INTO attempts (factory_id, attempt_id, repo, issue, config_version, state, started_at, ended_at) VALUES
			('f', 'a1', 'o/r', 7, 'v', 'failed', '2026-11-10T10:00:00.000Z', '2026-11-10T11:00:00.000Z'),
			('f', 'a2', 'o/r', 7, 'v', 'completed', '2026-11-10T12:00:00.000Z', '2026-11-10T13:00:00.000Z'),
			('f', 'b1', 'o/r', 8, 'v', 'completed', '2026-11-10T10:00:00.000Z', '2026-11-10T11:00:00.000Z')`,
		ev("1", "a1", "s1", "analyzer", "2026-11-10T10:00:00.000Z"), ev("2", "a1", "s1", "analyzer", "2026-11-10T10:05:00.000Z"),
		ev("3", "a1", "s2", "green", "2026-11-10T10:10:00.000Z"), ev("4", "a1", "s2", "green", "2026-11-10T10:20:00.000Z"),
		ev("5", "a1", "s3", " reviewer ", "2026-11-10T10:30:00.000Z"),
		ev("6", "a1", "s4", "green", "2026-11-10T10:40:00.000Z"), ev("7", "a1", "s4", "green", "2026-11-10T10:45:00.000Z"),
		ev("8", "a1", "s5", "", "2026-11-10T10:50:00.000Z"), // no step: not a step row
		ev("9", "b1", "s9", "other", "2026-11-10T10:00:00.000Z"),
		use("a1", "analyzer", "2026-11-10T10:00:00.000Z", "0.25"), use("a1", "green", "2026-11-10T10:10:00.000Z", "1"),
		use("a1", "green", "2026-11-10T10:40:00.000Z", "0.5"), use("a1", "reviewer", "2026-11-10T10:30:00.000Z", "NULL"),
		use("b1", "other", "2026-11-10T10:00:00.000Z", "9"),
		`INSERT INTO gates (factory_id, attempt_id, ts, name, passed, reason) VALUES ('f', 'a1', '2026-11-10T10:21:00.000Z', 'unit', 1, NULL), ('f', 'a1', '2026-11-10T10:31:00.000Z', 'e2e', 0, 'timeout'),
			('f', 'a1', '2026-11-10T10:02:00.000Z', 'red', 0, 'no failing test'), ('f', 'a1', '2026-11-10T10:04:00.000Z', 'red', 1, NULL),
			('f', 'a2', '2026-11-10T12:10:00.000Z', 'red', 1, NULL), ('f', 'b1', '2026-11-10T10:01:00.000Z', 'red', 0, NULL)`,
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
	defer db.Close()
	h := Handler(db, Options{Addr: "127.0.0.1:8081"})

	var item map[string]any
	get(t, h, "/api/item?repo=o/r&issue=7", &item)
	var a1 map[string]any // the issue's step sessions are sessions[] with a step; the attempt endpoint lists them as steps[]
	get(t, h, "/api/attempt?factory=f&id=a1", &a1)
	var steps []string
	for _, s := range a1["steps"].([]any) {
		steps = append(steps, s.(map[string]any)["step"].(string))
	}
	if strings.Join(steps, " ") != "analyzer green reviewer green" {
		t.Errorf("steps %v", steps)
	}
	if s := a1["steps"].([]any)[1].(map[string]any); s["cost_usd"] != 1.0 || s["at"] != "2026-11-10T10:10:00.000Z" || s["end"] != "2026-11-10T10:20:00.000Z" || s["phase"] != "implement" {
		t.Errorf("green session: %v", s)
	}
	stats := map[string]map[string]any{}
	for _, s := range item["step_stats"].([]any) {
		stats[s.(map[string]any)["step"].(string)] = s.(map[string]any)
	}
	if g := stats["green"]; g["sessions"] != 2.0 || g["cost_usd"] != 1.5 || g["sent_back"] != 1.0 || g["ms"] != 15*60e3 {
		t.Errorf("green stats: %v", g)
	}
	if r := stats["reviewer"]; r["cost_usd"] != nil || r["unpriced"] != 1.0 || r["sent_back"] != 0.0 { // unknown cost stays unknown
		t.Errorf("reviewer stats: %v", r)
	}
	if _, ok := stats["other"]; ok || len(stats) != 3 {
		t.Errorf("another issue's steps leaked in: %v", stats)
	}
	if g := item["checks"].([]any); len(g) != 5 || g[0].(map[string]any)["name"] != "red" || g[4].(map[string]any)["attempt_id"] != "a2" {
		t.Errorf("checks: %v", g)
	}
	gs := map[string]map[string]any{}
	for _, g := range item["gate_stats"].([]any) {
		gs[g.(map[string]any)["name"].(string)] = g.(map[string]any)
	}
	if r := gs["red"]; r["results"] != 3.0 || r["failed"] != 1.0 || r["first_try_passed"] != 1.0 || r["attempts"] != 2.0 {
		t.Errorf("red: %v", r)
	}
	if e := gs["e2e"]; e["failed"] != 1.0 || e["first_try_passed"] != 0.0 {
		t.Errorf("e2e: %v", e)
	}

	var at map[string]any
	get(t, h, "/api/attempt?factory=f&id=a2", &at)
	if len(at["steps"].([]any)) != 0 || len(at["gate_stats"].([]any)) != 1 {
		t.Errorf("a2 steps %v gates %v", at["steps"], at["gate_stats"])
	}
	get(t, h, "/api/attempt?factory=f&id=a1", &at)
	if r := at["step_runs"].([]any); len(r) != 1 || r[0].(map[string]any)["sent_back"] != 1.0 || r[0].(map[string]any)["reruns"] != 0.0 {
		t.Errorf("step runs: %v", r)
	}
	if len(at["steps"].([]any)) != 4 || len(at["step_stats"].([]any)) != 3 {
		t.Errorf("a1 steps %v stats %v", at["steps"], at["step_stats"])
	}
	if s := at["sessions"].([]any); s[2].(map[string]any)["step"] != "reviewer" || s[4].(map[string]any)["step"] != nil {
		t.Errorf("a1 sessions: %v", s)
	}
}

// Lanes of two factories on one card, both with attempt id a1: fa runs build → review → build (one bounce), fb runs
// review → build (none). Counted per factory and attempt, the card has 1 bounce; across factories it would have 2.
func TestLanesTwoFactories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fb.db")
	rw, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) string { return store.FormatTS(time.Date(2026, 1, 2, h, m, 0, 0, time.Local)) }
	for _, q := range []string{
		`INSERT INTO work_items (repo, issue, first_seen) VALUES ('o/r', 9, 'x')`,
		`INSERT INTO attempts (factory_id, attempt_id, repo, issue, config_version, state, started_at, ended_at) VALUES
			('fa', 'a1', 'o/r', 9, 'v', 'completed', '` + at(10, 0) + `', '` + at(10, 30) + `'),
			('fb', 'a1', 'o/r', 9, 'v', 'completed', '` + at(11, 0) + `', '` + at(11, 30) + `')`,
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
	defer db.Close()
	runs := filepath.Join(dir, "runs")
	os.MkdirAll(runs, 0o755)
	os.WriteFile(filepath.Join(runs, "2026-01-02-x.md"), []byte("[10:00:00] dispatch lane=build issue=#9 pid=1\n[10:10:00] dispatch lane=review issue=#9 pid=2\n"+
		"[10:20:00] dispatch lane=build issue=#9 pid=3\n[11:00:00] dispatch lane=review issue=#9 pid=4\n[11:10:00] dispatch lane=build issue=#9 pid=5\n"), 0o644)
	h := Handler(db, Options{Addr: "127.0.0.1:8081", Runs: runs, Idle: DefaultIdleTimeout})

	var fl cardFlow
	if get(t, h, "/api/flow?repo=o/r&issue=9", &fl); fl.Bounces != 1 {
		t.Errorf("flow bounces %d: %+v", fl.Bounces, fl.Dispatches)
	}
	for f, want := range map[string]int{"": 1, "fa": 1, "fb": 0} {
		var s summary
		if get(t, h, "/api/summary?factory="+f, &s); s.Bounces != want {
			t.Errorf("summary factory=%q bounces %d, want %d", f, s.Bounces, want)
		}
	}
}

func TestQuestionCounts(t *testing.T) {
	ev := func(typ, ts, kind, reason string) map[string]any {
		return map[string]any{"factory_id": "f", "attempt_id": "a", "type": typ, "ts": ts, "kind": kind, "reason": reason}
	}
	ws := waits([]map[string]any{
		ev("human.waiting", "2026-11-20T10:00:00Z", "blocking", "q1"), ev("human.resumed", "2026-11-20T10:00:02Z", "", "ok"), // auto-answered
		ev("human.waiting", "2026-11-20T10:01:00Z", "blocking", "q2"), ev("human.resumed", "2026-11-20T10:05:00Z", "", "a person"),
		ev("human.waiting", "2026-11-20T10:06:00Z", "blocking", "q3"), ev("human.resumed", "2026-11-20T10:06:01Z", "", "bench ended with the question unanswered"),
		ev("human.waiting", "2026-11-20T10:07:00Z", "planning", "p"), ev("human.resumed", "2026-11-20T10:07:01Z", "", ""),
	}, "")
	if got := questionCounts(ws)["f\x00a"]; got != [3]int{3, 1, 1} {
		t.Errorf("blocking, auto-answered, unanswered = %v, want [3 1 1]", got)
	}
}

func TestWaitsResumeTheOldestOpenWait(t *testing.T) {
	ev := func(typ, ts, by string) map[string]any {
		return map[string]any{"factory_id": "f", "attempt_id": "a", "type": typ, "ts": ts, "kind": "blocking", "by": by}
	}
	ws := waits([]map[string]any{
		ev("human.waiting", "2026-11-20T10:00:00Z", ""), ev("human.waiting", "2026-11-20T10:01:00Z", ""),
		ev("human.resumed", "2026-11-20T10:02:00Z", "person"), ev("human.resumed", "2026-11-20T10:03:00Z", "observer"),
		ev("human.waiting", "2026-11-20T10:04:00Z", ""),
	}, "")
	if len(ws) != 3 || ws[0]["end"] != "2026-11-20T10:02:00Z" || ws[0]["by"] != "person" ||
		ws[1]["end"] != "2026-11-20T10:03:00Z" || ws[1]["by"] != "observer" || ws[2]["end"] != nil {
		t.Errorf("waits %v", ws)
	}
}
