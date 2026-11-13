package report

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// singleSession renders every report of golden cases without a stage handoff: fb's runs from the metrics case
// (a build retried after a failure, with PRs, reviews and checks) and two single-session golden cases.
// fbOnlyCase writes the metrics golden case with only factory fb's batches (single session) and returns its path.
func fbOnlyCase(t *testing.T) string {
	b, err := os.ReadFile("../../testdata/golden/metrics/events.json")
	if err != nil {
		t.Fatal(err)
	}
	var gc map[string]json.RawMessage
	if err := json.Unmarshal(b, &gc); err != nil {
		t.Fatal(err)
	}
	var batches []map[string]any
	if err := json.Unmarshal(gc["batches"], &batches); err != nil {
		t.Fatal(err)
	}
	var fb []map[string]any
	for _, x := range batches {
		if x["factory"] == "fb" {
			fb = append(fb, x)
		}
	}
	if gc["batches"], err = json.Marshal(fb); err != nil {
		t.Fatal(err)
	}
	if b, err = json.Marshal(gc); err != nil {
		t.Fatal(err)
	}
	fbOnly := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(fbOnly, b, 0o644); err != nil {
		t.Fatal(err)
	}

	return fbOnly
}

func singleSession(t *testing.T) []byte {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	fbOnly := fbOnlyCase(t)
	var out bytes.Buffer
	for _, path := range []string{fbOnly, "../../testdata/golden/retry_failed_no_usage/events.json",
		"../../testdata/golden/plan_waiting/events.json"} {
		db := loadGolden(t, path)
		rows, err := db.Query(`SELECT DISTINCT repo, issue FROM attempts ORDER BY 1, 2`)
		if err != nil {
			t.Fatal(err)
		}
		type key struct {
			repo  string
			issue int64
		}
		var items []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.repo, &k.issue); err != nil {
				t.Fatal(err)
			}
			items = append(items, k)
		}
		rows.Close()
		for _, k := range items {
			if err := Item(db, &out, k.repo, k.issue, now); err != nil {
				t.Fatal(err)
			}
		}
		err = firstErr(Items(db, &out, "", "", now), Factories(db, &out, false, now), Factories(db, &out, true, now))
		if err != nil {
			t.Fatal(err)
		}
		ir, err := ItemRows(db, now)
		if err != nil {
			t.Fatal(err)
		}
		j, _ := json.MarshalIndent(ir, "", " ")
		out.Write(append(j, '\n'))
	}
	return out.Bytes()
}

