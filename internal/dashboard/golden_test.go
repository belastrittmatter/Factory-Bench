package dashboard

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"factorybench/internal/intake"
	"factorybench/internal/normalize"
	"factorybench/internal/store"
)

var update = flag.Bool("update", false, "rewrite dashboard.json of the golden cases")

// TestItemGolden loads a golden case (testdata/golden/<case>/events.json: contract batches and inline OTLP bodies) and
// compares the issue endpoint's per-factory, per-step and timeline data, and the attempt endpoint's timeline data of one
// attempt, with its dashboard.json.
func TestItemGolden(t *testing.T) {
	for _, c := range []struct{ dir, repo, issue, factory, attempt string }{
		{"two_factories_sessions", "acme/app", "40", "omp", "o1"},
		{"omp_new_core", "acme/app", "41", "omp", "t12-r1"},
	} {
		t.Run(c.dir, func(t *testing.T) {
			dir := filepath.Join("../../testdata/golden", c.dir)
			var gc struct {
				Factories map[string]string `json:"factories"`
				Batches   []struct {
					Factory string            `json:"factory"`
					Events  []json.RawMessage `json:"events"`
				} `json:"batches"`
				OTLP []struct {
					Factory string            `json:"factory"`
					Bodies  []json.RawMessage `json:"bodies"`
				} `json:"otlp"`
			}
			b, err := os.ReadFile(filepath.Join(dir, "events.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &gc); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "fb.db")
			rw, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			tokens := map[string]string{}
			for id, email := range gc.Factories {
				if tokens[id], err = store.AddFactory(rw, id, email, ""); err != nil {
					t.Fatal(err)
				}
			}
			post := func(h http.Handler, factory string, body []byte) {
				req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+tokens[factory])
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(`"error"`)) {
					t.Fatalf("post: %d %s", w.Code, w.Body)
				}
			}
			for _, x := range gc.Batches {
				body, _ := json.Marshal(x.Events)
				post(intake.Handler(rw, nil), x.Factory, body)
			}
			for _, x := range gc.OTLP {
				for _, body := range x.Bodies {
					post(intake.LogsHandler(rw, nil), x.Factory, body)
				}
			}
			if err := normalize.Run(rw); err != nil {
				t.Fatal(err)
			}
			rw.Close()
			db, err := OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var item map[string]any
			get(t, Handler(db, Options{Addr: "127.0.0.1:8081"}), "/api/item?repo="+c.repo+"&issue="+c.issue, &item)
			var at map[string]any
			get(t, Handler(db, Options{Addr: "127.0.0.1:8081"}), "/api/attempt?factory="+c.factory+"&id="+c.attempt, &at)
			got := map[string]any{}
			timeline := []string{"sessions", "checks", "send_backs", "waits"}
			for _, k := range append([]string{"step_stats"}, timeline...) {
				got[k] = item[k]
			}
			atl := map[string]any{}
			for _, k := range append([]string{"steps", "step_runs"}, timeline...) {
				atl[k] = at[k]
			}
			got["attempt "+c.attempt] = atl
			out, _ := json.MarshalIndent(got, "", "  ")
			out = append(out, '\n')
			file := filepath.Join(dir, "dashboard.json")
			if *update {
				if err := os.WriteFile(file, out, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, want) {
				t.Errorf("%s differs (go test -run TestItemGolden -update):\n%s", file, out)
			}
		})
	}
}
