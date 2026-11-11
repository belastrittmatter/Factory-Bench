package emit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, url string) string {
	p := filepath.Join(t.TempDir(), "spool.jsonl")
	t.Setenv("FACTORYBENCH_SPOOL", p)
	t.Setenv("FACTORYBENCH_URL", url)
	t.Setenv("FACTORYBENCH_TOKEN", "tok")
	return p
}

func lines(t *testing.T, p string) []string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func TestBadURLSpools(t *testing.T) {
	p := setup(t, "http://bad host")
	Run([]string{"activity", "--attempt", "a", "--key", "k"}, "usage")
	if l := lines(t, p); len(l) != 1 || !strings.Contains(l[0], `"id":"a:k"`) {
		t.Fatalf("spool = %q", l)
	}
}

func TestFlushSkipsCorruptLine(t *testing.T) {
	var got []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch []json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		got = append(got, batch...)
		io.WriteString(w, `{"accepted":2,"duplicates":0,"rejected":[]}`)
	}))
	defer srv.Close()
	p := setup(t, srv.URL)
	if err := os.WriteFile(p, []byte("{\"id\":\"a:1\"}\n{\"id\":\"a:2\",\"trunc\n{\"id\":\"a:3\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flush(srv.URL, "tok")
	if len(got) != 2 || string(got[0]) != `{"id":"a:1"}` || string(got[1]) != `{"id":"a:3"}` {
		t.Fatalf("delivered %s", got)
	}
	for _, f := range []string{p, p + ".sending"} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", f)
		}
	}
}

func TestFlushNon2xxKeepsSpool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", 503)
	}))
	defer srv.Close()
	p := setup(t, srv.URL)
	// A leftover .sending from a crashed flush plus a fresh spool: both must survive.
	if err := os.WriteFile(p+".sending", []byte("{\"id\":\"a:1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{\"id\":\"a:2\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flush(srv.URL, "tok")
	if l := lines(t, p); len(l) != 2 {
		t.Fatalf("spool = %q", l)
	}
	if _, err := os.Stat(p + ".sending"); !os.IsNotExist(err) {
		t.Fatal(".sending not removed after re-spool")
	}
}
