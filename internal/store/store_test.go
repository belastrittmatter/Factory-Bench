package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMigratesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fb.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	v1 := strings.Replace(schema, "  waiting_since     TEXT,", "", 1)
	if v1 == schema {
		t.Fatal("schema.sql no longer has the waiting_since line this test removes")
	}
	if _, err := raw.Exec(v1 + ";PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("user_version %d (%v), want 2", v, err)
	}
	// positional inserts (normalize) need waiting_since as the 13th and last column, like a fresh database
	var cols []string
	if err := Each(db, "SELECT name FROM pragma_table_info('attempts')", func(r *sql.Rows) error {
		var c string
		err := r.Scan(&c)
		cols = append(cols, c)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(cols) != 13 || cols[12] != "waiting_since" {
		t.Errorf("attempts columns %v", cols)
	}
}
