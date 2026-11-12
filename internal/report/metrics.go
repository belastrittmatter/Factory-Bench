package report

import (
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"factorybench/internal/outcome"
	"factorybench/internal/store"
)

// Metrics for `report items` and `report factories`. Every number is built from units: one factory (or
// factory+config) on one work item. Definitions are in docs/reference.md "Metrics".

type mAtt struct {
	factory, id, repo, role, config, state, started string
	issue                                           int64
	parent, ended, pr                               sql.NullString
	waitMS                                          int64
}

type mUse struct {
	tokens      int64
	cost        sql.NullFloat64
	prov, phase sql.NullString
	ms          sql.NullInt64
}

type mTool struct {
	name string
	ok   sql.NullBool
	ms   sql.NullInt64
}

type mItem struct {
	key, title, firstSeen string
	issueState, closedBy  sql.NullString
}

type mPR struct {
	key, state, merged string // key is repo#number; merged is "" if not merged
	number             int64
	add, del, files    sql.NullInt64
}

type mReview struct{ who, state, at string } // at is "" when not submitted
type mCommit struct {
	human bool
	at    string
}
type mCheck struct{ name, sha, concl, started, done string } // "" = NULL
type mEvent struct{ typ, attempt, ts string }
type mGate struct {
	name   string
	passed bool
}

type metricsData struct {
	atts     []*mAtt            // ordered by started_at
	usage    map[string][]mUse  // factory \x00 attempt
	gates    map[string][]mGate // factory \x00 attempt
	items    map[string]*mItem  // repo#issue
	login    map[string]string  // factory -> lower-cased github login
	bot      map[string]bool    // lower-cased logins of all factories: their reviews aren't human
	prs      map[string]*mPR    // url
	reviews  map[string][]mReview
	commits  map[string][]mCommit
	checks   map[string][]mCheck
	human    map[string][]mEvent // factory \x00 repo#issue, ordered by ts
	rejected map[string]int
	tools    map[string][]mTool // factory \x00 attempt
	agents   map[string]int     // factory \x00 attempt -> subagent runs
	apiErrs  map[string]int     // factory \x00 attempt
	linked   map[string]string  // factory \x00 attempt -> first pr.linked ts
}

