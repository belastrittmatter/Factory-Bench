// Package github polls the GitHub REST API and stores what it sees as raw github events.
package github

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"factorybench/internal/store"
)

var api = "https://api.github.com" // a var so tests can point it at a fake server

// Stats counts the events one Reconcile stored and the ones it already had.
type Stats struct{ Added, Duplicates int }

var client = &http.Client{Timeout: 30 * time.Second}

// ErrNotFound is returned (wrapped) when the API answers 404, e.g. a repo the token can't see.
var ErrNotFound = errors.New("not found")

// reconciler holds one Reconcile run: the repo, its API token, and what it stored so far.
type reconciler struct {
	db          *sql.DB
	repo, token string
	st          Stats
}

// Reconcile stores the 100 most recently updated PRs of repo, the commits, reviews, and check runs
// (reruns included) of the factory PRs among them, and every issue of repo a factory started an attempt on, once each, as events.
// A factory PR is one linked by an accepted pr.linked event or authored by a factory's github_login; other PRs cost no
// requests beyond the list call. A closed PR that hasn't changed since the last run and whose details are already stored is skipped,
// to stay within GitHub's rate limit.
// shortcut: only the 100 most recently updated PRs and 100 commits/reviews/checks per PR (no pagination); add Link-header paging when a repo needs more.
// shortcut: every linked issue is re-fetched on every run (one request each); skip closed ones when repos have hundreds of work items.
func Reconcile(db *sql.DB, repo, token string) (Stats, error) {
	r := &reconciler{db: db, repo: repo, token: token}
	if err := r.prs(); err != nil {
		return r.st, err
	}
	return r.st, r.issues()
}

// Repos returns every repo a factory reported an accepted attempt.started for, plus extra, sorted and without duplicates.
// Polling them means a factory's new repo is picked up as soon as it reports work on it.
func Repos(db *sql.DB, extra []string) ([]string, error) {
	seen := map[string]bool{}
	for _, r := range extra {
		seen[r] = true
	}
	err := store.Each(db, `SELECT DISTINCT json_extract(payload, '$.payload.work_item.repo') FROM events
		WHERE source = 'contract' AND type = 'attempt.started' AND rejected IS NULL`, func(rows *sql.Rows) error {
		var r sql.NullString
		err := rows.Scan(&r)
		if r.Valid {
			seen[r.String] = true
		}
		return err
	})
	return slices.Sorted(maps.Keys(seen)), err
}

