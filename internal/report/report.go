// Package report renders the live and work-item timeline reports from derived rows (plus human/PR events).
package report

import (
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"factorybench/internal/store"
)

func parse(ts string) time.Time { t, _ := time.Parse(time.RFC3339Nano, ts); return t }

func dur(d time.Duration) string { return d.Round(time.Second).String() }

func tok(n sql.NullInt64) string {
	switch {
	case !n.Valid:
		return "?"
	case n.Int64 < 1000:
		return fmt.Sprint(n.Int64)
	}
	return fmt.Sprintf("%.1fk", float64(n.Int64)/1000)
}

// cost sums sessions and keeps reported, estimated, and unknown apart.
type cost struct {
	rep, est             float64
	sessions, nRep, nUnk int
}

// merge adds the sessions of o to c.
func (c *cost) merge(o cost) {
	c.rep, c.est = c.rep+o.rep, c.est+o.est
	c.sessions, c.nRep, c.nUnk = c.sessions+o.sessions, c.nRep+o.nRep, c.nUnk+o.nUnk
}

func (c *cost) add(v sql.NullFloat64, prov sql.NullString) {
	c.sessions++
	switch {
	case !v.Valid:
		c.nUnk++
	case prov.String == "reported":
		c.rep += v.Float64
		c.nRep++
	default:
		c.est += v.Float64
	}
}

// usd shows a USD amount with 2 decimals; a real cost too small to show is "<$0.01", never "$0.00".
func usd(v float64) string {
	if s := fmt.Sprintf("$%.2f", v); v <= 0 || s != "$0.00" {
		return s
	}
	return "<$0.01"
}

// String renders the total, keeping reported, estimated, and unpriced sessions apart.
func (c cost) String() string {
	if c.sessions == 0 {
		return "cost unknown (no usage)"
	}
	var s string
	switch nEst := c.sessions - c.nRep - c.nUnk; {
	case c.nRep > 0 && nEst > 0:
		s = fmt.Sprintf("%s (reported %s + est. %s)", usd(c.rep+c.est), usd(c.rep), usd(c.est))
	case c.nRep > 0:
		s = usd(c.rep) + " reported"
	case nEst > 0:
		s = usd(c.est) + " est."
	}
	if c.nUnk > 0 {
		s = strings.TrimPrefix(s+fmt.Sprintf(" + unknown (%d session(s) unpriced)", c.nUnk), " + ")
	}
	return s
}

type attempt struct {
	factory, id, state, started, config string
	parent, role, ended, pr             sql.NullString
	waitMS                              int64
	inferred                            bool
	stage                               string // StageKind against its parent
	staged                              bool   // its factory's run on the item has a handoff
	tools, toolsFailed, apiErrors       int
	cost                                cost
	in, out, cache                      sql.NullInt64 // token sums; cache is NULL when no session reported it
}

func addTok(sum *sql.NullInt64, n sql.NullInt64) {
	if n.Valid {
		sum.Int64 += n.Int64
		sum.Valid = true
	}
}

type line struct {
	ts        string
	rank      int // order of lines with equal ts
	who, text string
	extra     string
}

