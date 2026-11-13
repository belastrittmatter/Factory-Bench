// Package dashboard serves the web dashboard: a JSON API over the report code plus one embedded page.
// It reads through a read-only handle and never migrates the database. The only writes are the Factories page's
// (register a factory, issue or revoke its token, edit its identity), on a separate handle, only on a loopback address
// (see factories.go). The conversation views (run manifests, worker logs, Claude transcripts) read
// local files on request and are only served on a loopback address.
package dashboard

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"factorybench/internal/report"
	"factorybench/internal/store"
)

//go:embed index.html
var page []byte

// Options configures Handler. Empty Runs or Transcripts switch the matching local view off.
type Options struct {
	Addr        string        // listen address; local views need a loopback one
	Runs        string        // super-board runs dir: <date>-<name>.md manifests and <date>-<name>-workers/ logs
	Transcripts string        // Claude Code project dir with <session id>.jsonl files
	Idle        time.Duration // a running attempt with no event for this long is idle; 0 = DefaultIdleTimeout
	Admin       *sql.DB       // writable handle for managing factories (OpenWritable); nil, or a non-loopback Addr, = read-only
}

// DefaultIdleTimeout is how long a running attempt may go without any event before it shows as idle instead of live.
const DefaultIdleTimeout = 10 * time.Minute

// status is the state to show for an attempt: running with no event within idle (or no event at all) is "idle".
func status(state string, lastEvent *time.Time, now time.Time, idle time.Duration) string {
	if state == "running" && (lastEvent == nil || now.Sub(*lastEvent) > idle) {
		return "idle"
	}
	return state
}

// lastEventSQL is the newest event of attempt a (any source), the input to status.
const lastEventSQL = `(SELECT max(ts) FROM events e WHERE e.factory_id = a.factory_id AND json_extract(e.payload, '$.attempt_id') = a.attempt_id)`

// withStatus adds "status" to attempt rows that carry state and last_event.
func withStatus(rs []map[string]any, now time.Time, idle time.Duration) {
	for _, r := range rs {
		var last *time.Time
		if v, ok := r["last_event"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				last = &t
			}
		}
		state, _ := r["state"].(string)
		r["status"] = status(state, last, now, idle)
	}
}

// withStages adds "stage" (report.StageKind against the parent: handoff, retry or "") and "staged" (the factory's run on
// the issue has a handoff) to attempt rows that carry factory_id, attempt_id, repo, issue, role, state and parent_attempt_id.
func withStages(rs []map[string]any) {
	str := func(v any) string { s, _ := v.(string); return s }
	run := func(r map[string]any) string { return fmt.Sprint(r["factory_id"], "\x00", r["repo"], "#", r["issue"]) }
	byID, staged := map[string]map[string]any{}, map[string]bool{}
	for _, r := range rs {
		byID[str(r["factory_id"])+"\x00"+str(r["attempt_id"])] = r
	}
	for _, r := range rs {
		r["stage"] = ""
		if p := byID[str(r["factory_id"])+"\x00"+str(r["parent_attempt_id"])]; p != nil && r["parent_attempt_id"] != nil {
			r["stage"] = report.StageKind(str(r["role"]), str(p["role"]), str(p["state"]))
		}
		staged[run(r)] = staged[run(r)] || r["stage"] == "handoff"
	}
	for _, r := range rs {
		r["staged"] = staged[run(r)]
	}
}

// OpenReadOnly opens the database without ever writing or migrating it. It refuses any schema but store.SchemaVersion.
func OpenReadOnly(path string) (*sql.DB, error) {
	return openAt(path, "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
}

// OpenWritable opens the database for the Factories page's writes. Like OpenReadOnly it never migrates.
func OpenWritable(path string) (*sql.DB, error) {
	return openAt(path, "mode=rw&_pragma=busy_timeout(5000)")
}

func openAt(path, params string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?"+params)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // the report code runs one query at a time
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		db.Close()
		return nil, err
	}
	if v != store.SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("database schema is v%d, want v%d: run serve or replay with the current binary first", v, store.SchemaVersion)
	}
	return db, nil
}

