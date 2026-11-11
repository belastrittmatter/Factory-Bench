// Package intake serves POST /v1/events. It writes only the events table.
package intake

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"factorybench/internal/contract"
	"factorybench/internal/store"
)

// Rejected names one event that failed validation and why.
type Rejected struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// Response is the body of a successful POST /v1/events.
type Response struct {
	Accepted   int        `json:"accepted"`
	Duplicates int        `json:"duplicates"`
	Rejected   []Rejected `json:"rejected"`
}

// Handler returns the /v1/events handler. after runs once a batch is stored (serve uses it to re-normalize).
func Handler(db *sql.DB, after func()) http.Handler {
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		factory, err := store.FactoryByToken(db, token)
		if err != nil {
			internalError(w, err)
			return
		}
		if factory == "" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		var batch []json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&batch); err != nil {
			http.Error(w, "body must be a JSON array of events", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		resp := Response{Rejected: []Rejected{}}
		for _, raw := range batch {
			dup, rej, err := insert(db, factory, raw)
			if err != nil {
				internalError(w, err)
				return
			}
			switch {
			case rej != nil:
				resp.Rejected = append(resp.Rejected, *rej)
			case dup:
				resp.Duplicates++
			default:
				resp.Accepted++
			}
		}
		if after != nil && resp.Accepted+len(resp.Rejected) > 0 {
			after()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}

func internalError(w http.ResponseWriter, err error) {
	log.Print("intake: ", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// insert validates and stores one contract event for factory. Invalid events are stored with rejected set.
func insert(db *sql.DB, factory string, raw json.RawMessage) (dup bool, rej *Rejected, err error) {
	ev, _, verr := contract.Validate(raw)
	ts := store.Now()
	if t, err := time.Parse(time.RFC3339Nano, ev.TS); err == nil {
		ts = store.FormatTS(t)
	}
	id := ev.ID
	if id == "" { // still keep it, under a key derived from its content
		s := sha256.Sum256(raw)
		id = "nokey:" + hex.EncodeToString(s[:8])
	}
	var reason *string
	if verr != nil {
		msg := verr.Error()
		reason = &msg
		rej = &Rejected{ID: ev.ID, Error: msg}
	}
	dup, err = store.InsertEvent(db, "contract", factory, id, ev.Type, ts, string(raw), reason)
	return dup, rej, err
}
