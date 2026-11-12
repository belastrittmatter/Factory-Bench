// Package store owns the SQLite database: migration, raw event insert, and factories.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// SchemaVersion is the user_version that Open creates or migrates to.
const SchemaVersion = 3

// Open opens (and on first use migrates) the SQLite database at path.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return nil, err
	}
	switch v {
	case 0:
		_, err = db.Exec(fmt.Sprintf("%s;PRAGMA user_version = %d", schema, SchemaVersion))
	case 1, 2:
		if err = migrate(db, v); err == nil {
			log.Print("database migrated to v3: run `factorybench replay` to fill the new derived tables and columns")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// migrate upgrades a v1 or v2 database to v3 in one transaction. Only events holds source data;
// every other change is to derived tables, which `replay` refills.
func migrate(db *sql.DB, from int) error {
	q := ""
	if from < 2 {
		q = "ALTER TABLE attempts ADD COLUMN waiting_since TEXT;"
	}
	// SQLite can't alter the CHECK on events.source to allow 'otlp', so the table is copied.
	q += "ALTER TABLE events RENAME TO events_v2;" + create("events") + ";INSERT INTO events SELECT * FROM events_v2; DROP TABLE events_v2;" +
		create("tool_calls") + ";" + create("agent_runs") + ";" + create("api_errors") + `;
		ALTER TABLE usage ADD COLUMN agent_id TEXT;
		ALTER TABLE prs ADD COLUMN additions INTEGER;
		ALTER TABLE prs ADD COLUMN deletions INTEGER;
		ALTER TABLE prs ADD COLUMN changed_files INTEGER;
		PRAGMA user_version = 3`
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(q); err != nil {
		return err
	}
	return tx.Commit()
}

// create returns the CREATE TABLE statement for table from schema.sql, so migrations and fresh databases agree.
func create(table string) string {
	s := schema[strings.Index(schema, "CREATE TABLE "+table+" ("):]
	return s[:strings.Index(s, "\n);")+3]
}

// Querier is a *sql.DB or a *sql.Tx.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// Each runs query with args and calls scan once per row.
func Each(db Querier, query string, scan func(*sql.Rows) error, args ...any) error {
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Now returns the current time in the format used for every stored timestamp: fixed width, so text order is time order.
func Now() string { return FormatTS(time.Now()) }

// FormatTS formats t in the Now format.
func FormatTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// InsertEvent stores one raw event. A repeated (source, factory_id, id) is a duplicate, not an error,
// except that a re-send replaces a previously rejected row; an accepted row is never overwritten.
func InsertEvent(db *sql.DB, source, factoryID, id, typ, ts, payload string, rejected *string) (dup bool, err error) {
	res, err := db.Exec(`INSERT INTO events (source, factory_id, id, type, ts, received_at, payload, rejected)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, factory_id, id) DO UPDATE SET type = excluded.type, ts = excluded.ts,
		  received_at = excluded.received_at, payload = excluded.payload, rejected = excluded.rejected
		WHERE events.rejected IS NOT NULL`, source, factoryID, id, typ, ts, Now(), payload, rejected)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 0, err
}

func hash(token string) string { s := sha256.Sum256([]byte(token)); return hex.EncodeToString(s[:]) }

// AddFactory registers a factory and returns its API token; only the token's hash is stored.
// githubLogin may be empty (stored as NULL).
func AddFactory(db *sql.DB, id, commitEmail, githubLogin string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := "fbt_" + hex.EncodeToString(b)
	var login *string
	if githubLogin != "" {
		l := strings.ToLower(githubLogin)
		login = &l
	}
	_, err := db.Exec(`INSERT INTO factories (factory_id, token_hash, commit_email, github_login, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, hash(token), strings.ToLower(commitEmail), login, Now())
	return token, err
}

// FactoryByToken returns the factory id for a token, or "" if the token is unknown.
func FactoryByToken(db *sql.DB, token string) (string, error) {
	var id string
	err := db.QueryRow(`SELECT factory_id FROM factories WHERE token_hash = ?`, hash(token)).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}
