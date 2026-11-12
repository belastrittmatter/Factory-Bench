package dashboard

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"factorybench/internal/store"
)

// Local views read files on request only, never into the database. They can hold prompts and code,
// so they exist only when the server is bound to a loopback address and the directory is configured.

var (
	manifestName = regexp.MustCompile(`^(\d{4}-\d\d-\d\d)-[\w.-]+\.md$`)
	workerName   = regexp.MustCompile(`^[\w.-]+-workers/worker-([\w-]+)-(\d+)\.log$`)
	sessionID    = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	manifestLine = regexp.MustCompile(`^\[(\d\d:\d\d:\d\d)\] (.*)$`)
	dispatchLine = regexp.MustCompile(`dispatch lane=(\w+) issue=#(\d+)`)
	workerHeader = regexp.MustCompile(`^=== dispatch lane=\w+ issue=#\d+ at (\S+) ===$`)
)

const maxFile = 4 << 20 // bytes served from one local file

// manifests parses every run manifest in the runs dir.
// shortcut: the manifest's clock is local time on the date in its file name; a run crossing midnight gets wrong dates.
func (s *server) manifests() runLog {
	out := newRunLog()
	if !s.local || s.o.Runs == "" {
		return out
	}
	files, _ := filepath.Glob(filepath.Join(s.o.Runs, "*.md"))
	for _, f := range files {
		m := manifestName.FindStringSubmatch(filepath.Base(f))
		fh, err := os.Open(f)
		if m == nil || err != nil {
			continue
		}
		parseManifest(m[1], filepath.Base(f), fh, out)
		fh.Close()
	}
	return out
}

// flow returns one card's dispatches with the model calls made in each, its board moves and waits, its attempts,
// its merge time and its rework counts. Without manifests every attempt is one row of calls with no lane.
func (s *server) flow(r *http.Request) (any, error) {
	repo, issue := r.URL.Query().Get("repo"), r.URL.Query().Get("issue")
	n, err := strconv.ParseInt(issue, 10, 64)
	if err != nil || repo == "" {
		return nil, httpError{http.StatusBadRequest, "repo and issue required"}
	}
	return s.cardFlow(repo, n, time.Now())
}

func (s *server) cardFlow(repo string, n int64, now time.Time) (map[string]any, error) {
	att, err := rows(s.db, `SELECT a.attempt_id, a.role, a.state, a.started_at, a.ended_at, a.waiting_since, a.pr_url, `+lastEventSQL+` AS last_event
		FROM attempts a WHERE repo = ?1 AND issue = ?2 ORDER BY started_at`, repo, n)
	if err != nil {
		return nil, err
	}
	withStatus(att, now, s.o.Idle)
	merged, err := rows(s.db, `SELECT min(p.merged_at) AS merged_at FROM prs p WHERE p.merged_at IS NOT NULL AND p.url IN (SELECT pr_url FROM attempts WHERE repo = ?1 AND issue = ?2)`, repo, n)
	if err != nil {
		return nil, err
	}
	waits, err := rows(s.db, `SELECT e.ts, e.type, json_extract(e.payload, '$.attempt_id') AS attempt_id, json_extract(e.payload, '$.payload.reason') AS reason
		FROM events e JOIN attempts a ON a.factory_id = e.factory_id AND a.attempt_id = json_extract(e.payload, '$.attempt_id')
		WHERE e.source = 'contract' AND e.rejected IS NULL AND e.type IN ('human.waiting', 'human.resumed') AND a.repo = ?1 AND a.issue = ?2 ORDER BY e.ts`, repo, n)
	if err != nil {
		return nil, err
	}
	var wins []attemptWin
	for _, a := range att {
		w := attemptWin{ID: a["attempt_id"].(string), State: a["status"].(string), Start: parseTS(a["started_at"]), End: tsPtr(a["ended_at"])}
		if w.State != "running" { // an open attempt that went idle or waits stops at its last event, not at now
			w.LastActive = tsPtr(a["last_event"])
		}
		a["bar_end"], a["bar_state"] = barEnd(w.State, w.End, tsPtr(a["waiting_since"]), tsPtr(a["last_event"]), now)
		wins = append(wins, w)
	}
	var calls []call
	err = store.Each(s.db, `SELECT u.ts, u.attempt_id, u.model, u.phase, u.input + u.output + coalesce(u.cache_read, 0) + coalesce(u.cache_write, 0),
		u.cost_usd, u.duration_ms FROM usage u JOIN attempts a USING (factory_id, attempt_id) WHERE a.repo = ? AND a.issue = ? ORDER BY u.ts`, func(r *sql.Rows) error {
		var c call
		var ts string
		var phase sql.NullString
		var cost sql.NullFloat64
		var d sql.NullInt64
		if err := r.Scan(&ts, &c.Attempt, &c.Model, &phase, &c.Tokens, &cost, &d); err != nil {
			return err
		}
		c.TS = parseTS(ts)
		if phase.Valid {
			c.Phase = &phase.String
		}
		if cost.Valid {
			c.Cost = &cost.Float64
		}
		if d.Valid {
			c.DurationMS = &d.Int64
		}
		calls = append(calls, c)
		return nil
	}, repo, n)
	if err != nil {
		return nil, err
	}
	rl := s.manifests()
	ds := rl.dispatches[n]
	grouped := groupByDispatch(ds, wins, calls, now)
	var lanes []string
	retryCost, retryRows := 0.0, 0
	for _, d := range grouped {
		if d.Lane != "" {
			lanes = append(lanes, d.Lane)
		}
		if d.Retry {
			retryCost += d.Cost
			retryRows++
		}
	}
	bounces, redis := rework(lanes)
	return map[string]any{"attempts": att, "merged_at": merged[0]["merged_at"], "dispatches": grouped, "notes": rl.notes[n], "moves": rl.moves[n],
		"waits": waits, "retry_cost_usd": retryCost, "retry_dispatches": retryRows, "bounces": bounces, "redispatches": redis,
		"manifest": s.local && s.o.Runs != ""}, nil
}

