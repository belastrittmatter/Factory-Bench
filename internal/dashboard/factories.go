package dashboard

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"factorybench/internal/normalize"
	"factorybench/internal/store"
)

// Factory is one registered factory as the Factories page lists it. Tokens are never returned, only whether one works.
type Factory struct {
	ID          string `json:"id"`
	CommitEmail string `json:"commit_email"`
	GitHubLogin string `json:"github_login"`
	CreatedAt   string `json:"created_at"`
	Active      bool   `json:"active"`
	Events      int    `json:"events"`
	LastEvent   string `json:"last_event"` // received_at of its newest contract event, "" if none
	Attempts    int    `json:"attempts"`
}

var (
	idRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+$`)
	loginRE = regexp.MustCompile(`^[A-Za-z0-9-]{0,39}$`)
)

func bad(msg string) error { return httpError{http.StatusBadRequest, msg} }

// factoryRoutes adds GET /api/factories and, when managing, POST /api/factories/{add,rotate,revoke,update}.
func (s *server) factoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/factories", s.json(func(r *http.Request) (any, error) { return s.factories() }))
	type form struct {
		ID          string `json:"id"`
		CommitEmail string `json:"commit_email"`
		GitHubLogin string `json:"github_login"`
	}
	post := func(path string, do func(form) (any, error)) {
		mux.HandleFunc("/api/factories/"+path, s.serve(http.MethodPost, func(r *http.Request) (any, error) {
			if err := s.allowWrite(r); err != nil {
				return nil, err
			}
			var f form
			if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(&f); err != nil {
				return nil, bad("body must be JSON")
			}
			f.ID, f.CommitEmail, f.GitHubLogin = strings.TrimSpace(f.ID), strings.TrimSpace(f.CommitEmail), strings.TrimSpace(f.GitHubLogin)
			v, err := do(f)
			if errors.Is(err, store.ErrNoFactory) {
				return nil, httpError{http.StatusNotFound, "no factory " + f.ID}
			}
			return v, err
		}))
	}
	identity := func(f form) error {
		if !emailRE.MatchString(f.CommitEmail) {
			return bad("commit email must look like name@example.com")
		}
		if !loginRE.MatchString(f.GitHubLogin) {
			return bad("GitHub login may only use letters, digits and dashes (max 39)")
		}
		return nil
	}
	post("add", func(f form) (any, error) {
		if !idRE.MatchString(f.ID) {
			return nil, bad("factory id must start with a letter or digit and use only letters, digits, '.', '_' or '-' (max 64)")
		}
		if err := identity(f); err != nil {
			return nil, err
		}
		var n int
		if err := s.admin.QueryRow(`SELECT count(*) FROM factories WHERE factory_id = ?`, f.ID).Scan(&n); err != nil {
			return nil, err
		} else if n > 0 {
			return nil, bad("factory " + f.ID + " already exists; issue it a new token instead")
		}
		token, err := store.AddFactory(s.admin, f.ID, f.CommitEmail, f.GitHubLogin)
		return map[string]string{"token": token}, err
	})
	post("rotate", func(f form) (any, error) {
		token, err := store.RotateToken(s.admin, f.ID)
		return map[string]string{"token": token}, err
	})
	post("revoke", func(f form) (any, error) { return map[string]bool{"ok": true}, store.RevokeToken(s.admin, f.ID) })
	post("update", func(f form) (any, error) {
		if err := identity(f); err != nil {
			return nil, err
		}
		if err := store.UpdateFactory(s.admin, f.ID, f.CommitEmail, f.GitHubLogin); err != nil {
			return nil, err
		}
		// commit email and login decide factory vs human commits and closes: rebuild what depends on them
		return map[string]bool{"ok": true}, normalize.Run(s.admin)
	})
}

// allowWrite lets a change through only from this machine's own page. serve already checked the loopback Host
// (DNS rebinding); the custom header can't be sent cross-origin without a CORS preflight, which nothing here answers.
func (s *server) allowWrite(r *http.Request) error {
	if s.admin == nil {
		return httpError{http.StatusForbidden, "managing factories is off: run the dashboard on a loopback address to make changes"}
	}
	if r.Header.Get("X-FactoryBench") != "1" {
		return httpError{http.StatusForbidden, "missing X-FactoryBench header"}
	}
	if v := r.Header.Get("Sec-Fetch-Site"); v != "" && v != "same-origin" && v != "none" {
		return httpError{http.StatusForbidden, "cross-site request refused"}
	}
	return nil
}

func (s *server) factories() ([]Factory, error) {
	out := []Factory{}
	err := store.Each(s.db, `SELECT f.factory_id, coalesce(f.commit_email, ''), coalesce(f.github_login, ''), f.created_at, f.token_hash,
		(SELECT count(*) FROM events e WHERE e.source = 'contract' AND e.factory_id = f.factory_id),
		(SELECT coalesce(max(received_at), '') FROM events e WHERE e.source = 'contract' AND e.factory_id = f.factory_id),
		(SELECT count(*) FROM attempts a WHERE a.factory_id = f.factory_id)
		FROM factories f ORDER BY f.factory_id`, func(r *sql.Rows) error {
		var f Factory
		var h string
		err := r.Scan(&f.ID, &f.CommitEmail, &f.GitHubLogin, &f.CreatedAt, &h, &f.Events, &f.LastEvent, &f.Attempts)
		f.Active = !store.Revoked(h)
		out = append(out, f)
		return err
	})
	return out, err
}