// Loopback reports whether a host or host:port is localhost or a loopback IP.
func Loopback(hostport string) bool {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || net.ParseIP(h).IsLoopback()
}

// Handler returns the dashboard.
func Handler(db *sql.DB, o Options) http.Handler {
	local := Loopback(o.Addr)
	if o.Idle <= 0 {
		o.Idle = DefaultIdleTimeout
	}
	s := &server{db: db, o: o, local: local}
	if local {
		s.admin = o.Admin
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(page)
	})
	mux.HandleFunc("/api/meta", s.json(func(r *http.Request) (any, error) {
		return map[string]any{"now": time.Now().UTC(), "idle_s": o.Idle.Seconds(), "local": local && o.Runs != "", "transcripts": local && o.Transcripts != "", "manage": s.admin != nil}, nil
	}))
	mux.HandleFunc("/api/live", s.json(s.live))
	mux.HandleFunc("/api/items", s.json(func(r *http.Request) (any, error) {
		rows, err := report.ItemRows(db, time.Now())
		if rows == nil {
			rows = []report.ItemRow{} // an empty list, not null
		}
		return rows, err
	}))
	mux.HandleFunc("/api/scorecard", s.json(func(r *http.Request) (any, error) {
		groups, rows, err := report.Scorecard(db, r.URL.Query().Get("by") == "config", time.Now())
		return map[string]any{"groups": groups, "rows": rows}, err
	}))
	mux.HandleFunc("/api/item", s.json(s.item))
	mux.HandleFunc("/api/attempt", s.json(s.attempt))
	mux.HandleFunc("/api/series", s.json(s.series))
	mux.HandleFunc("/api/summary", s.json(s.summaryAPI))
	mux.HandleFunc("/api/flow", s.json(s.flow))
	mux.HandleFunc("/api/local/manifest", s.json(s.manifest))
	mux.HandleFunc("/api/local/worker", s.json(s.worker))
	mux.HandleFunc("/api/local/transcript", s.json(s.transcript))
	s.factoryRoutes(mux)
	return mux
}

type server struct {
	db    *sql.DB
	admin *sql.DB // nil = managing factories is off
	o     Options
	local bool
}

// httpError is an error with a status code.
type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string { return e.msg }

// json wraps an API function: GET only, a loopback Host when bound to loopback (DNS rebinding), JSON out, no caching.
func (s *server) json(f func(*http.Request) (any, error)) http.HandlerFunc {
	return s.serve(http.MethodGet, f)
}

func (s *server) serve(method string, f func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.local && !Loopback(r.Host) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		v, err := f(r)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			code := http.StatusInternalServerError
			var e httpError
			if errors.As(err, &e) {
				code = e.code
			}
			w.WriteHeader(code)
			v = map[string]string{"error": err.Error()}
		}
		json.NewEncoder(w).Encode(v)
	}
}

