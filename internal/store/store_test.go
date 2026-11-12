package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

//go:embed testdata/schema_v2.sql
var schemaV2 string

// columns returns every table's column names, in order, for comparing a migrated database with a fresh one.
func columns(t *testing.T, db *sql.DB) map[string]string {
	cols := map[string]string{}
	if err := Each(db, `SELECT m.name, group_concat(p.name, ',') FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' GROUP BY m.name`, func(r *sql.Rows) error {
		var tbl, c string
		err := r.Scan(&tbl, &c)
		cols[tbl] = c
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return cols
}

func TestOpenMigrates(t *testing.T) {
	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	want := columns(t, fresh)

	v1 := strings.Replace(schemaV2, "  waiting_since     TEXT,", "", 1)
	if v1 == schemaV2 {
		t.Fatal("schema_v2.sql no longer has the waiting_since line this test removes")
	}
	for v, old := range map[int]string{1: v1, 2: schemaV2} {
		t.Run(fmt.Sprint("v", v), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fb.db")
			raw, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(fmt.Sprintf("%s;PRAGMA user_version = %d", old, v)); err != nil {
				t.Fatal(err)
			}
			if _, err := InsertEvent(raw, "contract", "f", "a:1", "activity", "2026-11-11T09:00:00.000Z", `{"x":1}`, nil); err != nil {
				t.Fatal(err)
			}
			raw.Close()

			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRow("PRAGMA user_version").Scan(&n); err != nil || n != 3 {
				t.Fatalf("user_version %d (%v), want 3", n, err)
			}
			// positional inserts (normalize) need the same column order as a fresh database
			got := columns(t, db)
			if len(got) != len(want) {
				t.Errorf("tables %v, want %v", got, want)
			}
			for tbl, c := range want {
				if got[tbl] != c {
					t.Errorf("%s columns %s, want %s", tbl, got[tbl], c)
				}
			}
			var p string
			if err := db.QueryRow(`SELECT payload FROM events WHERE id = 'a:1'`).Scan(&p); err != nil || p != `{"x":1}` {
				t.Fatalf("event lost in migration: %q %v", p, err)
			}
			if _, err := InsertEvent(db, "otlp", "f", "s:0", "otlp.api_request", "2026-11-11T09:00:00.000Z", `{}`, nil); err != nil {
				t.Fatalf("otlp event after migration: %v", err)
			}
		})
	}
}
