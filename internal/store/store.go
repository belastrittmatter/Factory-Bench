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
		_, err = db.Exec(schema + ";PRAGMA user_version = 2")
	case 1: // attempts is derived, so the new column only needs a rebuild
		if _, err = db.Exec("ALTER TABLE attempts ADD COLUMN waiting_since TEXT; PRAGMA user_version = 2"); err == nil {
			log.Print("database migrated to v2: run `factorybench replay` to fill attempts.waiting_since")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
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

// Outcome is a work item's outcome from the states of its linked PRs: merged > rejected (all closed) > open.
func Outcome(prStates map[string]bool) string {
	switch {
	case prStates["merged"]:
		return "merged"
	case prStates["closed"] && !prStates["open"]:
		return "rejected"
	}
	return "open"
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