// Item prints the timeline of one work item across every factory that worked on it.
func Item(db *sql.DB, w io.Writer, repo string, issue int64, now time.Time) error {
	var outcome, survival, task, title sql.NullString
	err := db.QueryRow(`SELECT outcome, survival, task_type, title FROM work_items WHERE repo = ? AND issue = ?`, repo, issue).
		Scan(&outcome, &survival, &task, &title)
	if err == sql.ErrNoRows {
		return fmt.Errorf("no work item %s#%d", repo, issue)
	} else if err != nil {
		return err
	}
	atts, lines, err := timeline(db, repo, issue, now)
	if err != nil {
		return err
	}

	header := fmt.Sprintf("%s#%d", repo, issue)
	if title.Valid {
		header += fmt.Sprintf(" %q", title.String)
	}
	if task.Valid {
		header += " [" + task.String + "]"
	}
	fmt.Fprintf(w, "%s   outcome %s · survival %s\n", header, outcome.String, orNA(survival))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	prev := ""
	for _, l := range lines {
		who := l.who
		if who == prev {
			who = ""
		}
		prev = l.who
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", parse(l.ts).Format("01-02 15:04"), who, l.text, l.extra)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	// Totals per factory, then for the whole item when more than one factory worked on it.
	var factories []string
	seen := map[string]bool{}
	for _, a := range atts {
		if !seen[a.factory] {
			seen[a.factory] = true
			factories = append(factories, a.factory)
		}
	}
	if len(factories) > 1 {
		factories = append(factories, "")
	}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range factories {
		var sel []*attempt
		for _, a := range atts {
			if f == "" || a.factory == f {
				sel = append(sel, a)
			}
		}
		name := f
		if f == "" {
			name = "all factories"
		}
		fmt.Fprintf(tw, "  totals\t%s\t%s\n", name, totals(sel, now, f != ""))
	}
	return tw.Flush()
}

// timeline loads the attempts of one work item (with their usage summed) and its timeline lines, sorted.
// Queries run one after another: the DB has one connection, so a query inside a scan would deadlock.
func timeline(db *sql.DB, repo string, issue int64, now time.Time) ([]*attempt, []line, error) {
	var atts []*attempt
	var lines []line
	byKey := map[string]*attempt{}
	err := store.Each(db, `SELECT a.factory_id, a.attempt_id, a.state, a.started_at, a.config_version, a.parent_attempt_id,
		a.role, a.ended_at, a.pr_url, a.human_wait_ms,
		coalesce((SELECT json_extract(e.payload, '$.payload.parent_attempt_id') IS NULL FROM events e
		  WHERE e.source = 'contract' AND e.factory_id = a.factory_id AND e.type = 'attempt.started' AND e.rejected IS NULL
		    AND json_extract(e.payload, '$.attempt_id') = a.attempt_id ORDER BY e.ts, e.id LIMIT 1), 0),
		(SELECT count(*) FROM tool_calls t WHERE t.factory_id = a.factory_id AND t.attempt_id = a.attempt_id),
		(SELECT count(*) FROM tool_calls t WHERE t.factory_id = a.factory_id AND t.attempt_id = a.attempt_id AND t.success = 0),
		(SELECT count(*) FROM api_errors x WHERE x.factory_id = a.factory_id AND x.attempt_id = a.attempt_id)
		FROM attempts a WHERE repo = ? AND issue = ? ORDER BY started_at, factory_id, attempt_id`, func(r *sql.Rows) error {
		a := &attempt{}
		if err := r.Scan(&a.factory, &a.id, &a.state, &a.started, &a.config, &a.parent, &a.role, &a.ended, &a.pr,
			&a.waitMS, &a.inferred, &a.tools, &a.toolsFailed, &a.apiErrors); err != nil {
			return err
		}
		atts = append(atts, a)
		byKey[a.factory+"\x00"+a.id] = a
		return nil
	}, repo, issue)
	if err != nil {
		return nil, nil, err
	}
	staged := map[string]bool{} // factory -> its run has a handoff
	for _, a := range atts {
		if p := byKey[a.factory+"\x00"+a.parent.String]; a.parent.Valid && p != nil {
			a.stage = StageKind(a.role.String, p.role.String, p.state)
		}
		staged[a.factory] = staged[a.factory] || a.stage == "handoff"
	}
	for _, a := range atts {
		a.staged = staged[a.factory]
		var desc []string
		if a.role.Valid {
			desc = append(desc, a.role.String)
		}
		if a.staged && a.stage != "" {
			desc = append(desc, a.stage)
		}
		if a.parent.Valid {
			desc = append(desc, "parent "+a.parent.String)
			if a.inferred {
				desc = append(desc, "inferred")
			}
		}
		text := "attempt " + a.id + " started"
		if desc != nil {
			text += " (" + strings.Join(desc, ", ") + ")"
		}
		lines = append(lines, line{a.started, 0, a.factory, text, "config " + a.config})
	}

	err = store.Each(db, `SELECT u.factory_id, u.attempt_id, u.ts, u.model, u.input, u.output, u.cache_read, u.cache_write,
		u.duration_ms, u.cost_usd, u.cost_provenance, u.price_version
		FROM usage u JOIN attempts a USING (factory_id, attempt_id) WHERE a.repo = ? AND a.issue = ?`, func(r *sql.Rows) error {
		var f, id, ts, model string
		var in, out, cr, cw, d sql.NullInt64
		var amount sql.NullFloat64
		var prov, ver sql.NullString
		if err := r.Scan(&f, &id, &ts, &model, &in, &out, &cr, &cw, &d, &amount, &prov, &ver); err != nil {
			return err
		}
		a := byKey[f+"\x00"+id]
		a.cost.add(amount, prov)
		addTok(&a.in, in)
		addTok(&a.out, out)
		addTok(&a.cache, cr)
		addTok(&a.cache, cw)
		one := cost{}
		one.add(amount, prov)
		c := one.String()
		if ver.Valid {
			c += " (prices " + ver.String + ")"
		}
		if d.Valid {
			c = dur(time.Duration(d.Int64)*time.Millisecond) + " session, " + c
		}
		lines = append(lines, line{ts, 1, f, fmt.Sprintf("session %s %s: %s in / %s cache read / %s cache write / %s out",
			id, model, tok(in), tok(cr), tok(cw), tok(out)), c})
		return nil
	}, repo, issue)
	if err != nil {
		return nil, nil, err
	}

	// Subagent runs, indented under their parent subagent; the line names the attempt (or parent) they ran in.
	type run struct {
		l                            line
		factory, attempt, id, parent string
	}
	var runs []run
	parents := map[string]string{} // factory \x00 attempt \x00 agent -> parent agent ("" = main agent)
	err = store.Each(db, `SELECT r.factory_id, r.attempt_id, r.agent_id, coalesce(r.parent_agent_id, ''), coalesce(r.agent_type, '?'),
		r.status, r.tool_calls, r.duration_ms, r.started_at,
		(SELECT count(*) FROM tool_calls t WHERE t.factory_id = r.factory_id AND t.attempt_id = r.attempt_id
		   AND t.agent_id = r.agent_id AND t.success = 0),
		(SELECT sum(input + output + coalesce(cache_read, 0) + coalesce(cache_write, 0)) FROM usage u
		   WHERE u.factory_id = r.factory_id AND u.attempt_id = r.attempt_id AND u.agent_id = r.agent_id)
		FROM agent_runs r JOIN attempts a USING (factory_id, attempt_id) WHERE a.repo = ? AND a.issue = ?
		ORDER BY r.started_at, r.factory_id, r.attempt_id, r.agent_id`, func(r *sql.Rows) error {
		var k run
		var typ, status, ts string
		var calls, failed int
		var d, tokens sql.NullInt64
		if err := r.Scan(&k.factory, &k.attempt, &k.id, &k.parent, &typ, &status, &calls, &d, &ts, &failed, &tokens); err != nil {
			return err
		}
		parents[k.factory+"\x00"+k.attempt+"\x00"+k.id] = k.parent
		in := "attempt " + k.attempt
		if k.parent != "" {
			in = "subagent " + k.parent
		}
		extra := plural(calls, "tool call")
		if failed > 0 {
			extra += fmt.Sprintf(" (%d failed)", failed)
		}
		extra += ", " + tok(tokens) + " tokens"
		if d.Valid {
			extra += ", " + dur(time.Duration(d.Int64)*time.Millisecond)
		}
		k.l = line{ts, 1, k.factory, fmt.Sprintf("subagent %s (%s) in %s %s", k.id, typ, in, status), extra}
		runs = append(runs, k)
		return nil
	}, repo, issue)
	if err != nil {
		return nil, nil, err
	}
	for _, k := range runs {
		for p, depth := k.parent, 0; p != "" && depth < 8; p, depth = parents[k.factory+"\x00"+k.attempt+"\x00"+p], depth+1 { // a factory may send a parent loop
			k.l.text = "  " + k.l.text
		}
		lines = append(lines, k.l)
	}

	err = store.Each(db, `SELECT g.factory_id, g.ts, g.name, g.passed, g.reason
		FROM gates g JOIN attempts a USING (factory_id, attempt_id) WHERE a.repo = ? AND a.issue = ?
		ORDER BY g.ts, g.factory_id, g.attempt_id, g.name`, func(r *sql.Rows) error {
		var f, ts, name string
		var passed bool
		var reason sql.NullString
		if err := r.Scan(&f, &ts, &name, &passed, &reason); err != nil {
			return err
		}
		text := "gate " + name + " passed"
		if !passed {
			text = "gate " + name + " failed"
		}
		if reason.Valid {
			text += ": " + reason.String
		}
		lines = append(lines, line{ts, 2, f, text, ""})
		return nil
	}, repo, issue)
	if err != nil {
		return nil, nil, err
	}

	waitingSince := map[string]string{} // factory \x00 attempt -> open wait, paired like normalize does
	err = store.Each(db, `SELECT e.factory_id, a.attempt_id, a.parent_attempt_id, e.ts, e.type, json_extract(e.payload, '$.payload.reason'),
		json_extract(e.payload, '$.payload.pr_url'), json_extract(e.payload, '$.payload.source')
		FROM events e JOIN attempts a ON a.factory_id = e.factory_id AND a.attempt_id = json_extract(e.payload, '$.attempt_id')
		WHERE e.source = 'contract' AND e.rejected IS NULL AND a.repo = ? AND a.issue = ?
		  AND e.type IN ('human.waiting', 'human.resumed', 'human.steering', 'pr.linked')
		ORDER BY e.ts, e.factory_id, e.id`, func(r *sql.Rows) error {
		var f, att, ts, typ string
		var parent, reason, url, src sql.NullString
		if err := r.Scan(&f, &att, &parent, &ts, &typ, &reason, &url, &src); err != nil {
			return err
		}
		l := line{ts: ts, rank: 2, who: f}
		switch typ {
		case "human.waiting":
			l.text = "waiting for human (" + reason.String + ")"
			if k := f + "\x00" + att; waitingSince[k] == "" {
				waitingSince[k] = ts
			}
		case "human.resumed":
			l.text = "resumed by human (" + reason.String + ")"
			k := f + "\x00" + att
			if waitingSince[k] == "" && parent.Valid { // plan waited, its build resumed
				k = f + "\x00" + parent.String
			}
			if s := waitingSince[k]; s != "" {
				l.extra = "waited " + dur(parse(ts).Sub(parse(s)))
				delete(waitingSince, k)
			}
		case "human.steering":
			l.text = "human steering (" + src.String + ")"
		case "pr.linked":
			l.text = "PR linked " + url.String
		}
		lines = append(lines, l)
		return nil
	}, repo, issue)
	if err != nil {
		return nil, nil, err
	}

	for _, a := range atts {
		if a.ended.Valid {
			text := fmt.Sprintf("attempt %s %s", a.id, a.state)
			if a.state == "waiting_for_human" { // finished its run, but a human still has to act
				text = fmt.Sprintf("attempt %s finished, waiting for human", a.id)
			}
			extra := dur(parse(a.ended.String).Sub(parse(a.started))) + ", " + a.cost.String()
			if a.tools > 0 {
				extra += ", " + plural(a.tools, "tool call")
				if a.toolsFailed > 0 {
					extra += fmt.Sprintf(" (%d failed)", a.toolsFailed)
				}
			}
			if a.apiErrors > 0 {
				extra += ", " + plural(a.apiErrors, "API error")
			}
			lines = append(lines, line{a.ended.String, 3, a.factory, text, extra})
		}
	}

	type pr struct {
		repo   string
		number int64
	}
	var prs []pr
	err = store.Each(db, `SELECT p.repo, p.number, p.state, p.merged_at, p.factory_commits, p.human_commits FROM prs p
		WHERE p.url IN (SELECT pr_url FROM attempts WHERE repo = ? AND issue = ?)`, func(r *sql.Rows) error {
		var p pr
		var state string
		var merged sql.NullString
		var fc, hc int64
		if err := r.Scan(&p.repo, &p.number, &state, &merged, &fc, &hc); err != nil {
			return err
		}
		prs = append(prs, p)
		ts, verb := now.UTC().Format(time.RFC3339), state+" (as of now)"
		if merged.Valid {
			ts, verb = merged.String, "merged"
		}
		lines = append(lines, line{ts, 4, "github", fmt.Sprintf("PR %s#%d %s", p.repo, p.number, verb),
			fmt.Sprintf("%d factory / %d human commits", fc, hc)})
		return nil
	}, repo, issue)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range prs {
		err = store.Each(db, `SELECT reviewer, state, submitted_at FROM reviews WHERE repo = ? AND pr_number = ? AND submitted_at IS NOT NULL
			ORDER BY submitted_at, reviewer, state`, func(r *sql.Rows) error {
			var who, state, ts string
			if err := r.Scan(&who, &state, &ts); err != nil {
				return err
			}
			lines = append(lines, line{ts, 4, "github", "review by " + who + ": " + state, ""})
			return nil
		}, p.repo, p.number)
		if err != nil {
			return nil, nil, err
		}
		err = store.Each(db, `SELECT name, conclusion, completed_at FROM checks WHERE repo = ? AND pr_number = ?
			ORDER BY completed_at, name, head_sha`, func(r *sql.Rows) error {
			var name string
			var concl, done sql.NullString
			if err := r.Scan(&name, &concl, &done); err != nil {
				return err
			}
			c := concl.String
			if !concl.Valid {
				c = "pending"
			}
			ts := done.String
			if !done.Valid { // still running: show it as of now, like an open PR
				ts = now.UTC().Format(time.RFC3339)
			}
			lines = append(lines, line{ts, 4, "github", fmt.Sprintf("check %s on PR #%d: %s", name, p.number, c), ""})
			return nil
		}, p.repo, p.number)
		if err != nil {
			return nil, nil, err
		}
	}

	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].ts != lines[j].ts {
			return lines[i].ts < lines[j].ts
		}
		return lines[i].rank < lines[j].rank
	})
	return atts, lines, nil
}