func (r *reconciler) get(path string, v any) error {
	req, err := http.NewRequest("GET", api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return fmt.Errorf("GET %s: %w", path, ErrNotFound)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// put stores one github event. Its id starts with the repo: PR and issue numbers repeat across repos.
func (r *reconciler) put(typ, id, ts string, payload any) (dup bool, err error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	dup, err = store.InsertEvent(r.db, "github", "", r.repo+":"+id, typ, ts, string(b), nil)
	if err != nil {
		return false, err
	}
	if dup {
		r.st.Duplicates++
	} else {
		r.st.Added++
	}
	return dup, nil
}

type pullRequest struct {
	Number    int64  `json:"number"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
	HTMLURL   string `json:"html_url"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// prs stores the recently updated PRs, and the details of the factory PRs among them.
func (r *reconciler) prs() error {
	// linked PR URLs and (lowercased) factory logins in one set; a URL never equals a login
	factory := map[string]bool{}
	err := store.Each(r.db, `SELECT json_extract(payload, '$.payload.pr_url') FROM events
		WHERE source = 'contract' AND type = 'pr.linked' AND rejected IS NULL
		UNION SELECT github_login FROM factories WHERE github_login <> ''`, func(rows *sql.Rows) error {
		var v sql.NullString
		err := rows.Scan(&v)
		if v.Valid {
			factory[v.String] = true
		}
		return err
	})
	if err != nil {
		return err
	}
	var prs []json.RawMessage
	if err := r.get("/repos/"+r.repo+"/pulls?state=all&sort=updated&direction=desc&per_page=100", &prs); err != nil {
		return err
	}
	for _, raw := range prs {
		var pr pullRequest
		if err := json.Unmarshal(raw, &pr); err != nil {
			return err
		}
		ts := normTS(pr.UpdatedAt)
		dup, err := r.put("pr", fmt.Sprintf("pr:%d:%s", pr.Number, pr.UpdatedAt), ts, map[string]any{"repo": r.repo, "pr": raw})
		if err != nil {
			return err
		}
		if dup && pr.State == "closed" {
			// a pr.linked event can arrive after the PR was stored closed, so skip only if details were fetched
			var fetched bool
			if err := r.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM events WHERE source = 'github' AND type = 'pr.commits'
				AND json_extract(payload, '$.repo') = ? AND json_extract(payload, '$.number') = ?)`, r.repo, pr.Number).Scan(&fetched); err != nil {
				return err
			}
			if fetched {
				continue // unchanged and closed: its commits and checks are already stored
			}
		}
		if !factory[pr.HTMLURL] && !factory[strings.ToLower(pr.User.Login)] {
			continue // not a factory PR: FactoryBench never reads its details
		}
		if err := r.prDetails(pr, ts); err != nil {
			return err
		}
	}
	return nil
}

// prDetails stores the commits, reviews, and check runs of one factory PR.
func (r *reconciler) prDetails(pr pullRequest, ts string) error {
	var commits []json.RawMessage
	if err := r.get(fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", r.repo, pr.Number), &commits); err != nil {
		return err
	}
	if _, err := r.put("pr.commits", fmt.Sprintf("commits:%d:%s", pr.Number, pr.Head.SHA), ts,
		map[string]any{"repo": r.repo, "number": pr.Number, "head_sha": pr.Head.SHA, "commits": commits}); err != nil {
		return err
	}
	var reviews []json.RawMessage
	if err := r.get(fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", r.repo, pr.Number), &reviews); err != nil {
		return err
	}
	rts, last := ts, ""
	for _, raw := range reviews {
		var rv struct {
			SubmittedAt string `json:"submitted_at"`
		}
		if err := json.Unmarshal(raw, &rv); err != nil {
			return err
		}
		if rv.SubmittedAt > last { // a pending review has none
			last = rv.SubmittedAt
			rts = normTS(last)
		}
	}
	if _, err := r.put("pr.reviews", fmt.Sprintf("reviews:%d:%d:%s", pr.Number, len(reviews), last), rts,
		map[string]any{"repo": r.repo, "number": pr.Number, "reviews": reviews}); err != nil {
		return err
	}
	var checks struct {
		CheckRuns []json.RawMessage `json:"check_runs"`
	}
	// filter=all keeps reruns; the default (latest) would drop a failed run once it is re-run
	if err := r.get(fmt.Sprintf("/repos/%s/commits/%s/check-runs?filter=all&per_page=100", r.repo, pr.Head.SHA), &checks); err != nil {
		return err
	}
	for _, c := range checks.CheckRuns {
		var cr struct {
			ID          int64   `json:"id"`
			Status      string  `json:"status"`
			StartedAt   string  `json:"started_at"`
			CompletedAt *string `json:"completed_at"`
		}
		if err := json.Unmarshal(c, &cr); err != nil {
			return err
		}
		done, ts := "", cr.StartedAt
		if cr.CompletedAt != nil {
			done, ts = *cr.CompletedAt, *cr.CompletedAt
		}
		if _, err := r.put("check_run", fmt.Sprintf("check:%d:%s:%s", cr.ID, cr.Status, done), normTS(ts),
			map[string]any{"repo": r.repo, "pr_number": pr.Number, "check_run": c}); err != nil {
			return err
		}
	}
	return nil
}

// issues stores every issue of the repo a factory started an attempt on.
func (r *reconciler) issues() error {
	var issues []int64
	err := store.Each(r.db, `SELECT DISTINCT json_extract(payload, '$.payload.work_item.issue') FROM events
		WHERE source = 'contract' AND type = 'attempt.started' AND rejected IS NULL
		  AND json_extract(payload, '$.payload.work_item.repo') = ? ORDER BY 1`, func(rows *sql.Rows) error {
		var n int64
		err := rows.Scan(&n)
		issues = append(issues, n)
		return err
	}, r.repo)
	if err != nil {
		return err
	}
	for _, n := range issues {
		var raw json.RawMessage
		if err := r.get(fmt.Sprintf("/repos/%s/issues/%d", r.repo, n), &raw); errors.Is(err, ErrNotFound) {
			continue // e.g. a test factory's made-up issue number
		} else if err != nil {
			return err
		}
		var is struct {
			UpdatedAt string `json:"updated_at"`
		}
		if err := json.Unmarshal(raw, &is); err != nil {
			return err
		}
		if _, err := r.put("issue", fmt.Sprintf("issue:%d:%s", n, is.UpdatedAt), normTS(is.UpdatedAt),
			map[string]any{"repo": r.repo, "issue": raw}); err != nil {
			return err
		}
	}
	return nil
}

func normTS(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return store.Now()
	}
	return store.FormatTS(t)
}