// rows runs a query and returns every row as an object keyed by column; SQL NULL is JSON null, so unknown stays unknown.
func rows(db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	out := []map[string]any{}
	err := store.Each(db, query, func(r *sql.Rows) error {
		cols, _ := r.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := r.Scan(ptrs...); err != nil {
			return err
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
		return nil
	}, args...)
	return out, err
}

// live is report.LiveRows plus the attempt's role, latest usage phase, super-board lane and its shown status (running or idle).
func (s *server) live(r *http.Request) (any, error) {
	now := time.Now()
	lr, err := report.LiveRows(s.db, now)
	if err != nil {
		return nil, err
	}
	type row struct {
		report.LiveRow
		ElapsedS, ActiveS, WaitS float64
		LastEventAgeS            *float64 // nil = no event at all
		LastToolAgeS             *float64
		Role, Phase, Lane        *string
		Status                   string // state, or "idle" (see status)
		Idle                     bool
		HasWaitEvents            bool // false: human wait is unavailable, not zero
	}
	rl, err := s.manifests()
	if err != nil {
		return nil, err
	}
	ds := rl.dispatches
	out := []row{}
	for _, l := range lr {
		x := row{LiveRow: l, ElapsedS: l.Elapsed.Seconds(), ActiveS: (l.Elapsed - l.Wait).Seconds(), WaitS: l.Wait.Seconds()}
		if l.LastEvent != nil {
			a := now.Sub(*l.LastEvent).Seconds()
			x.LastEventAgeS = &a
		}
		if l.LastToolAt != nil {
			a := now.Sub(*l.LastToolAt).Seconds()
			x.LastToolAgeS = &a
		}
		x.Status = status(l.State, l.LastEvent, now, s.o.Idle)
		x.Idle = x.Status == "idle"
		var role, phase sql.NullString
		if err := s.db.QueryRow(`SELECT role, (SELECT phase FROM usage u WHERE u.factory_id = a.factory_id AND u.attempt_id = a.attempt_id
			AND phase IS NOT NULL ORDER BY ts DESC LIMIT 1), EXISTS (SELECT 1 FROM events e WHERE e.factory_id = a.factory_id
			AND e.type IN ('human.waiting', 'human.resumed') AND json_extract(e.payload, '$.attempt_id') = a.attempt_id)
			FROM attempts a WHERE factory_id = ? AND attempt_id = ?`, l.Factory, l.Attempt).Scan(&role, &phase, &x.HasWaitEvents); err != nil {
			return nil, err
		}
		if role.Valid {
			x.Role = &role.String
		}
		if phase.Valid {
			x.Phase = &phase.String
		}
		if d := ds[l.Issue]; len(d) > 0 {
			att, err := rows(s.db, `SELECT attempt_id, started_at, ended_at FROM attempts WHERE repo = ?1 AND issue = ?2`, l.Repo, l.Issue)
			if err != nil {
				return nil, err
			}
			var wins []attemptWin
			for _, a := range att {
				wins = append(wins, attemptWin{ID: a["attempt_id"].(string), Start: parseTS(a["started_at"]), End: tsPtr(a["ended_at"])})
			}
			for _, r := range groupByDispatch(d, wins, nil, now) { // the newest dispatch that ran in this attempt
				if r.Attempt == l.Attempt && r.Lane != "" {
					lane := r.Lane
					x.Lane = &lane
				}
			}
		}
		out = append(out, x)
	}
	return out, nil
}

func (s *server) item(r *http.Request) (any, error) {
	repo, issue := r.URL.Query().Get("repo"), r.URL.Query().Get("issue")
	n, err := strconv.ParseInt(issue, 10, 64)
	if err != nil || repo == "" {
		return nil, httpError{http.StatusBadRequest, "repo and issue required"}
	}
	var title sql.NullString
	switch err := s.db.QueryRow(`SELECT title FROM work_items WHERE repo = ? AND issue = ?`, repo, n).Scan(&title); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, httpError{http.StatusNotFound, fmt.Sprintf("no work item %s#%d", repo, n)}
	case err != nil:
		return nil, err
	}
	tl, err := report.Timeline(s.db, repo, n, time.Now())
	if err != nil {
		return nil, err
	}
	q := func(query string) ([]map[string]any, error) { return rows(s.db, query, repo, n) }
	res := map[string]any{"timeline": tl, "title": title.String}
	for k, query := range map[string]string{
		"attempts": `SELECT a.factory_id, a.attempt_id, a.repo, a.issue, a.role, a.state, a.started_at, a.ended_at, a.config_version, a.parent_attempt_id, a.human_wait_ms, a.pr_url,
			(SELECT sum(cost_usd) FROM usage u WHERE u.factory_id = a.factory_id AND u.attempt_id = a.attempt_id) AS cost_usd,
			(SELECT count(*) FROM usage u WHERE u.factory_id = a.factory_id AND u.attempt_id = a.attempt_id AND cost_usd IS NULL) AS unpriced,
			(SELECT sum(input + output + coalesce(cache_read, 0) + coalesce(cache_write, 0)) FROM usage u WHERE u.factory_id = a.factory_id AND u.attempt_id = a.attempt_id) AS tokens, ` + lastEventSQL + ` AS last_event
			FROM attempts a WHERE repo = ?1 AND issue = ?2 ORDER BY started_at`,
		"prs": `SELECT p.* FROM prs p WHERE p.url IN (SELECT pr_url FROM attempts WHERE repo = ?1 AND issue = ?2)`,
		"commits": `SELECT c.* FROM pr_commits c JOIN prs p ON p.repo = c.repo AND p.number = c.pr_number
			WHERE p.url IN (SELECT pr_url FROM attempts WHERE repo = ?1 AND issue = ?2) ORDER BY committed_at`,
		"reviews": `SELECT v.* FROM reviews v JOIN prs p ON p.repo = v.repo AND p.number = v.pr_number
			WHERE p.url IN (SELECT pr_url FROM attempts WHERE repo = ?1 AND issue = ?2) ORDER BY submitted_at`,
		"checks": `SELECT c.* FROM checks c JOIN prs p ON p.repo = c.repo AND p.number = c.pr_number
			WHERE p.url IN (SELECT pr_url FROM attempts WHERE repo = ?1 AND issue = ?2) ORDER BY completed_at, name`,
	} {
		if res[k], err = q(query); err != nil {
			return nil, err
		}
	}
	withStatus(res["attempts"].([]map[string]any), time.Now(), s.o.Idle)
	withStages(res["attempts"].([]map[string]any))
	// per-factory metrics of this item from the report (CI on first push, time to PR), so the numbers match `report items`
	all, err := report.ItemRows(s.db, time.Now())
	if err != nil {
		return nil, err
	}
	units := []report.ItemRow{}
	for _, u := range all {
		if u.Item == fmt.Sprintf("%s#%d", repo, n) {
			units = append(units, u)
		}
	}
	res["units"] = units
	return res, nil
}

