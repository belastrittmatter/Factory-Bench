package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"factorybench/internal/store"
)

func TestReconcileFetchesDetailsOnlyForFactoryPRs(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/o/r/pulls":
			pr := `{"number":%d,"state":"open","updated_at":"2026-11-11T09:00:00Z","html_url":"https://github.com/o/r/pull/%d","user":{"login":"%s"},"head":{"sha":"sha%d"}}`
			closed := `{"number":4,"state":"closed","updated_at":"2026-11-11T09:00:00Z","html_url":"https://github.com/o/r/pull/4","user":{"login":"someone"},"head":{"sha":"sha4"}}`
			fmt.Fprintf(w, "[%s,%s,%s,%s]", fmt.Sprintf(pr, 1, 1, "someone", 1), fmt.Sprintf(pr, 2, 2, "someone", 2), fmt.Sprintf(pr, 3, 3, "Bot-Login", 3), closed)
		case "/repos/o/r/commits/sha1/check-runs", "/repos/o/r/commits/sha2/check-runs", "/repos/o/r/commits/sha3/check-runs", "/repos/o/r/commits/sha4/check-runs":
			fmt.Fprintf(w, `{"check_runs":[{"id":%c,"status":"completed","started_at":"2026-11-11T09:01:00Z","completed_at":"2026-11-11T09:02:00Z"}]}`, r.URL.Path[len("/repos/o/r/commits/sha")])
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	old := api
	api = srv.URL
	defer func() { api = old }()

	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddFactory(db, "f1", "f1@example.com", "bot-login"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(db, "contract", "f1", "e1", "pr.linked", "2026-11-11T08:00:00.000Z",
		`{"type":"pr.linked","payload":{"pr_url":"https://github.com/o/r/pull/2"}}`, nil); err != nil {
		t.Fatal(err)
	}

	st, err := Reconcile(db, "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	// 4 pr events, plus commits + reviews + 1 check run for PRs 2 and 3
	if st.Added != 10 {
		t.Errorf("first run added %d events, want 10", st.Added)
	}
	for _, n := range []int{1, 2, 3, 4} {
		want := 0
		if n == 2 || n == 3 {
			want = 1
		}
		for _, p := range []string{fmt.Sprintf("/repos/o/r/pulls/%d/commits", n), fmt.Sprintf("/repos/o/r/pulls/%d/reviews", n), fmt.Sprintf("/repos/o/r/commits/sha%d/check-runs", n)} {
			if hits[p] != want {
				t.Errorf("%s: %d requests, want %d", p, hits[p], want)
			}
		}
	}

	st, err = Reconcile(db, "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 0 {
		t.Errorf("second run added %d events, want 0", st.Added)
	}
	if hits["/repos/o/r/pulls/1/commits"] != 0 || hits["/repos/o/r/pulls/4/commits"] != 0 {
		t.Error("second run fetched details of an unlinked PR")
	}

	// a pr.linked event for the closed PR 4 arrives late (e.g. from a factory's spool)
	if _, err := store.InsertEvent(db, "contract", "f1", "e2", "pr.linked", "2026-11-11T10:00:00.000Z",
		`{"type":"pr.linked","payload":{"pr_url":"https://github.com/o/r/pull/4"}}`, nil); err != nil {
		t.Fatal(err)
	}
	for run := 3; run <= 4; run++ {
		if _, err := Reconcile(db, "o/r", ""); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"/repos/o/r/pulls/4/commits", "/repos/o/r/pulls/4/reviews", "/repos/o/r/commits/sha4/check-runs"} {
			if hits[p] != 1 {
				t.Errorf("after run %d: %s: %d requests, want 1", run, p, hits[p])
			}
		}
	}
}

func TestReconcileKeepsSamePRNumberInTwoRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/a/pulls", "/repos/o/b/pulls": // PR #7 in both repos, updated in the same second
			fmt.Fprintf(w, `[{"number":7,"state":"open","updated_at":"2026-11-11T09:00:00Z","html_url":"https://github.com%s/7","user":{"login":"someone"},"head":{"sha":"x"}}]`,
				r.URL.Path[len("/repos"):len(r.URL.Path)-len("/pulls")]+"/pull")
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	old := api
	api = srv.URL
	defer func() { api = old }()

	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"o/a", "o/b"} {
		st, err := Reconcile(db, repo, "")
		if err != nil {
			t.Fatal(err)
		}
		if st.Added != 1 || st.Duplicates != 0 {
			t.Errorf("%s: added %d, duplicates %d; want the PR stored once per repo", repo, st.Added, st.Duplicates)
		}
	}
}
