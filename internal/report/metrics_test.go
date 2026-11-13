package report

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factorybench/internal/normalize"
	"factorybench/internal/store"
)

// Hand-computed from testdata/golden/metrics/events.json (fa: #10 attempts p1,b1 and #11 attempts p3,b3; fb: #10 attempts c1,c2):
//
//	                     fa                                         fb
//	items, attempts      2, 4 (0 failed), 5 sessions                1, 2 (1 failed, c1 has no usage), 2 sessions
//	tokens               1300+5500+1100 (#10) + 600+1100 (#11) = 9600   2400+700 = 3100, coverage 1/2
//	time #10             10:00-10:21 = 21m, wait 7m -> active 14m   10:00-10:13 = 13m, active 13m
//	time #11             11:00-11:15 = 15m, wait 8m -> active 7m
//	cost                 .012+.03+.01+.005 = .057, 1 unpriced       .02+.006 = .026, 0 unpriced
//	merged items         #10 (PR 20 merged)                         0 (PR 21 closed)
//	verify tokens / PR   b1 verify 1100 + b3 review 1100 = 2200 / 2 c2 review 700 / 1
//	closed by agent      #10 closed by fa-bot: 1/2                  0/1
//	plan first-pass      #10 steered before resume: fail, #11 pass  no plans: 0/0
//	PR first-pass        PR20 first human CR, PR22 alice APPROVED (fa-bot's own approval ignored): 1/2   PR21 CR: 0/1
//	review rounds        PR20 1 CR, PR22 0 CR: 1 / 2 PRs            PR21 2 CR: 2 / 1
//	human interventions  #10 wait+steer+human commit+CR = 4, #11 wait = 1 -> 5/2   2 CR -> 2/1
//	escaped              PR20: CR 10:40 + commit 10:45 after b1 ended 10:21 = 2 on 1/2 PRs   PR21: 2 CR after 10:13 = 2 on 1/1
//	rejected events      1 (usage without model)                    0
func TestScorecard(t *testing.T) {
	db := loadGolden(t, "../../testdata/golden/metrics/events.json")
	d, err := loadMetrics(db)
	if err != nil {
		t.Fatal(err)
	}
	cards := buildCards(d, false, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if len(cards) != 2 {
		t.Fatalf("got %d cards", len(cards))
	}
	type want struct {
		items, attempts, failed, sessions, withUsage                  int
		tokens, verifyTok                                             int64
		elapsed, active                                               time.Duration
		cost                                                          float64
		unpriced, merged, prs, closed, planPass, planN, prPass, prN   int
		changesReq, reviewedPRs, human, escaped, escapedPRs, rejected int
	}
	wants := map[string]want{
		"fa": {2, 4, 0, 5, 4, 9600, 2200, 36 * time.Minute, 21 * time.Minute, 0.057, 1, 1, 2, 1, 1, 2, 1, 2, 1, 2, 5, 2, 1, 1},
		"fb": {1, 2, 1, 2, 1, 3100, 700, 13 * time.Minute, 13 * time.Minute, 0.026, 0, 0, 1, 0, 0, 0, 0, 1, 2, 1, 2, 2, 1, 0},
	}
	for _, c := range cards {
		got := want{c.items, c.attempts, c.failed, c.sessions, c.withUsage, c.tokens, c.verifyTok, c.elapsed, c.active,
			c.cost.rep + c.cost.est, c.cost.nUnk, c.merged, c.prs, c.closed, c.planPass, c.planN, c.prPass, c.prN,
			c.changesReq, c.reviewedPRs, c.human, c.escaped, c.escapedPRs, c.rejected}
		w := wants[c.group]
		if diff := got.cost - w.cost; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s cost %v, want %v", c.group, got.cost, w.cost)
		}
		got.cost = w.cost
		if got != w {
			t.Errorf("%s:\n got %+v\nwant %+v", c.group, got, w)
		}
	}

	// fb's c1 sent no usage, so its cost is a lower bound even though every session is priced.
	for _, m := range scorecard {
		if m.label == "cost total" {
			if got, want := m.val(cards[1]), "$0.03 reported +? (1 attempt(s) without usage)"; got != want {
				t.Errorf("fb cost total %q, want %q", got, want)
			}
		}
	}

	// --by config splits fa into fa@1 (#10) and fa@2 (#11).
	var groups []string
	for _, c := range buildCards(d, true, time.Time{}) {
		groups = append(groups, c.group)
	}
	if g := []string{"fa fa@1", "fa fa@2", "fb fb@2"}; len(groups) != 3 || groups[0] != g[0] || groups[1] != g[1] || groups[2] != g[2] {
		t.Errorf("by config groups %v, want %v", groups, g)
	}
}