// attempt returns everything derived for one attempt: sessions (usage rows), phases, tools, subagents, errors, gates.
func (s *server) attempt(r *http.Request) (any, error) {
	f, id := r.URL.Query().Get("factory"), r.URL.Query().Get("id")
	if f == "" || id == "" {
		return nil, httpError{http.StatusBadRequest, "factory and id required"}
	}
	res := map[string]any{}
	var err error
	for _, q := range []struct{ key, sql string }{
		{"attempt", `SELECT a.*, ` + lastEventSQL + ` AS last_event, EXISTS (SELECT 1 FROM events e WHERE e.factory_id = a.factory_id
			AND e.type IN ('human.waiting', 'human.resumed') AND json_extract(e.payload, '$.attempt_id') = a.attempt_id) AS has_wait_events
			FROM attempts a WHERE factory_id = ?1 AND attempt_id = ?2`},
		{"usage", `SELECT ts, model, agent_id, phase, input, output, cache_read, cache_write, duration_ms, cost_usd, cost_provenance FROM usage WHERE factory_id = ?1 AND attempt_id = ?2 ORDER BY ts`},
		{"phases", `SELECT phase, count(*) AS sessions, sum(input + output + coalesce(cache_read, 0) + coalesce(cache_write, 0)) AS tokens, sum(cost_usd) AS cost_usd,
			sum(cost_usd IS NULL) AS unpriced, sum(duration_ms) AS duration_ms FROM usage WHERE factory_id = ?1 AND attempt_id = ?2 GROUP BY phase`},
		{"tools", `SELECT tool_name, count(*) AS calls, sum(success = 0) AS failed, sum(success IS NULL) AS unknown, sum(duration_ms) AS duration_ms
			FROM tool_calls WHERE factory_id = ?1 AND attempt_id = ?2 GROUP BY tool_name ORDER BY calls DESC`},
		{"tool_calls", `SELECT ts, agent_id, tool_name, success, duration_ms, error_type FROM tool_calls WHERE factory_id = ?1 AND attempt_id = ?2 ORDER BY ts DESC, rowid DESC LIMIT 500`},
		{"agents", `SELECT agent_id, parent_agent_id, agent_type, is_async, model, final_model, tool_calls, duration_ms, status, started_at FROM agent_runs WHERE factory_id = ?1 AND attempt_id = ?2 ORDER BY started_at`},
		{"api_errors", `SELECT ts, agent_id, kind, model, status_code, error, duration_ms FROM api_errors WHERE factory_id = ?1 AND attempt_id = ?2 ORDER BY ts`},
		{"gates", `SELECT ts, name, passed, reason FROM gates WHERE factory_id = ?1 AND attempt_id = ?2 ORDER BY ts`},
		{"sessions", `SELECT json_extract(payload, '$."session.id"') AS session, min(ts) AS first_ts, max(ts) AS last_ts, count(*) AS events FROM events
			WHERE source = 'otlp' AND factory_id = ?1 AND json_extract(payload, '$."factorybench.attempt"') = ?2 GROUP BY 1 ORDER BY 2`},
	} {
		if res[q.key], err = rows(s.db, q.sql, f, id); err != nil {
			return nil, err
		}
	}
	a := res["attempt"].([]map[string]any)
	if len(a) == 0 {
		return nil, httpError{http.StatusNotFound, "no such attempt"}
	}
	withStatus(a, time.Now(), s.o.Idle)
	res["attempt"] = a[0]
	// the attempt's own dispatches, and the lane each call, tool call, error and gate ran in
	at := res["attempt"].(map[string]any)
	fl, err := s.cardFlow(at["repo"].(string), at["issue"].(int64), time.Now())
	if err != nil {
		return nil, err
	}
	mine := []dispatchRow{}
	for _, d := range fl.Dispatches {
		if d.Attempt == id {
			mine = append(mine, d)
		}
	}
	for _, x := range fl.Attempts {
		if x["attempt_id"] == id {
			at["bar_end"], at["bar_state"] = x["bar_end"], x["bar_state"]
		}
	}
	res["dispatches"] = mine
	var tcs []toolCall
	err = store.Each(s.db, `SELECT ts, tool_name, success FROM tool_calls WHERE factory_id = ? AND attempt_id = ? ORDER BY ts`, func(r *sql.Rows) error {
		var ts string
		var c toolCall
		var ok sql.NullBool
		if err := r.Scan(&ts, &c.Tool, &ok); err != nil {
			return err
		}
		c.TS = parseTS(ts)
		if ok.Valid {
			c.Success = &ok.Bool
		}
		tcs = append(tcs, c)
		return nil
	}, f, id)
	if err != nil {
		return nil, err
	}
	res["tools_by_lane"] = toolsByLane(mine, tcs)
	// what each model was used for, if the agent reports it (Claude Code's query_source on api_request records)
	if res["model_purposes"], err = rows(s.db, `SELECT json_extract(payload, '$.model') AS model, json_extract(payload, '$.query_source') AS purpose,
		count(*) AS calls, sum(json_extract(payload, '$.cost_usd')) AS cost_usd FROM events WHERE source = 'otlp' AND factory_id = ?1 AND type = 'otlp.api_request'
		AND json_extract(payload, '$."factorybench.attempt"') = ?2 AND json_extract(payload, '$.query_source') IS NOT NULL GROUP BY 1, 2 ORDER BY calls DESC`, f, id); err != nil {
		return nil, err
	}
	// tool decisions as the agent reported them: a rejected tool never runs, so it has no tool call row
	if res["tool_decisions"], err = rows(s.db, `SELECT json_extract(payload, '$.tool_name') AS tool_name, json_extract(payload, '$.decision') AS decision,
		json_extract(payload, '$.source') AS source, count(*) AS n FROM events WHERE source = 'otlp' AND factory_id = ?1 AND type = 'otlp.tool_decision'
		AND json_extract(payload, '$."factorybench.attempt"') = ?2 GROUP BY 1, 2, 3 ORDER BY n DESC`, f, id); err != nil {
		return nil, err
	}
	for _, k := range []string{"usage", "tool_calls", "api_errors", "gates"} {
		for _, r := range res[k].([]map[string]any) {
			r["lane"] = laneAt(mine, parseTS(r["ts"]))
		}
	}
	if s.local && s.o.Runs != "" {
		if res["workers"], err = s.workerLogs(res["attempt"].(map[string]any)); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// series is everything the overview and compare charts filter client-side: every attempt with its usage summed,
// usage per hour and model, merges, and tool calls per day and tool. All carry factory, config and repo.
func (s *server) series(r *http.Request) (any, error) {
	res := map[string]any{}
	var err error
	for _, q := range []struct{ key, sql string }{
		{"attempts", `SELECT a.factory_id, a.attempt_id, a.repo, a.issue, a.config_version, a.role, a.state, a.parent_attempt_id, a.started_at, a.ended_at, a.human_wait_ms,
			u.n AS sessions, u.tokens, u.cost AS cost_usd, u.unpriced, w.title, ` + lastEventSQL + ` AS last_event
			FROM attempts a LEFT JOIN (SELECT factory_id, attempt_id, count(*) AS n, sum(cost_usd) AS cost, sum(cost_usd IS NULL) AS unpriced,
			  sum(input + output + coalesce(cache_read, 0) + coalesce(cache_write, 0)) AS tokens FROM usage GROUP BY 1, 2) u USING (factory_id, attempt_id)
			LEFT JOIN work_items w USING (repo, issue) ORDER BY a.started_at`},
		{"usage", `SELECT substr(u.ts, 1, 13) AS hour, u.factory_id, a.config_version, a.repo, u.model, u.phase, count(*) AS sessions,
			sum(u.input + u.output + coalesce(u.cache_read, 0) + coalesce(u.cache_write, 0)) AS tokens, sum(u.cost_usd) AS cost_usd, sum(u.cost_usd IS NULL) AS unpriced
			FROM usage u JOIN attempts a USING (factory_id, attempt_id) GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 1, min(u.ts)`}, // first-use order: page colors follow it
		{"attempt_phases", `SELECT u.factory_id, u.attempt_id, u.phase, sum(u.cost_usd) AS cost_usd, sum(u.cost_usd IS NULL) AS unpriced
			FROM usage u JOIN attempts a USING (factory_id, attempt_id) GROUP BY 1, 2, 3 ORDER BY min(u.ts)`}, // cost per attempt and phase, for the per-issue spend chart
		{"merged", `SELECT p.merged_at, a.factory_id, a.config_version, a.repo, a.issue FROM attempts a JOIN prs p ON p.url = a.pr_url
			WHERE p.merged_at IS NOT NULL GROUP BY a.factory_id, a.config_version, a.repo, a.issue`},
		{"tools", `SELECT substr(t.ts, 1, 10) AS day, t.factory_id, a.config_version, a.repo, t.tool_name, count(*) AS calls, sum(t.success = 0) AS failed,
			sum(t.duration_ms) AS duration_ms FROM tool_calls t JOIN attempts a USING (factory_id, attempt_id) GROUP BY 1, 2, 3, 4, 5`},
	} {
		if res[q.key], err = rows(s.db, q.sql); err != nil {
			return nil, err
		}
	}
	withStatus(res["attempts"].([]map[string]any), time.Now(), s.o.Idle)
	withStages(res["attempts"].([]map[string]any))
	return res, nil
}
