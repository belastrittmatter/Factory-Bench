package normalize

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"factorybench/internal/intake"
	"factorybench/internal/report"
	"factorybench/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden expected files")

// A golden case: factories, contract batches posted over HTTP, github events, and work items to report.
type goldenCase struct {
	// id -> commit email, or id -> {"commit_email", "github_login"}
	Factories map[string]json.RawMessage `json:"factories"`
	Batches   []struct {
		Factory string            `json:"factory"`
		Events  []json.RawMessage `json:"events"`
	} `json:"batches"`
	GitHub []struct {
		Type, ID, TS string
		Payload      json.RawMessage
	} `json:"github"`
	Report []string `json:"report"`
}

func TestGolden(t *testing.T) {
	dirs, err := filepath.Glob("../../testdata/golden/*")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no golden cases")
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) { runGolden(t, dir) })
	}
}

func runGolden(t *testing.T, dir string) {
	var gc goldenCase
	b, err := os.ReadFile(filepath.Join(dir, "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &gc); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for id, raw := range gc.Factories {
		var f struct{ CommitEmail, GitHubLogin string }
		if json.Unmarshal(raw, &f.CommitEmail) != nil {
			if err := json.Unmarshal(raw, &struct {
				CommitEmail *string `json:"commit_email"`
				GitHubLogin *string `json:"github_login"`
			}{&f.CommitEmail, &f.GitHubLogin}); err != nil {
				t.Fatal(err)
			}
		}
		if tokens[id], err = store.AddFactory(db, id, f.CommitEmail, f.GitHubLogin); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(intake.Handler(db, nil))
	defer srv.Close()

	var responses []json.RawMessage
	for _, batch := range gc.Batches {
		body, err := json.Marshal(batch.Events)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", srv.URL, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[batch.Factory])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		rb, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, bytes.TrimSpace(rb))
	}
	for _, g := range gc.GitHub {
		if _, err := store.InsertEvent(db, "github", "", g.ID, g.Type, g.TS, string(g.Payload), nil); err != nil {
			t.Fatal(err)
		}
	}

	if err := Run(db); err != nil {
		t.Fatal(err)
	}
	derived, reports := snapshot(t, db, gc.Report)

	var rejected []map[string]any
	rows, err := db.Query(`SELECT factory_id, id, rejected FROM events WHERE rejected IS NOT NULL ORDER BY factory_id, id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var f, id, why string
		if err := rows.Scan(&f, &id, &why); err != nil {
			t.Fatal(err)
		}
		rejected = append(rejected, map[string]any{"factory": f, "id": id, "error": why})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	got, err := json.MarshalIndent(map[string]any{"responses": responses, "rejected_stored": rejected, "derived": json.RawMessage(derived)}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	compare(t, filepath.Join(dir, "expected.json"), got)
	compare(t, filepath.Join(dir, "report.txt"), reports)

	// Replay: rebuilding from events must give byte-identical derived rows and reports.
	if err := Run(db); err != nil {
		t.Fatal(err)
	}
	derived2, reports2 := snapshot(t, db, gc.Report)
	if !bytes.Equal(derived, derived2) || !bytes.Equal(reports, reports2) {
		t.Fatalf("replay changed the result:\n%s\n---\n%s", derived, derived2)
	}
}

var itemRe = regexp.MustCompile(`^(.+)#(\d+)$`)

// fixedNow makes reports of unfinished attempts deterministic.
var fixedNow = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

func snapshot(t *testing.T, db *sql.DB, items []string) (derived, reports []byte) {
	d, err := dump(db)
	if err != nil {
		t.Fatal(err)
	}
	if derived, err = json.MarshalIndent(d, "  ", "  "); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, it := range items {
		m := itemRe.FindStringSubmatch(it)
		n, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if err := report.Item(db, &buf, m[1], n, fixedNow); err != nil {
			t.Fatal(err)
		}
		buf.WriteString("\n")
	}
	buf.WriteString("$ report items\n")
	if err := report.Items(db, &buf, "", "", fixedNow); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("\n$ report factories\n")
	if err := report.Factories(db, &buf, false, fixedNow); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("\n$ report live\n")
	if err := report.Live(db, &buf, fixedNow); err != nil {
		t.Fatal(err)
	}
	return derived, buf.Bytes()
}

func compare(t *testing.T, path string, got []byte) {
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/normalize -update)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// dump returns every derived table as rows sorted by all columns, for golden and replay comparisons.
func dump(db *sql.DB) (map[string][]map[string]any, error) {
	out := map[string][]map[string]any{}
	for _, t := range tables {
		// ORDER BY 1, 2, ..., n needs the column count before the query, so read it from the schema.
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?)`, t).Scan(&n); err != nil {
			return nil, err
		}
		order := make([]string, n)
		for i := range order {
			order[i] = strconv.Itoa(i + 1)
		}
		rows, err := db.Query("SELECT * FROM " + t + " ORDER BY " + strings.Join(order, ","))
		if err != nil {
			return nil, err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		out[t] = []map[string]any{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			m := map[string]any{}
			for i, c := range cols {
				m[c] = vals[i]
			}
			out[t] = append(out[t], m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