// loadGolden stores a golden case's events directly (contract rows hold the whole raw event) and normalizes them.
func loadGolden(t *testing.T, path string) *sql.DB {
	var gc struct {
		Factories map[string]json.RawMessage `json:"factories"` // id -> commit email, or id -> {"commit_email", "github_login"}
		Batches   []struct {
			Factory string            `json:"factory"`
			Events  []json.RawMessage `json:"events"`
		} `json:"batches"`
		GitHub []struct {
			Type, ID, TS string
			Payload      json.RawMessage
		} `json:"github"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &gc); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	for id, raw := range gc.Factories {
		var f struct {
			CommitEmail string `json:"commit_email"`
			GitHubLogin string `json:"github_login"`
		}
		if json.Unmarshal(raw, &f) != nil {
			if err := json.Unmarshal(raw, &f.CommitEmail); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.AddFactory(db, id, f.CommitEmail, f.GitHubLogin); err != nil {
			t.Fatal(err)
		}
	}
	for _, batch := range gc.Batches {
		for _, raw := range batch.Events {
			var e struct {
				ID, Type, TS string
				Payload      struct {
					Model *string `json:"model"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatal(err)
			}
			var rejected *string
			if e.Type == "usage" && e.Payload.Model == nil { // the case's one invalid event
				rejected = new(string)
				*rejected = "usage: model required"
			}
			if _, err := store.InsertEvent(db, "contract", batch.Factory, e.ID, e.Type, e.TS, string(raw), rejected); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, g := range gc.GitHub {
		if _, err := store.InsertEvent(db, "github", "", g.ID, g.Type, g.TS, string(g.Payload), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := normalize.Run(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestUSD(t *testing.T) {
	for v, want := range map[float64]string{0: "$0.00", 0.004: "<$0.01", 0.005: "$0.01", 0.7448: "$0.74", 1.3206: "$1.32"} {
		if got := usd(v); got != want {
			t.Errorf("usd(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestCIStats(t *testing.T) {
	run := func(sha, concl, started, done string) mCheck {
		return mCheck{name: "test", sha: sha, concl: concl, started: started, done: done}
	}
	cs := ciStats([]mCheck{
		run("a", "failure", "2026-11-11T10:00:00Z", "2026-11-11T10:02:00Z"),
		run("a", "success", "2026-11-11T10:05:00Z", "2026-11-11T10:09:00Z"), // same sha failed and passed: flaky
		run("b", "timed_out", "2026-11-11T11:00:00Z", "2026-11-11T11:06:00Z"),
		run("c", "success", "2026-11-11T12:00:00Z", "2026-11-11T12:10:00Z"),
		run("d", "skipped", "", ""),                     // never ran: ignored
		run("e", "", "2026-11-11T13:00:00Z", ""),        // still running: ignored
		run("c", "success", "2026-11-11T12:20:00Z", ""), // finished without completed_at: counts, no runtime
	})
	// runtimes 2m, 4m, 6m, 10m: even count, median is the mean of the middle two
	want := checkStats{fails: 2, runs: 5, median: "5m0s", flaky: 1, shas: 3}
	if cs != want {
		t.Errorf("ciStats = %+v, want %+v", cs, want)
	}
	if cs := ciStats(nil); cs.median != "n/a" || cs.runs != 0 {
		t.Errorf("ciStats(nil) = %+v", cs)
	}
}

func TestItemRows(t *testing.T) {
	db := loadGolden(t, "../../testdata/golden/metrics/events.json")
	rows, err := ItemRows(db, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// review rounds are the hand-computed ones from TestScorecard; unknowns stay "?" / "n/a"
	want := map[string]string{
		"fa acme/app#10": "merged | 1 on 1 reviewed PRs | ? | 20m0s (staged run: not comparable, see time to merge) | 0% (0/1)", // plan -> build
		"fa acme/app#11": "open | 0 on 1 reviewed PRs | ? | 14m0s (staged run: not comparable, see time to merge) | 100% (1/1)",
		"fb acme/app#10": "rejected | 2 on 1 reviewed PRs | ? | 12m0s | 0% (0/1)",
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		if got := strings.Join([]string{r.Outcome, r.ReviewRounds, r.Size, r.ToPR, r.CIFirstPush}, " | "); got != want[r.Factory+" "+r.Item] {
			t.Errorf("%s %s: %q, want %q", r.Factory, r.Item, got, want[r.Factory+" "+r.Item])
		}
	}
	for _, r := range rows { // the numeric fields say what the text columns say
		if r.HasPR != (r.PRs != "") || r.Merged != (r.Outcome == "merged") || (r.ToPRSeconds == nil) != (r.ToPR == "n/a") || r.CostUSD == nil {
			t.Errorf("%s %s numeric fields disagree with the text: %+v", r.Factory, r.Item, r)
		}
		if r.CostLowerBound != strings.HasSuffix(r.Cost, "+?") {
			t.Errorf("%s %s lower bound %v, cost %q", r.Factory, r.Item, r.CostLowerBound, r.Cost)
		}
	}
}

func TestScorecardRows(t *testing.T) {
	db := loadGolden(t, "../../testdata/golden/metrics/events.json")
	groups, rows, err := Scorecard(db, false, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range rows {
		got[r.Label] = r.Values
	}
	if len(groups) != 2 || groups[0] != "fa" || got["review rounds / PR"][1] != "2.0 (2/1)" || got["work items"][0] != "2" {
		t.Errorf("groups %v, rows %v", groups, got)
	}
}
