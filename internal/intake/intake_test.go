package intake

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"factorybench/internal/store"
)

func TestAuth(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	tokA, err := store.AddFactory(db, "factory-a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddFactory(db, "factory-b", "", ""); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(db, nil))
	defer srv.Close()

	post := func(token, body string) int {
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// Token A, body claims factory B: stored as A.
	ev := `[{"spec":"factorybench/v1","id":"x:1","type":"activity","ts":"2026-11-11T09:00:00Z","attempt_id":"x","factory_id":"factory-b","payload":{}}]`
	if c := post(tokA, ev); c != 200 {
		t.Fatalf("status %d", c)
	}
	if c := post("fbt_wrong", ev); c != 401 {
		t.Fatalf("bad token: status %d", c)
	}
	if c := post("", ev); c != 401 {
		t.Fatalf("no token: status %d", c)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM events WHERE factory_id = 'factory-b'`).Scan(&n)
	if n != 0 {
		t.Fatal("factory A wrote an event as factory B")
	}
	db.QueryRow(`SELECT count(*) FROM events WHERE factory_id = 'factory-a'`).Scan(&n)
	if n != 1 {
		t.Fatalf("factory-a events = %d", n)
	}
}

// A corrected re-send of a rejected event replaces it; an accepted event is never overwritten.
func TestResendRejected(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.AddFactory(db, "fa", "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(db, nil))
	defer srv.Close()
	post := func(payload string) Response {
		ev := `[{"spec":"factorybench/v1","id":"a:u","type":"usage","ts":"2026-11-11T09:00:00Z","attempt_id":"a","payload":` + payload + `}]`
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(ev))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r Response
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	stored := func() (payload string, rejected bool) {
		var rej *string
		if err := db.QueryRow(`SELECT json_extract(payload, '$.payload.input'), rejected FROM events WHERE id = 'a:u'`).
			Scan(&payload, &rej); err != nil {
			t.Fatal(err)
		}
		return payload, rej != nil
	}
	if r := post(`{"model":"m","input":1}`); len(r.Rejected) != 1 {
		t.Fatalf("invalid: %+v", r)
	}
	if r := post(`{"model":"m","input":2,"output":1}`); r.Accepted != 1 {
		t.Fatalf("corrected re-send: %+v", r)
	}
	if p, rej := stored(); p != "2" || rej {
		t.Fatalf("stored input=%s rejected=%v", p, rej)
	}
	if r := post(`{"model":"m","input":3,"output":1}`); r.Duplicates != 1 {
		t.Fatalf("re-send of accepted: %+v", r)
	}
	if p, _ := stored(); p != "2" {
		t.Fatalf("accepted event overwritten: input=%s", p)
	}
}