// TimelineLine is one line of `report item`: Who is a factory or "github"; Kind orders lines with equal TS
// (0 started, 1 session or subagent, 2 gate or human or PR link, 3 finished, 4 GitHub).
type TimelineLine struct {
	TS, Who, Text, Extra string
	Kind, Depth          int // Depth: subagent nesting below the attempt
}

// Timeline returns the `report item` timeline of one work item as data.
func Timeline(db *sql.DB, repo string, issue int64, now time.Time) ([]TimelineLine, error) {
	_, lines, err := timeline(db, repo, issue, now)
	out := make([]TimelineLine, len(lines))
	for i, l := range lines {
		t := strings.TrimLeft(l.text, " ")
		out[i] = TimelineLine{l.ts, l.who, t, l.extra, l.rank, (len(l.text) - len(t)) / 2}
	}
	return out, err
}

func orNA(s sql.NullString) string {
	if !s.Valid {
		return "n/a"
	}
	return s.String
}

// totals summarises attempts. Active time is only shown for one factory: different factories run in parallel.
func totals(atts []*attempt, now time.Time, oneFactory bool) string {
	var c cost
	var in, out, cache sql.NullInt64
	states := map[string]int{}
	withUsage, waits := 0, 0
	var waitMS int64
	var first, last time.Time
	stagedF := map[string]bool{}
	handoffs, retries := 0, 0
	for _, a := range atts {
		if a.staged {
			stagedF[a.factory] = true
			if a.stage == "handoff" {
				handoffs++
			} else if a.stage == "retry" {
				retries++
			}
		}
		c.merge(a.cost)
		if a.cost.sessions > 0 {
			withUsage++
		}
		addTok(&in, a.in)
		addTok(&out, a.out)
		addTok(&cache, a.cache)
		states[a.state]++
		if a.waitMS > 0 {
			waits++
			waitMS += a.waitMS
		}
		s, e := parse(a.started), now
		if a.ended.Valid {
			e = parse(a.ended.String)
		}
		if first.IsZero() || s.Before(first) {
			first = s
		}
		if e.After(last) {
			last = e
		}
	}
	var st []string
	for _, k := range []string{"completed", "failed", "cancelled", "running", "waiting_for_human"} {
		if states[k] > 0 {
			st = append(st, fmt.Sprintf("%d %s", states[k], k))
		}
	}
	elapsed := last.Sub(first)
	wait := time.Duration(waitMS) * time.Millisecond
	t := dur(elapsed) + " elapsed (wall clock)"
	if oneFactory {
		t = dur(elapsed-wait) + " active / " + dur(elapsed) + " elapsed"
	}
	cs := c.String()
	if c.sessions > 0 && withUsage < len(atts) {
		cs += " +?"
	}
	n := fmt.Sprintf("%d attempts", len(atts))
	if len(stagedF) > 0 { // every staged run has one stage more than its handoffs
		n += " · " + stagesText(len(stagedF)+handoffs, retries)
	}
	return fmt.Sprintf("%s (%s) · %d sessions · tokens %s in / %s out / %s cache · %s · %d human waits (%s) · %s · usage coverage %d/%d attempts",
		n, strings.Join(st, ", "), c.sessions, tok(in), tok(out), tok(cache), t, waits, dur(wait), cs, withUsage, len(atts))
}