// Runs without a stage handoff must report exactly what they did before staged factories were supported.
func TestSingleSessionUnchanged(t *testing.T) {
	got := singleSession(t)
	want, err := os.ReadFile("testdata/single_session.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("single-session reports changed:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// testdata/golden/staged_retry is shaped like vantrangradion/chat-bridge#29: plan (gemini, completed, opens the PR with
// failing tests) -> build (gemini, failed) -> build (opus, failed), parents inferred. CI fails on the plan's push at
// 10:12:30 and passes on the first push after the handoff (b1 started 10:16).
func TestStagedRun(t *testing.T) {
	db := loadGolden(t, "../../testdata/golden/staged_retry/events.json")
	d, err := loadMetrics(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for _, byConfig := range []bool{false, true} {
		us := buildUnits(d, byConfig, now)
		if len(us) != 1 {
			t.Fatalf("byConfig %v: %d units, want the staged run as one", byConfig, len(us))
		}
		u := us[0]
		got := fmt.Sprintf("%s | %s | ci %d/%d | escalations %v", u.group, u.attemptsText(), u.run.ciPass, u.run.ciN, u.escalations)
		want := "fs | 3 attempts · 2 stages · 1 retry (2 failed) | ci 1/1 | escalations []" // config columns only
		if byConfig {
			want = "fs fs@1+gemini | 3 attempts · 2 stages · 1 retry (2 failed) | ci 1/1 | escalations [fs@1+opus]"
		}
		if got != want {
			t.Errorf("byConfig %v:\n got %s\nwant %s", byConfig, got, want)
		}
	}
	rows, err := ItemRows(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if r := rows[0]; !r.Staged || r.Stages != 2 || r.Retries != 1 || r.Failed != 2 || r.CIFirstPush != "100% (1/1)" ||
		r.ToPR != "12m0s (staged run: not comparable, see time to merge)" {
		t.Errorf("item row %+v", r)
	}
	for _, c := range []struct{ role, parentRole, parentState, want string }{
		{"build", "plan", "completed", "handoff"},
		{"build", "plan", "failed", "handoff"}, // a new stage, even after a failure
		{"build", "build", "failed", "retry"},
		{"build", "build", "cancelled", "retry"},
		{"", "", "failed", "retry"},         // single-session rerun without roles
		{"build", "build", "completed", ""}, // same role after a success: neither
		{"build", "", "failed", ""},         // one role missing: not a handoff, and not the same role
	} {
		if got := StageKind(c.role, c.parentRole, c.parentState); got != c.want {
			t.Errorf("StageKind(%q, %q, %q) = %q, want %q", c.role, c.parentRole, c.parentState, got, c.want)
		}
	}
}

// A single-session factory reports the same whether or not another factory's staged run is in the database.
func TestSingleSessionBesideStaged(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	mixed, alone := loadGolden(t, "../../testdata/golden/metrics/events.json"), loadGolden(t, fbOnlyCase(t))
	for _, byConfig := range []bool{false, true} {
		cards := func(db *sql.DB) map[string]string { // "fb… label" -> value, for fb's columns
			groups, rows, err := Scorecard(db, byConfig, now)
			if err != nil {
				t.Fatal(err)
			}
			m := map[string]string{}
			for _, r := range rows {
				for i, g := range groups {
					if strings.HasPrefix(g, "fb") {
						m[g+" | "+r.Label] = r.Values[i]
					}
				}
			}
			return m
		}
		got, want := cards(mixed), cards(alone)
		for k, v := range got {
			// a row empty for every column is hidden, so a row only another factory fills shows n/a for fb
			if w, ok := want[k]; ok && w != v || !ok && v != "n/a" {
				t.Errorf("byConfig %v: %s = %q beside a staged run, %q alone", byConfig, k, v, w)
			}
		}
		if len(got) < len(want) {
			t.Errorf("byConfig %v: fb has %d values beside a staged run, %d alone", byConfig, len(got), len(want))
		}
	}
	rows := func(db *sql.DB) []ItemRow {
		ir, err := ItemRows(db, now)
		if err != nil {
			t.Fatal(err)
		}
		var fb []ItemRow
		for _, r := range ir {
			if r.Factory == "fb" {
				fb = append(fb, r)
			}
		}
		return fb
	}
	g, _ := json.Marshal(rows(mixed))
	if w, _ := json.Marshal(rows(alone)); string(g) != string(w) {
		t.Errorf("fb item rows beside a staged run:\n%s\nalone:\n%s", g, w)
	}
}

// A check on the plan's push that only starts after the handoff is still the plan's push, not the build's first.
func TestStagedCIIgnoresLatePlanCheck(t *testing.T) {
	db := loadGolden(t, "../../testdata/golden/staged_retry/events.json")
	// s1 (the plan's push, already checked at 10:12:30) gets a second, failing run at 10:17, after the handoff at 10:16
	if _, err := db.Exec(`INSERT INTO checks VALUES ('acme/app', 30, 'lint', 's1', 'failure', '2026-11-11T10:17:00Z', '2026-11-11T10:18:00Z')`); err != nil {
		t.Fatal(err)
	}
	d, err := loadMetrics(db)
	if err != nil {
		t.Fatal(err)
	}
	if u := buildUnits(d, false, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))[0]; u.run.ciPass != 1 || u.run.ciN != 1 {
		t.Errorf("CI on first push %d/%d, want 1/1 from s2", u.run.ciPass, u.run.ciN)
	}
}

// report item totals: a failed stage that was handed off is not a failure of the run, and with several factories
// the stage counts name their factory.
func TestTotalsStaged(t *testing.T) {
	end := sql.NullString{String: "2026-11-11T10:05:00Z", Valid: true}
	att := func(f, id, state, parent, stage string, staged bool) *attempt {
		return &attempt{factory: f, id: id, state: state, started: "2026-11-11T10:00:00Z", ended: end,
			parent: sql.NullString{String: parent, Valid: parent != ""}, stage: stage, staged: staged}
	}
	fs := []*attempt{att("fs", "p", "failed", "", "", true), att("fs", "b", "completed", "p", "handoff", true)}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if got := totals(fs, now, true); !strings.HasPrefix(got, "2 attempts · 2 stages · 0 retries (1 completed, 1 failed, then handed off)") {
		t.Errorf("one factory: %s", got)
	}
	all := append(fs, att("fx", "x1", "failed", "", "", false), att("fx", "x2", "completed", "x1", "retry", false))
	if got := totals(all, now, false); !strings.HasPrefix(got, "4 attempts · fs: 2 stages · 0 retries (2 completed, 1 failed, 1 failed, then handed off)") {
		t.Errorf("all factories: %s", got)
	}
}
