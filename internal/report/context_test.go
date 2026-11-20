package report

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func fturn(n int64, move string, words int64) mTurn {
	return mTurn{n: n, speaker: "factory", move: move, words: words}
}

func pturn(n int64, words int64, f func(*mTurn)) mTurn {
	t := mTurn{n: n, speaker: "person", words: words}
	if f != nil {
		f(&t)
	}
	return t
}

func metricMap(s *ctxStats) map[string]string {
	m := map[string]string{}
	for _, x := range s.metrics() {
		m[x.Label] = x.Value
	}
	return m
}

func TestContextNA(t *testing.T) {
	var empty ctxStats
	for k, v := range metricMap(&empty) {
		if v != "n/a" {
			t.Errorf("empty %s = %q, want n/a", k, v)
		}
	}
	var s ctxStats
	s.add([]mTurn{pturn(1, 7, nil), fturn(2, "ask", 3)}, nil)
	got := metricMap(&s)
	for _, k := range []string{"recall", "weighted recall", "G4 recall", "items per turn", "latent drawn", "traps raised", "traceability",
		"builder agreement", "hidden tests", "invented", "simulator violations", "bench cost (not factory cost)", "suggestions accepted",
		"don't-know recovered", "frustration"} {
		if got[k] != "n/a" {
			t.Errorf("unscored %s = %q, want n/a", k, got[k])
		}
	}
	if got["could not answer"] != "0% (0/1)" || got["context turns / conversation"] != "1.0 (1/1)" {
		t.Errorf("turn metrics: %v", got)
	}
	var z ctxStats
	z.add(nil, &mScore{})
	if g := metricMap(&z); g["recall"] != "n/a" || g["hidden tests"] != "n/a" || g["context turns / conversation"] != "n/a" {
		t.Errorf("zero score: %v", g)
	}
}

