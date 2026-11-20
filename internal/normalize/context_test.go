package normalize

import (
	"fmt"
	"path/filepath"
	"testing"

	"factorybench/internal/store"
)

func TestUpgradeV4ThenReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fb.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ev := func(id, typ, ts, payload string) {
		raw := fmt.Sprintf(`{"spec":"factorybench/v1","id":%q,"type":%q,"ts":%q,"attempt_id":"c1","payload":%s}`, id, typ, ts, payload)
		if _, err := store.InsertEvent(db, "contract", "f", id, typ, ts, raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	ev("c1:started", "attempt.started", "2026-11-21T10:00:00Z", `{"work_item":{"repo":"b/ctx","issue":2},"factory_config_version":"v","role":"context","labels":["max-turns:5","max-turns:5","scenario:s"]}`)
	ev("c1:turn:1", "context.turn", "2026-11-21T10:00:01Z", `{"n":1,"speaker":"person","words":3,"revealed":["i1"]}`)
	ev("c1:turn:2", "context.turn", "2026-11-21T10:00:02Z", `{"n":2,"speaker":"factory","move":"ask","words":2}`)
	ev("c1:score", "context.score", "2026-11-21T10:00:03Z", `{"scenario":"s","truth":{"items":2},"captured":{"full":1},"latent_by_move":{"show":1},"build":{"hidden_pass":1,"hidden_total":2}}`)
	for _, q := range []string{"DROP TABLE attempt_labels", "DROP TABLE context_turns", "DROP TABLE context_scores", "PRAGMA user_version = 4"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	if db, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	var v, n int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != store.SchemaVersion {
		t.Fatalf("user_version %d (%v)", v, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM context_turns").Scan(&n); err != nil || n != 0 {
		t.Fatalf("new tables start empty: %d %v", n, err)
	}
	if err := Run(db); err != nil {
		t.Fatal(err)
	}
	var revealed, byMove string
	var built, pass int
	if err := db.QueryRow(`SELECT count(*) FROM context_turns`).Scan(&n); err != nil || n != 2 {
		t.Errorf("context_turns %d (%v)", n, err)
	}
	if err := db.QueryRow(`SELECT revealed FROM context_turns WHERE n = 1`).Scan(&revealed); err != nil || revealed != `["i1"]` {
		t.Errorf("revealed %q (%v)", revealed, err)
	}
	if err := db.QueryRow(`SELECT latent_by_move, built, hidden_pass FROM context_scores`).Scan(&byMove, &built, &pass); err != nil || byMove != `{"show":1}` || built != 1 || pass != 1 {
		t.Errorf("context_scores %q %d %d (%v)", byMove, built, pass, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM attempt_labels WHERE attempt_id = 'c1'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("attempt_labels %d (a repeated label is one row) (%v)", n, err)
	}
}
