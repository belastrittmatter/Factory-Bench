package dashboard

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

type dispatch struct {
	At   time.Time
	Lane string
	File string
}

type note struct { // a manifest line that mentions a card but is not a dispatch (reaps, halts)
	At   time.Time
	Text string
}

// manifests parses every run manifest: dispatch lines per issue and the other lines that name the issue.
// shortcut: the manifest's clock is local time on the date in its file name; a run crossing midnight gets wrong dates.
func (s *server) manifests() (map[int64][]dispatch, map[int64][]note) {
	d, n := map[int64][]dispatch{}, map[int64][]note{}
	if !s.local || s.o.Runs == "" {
		return d, n
	}
	files, _ := filepath.Glob(filepath.Join(s.o.Runs, "*.md"))
	for _, f := range files {
		m := manifestName.FindStringSubmatch(filepath.Base(f))
		fh, err := os.Open(f)
		if m == nil || err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			l := manifestLine.FindStringSubmatch(sc.Text())
			if l == nil {
				continue
			}
			at, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+" "+l[1], time.Local)
			if err != nil {
				continue
			}
			if x := dispatchLine.FindStringSubmatch(l[2]); x != nil {
				issue, _ := strconv.ParseInt(x[2], 10, 64)
				d[issue] = append(d[issue], dispatch{at, x[1], filepath.Base(f)})
			} else {
				for _, w := range strings.Fields(l[2]) {
					if issue, err := strconv.ParseInt(strings.TrimPrefix(strings.Trim(w, ",()"), "#"), 10, 64); err == nil && strings.HasPrefix(w, "#") {
						n[issue] = append(n[issue], note{at, l[2]})
						break
					}
				}
			}
		}
		fh.Close()
	}
	return d, n
}

func (s *server) dispatches(issue int64) []dispatch {
	d, _ := s.manifests()
	return d[issue]
}

// flow returns the dispatches (lane moves) of one card from the run manifests, its attempts and its merge time.
func (s *server) flow(r *http.Request) (any, error) {
	repo, issue := r.URL.Query().Get("repo"), r.URL.Query().Get("issue")
	n, err := strconv.ParseInt(issue, 10, 64)
	if err != nil || repo == "" {
		return nil, httpError{http.StatusBadRequest, "repo and issue required"}
	}
	att, err := rows(s.db, `SELECT attempt_id, role, state, started_at, ended_at FROM attempts WHERE repo = ?1 AND issue = ?2 ORDER BY started_at`, repo, n)
	if err != nil {
		return nil, err
	}
	merged, err := rows(s.db, `SELECT min(p.merged_at) AS merged_at FROM prs p WHERE p.merged_at IS NOT NULL AND p.url IN (SELECT pr_url FROM attempts WHERE repo = ?1 AND issue = ?2)`, repo, n)
	if err != nil {
		return nil, err
	}
	d, notes := s.manifests()
	return map[string]any{"attempts": att, "merged_at": merged[0]["merged_at"], "dispatches": d[n], "notes": notes[n], "manifest": s.local && s.o.Runs != ""}, nil
}

// workerLogs lists the manifests and worker logs that match an attempt: same issue, a dispatch inside its time window.
func (s *server) workerLogs(a map[string]any) map[string]any {
	issue, _ := a["issue"].(int64)
	start, _ := time.Parse(time.RFC3339Nano, a["started_at"].(string))
	end := time.Now()
	if e, ok := a["ended_at"].(string); ok {
		end, _ = time.Parse(time.RFC3339Nano, e)
	}
	lo, hi := start.Add(-time.Minute), end.Add(time.Minute)
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
	buf := make([]byte, maxFile+1)
	n, _ := f.Read(buf)
	trunc := n > maxFile
	b := buf[:min(n, maxFile)]
	for !utf8.Valid(b) && len(b) > 0 && trunc {
		b = b[:len(b)-1]
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
				in, _ := json.Marshal(m["input"])
				name, _ := m["name"].(string)
				out = append(out, entry{l.Timestamp, l.Type, "tool_use", name, clip(string(in), 4000)})
			case "tool_result":
				out = append(out, entry{l.Timestamp, l.Type, "tool_result", "", clip(textOf(m["content"]), 4000)})
			}
		}
	}
	return map[string]any{"session": id, "entries": out, "truncated": truncated}, nil
}