// LiveRow is one attempt that is running or waiting for a human. Nil pointers are unknown values.
type LiveRow struct {
	Factory, Repo  string
	Issue          int64
	Attempt, State string
	Started        time.Time
	Elapsed, Wait  time.Duration // Wait includes a human wait that is still open
	Tokens         *int64
	Cost           *float64 // sum of priced sessions
	Unpriced       int64    // sessions that couldn't be priced
	LastEvent      *time.Time
	LastTool       string // "" = no tool call yet
	LastToolAt     *time.Time
}

// LiveRows loads the attempts that are running or waiting for a human, oldest first.
func LiveRows(db *sql.DB, now time.Time) ([]LiveRow, error) {
	var out []LiveRow
	err := store.Each(db, `SELECT a.factory_id, a.repo, a.issue, a.attempt_id, a.state, a.started_at, a.human_wait_ms,
		a.waiting_since,
		(SELECT sum(input + output + coalesce(cache_read, 0) + coalesce(cache_write, 0)) FROM usage u
		   WHERE u.factory_id = a.factory_id AND u.attempt_id = a.attempt_id),
		(SELECT sum(cost_usd) FROM usage u WHERE u.factory_id = a.factory_id AND u.attempt_id = a.attempt_id),
		(SELECT count(*) FROM usage u WHERE u.factory_id = a.factory_id AND u.attempt_id = a.attempt_id AND cost_usd IS NULL),
		(SELECT max(ts) FROM events e WHERE e.factory_id = a.factory_id AND json_extract(e.payload, '$.attempt_id') = a.attempt_id),
		(SELECT tool_name || ' by ' || coalesce('subagent ' || agent_id, 'main') FROM tool_calls t
		   WHERE t.factory_id = a.factory_id AND t.attempt_id = a.attempt_id ORDER BY ts DESC, rowid DESC LIMIT 1),
		(SELECT max(ts) FROM tool_calls t WHERE t.factory_id = a.factory_id AND t.attempt_id = a.attempt_id)
		FROM attempts a WHERE state IN ('running', 'waiting_for_human') ORDER BY started_at`, func(rows *sql.Rows) error {
		var l LiveRow
		var started string
		var waitMS int64
		var waitingSince, lastTS, lastTool, lastToolTS sql.NullString
		var tokens sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&l.Factory, &l.Repo, &l.Issue, &l.Attempt, &l.State, &started, &waitMS, &waitingSince, &tokens, &cost, &l.Unpriced, &lastTS, &lastTool, &lastToolTS); err != nil {
			return err
		}
		l.Started = parse(started)
		l.Wait = time.Duration(waitMS) * time.Millisecond
		if l.State == "waiting_for_human" && waitingSince.Valid {
			l.Wait += now.Sub(parse(waitingSince.String))
		}
		l.Elapsed = now.Sub(l.Started)
		if tokens.Valid {
			l.Tokens = &tokens.Int64
		}
		if cost.Valid {
			l.Cost = &cost.Float64
		}
		if lastTS.Valid {
			t := parse(lastTS.String)
			l.LastEvent = &t
		}
		if lastTool.Valid {
			t := parse(lastToolTS.String)
			l.LastTool, l.LastToolAt = lastTool.String, &t
		}
		out = append(out, l)
		return nil
	})
	return out, err
}

