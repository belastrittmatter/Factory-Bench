// Package emit implements `factorybench emit`: send one contract event, spool it on failure, and flush the spool.
package emit

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"factorybench/internal/contract"
	"factorybench/internal/intake"
	"factorybench/internal/store"
)

// chunk is how many spooled events one flush request carries, well under intake's 10 MB body limit.
const chunk = 500

// Run executes `factorybench emit` with args (after "emit"). It never fails the caller:
// problems go to stderr, undeliverable events to the spool, and the process exits 0.
func Run(args []string, usage string) {
	url, token := os.Getenv("FACTORYBENCH_URL"), os.Getenv("FACTORYBENCH_TOKEN")
	if len(args) == 1 && args[0] == "--flush" {
		Flush(log.Default(), os.Stdout)
		return
	}
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		log.Print(usage)
		return
	}
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	attempt := fs.String("attempt", "", "attempt id")
	key := fs.String("key", "", "event key, unique within the attempt (id = <attempt>:<key>)")
	payload := fs.String("json", "{}", "payload object")
	ts := fs.String("ts", "", "event time (RFC 3339); default now")
	if fs.Parse(args[1:]) != nil {
		return
	}
	if *ts == "" {
		*ts = store.Now()
	}
	if !json.Valid([]byte(*payload)) {
		log.Printf("emit: --json is not valid JSON: %s", *payload)
		return
	}
	ev, err := json.Marshal(contract.Event{Spec: contract.Spec, ID: *attempt + ":" + *key, Type: args[0], TS: *ts,
		AttemptID: *attempt, Payload: json.RawMessage(*payload)})
	if err != nil {
		log.Printf("emit: %v", err)
		return
	}
	if url == "" {
		return // not configured: do nothing
	}
	if err := post(log.Default(), url, token, []json.RawMessage{ev}); err != nil {
		p := spoolPath(log.Default())
		if serr := spool(p, ev); serr != nil {
			log.Printf("emit: %v; cannot spool, event lost: %v", err, serr)
			return
		}
		log.Printf("emit: %v; spooled to %s", err, p)
	}
}

// Send delivers events like Run does for one: posted to FACTORYBENCH_URL in chunks, what isn't delivered spooled,
// nothing when FACTORYBENCH_URL is unset. It never fails the caller. It is for programs that emit many events
// themselves, such as the context bench. Problems go to lg.
func Send(lg *log.Logger, evs ...[]byte) {
	url, token := os.Getenv("FACTORYBENCH_URL"), os.Getenv("FACTORYBENCH_TOKEN")
	if url == "" {
		return
	}
	rest, err := postAll(lg, url, token, evs)
	if err == nil {
		return
	}
	p := spoolPath(lg)
	if serr := spool(p, rest...); serr != nil {
		lg.Printf("emit: %v; cannot spool, %d events lost: %v", err, len(rest), serr)
		return
	}
	lg.Printf("emit: %v; %d events spooled to %s", err, len(rest), p)
}

func postAll(lg *log.Logger, url, token string, evs [][]byte) ([][]byte, error) {
	for len(evs) > 0 {
		n := min(chunk, len(evs))
		batch := make([]json.RawMessage, n)
		for i := range batch {
			batch[i] = evs[i]
		}
		if err := post(lg, url, token, batch); err != nil {
			return evs, err
		}
		evs = evs[n:]
	}
	return nil, nil
}

func spoolPath(lg *log.Logger) string {
	if p := os.Getenv("FACTORYBENCH_SPOOL"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		lg.Printf("emit: %v; spooling under %s", err, os.TempDir())
		home = os.TempDir()
	}
	return filepath.Join(home, ".factorybench", "spool.jsonl")
}

func post(lg *log.Logger, url, token string, evs []json.RawMessage) error {
	body, err := json.Marshal(evs)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", strings.TrimRight(url, "/")+"/v1/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var r intake.Response
	if json.Unmarshal(b, &r) == nil && len(r.Rejected) > 0 {
		lg.Printf("emit: rejected by FactoryBench (kept there): %s", b)
	}
	return nil
}

// spool appends events, one per line, to the spool file at p.
func spool(p string, evs ...[]byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, ev := range evs {
		buf.Write(ev)
		buf.WriteByte('\n')
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Flush sends the spool to FACTORYBENCH_URL: progress goes to out, problems to lg.
func Flush(lg *log.Logger, out io.Writer) {
	url, token := os.Getenv("FACTORYBENCH_URL"), os.Getenv("FACTORYBENCH_TOKEN")
	if url == "" {
		lg.Print("flush: FACTORYBENCH_URL not set")
		return
	}
	p := spoolPath(lg)
	sending := p + ".sending"
	// A .sending left by a crashed flush goes first, so the Rename below never overwrites it.
	if _, err := os.Stat(sending); err == nil && !send(lg, out, url, token, p, sending) {
		return
	}
	// Take the spool atomically so emits during the flush go to a fresh spool file.
	if err := os.Rename(p, sending); err != nil {
		fmt.Fprintln(out, "spool empty")
		return
	}
	send(lg, out, url, token, p, sending)
}

// send posts the events in file sending in chunks; what isn't delivered goes back to the spool p.
// sending is removed only when every event was delivered or re-spooled.
func send(lg *log.Logger, out io.Writer, url, token, p, sending string) bool {
	b, err := os.ReadFile(sending)
	if err != nil {
		lg.Printf("flush: %v", err)
		return false
	}
	var evs [][]byte
	for _, l := range bytes.Split(b, []byte("\n")) {
		l = bytes.TrimSpace(l)
		switch {
		case len(l) == 0:
		case !json.Valid(l):
			lg.Printf("flush: skipping corrupt spool line: %.200s", l)
		default:
			evs = append(evs, l)
		}
	}
	rest, perr := postAll(lg, url, token, evs)
	if perr != nil {
		if serr := spool(p, rest...); serr != nil {
			lg.Printf("flush: %v; cannot re-spool (%v), events kept in %s", perr, serr, sending)
			return false
		}
		lg.Printf("flush: %v; %d events kept in spool", perr, len(rest))
	}
	if sent := len(evs) - len(rest); sent > 0 || len(rest) == 0 {
		fmt.Fprintf(out, "flushed %d events\n", sent)
	}
	if err := os.Remove(sending); err != nil {
		lg.Printf("flush: %v", err)
	}
	return len(rest) == 0
}