func loadMetrics(db *sql.DB) (*metricsData, error) {
	d := &metricsData{usage: map[string][]mUse{}, gates: map[string][]mGate{}, items: map[string]*mItem{},
		login: map[string]string{}, bot: map[string]bool{}, prs: map[string]*mPR{}, reviews: map[string][]mReview{},
		commits: map[string][]mCommit{}, checks: map[string][]mCheck{}, human: map[string][]mEvent{}, rejected: map[string]int{},
		tools: map[string][]mTool{}, agents: map[string]int{}, apiErrs: map[string]int{}, linked: map[string]string{}}
	key := func(repo string, n int64) string { return fmt.Sprintf("%s#%d", repo, n) }
	return d, firstErr(
		store.Each(db, `SELECT factory_id, attempt_id, repo, issue, coalesce(role, ''), config_version, state, started_at,
			parent_attempt_id, ended_at, pr_url, human_wait_ms FROM attempts ORDER BY started_at, factory_id, attempt_id`,
			func(r *sql.Rows) error {
				a := &mAtt{}
				d.atts = append(d.atts, a)
				return r.Scan(&a.factory, &a.id, &a.repo, &a.issue, &a.role, &a.config, &a.state, &a.started,
					&a.parent, &a.ended, &a.pr, &a.waitMS)
			}),
		store.Each(db, `SELECT factory_id, attempt_id, input + output + coalesce(cache_read, 0) + coalesce(cache_write, 0),
			cost_usd, cost_provenance, phase, duration_ms FROM usage`, func(r *sql.Rows) error {
			var f, id string
			var u mUse
			err := r.Scan(&f, &id, &u.tokens, &u.cost, &u.prov, &u.phase, &u.ms)
			d.usage[f+"\x00"+id] = append(d.usage[f+"\x00"+id], u)
			return err
		}),
		store.Each(db, `SELECT factory_id, attempt_id, name, passed FROM gates`, func(r *sql.Rows) error {
			var f, id string
			var g mGate
			err := r.Scan(&f, &id, &g.name, &g.passed)
			d.gates[f+"\x00"+id] = append(d.gates[f+"\x00"+id], g)
			return err
		}),
		store.Each(db, `SELECT repo, issue, coalesce(title, ''), first_seen, issue_state, closed_by FROM work_items`, func(r *sql.Rows) error {
			var repo string
			var n int64
			it := &mItem{}
			err := r.Scan(&repo, &n, &it.title, &it.firstSeen, &it.issueState, &it.closedBy)
			it.key = key(repo, n)
			d.items[it.key] = it
			return err
		}),
		store.Each(db, `SELECT factory_id, lower(coalesce(github_login, '')) FROM factories`, func(r *sql.Rows) error {
			var f, l string
			err := r.Scan(&f, &l)
			if l != "" {
				d.login[f], d.bot[l] = l, true
			}
			return err
		}),
		store.Each(db, `SELECT url, repo, number, coalesce(state, ''), coalesce(merged_at, ''), additions, deletions, changed_files
			FROM prs`, func(r *sql.Rows) error {
			var url, repo string
			p := &mPR{}
			err := r.Scan(&url, &repo, &p.number, &p.state, &p.merged, &p.add, &p.del, &p.files)
			p.key = key(repo, p.number)
			d.prs[url] = p
			return err
		}),
		store.Each(db, `SELECT repo, pr_number, reviewer, state, coalesce(submitted_at, '') FROM reviews ORDER BY submitted_at`, func(r *sql.Rows) error {
			var repo string
			var n int64
			var v mReview
			err := r.Scan(&repo, &n, &v.who, &v.state, &v.at)
			d.reviews[key(repo, n)] = append(d.reviews[key(repo, n)], v)
			return err
		}),
		store.Each(db, `SELECT repo, pr_number, factory_id IS NULL, coalesce(committed_at, '') FROM pr_commits`, func(r *sql.Rows) error {
			var repo string
			var n int64
			var c mCommit
			err := r.Scan(&repo, &n, &c.human, &c.at)
			d.commits[key(repo, n)] = append(d.commits[key(repo, n)], c)
			return err
		}),
		store.Each(db, `SELECT repo, pr_number, name, head_sha, coalesce(conclusion, ''), coalesce(started_at, ''), coalesce(completed_at, '')
			FROM checks`, func(r *sql.Rows) error {
			var repo string
			var n int64
			var c mCheck
			err := r.Scan(&repo, &n, &c.name, &c.sha, &c.concl, &c.started, &c.done)
			d.checks[key(repo, n)] = append(d.checks[key(repo, n)], c)
			return err
		}),
		store.Each(db, `SELECT e.factory_id, a.repo, a.issue, e.type, a.attempt_id, e.ts FROM events e
			JOIN attempts a ON a.factory_id = e.factory_id AND a.attempt_id = json_extract(e.payload, '$.attempt_id')
			WHERE e.source = 'contract' AND e.rejected IS NULL AND e.type IN ('human.waiting', 'human.resumed', 'human.steering')
			ORDER BY e.ts, e.id`, func(r *sql.Rows) error {
			var f, repo string
			var n int64
			var e mEvent
			err := r.Scan(&f, &repo, &n, &e.typ, &e.attempt, &e.ts)
			k := f + "\x00" + key(repo, n)
			d.human[k] = append(d.human[k], e)
			return err
		}),
		store.Each(db, `SELECT factory_id, count(*) FROM events WHERE rejected IS NOT NULL GROUP BY factory_id`, func(r *sql.Rows) error {
			var f string
			var n int
			err := r.Scan(&f, &n)
			d.rejected[f] = n
			return err
		}),
		store.Each(db, `SELECT factory_id, attempt_id, tool_name, success, duration_ms FROM tool_calls`, func(r *sql.Rows) error {
			var f, id string
			var t mTool
			err := r.Scan(&f, &id, &t.name, &t.ok, &t.ms)
			d.tools[f+"\x00"+id] = append(d.tools[f+"\x00"+id], t)
			return err
		}),
		store.Each(db, `SELECT factory_id, attempt_id, 'a' FROM agent_runs UNION ALL SELECT factory_id, attempt_id, 'e' FROM api_errors`,
			func(r *sql.Rows) error {
				var f, id, kind string
				err := r.Scan(&f, &id, &kind)
				if kind == "a" {
					d.agents[f+"\x00"+id]++
				} else {
					d.apiErrs[f+"\x00"+id]++
				}
				return err
			}),
		store.Each(db, `SELECT factory_id, json_extract(payload, '$.attempt_id'), min(ts) FROM events
			WHERE source = 'contract' AND type = 'pr.linked' AND rejected IS NULL GROUP BY 1, 2`, func(r *sql.Rows) error {
			var f, id, ts string
			err := r.Scan(&f, &id, &ts)
			d.linked[f+"\x00"+id] = ts
			return err
		}),
	)
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// unit is one factory (or factory+config) on one work item.
type unit struct {
	group, factory string
	item           *mItem
	atts           []*mAtt
	prs            []*mPR
	outcome        string // from this unit's own PRs, by outcome.FromPRStates like the work item

	attempts, failed, sessions, withUsage, phased int
	tokens, verifyTok                             int64
	cost                                          cost
	elapsed, active                               time.Duration
	human, escaped, escapedPRs                    int
	planN, planPass, prN, prPass                  int // first-pass approvals: denominators and passes
	reviewedPRs, changesReq                       int
	run                                           runStats
}

// runStats are the tool call, subagent and PR metrics of a unit, summed into its card by merge.
type runStats struct {
	withActivity, toolCalls, toolKnown, toolFailed, subagents, apiErrors int
	modelMS, toolMS                                                      int64
	tools                                                                map[string]int
	sizedPRs, sizedNoUsage                                               int
	added, deleted, files                                                int64
	sizedCost                                                            cost // cost of units with a sized PR, for cost / changed line
	toPR, toMerge                                                        time.Duration
	nToPR, nToMerge, ciN, ciPass                                         int
}

func (s *runStats) merge(o runStats) {
	s.withActivity += o.withActivity
	s.toolCalls += o.toolCalls
	s.toolKnown += o.toolKnown
	s.toolFailed += o.toolFailed
	s.subagents += o.subagents
	s.apiErrors += o.apiErrors
	s.modelMS += o.modelMS
	s.toolMS += o.toolMS
	if s.tools == nil {
		s.tools = map[string]int{}
	}
	for k, v := range o.tools {
		s.tools[k] += v
	}
	s.sizedPRs += o.sizedPRs
	s.sizedNoUsage += o.sizedNoUsage
	s.added += o.added
	s.deleted += o.deleted
	s.files += o.files
	s.sizedCost.merge(o.sizedCost)
	s.toPR += o.toPR
	s.toMerge += o.toMerge
	s.nToPR += o.nToPR
	s.nToMerge += o.nToMerge
	s.ciN += o.ciN
	s.ciPass += o.ciPass
}

func buildUnits(d *metricsData, byConfig bool, now time.Time) []*unit {
	var units []*unit
	byKey := map[string]*unit{}
	for _, a := range d.atts {
		g := a.factory
		if byConfig {
			g += " " + a.config
		}
		k := g + "\x00" + fmt.Sprintf("%s#%d", a.repo, a.issue)
		u := byKey[k]
		if u == nil {
			it := d.items[fmt.Sprintf("%s#%d", a.repo, a.issue)]
			if it == nil { // attempts and work items are derived together; this only guards a half-written DB
				continue
			}
			u = &unit{group: g, factory: a.factory, item: it}
			byKey[k] = u
			units = append(units, u)
		}
		u.atts = append(u.atts, a)
	}
	for _, u := range units {
		u.compute(d, now)
	}
	sort.SliceStable(units, func(i, j int) bool {
		if a, b := units[i].item.firstSeen, units[j].item.firstSeen; a != b {
			return a > b
		}
		if units[i].item.key != units[j].item.key {
			return units[i].item.key < units[j].item.key
		}
		return units[i].group < units[j].group
	})
	return units
}

func (u *unit) compute(d *metricsData, now time.Time) {
	var first, last time.Time
	var waitMS int64
	open := false // an attempt still running: nothing can have escaped it yet
	ids := map[string]*mAtt{}
	seenPR := map[string]bool{}
	linked := ""
	u.run.tools = map[string]int{}
	for _, a := range u.atts {
		k := a.factory + "\x00" + a.id
		if ts := d.linked[k]; ts != "" && (linked == "" || ts < linked) {
			linked = ts
		}
		if len(d.tools[k]) > 0 {
			u.run.withActivity++
		}
		for _, t := range d.tools[k] {
			u.run.toolCalls++
			u.run.tools[t.name]++
			u.run.toolMS += t.ms.Int64
			if t.ok.Valid {
				u.run.toolKnown++
				if !t.ok.Bool {
					u.run.toolFailed++
				}
			}
		}
		u.run.subagents += d.agents[k]
		u.run.apiErrors += d.apiErrs[k]
		ids[a.id] = a
		u.attempts++
		if a.state == "failed" {
			u.failed++
		}
		uses := d.usage[a.factory+"\x00"+a.id]
		if len(uses) > 0 {
			u.withUsage++
		}
		for _, s := range uses {
			u.run.modelMS += s.ms.Int64
			u.sessions++
			u.tokens += s.tokens
			u.cost.add(s.cost, s.prov)
			if s.phase.Valid {
				u.phased++
				if s.phase.String == "verify" || s.phase.String == "review" {
					u.verifyTok += s.tokens
				}
			}
		}
		waitMS += a.waitMS
		s, e := parse(a.started), now
		if a.ended.Valid {
			e = parse(a.ended.String)
		} else {
			open = true
		}
		if first.IsZero() || s.Before(first) {
			first = s
		}
		if e.After(last) {
			last = e
		}
		if p := d.prs[a.pr.String]; a.pr.Valid && p != nil && !seenPR[a.pr.String] {
			seenPR[a.pr.String] = true
			u.prs = append(u.prs, p)
		}
	}
	u.elapsed = last.Sub(first)
	u.active = u.elapsed - time.Duration(waitMS)*time.Millisecond
	u.prStats(d, first, linked)

	st := map[string]bool{}
	for _, p := range u.prs {
		st[p.state] = true
	}
	u.outcome = outcome.FromPRStates(st)

	evs := d.human[u.factory+"\x00"+u.item.key]
	for _, e := range evs {
		if ids[e.attempt] != nil && (e.typ == "human.waiting" || e.typ == "human.steering") {
			u.human++
		}
	}

	// Escaped: human pushback after the factory's last attempt on the item ended (it had handed the PR over).
	after := func(ts string) bool { return !open && ts != "" && parse(ts).After(last) }
	for _, p := range u.prs {
		esc := 0
		var firstVerdict *mReview
		human := false
		for i, r := range d.reviews[p.key] {
			if d.bot[strings.ToLower(r.who)] {
				continue
			}
			human = true
			if r.state == "CHANGES_REQUESTED" {
				u.changesReq++
				u.human++
				if after(r.at) {
					esc++
				}
			}
			if (r.state == "APPROVED" || r.state == "CHANGES_REQUESTED") && r.at != "" && firstVerdict == nil {
				firstVerdict = &d.reviews[p.key][i] // reviews are loaded in submitted_at order
			}
		}
		for _, c := range d.commits[p.key] {
			if c.human {
				u.human++
				if after(c.at) {
					esc++
				}
			}
		}
		if human {
			u.reviewedPRs++
		}
		if firstVerdict != nil {
			u.prN++
			if firstVerdict.state == "APPROVED" {
				u.prPass++
			}
		}
		if esc > 0 {
			u.escaped += esc
			u.escapedPRs++
		}
	}

	// First-pass plan approval: the first plan waited, was resumed with no steering before, and was never redone.
	var plans []*mAtt
	for _, a := range u.atts {
		if a.role == "plan" {
			plans = append(plans, a)
		}
	}
	if len(plans) == 0 {
		return
	}
	waited, resumed := false, ""
	for _, e := range evs {
		a := ids[e.attempt]
		if a == nil {
			continue
		}
		if e.typ == "human.waiting" && a.role == "plan" {
			waited = true
		}
		if e.typ == "human.resumed" && resumed == "" && (a.id == plans[0].id || a.parent.String == plans[0].id) {
			resumed = e.ts
		}
	}
	if !waited || resumed == "" { // never waited, or the review is still pending: not decided yet
		return
	}
	u.planN = 1
	for _, e := range evs {
		if e.typ == "human.steering" && ids[e.attempt] != nil && parse(e.ts).Before(parse(resumed)) {
			return
		}
	}
	if len(plans) == 1 {
		u.planPass = 1
	}
}

// prStats sets the unit's PR size, time to first PR and to merge (from its first attempt start), and CI on the first push.
func (u *unit) prStats(d *metricsData, first time.Time, linked string) {
	if linked != "" {
		u.run.nToPR, u.run.toPR = 1, parse(linked).Sub(first)
	}
	merged := ""
	for _, p := range u.prs {
		if p.merged != "" && (merged == "" || p.merged < merged) {
			merged = p.merged
		}
		if p.add.Valid && p.del.Valid {
			u.run.sizedPRs++
			u.run.added, u.run.deleted, u.run.files = u.run.added+p.add.Int64, u.run.deleted+p.del.Int64, u.run.files+p.files.Int64
		}
		// shortcut: the first push is the head SHA of the earliest-started check run; pushes without checks are invisible
		var sha, at string
		for _, c := range d.checks[p.key] {
			if c.started != "" && (at == "" || c.started < at) {
				sha, at = c.sha, c.started
			}
		}
		runs, ok := 0, true
		for _, c := range d.checks[p.key] {
			if c.sha == sha && (c.concl == "success" || c.concl == "failure" || c.concl == "timed_out") {
				runs++
				ok = ok && c.concl == "success"
			}
		}
		if runs > 0 {
			u.run.ciN++
			if ok {
				u.run.ciPass++
			}
		}
	}
	if merged != "" {
		u.run.nToMerge, u.run.toMerge = 1, parse(merged).Sub(first)
	}
	if u.run.sizedPRs > 0 {
		u.run.sizedCost, u.run.sizedNoUsage = u.cost, u.attempts-u.withUsage
	}
}

func rate(k, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%% (%d/%d)", 100*float64(k)/float64(n), k, n)
}

// money shows a USD amount, "?" when nothing was priced, and "+?" (a lower bound) when some sessions are unpriced
// or noUsage attempts sent no usage.
func money(v float64, c cost, noUsage int) string {
	if c.sessions == c.nUnk {
		return "?"
	}
	s := usd(v)
	if c.nUnk > 0 || noUsage > 0 {
		s += " +?"
	}
	return s
}

const legend = "+? = lower bound: some attempts sent no usage or some sessions couldn't be priced"

func (u *unit) tokensString() string {
	if u.withUsage == 0 {
		return "?"
	}
	s := tok(sql.NullInt64{Int64: u.tokens, Valid: true})
	if u.withUsage < u.attempts {
		s += " +?"
	}
	return s
}

// Items prints one row per (work item, factory).
func Items(db *sql.DB, w io.Writer, factory, outcome string, now time.Time) error {
	d, err := loadMetrics(db)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "item\ttitle\tfactory\toutcome\tattempts\ttokens\tcost\tactive\telapsed\thuman\tPR\tescaped")
	for _, u := range buildUnits(d, false, now) {
		if (factory != "" && u.factory != factory) || (outcome != "" && u.outcome != outcome) {
			continue
		}
		title := u.item.title
		if r := []rune(title); len(r) > 40 {
			title = string(r[:39]) + "…"
		}
		att := fmt.Sprint(u.attempts)
		if u.failed > 0 {
			att += fmt.Sprintf(" (%d failed)", u.failed)
		}
		var prs []string
		for _, p := range u.prs {
			prs = append(prs, fmt.Sprintf("#%d %s", p.number, p.state))
		}
		pr := strings.Join(prs, ", ")
		if pr == "" {
			pr = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%d\n", u.item.key, title, u.factory, u.outcome, att,
			u.tokensString(), money(u.cost.rep+u.cost.est, u.cost, u.attempts-u.withUsage), dur(u.active), dur(u.elapsed), u.human, pr, u.escaped)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, legend+"\nopen one: factorybench report item <owner/repo#n>")
	return err
}