func parseTS(v any) time.Time { s, _ := v.(string); t, _ := time.Parse(time.RFC3339Nano, s); return t }

// tsPtr parses a nullable timestamp column.
func tsPtr(v any) *time.Time {
	if _, ok := v.(string); !ok {
		return nil
	}
	t := parseTS(v)
	return &t
}

// workerLogs lists the manifests and worker logs that match an attempt: same issue, a dispatch inside its time window.
func (s *server) workerLogs(a map[string]any) map[string]any {
	issue, _ := a["issue"].(int64)
	start, _ := time.Parse(time.RFC3339Nano, a["started_at"].(string))
	end := time.Now()
	if e, ok := a["ended_at"].(string); ok {
		end, _ = time.Parse(time.RFC3339Nano, e)
	}
	lo, hi := start.Add(-dispatchSkew), end.Add(dispatchSkew)
	workers := []map[string]any{}
	files, _ := filepath.Glob(filepath.Join(s.o.Runs, "*-workers", "worker-*-"+strconv.FormatInt(issue, 10)+".log"))
	for _, f := range files {
		rel, _ := filepath.Rel(s.o.Runs, f)
		m := workerName.FindStringSubmatch(filepath.ToSlash(rel))
		fh, err := os.Open(f)
		if m == nil || err != nil {
			continue
		}
		var hits []string
		sc := bufio.NewScanner(fh)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			if h := workerHeader.FindStringSubmatch(sc.Text()); h != nil {
				if t, err := time.Parse(time.RFC3339, h[1]); err == nil && !t.Before(lo) && !t.After(hi) {
					hits = append(hits, h[1])
				}
			}
		}
		fh.Close()
		if len(hits) > 0 {
			workers = append(workers, map[string]any{"file": filepath.ToSlash(rel), "lane": m[1], "dispatches": hits})
		}
	}
	manifests := []string{}
	files, _ = filepath.Glob(filepath.Join(s.o.Runs, "*.md"))
	for _, f := range files {
		if m := manifestName.FindStringSubmatch(filepath.Base(f)); m != nil && m[1] == start.In(time.Local).Format("2006-01-02") {
			manifests = append(manifests, filepath.Base(f))
		}
	}
	sort.Strings(manifests)
	return map[string]any{"manifests": manifests, "workers": workers}
}

