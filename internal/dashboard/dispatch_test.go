package dashboard

import (
	"strings"
	"testing"
	"time"

	"factorybench/internal/report"
)

// Synthetic lanes and states only: nothing here may match a real board.
const manifestFixture = `[10:00:00] dispatch lane=alpha issue=#7 pid=11 claim=x
[10:00:30] tick — unrelated line
[10:02:00] worker on #7 (pid=11) — card moved to 'Gamma'; killing
[10:03:00] dispatch lane=beta issue=#7 pid=12
[10:05:00] dispatch lane=alpha issue=#7 pid=13
[10:06:00] reaped lock on #7 (pid=13)
[10:07:00] dispatch lane=alpha issue=#8 pid=14
`

func TestParseManifest(t *testing.T) {
	rl := newRunLog()
	if err := parseManifest("2026-01-02", "m.md", strings.NewReader(manifestFixture), rl); err != nil {
		t.Fatal(err)
	}
	d := rl.dispatches[7]
	if len(d) != 3 || len(rl.dispatches[8]) != 1 {
		t.Fatalf("dispatches: %+v", rl.dispatches)
	}
	if d[0].Lane != "alpha" || d[0].Pid != "11" || d[0].End == nil || d[0].End.Format("15:04") != "10:02" ||
		d[0].Outcome != "worker on #7 — card moved to 'Gamma'; killing" {
		t.Errorf("first dispatch: %+v", d[0])
	}
	if d[1].End != nil || d[1].Outcome != "" { // nothing named pid 12 again
		t.Errorf("unreported end: %+v", d[1])
	}
	if d[2].Outcome != "reaped lock on #7" {
		t.Errorf("outcome: %q", d[2].Outcome)
	}
	if mv := rl.moves[7]; len(mv) != 1 || mv[0].State != "Gamma" || mv[0].At.Format("15:04") != "10:02" {
		t.Errorf("moves: %+v", mv)
	}
	if len(rl.notes[7]) != 2 {
		t.Errorf("notes: %+v", rl.notes[7])
	}
}

func TestGroupByDispatch(t *testing.T) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	at := func(hm string) time.Time {
		d, _ := time.ParseDuration(strings.Replace(hm, ":", "h", 1) + "m")
		return day.Add(d)
	}
	ptr := func(t time.Time) *time.Time { return &t }
	cost := func(v float64) *float64 { return &v }
	wins := []attemptWin{
		{ID: "b", State: "running", Start: at("10:04")},
		{ID: "a", State: "failed", Start: at("09:59"), End: ptr(at("10:04"))},
	}
	ds := []dispatch{
		{At: at("10:05"), Lane: "alpha", End: ptr(at("10:06")), Outcome: "done"},
		{At: at("10:00"), Lane: "alpha", End: ptr(at("10:02"))},
		{At: at("10:03"), Lane: "beta"},
	}
	calls := []call{
		{TS: at("10:01"), Attempt: "a", Tokens: 10, Cost: cost(.1)},
		{TS: at("09:57"), Attempt: "a", Tokens: 1, Cost: cost(.01)}, // before every dispatch
		{TS: at("10:03"), Attempt: "a", Tokens: 5},                  // unpriced
		{TS: at("10:05"), Attempt: "b", Tokens: 20, Cost: cost(.2)},
	}
	rows := groupByDispatch(ds, wins, calls, at("12:00"))
	if len(rows) != 4 {
		t.Fatalf("got %d rows: %+v", len(rows), rows)
	}
	type want struct {
		lane, attempt string
		retry         bool
		calls, unp    int
		end           string
		reported      bool
	}
	wants := []want{
		{"", "a", true, 1, 0, "09:57", false},     // calls outside every dispatch keep a row of their own
		{"alpha", "a", true, 1, 0, "10:02", true}, // reported end
		{"beta", "a", true, 1, 1, "10:04", false}, // derived: its attempt ended before the next dispatch
		{"alpha", "b", false, 1, 0, "10:06", true},
	}
	total := 0
	for i, w := range wants {
		r := rows[i]
		got := want{r.Lane, r.Attempt, r.Retry, len(r.Calls), r.Unpriced, r.End.Format("15:04"), r.EndReported}
		if got != w {
			t.Errorf("row %d: %+v, want %+v", i, got, w)
		}
		total += len(r.Calls)
	}
	if total != len(calls) {
		t.Errorf("calls kept %d of %d", total, len(calls))
	}
	if rows[3].Cost != .2 || rows[1].Tokens != 10 {
		t.Errorf("sums: %+v", rows)
	}

	// no manifest: one row of calls per attempt; nothing at all: an empty list, not nil
	if r := groupByDispatch(nil, wins, calls, at("12:00")); len(r) != 2 || r[0].Lane != "" || len(r[0].Calls) != 3 {
		t.Errorf("no dispatches: %+v", r)
	}
	if r := groupByDispatch(nil, nil, nil, at("12:00")); r == nil || len(r) != 0 {
		t.Errorf("empty: %#v", r)
	}
	// an idle attempt with one dispatch ends at its last event, not at now
	idle := []attemptWin{{ID: "c", State: "idle", Start: at("10:00"), LastActive: ptr(at("10:30"))}}
	if r := groupByDispatch([]dispatch{{At: at("10:00"), Lane: "alpha"}}, idle, nil, at("23:00")); r[0].End.Format("15:04") != "10:30" {
		t.Errorf("idle end: %v", r[0].End)
	}
}