// Live prints one row per attempt that is running or waiting for a human. The last tool column (latest tool call,
// the agent that made it, how long ago) is only shown when one of the attempts has tool calls.
func Live(db *sql.DB, w io.Writer, now time.Time) error {
	rows, err := LiveRows(db, now)
	if err != nil {
		return err
	}
	var out []string
	tools := false
	for _, l := range rows {
		c := "unknown"
		if l.Cost != nil {
			c = usd(*l.Cost)
			if l.Unpriced > 0 {
				c += " + unknown"
			}
		}
		tool := "-"
		if l.LastTool != "" {
			tools, tool = true, l.LastTool+" "+dur(now.Sub(*l.LastToolAt))+" ago"
		}
		last := "?"
		if l.LastEvent != nil {
			last = dur(now.Sub(*l.LastEvent))
		}
		var tokens sql.NullInt64
		if l.Tokens != nil {
			tokens = sql.NullInt64{Int64: *l.Tokens, Valid: true}
		}
		out = append(out, fmt.Sprintf("%s\t%s#%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s ago\t%s\n", l.Factory, l.Repo, l.Issue, l.Attempt, l.State, dur(l.Elapsed),
			dur(l.Elapsed-l.Wait), dur(l.Wait), tok(tokens), c, last, tool))
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "factory\tissue\tattempt\tstatus\telapsed\tactive\thuman wait\ttokens\tcost\tlast event"
	if tools {
		header += "\tlast tool"
	}
	fmt.Fprintln(tw, header)
	for _, l := range out {
		if !tools {
			l = l[:strings.LastIndex(l, "\t")] + "\n"
		}
		fmt.Fprint(tw, l)
	}
	return tw.Flush()
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
