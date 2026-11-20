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

// One scored conversation, every number computed by hand:
//
//	factory turns 4 (ask, show, mirror, confirm); person turns 5, words 20+5+12+6+2 = 45
//	could not answer 1/5, asked again 1, asked what was given 0, suggestions 1 accepted / 1 answered
//	don't know (turn 3) -> show (4) -> turn 5 reveals 2 ids: recovered 1/1
//	frustration 1, 3, 0: mean 1.3, max 3
//	recall (4 + 0.5*2)/10; weighted (4 + 2*1 + 0.5*(2 + 2*1))/(10 + 2*2) = 8/14; G4 (1 + 0.5)/2; items/turn 5/4
//	latent 1/2 drawn by show; traps 1/2 raised, 1/2 followed; traceability (8-2)/8; builders 6/8; hidden 3/4
func TestContextFormulas(t *testing.T) {
	turns := []mTurn{
		pturn(1, 20, func(t *mTurn) { t.revealed = []string{"i1"} }),
		fturn(2, "ask", 10),
		pturn(3, 5, func(t *mTurn) { t.dontKnow, t.couldNot = true, true }),
		fturn(4, "show", 30),
		pturn(5, 12, func(t *mTurn) {
			t.suggestion = "accepted"
			t.frustration.Int64, t.frustration.Valid = 1, true
			t.revealed = []string{"i2", "L1"}
		}),
		fturn(6, "mirror", 8),
		pturn(7, 6, func(t *mTurn) { t.already = true; t.frustration.Int64, t.frustration.Valid = 3, true }),
		fturn(8, "confirm", 4),
		pturn(9, 2, func(t *mTurn) { t.confirmed = true; t.frustration.Valid = true }),
	}
	sc := &mScore{items: 10, g4: 2, latent: 2, traps: 2, noise: 1, full: 4, partial: 2, g4Full: 1, g4Partial: 1, capLatent: 1,
		latentByMove: map[string]int64{"show": 1}, raised: 1, followed: 1, invented: 2, unmarked: 1, tagErrors: 1,
		requirements: 8, untraced: 2, missingSlots: 1, agree: 6, total: 8, costPerson: .5, costJudge: .25, costBuilders: .125, built: true}
	sc.hiddenPass, sc.hiddenTotal, sc.escalations, sc.repairs = sql.NullInt64{Int64: 3, Valid: true}, sql.NullInt64{Int64: 4, Valid: true},
		sql.NullInt64{Int64: 1, Valid: true}, sql.NullInt64{Int64: 2, Valid: true}
	var s ctxStats
	s.add(turns, sc)
	want := map[string]string{
		"context turns / issue":         "4.0 (4/1)",
		"person words / issue":          "45.0 (45/1)",
		"moves":                         "ask 1, confirm 1, mirror 1",
		"could not answer":              "20% (1/5)",
		"asked again":                   "1.0 (1/1) / conversation",
		"asked what was given":          "0.0 (0/1) / conversation",
		"suggestions accepted":          "100% (1/1)",
		"don't-know recovered":          "100% (1/1)",
		"frustration":                   "1.3 mean, 3 max (3 turns)",
		"recall":                        "50% (5/10)",
		"weighted recall":               "57% (8/14)",
		"G4 recall":                     "75% (1.5/2)",
		"items per turn":                "1.2 (5/4)",
		"latent drawn":                  "50% (1/2) · show 1",
		"traps raised":                  "50% (1/2)",
		"traps followed":                "50% (1/2)",
		"invented":                      "2.0 (2/1) / run · 1 not marked assumed",
		"map tag errors":                "1.0 (1/1) / run",
		"noise left":                    "0.0 (0/1) / run",
		"missing slots":                 "1.0 (1/1) / run",
		"contradictions":                "0.0 (0/1) / run",
		"traceability":                  "75% (6/8)",
		"builder agreement":             "75% (6/8)",
		"hidden tests":                  "75% (3/4) · escalations 1.0 (1/1) · repairs 2.0 (2/1) / built run",
		"simulator violations":          "0.0 (0/1) / run",
		"bench cost (not factory cost)": "$0.88 (person $0.50, judge $0.25, builders $0.12, build $0.00)",
	}
	got := metricMap(&s)
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d metrics, want %d", len(got), len(want))
	}
	// the labels in docs/context-bench.md "Metrics", in order
	var labels []string
	for _, m := range contextMetrics {
		labels = append(labels, m.label)
	}
	if len(labels) != 26 || labels[0] != "context turns / issue" || labels[len(labels)-1] != "bench cost (not factory cost)" {
		t.Errorf("metric rows: %v", labels)
	}
}