func TestRework(t *testing.T) {
	for _, c := range []struct {
		lanes         []string
		bounces, redo int
	}{
		{nil, 0, 0},
		{[]string{"x"}, 0, 0},
		{[]string{"x", "x", "x"}, 0, 2},
		{[]string{"x", "y", "x", "z", "y"}, 2, 0},
		{[]string{"x", "x", "y", "x", "x"}, 1, 2},
	} {
		if b, r := rework(c.lanes); b != c.bounces || r != c.redo {
			t.Errorf("rework(%v) = %d, %d; want %d, %d", c.lanes, b, r, c.bounces, c.redo)
		}
	}
}

func TestBarEnd(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { t := now.Add(-time.Duration(h) * time.Hour); return &t }
	for _, c := range []struct {
		status            string
		ended, wait, last *time.Time
		want              time.Time
		why               string
	}{
		{"completed", at(3), nil, at(3), *at(3), "ended"},
		{"waiting_for_human", nil, at(2), at(1), *at(2), "waiting"}, // stops where the wait began, not at now
		{"waiting_for_human", nil, nil, at(1), now, "running"},      // no wait start reported
		{"idle", nil, nil, at(5), *at(5), "idle"},
		{"running", nil, nil, at(0), now, "running"},
	} {
		if got, why := barEnd(c.status, c.ended, c.wait, c.last, now); !got.Equal(c.want) || why != c.why {
			t.Errorf("barEnd(%s) = %v %s, want %v %s", c.status, got, why, c.want, c.why)
		}
	}
}

func TestLaneAt(t *testing.T) {
	day := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	m := func(n int) time.Time { return day.Add(time.Duration(n) * time.Minute) }
	rows := []dispatchRow{
		{Lane: "alpha", At: m(0), End: m(10)},
		{Lane: "", At: m(0), End: m(30)}, // calls outside dispatches never set a lane
		{Lane: "beta", At: m(20), End: m(25)},
	}
	for _, c := range []struct {
		min  int
		want string
	}{{5, "alpha"}, {15, ""}, {21, "beta"}, {40, ""}, {-5, ""}} {
		if got := laneAt(rows, m(c.min)); got != c.want {
			t.Errorf("laneAt(+%dm) = %q, want %q", c.min, got, c.want)
		}
	}
	if laneAt(nil, m(0)) != "" {
		t.Error("no dispatches")
	}
}

func TestSummarize(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rows := []report.ItemRow{
		{Item: "o/r#1", Factory: "x", HasPR: true, Merged: true, ToPRSeconds: f(100), CostUSD: f(2)},
		{Item: "o/r#1", Factory: "y", CostUSD: f(1)}, // same issue, another factory
		{Item: "o/r#2", Factory: "x", HasPR: true, ToPRSeconds: f(300), CostUSD: f(3), CostLowerBound: true},
		{Item: "o/r#3", Factory: "x"}, // no usage at all
	}
	s := summarize(rows, map[string][]string{"o/r#1": {"a", "b", "a"}, "o/r#2": {"a", "a"}})
	if s.Issues != 3 || s.WithPR != 2 || s.Merged != 1 {
		t.Errorf("funnel: %+v", s)
	}
	if *s.AvgToPRSeconds != 200 || *s.CostPerMerged != 6 || !s.CostLowerBound {
		t.Errorf("time/cost: %+v", s)
	}
	if s.IssuesWithDispatches != 2 || s.Bounces != 1 || s.Redispatches != 1 || *s.ReworkPerIssue != .5 {
		t.Errorf("rework: %+v", s)
	}
	// nothing merged, no manifests, no rows: every rate is unknown, never zero
	e := summarize(rows[3:], nil)
	if e.Issues != 1 || e.CostPerMerged != nil || e.AvgToPRSeconds != nil || e.ReworkPerIssue != nil {
		t.Errorf("unknowns: %+v", e)
	}
	if z := summarize(nil, nil); z.Issues != 0 || z.CostPerMerged != nil {
		t.Errorf("empty: %+v", z)
	}
}

func TestToolsByLane(t *testing.T) {
	day := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	m := func(n int) time.Time { return day.Add(time.Duration(n) * time.Minute) }
	no, yes := false, true
	rows := []dispatchRow{{Lane: "alpha", At: m(0), End: m(10)}, {Lane: "beta", At: m(20), End: m(30)}}
	calls := []toolCall{
		{m(1), "t1", &yes}, {m(2), "t2", &no}, {m(3), "t1", nil},
		{m(15), "t1", &yes}, // between dispatches
		{m(21), "t3", &no},
	}
	got := toolsByLane(rows, calls)
	if len(got) != 3 || got[0].Lane != "alpha" || got[0].Calls != 3 || got[1].Lane != "" || got[2].Lane != "beta" {
		t.Fatalf("lanes: %+v", got)
	}
	if a := got[0].Tools; a[0].Tool != "t1" || a[0].Calls != 2 || a[0].Failed != 0 || a[1].Tool != "t2" || a[1].Failed != 1 {
		t.Errorf("alpha tools: %+v", a)
	}
	if len(toolsByLane(nil, nil)) != 0 {
		t.Error("empty")
	}
}

// A manifest line longer than the scanner buffer is an error, not a silent end of the manifest.
func TestParseManifestLongLine(t *testing.T) {
	long := "[10:00:00] " + strings.Repeat("x", maxLine) + "\n[10:01:00] dispatch lane=alpha issue=#7 pid=1\n"
	if err := parseManifest("2026-01-02", "m.md", strings.NewReader(long), newRunLog()); err == nil {
		t.Error("no error for a line over maxLine")
	}
}
