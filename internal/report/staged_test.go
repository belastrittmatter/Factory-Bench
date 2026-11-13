package report

import (
	"bytes"
	"encoding/json"
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