// Nothing to divide by shows n/a, never 0% or NaN; a run with no score has turn metrics only.
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
	if got["could not answer"] != "0% (0/1)" || got["context turns / issue"] != "1.0 (1/1)" {
		t.Errorf("turn metrics: %v", got)
	}
	// scored but not built, with hidden_total 0 and no truth items
	var z ctxStats
	z.add(nil, &mScore{})
	if g := metricMap(&z); g["recall"] != "n/a" || g["hidden tests"] != "n/a" || g["context turns / issue"] != "0.0 (0/1)" {
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

// Weighted recall counts a G4 entry three times, G4 recall only G4 entries; summed over runs, not averaged.
func TestContextWeightedAndG4Recall(t *testing.T) {
	var s ctxStats
	s.add(nil, &mScore{items: 10, g4: 2, full: 5, g4Full: 2})  // (5 + 2*2) / (10 + 4) = 9/14
	s.add(nil, &mScore{items: 10, g4: 0, full: 0, partial: 4}) // 0.5*4 / 10 = 2/10
	got := metricMap(&s)
	if got["weighted recall"] != "46% (11/24)" { // 9 + 2 over 14 + 10
		t.Errorf("weighted recall %q", got["weighted recall"])
	}
	if got["G4 recall"] != "100% (2/2)" {
		t.Errorf("G4 recall %q", got["G4 recall"])
	}
	if got["recall"] != "35% (7/20)" { // (5 + 0) + (0 + 0.5*4) = 7 of 20
		t.Errorf("recall %q", got["recall"])
	}
	// half credit on G4 entries: G4 (1 + 0.5)/2; weighted (1 + 2*1 + 0.5*(1 + 2*1))/(4 + 2*2) = 4.5/8
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

// The context_bench golden case: c1 (max-turns 10, simulator tags) and c2 (max-turns 20, tagger tags); fb has none.
func TestContextSectionOnlyWithData(t *testing.T) {
	db := contextDB(t)
	out := render(t, func(b *bytes.Buffer) error { return Factories(db, b, false, nil, ctxNow) })
	for _, want := range []string{"\nContext\n", "recall                         35% (7/20)", "bench cost (not factory cost)"} {
		if !strings.Contains(out, want) {
			t.Errorf("factories lacks %q:\n%s", want, out)
		}
	}
	// a database without context data prints no Context section
	plain := loadGolden(t, "../../testdata/golden/gates_phase/events.json")
	for name, out := range map[string]string{
		"factories": render(t, func(b *bytes.Buffer) error { return Factories(plain, b, false, nil, ctxNow) }),
		"items":     render(t, func(b *bytes.Buffer) error { return Items(plain, b, "", "", nil, ctxNow) }),
	} {
		if strings.Contains(out, "Context") || strings.Contains(out, "recall") {
			t.Errorf("%s of a database without context data shows a Context section:\n%s", name, out)
		}
	}
	// scorecard data: Context rows carry their section and direction
	_, rows, err := Scorecard(db, false, nil, ctxNow)
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
	// every label must match
	if out := ctx("scenario:s1", "persona:talkative"); !strings.Contains(out, "1 run with context data") || !strings.Contains(out, "\n20 ") {
		t.Errorf("scenario + persona:\n%s", out)
	}
	if out := ctx("scenario:s1", "persona:unsure", "max-turns:20"); !strings.Contains(out, "no context data yet") {
		t.Errorf("labels that no attempt carries together:\n%s", out)
	}
	if out := ctx(); !strings.Contains(out, "2 runs with context data") {
		t.Errorf("no filter:\n%s", out)
	}
	// items: only the unit's matching attempts count; the unfiltered unit has 3 attempts
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

func TestContextReport(t *testing.T) {
	db := contextDB(t)
	out := render(t, func(b *bytes.Buffer) error { return Context(db, b, "", nil) })
	for _, want := range []string{
		"2 runs with context data · 2 scored · 5 factory turns",
		"10         1 (1 scored)  45.0 (45/1)         50% (5/10)", // effort curve: c1 at max-turns 10
		"20         1 (1 scored)  81.0 (81/1)         20% (2/10)", // c2: the resent turn 3 (31 words) replaced the first
		"show     1     2                       1             100% (1/1)",
		"suggest  1     0                       0             0% (0/1)",
		"s1        simulated  1 (1 scored)",
		"s1        real       1 (1 scored)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report context lacks %q:\n%s", want, out)
		}
	}
	if out := render(t, func(b *bytes.Buffer) error { return Context(db, b, "fb", nil) }); !strings.Contains(out, "no context data yet") {
		t.Errorf("factory fb:\n%s", out)
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
	item, err := ContextOfWorkItem(db, "acme/chat", 2)
	if err != nil || len(item.Units) != 1 || item.Units[0].Factory != "fa" || item.Units[0].Runs != "2 (2 scored)" || len(item.Runs) != 2 {
		t.Fatalf("ContextOfWorkItem: %+v %v", item, err)
	}
	if r := item.Runs[1]; r.Attempt != "c2" || r.Role != "context" || len(r.Labels) != 3 || len(r.Turns) != 3 || r.Turns[2].Words != 31 {
		t.Errorf("c2: %+v", r)
	}
	if item, err := ContextOfWorkItem(db, "acme/chat", 3); err != nil || len(item.Units) != 0 || len(item.Runs) != 0 {
		t.Errorf("item without context data: %+v %v", item, err)
	}
}
