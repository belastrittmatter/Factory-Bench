package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// singleSession renders every report of golden cases without a stage handoff: fb's runs from the metrics case
// (a build retried after a failure, with PRs, reviews and checks) and two single-session golden cases.
func singleSession(t *testing.T) []byte {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
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
		want := "fs | 3 attempts · 2 stages · 1 retry (2 failed) | ci 1/1 | escalations [fs@1+opus]"
		if byConfig {
			want = "fs fs@1+gemini" + want[2:]
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