func TestContextDontKnowRecovered(t *testing.T) {
	dk := func(t *mTurn) { t.dontKnow = true }
	reveal := func(t *mTurn) { t.revealed = []string{"x"} }
	cases := map[string]struct {
		turns []mTurn
		want  string
	}{
		"show then reveal":       {[]mTurn{pturn(1, 1, dk), fturn(2, "show", 1), pturn(3, 1, reveal)}, "100% (1/1)"},
		"suggest then reveal":    {[]mTurn{pturn(1, 1, dk), fturn(2, "suggest", 1), pturn(3, 1, reveal)}, "100% (1/1)"},
		"show, nothing revealed": {[]mTurn{pturn(1, 1, dk), fturn(2, "show", 1), pturn(3, 1, nil)}, "0% (0/1)"},
		"ask then reveal":        {[]mTurn{pturn(1, 1, dk), fturn(2, "ask", 1), pturn(3, 1, reveal)}, "0% (0/1)"},
		"no factory turn after":  {[]mTurn{pturn(1, 1, dk)}, "0% (0/1)"},
		"no person turn after":   {[]mTurn{pturn(1, 1, dk), fturn(2, "show", 1)}, "0% (0/1)"},
		"two, one recovered": {[]mTurn{pturn(1, 1, dk), fturn(2, "show", 1), pturn(3, 1, func(t *mTurn) { dk(t); reveal(t) }),
			fturn(4, "mirror", 1), pturn(5, 1, reveal)}, "50% (1/2)"},
		"only the next person turn counts": {[]mTurn{pturn(1, 1, dk), fturn(2, "show", 1), pturn(3, 1, nil), fturn(4, "ask", 1), pturn(5, 1, reveal)}, "0% (0/1)"},
		"no don't-know":                    {[]mTurn{pturn(1, 1, nil), fturn(2, "show", 1), pturn(3, 1, reveal)}, "n/a"},
	}
	for name, c := range cases {
		var s ctxStats
		s.add(c.turns, nil)
		if got := metricMap(&s)["don't-know recovered"]; got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestContextWeightedAndG4Recall(t *testing.T) {
	var s ctxStats
	s.add(nil, &mScore{items: 10, g4: 2, full: 5, g4Full: 2})
	s.add(nil, &mScore{items: 10, g4: 0, full: 0, partial: 4})
	got := metricMap(&s)
	if got["weighted recall"] != "46% (11/24)" {
		t.Errorf("weighted recall %q", got["weighted recall"])
	}
	if got["G4 recall"] != "100% (2/2)" {
		t.Errorf("G4 recall %q", got["G4 recall"])
	}
	if got["recall"] != "35% (7/20)" {
		t.Errorf("recall %q", got["recall"])
	}
	var h ctxStats
	h.add(nil, &mScore{items: 4, g4: 2, full: 1, partial: 1, g4Full: 1, g4Partial: 1})
	if g := metricMap(&h); g["G4 recall"] != "75% (1.5/2)" || g["weighted recall"] != "56% (4.5/8)" {
		t.Errorf("G4 %q weighted %q", g["G4 recall"], g["weighted recall"])
	}
}

func TestContextSimViolationsMarked(t *testing.T) {
	var s ctxStats
	s.add(nil, &mScore{simViolations: 2})
	s.add(nil, &mScore{})
	if got := metricMap(&s)["simulator violations"]; got != "1.0 (2/2) / run ⚠ 1 run marked" {
		t.Errorf("simulator violations %q", got)
	}
}

func contextDB(t *testing.T) *sql.DB {
	return loadGolden(t, "../../testdata/golden/context_bench/events.json")
}

var ctxNow = time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)

func render(t *testing.T, f func(*bytes.Buffer) error) string {
	var b bytes.Buffer
	if err := f(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestContextSectionOnlyWithData(t *testing.T) {
	db := contextDB(t)
	out := render(t, func(b *bytes.Buffer) error { return Factories(db, b, false, nil, ctxNow) })
	for _, want := range []string{"\nContext\n", "recall                         35% (7/20)", "bench cost (not factory cost)"} {
		if !strings.Contains(out, want) {
			t.Errorf("factories lacks %q:\n%s", want, out)
		}
	}
	plain := loadGolden(t, "../../testdata/golden/gates_phase/events.json")
	for name, out := range map[string]string{
		"factories": render(t, func(b *bytes.Buffer) error { return Factories(plain, b, false, nil, ctxNow) }),
		"items":     render(t, func(b *bytes.Buffer) error { return Items(plain, b, "", "", nil, ctxNow) }),
	} {
		if strings.Contains(out, "Context") || strings.Contains(out, "recall") {
			t.Errorf("%s of a database without context data shows a Context section:\n%s", name, out)
		}
	}
	_, rows, err := Scorecard(db, false, ctxNow)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Section == "Context" {
			n++
			if r.Label == "recall" && (r.Dir != 1 || r.Values[1] != "n/a") {
				t.Errorf("recall row: %+v", r)
			}
			if r.Label == "simulator violations" && (len(r.Warn) != 2 || !r.Warn[0] || r.Warn[1]) {
				t.Errorf("violations row is not marked for the factory with a violating run only: %+v", r)
			}
			if r.Label == "recall" && r.Warn != nil {
				t.Errorf("recall row has a warning mark: %+v", r)
			}
		}
	}
	if n != len(contextMetrics) {
		t.Errorf("%d Context rows, want %d", n, len(contextMetrics))
	}
}

func TestLabelFilter(t *testing.T) {
	db := contextDB(t)
	ctx := func(labels ...string) string {
		return render(t, func(b *bytes.Buffer) error { return Context(db, b, "", labels) })
	}
	if out := ctx("max-turns:10"); !strings.Contains(out, "1 run with context data") || strings.Contains(out, "\n20 ") {
		t.Errorf("max-turns:10:\n%s", out)
	}
	if out := ctx("scenario:s1", "persona:talkative"); !strings.Contains(out, "1 run with context data") || !strings.Contains(out, "\n20 ") {
		t.Errorf("scenario + persona:\n%s", out)
	}
	if out := ctx("scenario:s1", "persona:unsure", "max-turns:20"); !strings.Contains(out, "no context data yet") {
		t.Errorf("labels that no attempt carries together:\n%s", out)
	}
	if out := ctx(); !strings.Contains(out, "2 runs with context data") {
		t.Errorf("no filter:\n%s", out)
	}
	items := render(t, func(b *bytes.Buffer) error { return Items(db, b, "", "", []string{"max-turns:20"}, ctxNow) })
	if strings.Contains(items, "acme/chat#3") || !strings.Contains(items, "1 (1 scored)") {
		t.Errorf("items --label:\n%s", items)
	}
	fac := render(t, func(b *bytes.Buffer) error { return Factories(db, b, false, []string{"max-turns:10"}, ctxNow) })
	if !strings.Contains(fac, "recall                         50% (5/10)") {
		t.Errorf("factories --label:\n%s", fac)
	}
	if out := render(t, func(b *bytes.Buffer) error { return Factories(db, b, false, []string{"nope"}, ctxNow) }); !strings.Contains(out, "no attempts yet") {
		t.Errorf("factories with a label nobody carries:\n%s", out)
	}
}

func TestContextOfAttempt(t *testing.T) {
	db := contextDB(t)
	c, err := ContextOfAttempt(db, "fa", "c1")
	if err != nil || c == nil {
		t.Fatalf("c1: %v %v", c, err)
	}
	if len(c.Turns) != 9 || !c.Scored || c.Turns[4].Move != "" || c.Turns[1].Move != "ask" || len(c.Turns[4].Revealed) != 2 ||
		c.Turns[2].Tags[0] != "dont_know" || c.Turns[2].TaggedBy != "simulator" {
		t.Errorf("c1: %+v", c)
	}
	if c.Turns[0].Frustration != nil || c.Turns[4].Frustration == nil || *c.Turns[4].Frustration != 1 {
		t.Errorf("frustration: %+v", c.Turns)
	}
	if none, err := ContextOfAttempt(db, "fa", "b1"); err != nil || none != nil {
		t.Errorf("build attempt has no context data: %v %v", none, err)
	}
	rows, item, err := WorkItem(db, "acme/chat", 2, time.Now())
	if err != nil || len(rows) != 2 || len(item.Units) != 2 || item.Units[0].Factory != "fa" || item.Units[0].Runs != "1 (1 scored)" || len(item.Runs) != 2 {
		t.Fatalf("WorkItem: %+v %+v %v", rows, item, err)
	}
	if r := item.Runs[1]; r.Attempt != "c2" || len(r.Labels) != 3 || len(r.Turns) != 3 || r.Turns[2].Words != 31 {
		t.Errorf("c2: %+v", r)
	}
	if _, item, err := WorkItem(db, "acme/chat", 3, time.Now()); err != nil || len(item.Units) != 0 || len(item.Runs) != 0 {
		t.Errorf("item without context data: %+v %v", item, err)
	}
}