// readText returns up to maxFile bytes of a file under dir, cut at a rune boundary.
func readText(dir, rel string) (map[string]any, error) {
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, httpError{http.StatusNotFound, "no such file"}
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	trunc := len(b) > maxFile
	if trunc {
		b = b[:maxFile]
		// drop only a rune split by the cut; invalid bytes elsewhere in the file stay as they are
		for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
			if utf8.RuneStart(b[i]) {
				if !utf8.FullRune(b[i:]) {
					b = b[:i]
				}
				break
			}
		}
	}
	return map[string]any{"name": rel, "text": string(b), "truncated": trunc}, nil
}

func (s *server) needRuns() error {
	if !s.local || s.o.Runs == "" {
		return httpError{http.StatusForbidden, "local views need a loopback --addr and --runs"}
	}
	return nil
}

func (s *server) manifest(r *http.Request) (any, error) {
	if err := s.needRuns(); err != nil {
		return nil, err
	}
	name := r.URL.Query().Get("file")
	if !manifestName.MatchString(name) {
		return nil, httpError{http.StatusBadRequest, "bad manifest name"}
	}
	return readText(s.o.Runs, name)
}

func (s *server) worker(r *http.Request) (any, error) {
	if err := s.needRuns(); err != nil {
		return nil, err
	}
	name := r.URL.Query().Get("file")
	if !workerName.MatchString(name) {
		return nil, httpError{http.StatusBadRequest, "bad worker log name"}
	}
	return readText(s.o.Runs, name)
}

type entry struct {
	TS   string `json:"ts"`
	Role string `json:"role"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Text string `json:"text"`
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "… (" + strconv.Itoa(len(r)-n) + " more characters)"
	}
	return s
}

// textOf flattens a message content value (a string or a list of blocks) into text.
func textOf(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if m, ok := b.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// transcript turns a Claude Code session file into readable entries: text, tool calls and tool results.
// Thinking blocks and bookkeeping records are left out.
func (s *server) transcript(r *http.Request) (any, error) {
	if !s.local || s.o.Transcripts == "" {
		return nil, httpError{http.StatusForbidden, "transcripts need a loopback --addr and --transcripts"}
	}
	id := r.URL.Query().Get("session")
	if !sessionID.MatchString(id) {
		return nil, httpError{http.StatusBadRequest, "bad session id"}
	}
	f, err := os.Open(filepath.Join(s.o.Transcripts, id+".jsonl"))
	if err != nil {
		return nil, httpError{http.StatusNotFound, "no transcript for this session"}
	}
	defer f.Close()
	out := []entry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 32<<20)
	truncated := false
	for sc.Scan() {
		var l struct {
			Type, Timestamp string
			Message         struct{ Content any }
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || (l.Type != "user" && l.Type != "assistant") {
			continue
		}
		if len(out) >= 3000 {
			truncated = true
			break
		}
		blocks, ok := l.Message.Content.([]any)
		if !ok {
			out = append(out, entry{l.Timestamp, l.Type, "text", "", clip(textOf(l.Message.Content), 20000)})
			continue
		}
		for _, b := range blocks {
			m, _ := b.(map[string]any)
			switch m["type"] {
			case "text":
				out = append(out, entry{l.Timestamp, l.Type, "text", "", clip(textOf([]any{m}), 20000)})
			case "tool_use":
				name, _ := m["name"].(string)
				out = append(out, entry{l.Timestamp, l.Type, "tool_use", name, inputShape(m["input"])})
			case "tool_result":
				out = append(out, entry{l.Timestamp, l.Type, "tool_result", "", clip(textOf(m["content"]), 4000)})
			}
		}
	}
	return map[string]any{"session": id, "entries": out, "truncated": truncated}, nil
}

// inputShape describes a tool input by its argument names and value sizes only. Tool inputs hold shell commands and
// file contents, which can carry secrets, so their values never leave the server, whatever the tool is called.
func inputShape(in any) string {
	m, ok := in.(map[string]any)
	if !ok {
		return "input not shown"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		b, _ := json.Marshal(m[k])
		parts[i] = fmt.Sprintf("%s: %d bytes", k, len(b))
	}
	return "input not shown · " + strings.Join(parts, ", ")
}
