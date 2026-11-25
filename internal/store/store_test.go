package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"
	"regexp"
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
	// v5 is the current schema without the v5 -> v6 columns; v4 also without send_backs and usage.session, v3 also without usage.step
	v5 := regexp.MustCompile(`,( *(?:--[^\n]*)?)(?:\n[^\n]*\(v5 -> v6 ALTER\))+\n\);`).ReplaceAllString(schema, " $1\n);")
	if strings.Contains(v5, "v5 -> v6") || strings.Contains(v5, "harness") {
		t.Fatal("schema.sql no longer has the v5 -> v6 columns this test removes")
	}
	noSendBacks := regexp.MustCompile(`CREATE TABLE send_backs \([^;]*\n\);`).ReplaceAllString(v5, "")
	if noSendBacks == v5 {
		t.Fatal("schema.sql no longer has the send_backs table this test removes")
	}
	v4 := strings.Replace(noSendBacks, "\n  session           TEXT                    -- agent session id (OTLP session.id or payload.session), NULL if not sent (v4 -> v5 ALTER)", "", 1)
	v4 = strings.Replace(v4, "step              TEXT,                   -- worker step", "step              TEXT                    -- worker step", 1)
	if v4 == noSendBacks || strings.Contains(v4, "  session ") {
		t.Fatal("schema.sql no longer has the usage.session line this test removes")
	}
	v3 := strings.Replace(v4, "\n  step              TEXT                    -- worker step as the factory names it (factorybench.step), NULL if not sent (v3 -> v4 ALTER)", "", 1)
	v3 = strings.Replace(v3, "agent_id          TEXT,                   -- subagent", "agent_id          TEXT                    -- subagent", 1)
	if v3 == v4 || strings.Contains(v3, "  step ") {
		t.Fatal("schema.sql no longer has the usage.step line this test removes")
	}
	for v, old := range map[int]string{1: v1, 2: schemaV2, 3: v3, 4: v4, 5: v5} {
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
			if err := db.QueryRow("PRAGMA user_version").Scan(&n); err != nil || n != SchemaVersion {
				t.Fatalf("user_version %d (%v), want %d", n, err, SchemaVersion)
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
func TestTokenLifecycle(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	who := func(tok string) string {
		id, err := FactoryByToken(db, tok)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old, err := AddFactory(db, "fa", "Bot@x.io", "")
	if err != nil || who(old) != "fa" {
		t.Fatalf("add: %v", err)
	}
	fresh, err := RotateToken(db, "fa")
	if err != nil || who(old) != "" || who(fresh) != "fa" {
		t.Fatalf("rotate: old still works or new does not (%v)", err)
	}
	if err := RevokeToken(db, "fa"); err != nil || who(fresh) != "" || who("") != "" {
		t.Fatalf("revoke: token still works (%v)", err)
	}
	if err := UpdateFactory(db, "fa", "New@x.io", "Bot"); err != nil {
		t.Fatal(err)
	}
	var email, login string
	db.QueryRow(`SELECT commit_email, github_login FROM factories WHERE factory_id = 'fa'`).Scan(&email, &login)
	if email != "new@x.io" || login != "bot" {
		t.Errorf("update stored %q %q", email, login)
	}
	if err := RevokeToken(db, "nope"); err != ErrNoFactory {
		t.Errorf("revoke unknown: %v", err)
	}
}