// ItemRow is one row of `report items` plus the per-unit PR columns, preformatted: "?" and "n/a" are unknown, never 0.
type ItemRow struct {
	Item, Title, Factory, Outcome, Attempts, Tokens, Cost, Active, Elapsed string
	Human, Escaped                                                         int
	PRs, ToPR, ToMerge, ReviewRounds, CIFirstPush, Size                    string

	// the same values as numbers, for aggregates; nil = unknown or nothing to measure
	HasPR, Merged  bool
	ToPRSeconds    *float64
	CostUSD        *float64 // priced sessions; nil when nothing was priced
	CostLowerBound bool     // some sessions unpriced or attempts without usage (the +? of Cost)
}

// ItemRows returns one row per (work item, factory), newest first, from the same units as Items.
func ItemRows(db *sql.DB, now time.Time) ([]ItemRow, error) {
	d, err := loadMetrics(db)
	if err != nil {
		return nil, err
	}
	var out []ItemRow
	for _, u := range buildUnits(d, false, now) {
		r := ItemRow{Item: u.item.key, Title: u.item.title, Factory: u.factory, Outcome: u.outcome, Attempts: fmt.Sprint(u.attempts),
			Tokens: u.tokensString(), Cost: money(u.cost.rep+u.cost.est, u.cost, u.attempts-u.withUsage), Active: dur(u.active),
			Elapsed: dur(u.elapsed), Human: u.human, Escaped: u.escaped, ToPR: "n/a", ToMerge: "n/a", ReviewRounds: "n/a", CIFirstPush: "n/a", Size: "?"}
		if u.failed > 0 {
			r.Attempts += fmt.Sprintf(" (%d failed)", u.failed)
		}
		var prs []string
		for _, p := range u.prs {
			prs = append(prs, fmt.Sprintf("#%d %s", p.number, p.state))
		}
		r.PRs = strings.Join(prs, ", ")
		if u.run.nToPR > 0 {
			r.ToPR = dur(u.run.toPR)
			v := u.run.toPR.Seconds()
			r.ToPRSeconds = &v
		}
		r.HasPR, r.Merged = len(u.prs) > 0, u.outcome == "merged"
		if u.cost.sessions > u.cost.nUnk {
			v := u.cost.rep + u.cost.est
			r.CostUSD = &v
		}
		r.CostLowerBound = u.cost.nUnk > 0 || u.attempts > u.withUsage
		if u.run.nToMerge > 0 {
			r.ToMerge = dur(u.run.toMerge)
		}
		if u.reviewedPRs > 0 {
			r.ReviewRounds = fmt.Sprintf("%d on %d reviewed PRs", u.changesReq, u.reviewedPRs)
		}
		if u.run.ciN > 0 {
			r.CIFirstPush = rate(u.run.ciPass, u.run.ciN)
		}
		if u.run.sizedPRs > 0 {
			r.Size = fmt.Sprintf("+%d -%d, %d files", u.run.added, u.run.deleted, u.run.files)
		}
		out = append(out, r)
	}
	return out, nil
}
