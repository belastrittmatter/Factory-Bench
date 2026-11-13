// Package normalize turns raw events into the derived tables. derive is pure; Run truncates and rebuilds.
package normalize

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"factorybench/internal/contract"
	"factorybench/internal/outcome"
	"factorybench/internal/pricing"
	"factorybench/internal/store"
)

// tables lists every table normalize rebuilds.
var tables = []string{"work_items", "attempts", "usage", "gates", "prs", "checks", "reviews", "pr_commits",
	"tool_calls", "agent_runs", "api_errors"}

type event struct {
	Source, Factory, ID, Type, TS, AttemptID string
	Payload                                  []byte
}

type workItem struct {
	Repo                                  string
	Issue                                 int64
	TaskType                              *string
	Title, IssueState, ClosedAt, ClosedBy *string
	First                                 string
	Outcome                               *string
	Survival                              *string
}

type attempt struct {
	Factory, ID, Repo string
	Issue             int64
	Parent, Role      *string
	Config, State     string
	Started           string
	Ended             *string
	WaitMS            int64
	PRURL             *string
	WaitingSince      *string // open human wait, for the live report
}

type usage struct {
	Factory, Attempt, Model, TS                string
	Input, Output                              int64
	CacheRead, CacheWrite, Reasoning, Duration *int64
	Cost                                       *float64
	Provenance, PriceVersion, Phase, Agent     *string
}

type gate struct {
	Factory, Attempt, TS, Name string
	Passed                     bool
	Reason                     *string
}

type pr struct {
	Repo                   string
	Number                 int64
	URL                    string
	Branch, State, Merged  *string
	FactoryCommits, Humans int64
	Additions, Deletions   *int64
	Files                  *int64
}

type check struct {
	Repo                      string
	PR                        int64
	Name, HeadSHA             string
	Conclusion, Started, Done *string
}

type review struct {
	Repo            string
	PR              int64
	Reviewer, State string
	Submitted       *string
}

type prCommit struct {
	Repo                      string
	PR                        int64
	SHA                       string
	Email, Factory, Committed *string
}

type result struct {
	WorkItems []workItem
	Attempts  []attempt
	Usage     []usage
	Gates     []gate
	PRs       []pr
	Checks    []check
	Reviews   []review
	PRCommits []prCommit
	ToolCalls []toolCall
	AgentRuns []agentRun
	APIErrors []apiError
}

func ptr[T any](v T) *T { return &v }

func str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ms(from, to string) int64 {
	a, _ := time.Parse(time.RFC3339Nano, from)
	b, _ := time.Parse(time.RFC3339Nano, to)
	return b.Sub(a).Milliseconds()
}

func terminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

// derive computes all derived rows from accepted events sorted by (ts, source, factory, id).
// commitEmails maps a lower-cased commit author email to its factory, logins a lower-cased GitHub login.
func derive(events []event, commitEmails, logins map[string]string) result {
	var d result
	attempts := map[string]*attempt{} // factory \x00 attempt_id
	var order []string
	items := map[string]*workItem{}
	var itemOrder []string
	latest := map[string]string{} // factory \x00 repo#issue -> latest attempt id, for parent inference

	// Pass 1: attempts, in ts order, so a missing parent links to the latest earlier attempt.
	for _, e := range events {
		if e.Source != "contract" || e.Type != "attempt.started" {
			continue
		}
		k := e.Factory + "\x00" + e.AttemptID
		if attempts[k] != nil {
			continue
		}
		p := contract.ParsePayload(e.Payload)
		wi := p.WorkItem.Repo + "#" + itoa(p.WorkItem.Issue)
		parent := str(p.ParentAttemptID)
		if parent == nil {
			parent = str(latest[e.Factory+"\x00"+wi])
		}
		latest[e.Factory+"\x00"+wi] = e.AttemptID
		attempts[k] = &attempt{Factory: e.Factory, ID: e.AttemptID, Repo: p.WorkItem.Repo, Issue: p.WorkItem.Issue,
			Parent: parent, Role: str(p.Role), Config: p.FactoryConfigVersion, State: "running", Started: e.TS}
		order = append(order, k)
		if items[wi] == nil {
			items[wi] = &workItem{Repo: p.WorkItem.Repo, Issue: p.WorkItem.Issue, First: e.TS, TaskType: taskType(p.Labels)}
			itemOrder = append(itemOrder, wi)
		}
	}

	// Pass 2: everything else, in ts order.
	waits := map[string]string{}    // attempt key -> open human.waiting ts
	finished := map[string]string{} // attempt key -> status of an attempt that finished while its wait was open
	prs := map[string]*pr{}
	var prOrder []string
	runs := agents{runs: map[string]*agentRun{}}
	gh := github{commits: map[string][]prCommit{}, checks: map[int64]check{}, reviews: map[string][]review{},
		issues: map[string]ghIssue{}, emails: commitEmails, logins: logins}
	for _, e := range events {
		if e.Source == "github" {
			gh.add(e, prs, &prOrder)
			continue
		}
		k := e.Factory + "\x00" + e.AttemptID
		a := attempts[k]
		if a == nil || e.Type == "attempt.started" {
			continue // events of an attempt that never started stay in events only
		}
		if e.Source == "otlp" {
			d.otlp(e, &runs)
			continue
		}
		p := contract.ParsePayload(e.Payload)
		switch e.Type {
		case "human.waiting":
			if !terminal(a.State) {
				a.State = "waiting_for_human"
			}
			if waits[k] == "" {
				waits[k] = e.TS
			}
		case "human.resumed":
			if a.State == "waiting_for_human" {
				a.State = "running"
			}
			wk := k
			if waits[wk] == "" && a.Parent != nil { // plan waited, its build resumed
				wk = e.Factory + "\x00" + *a.Parent
			}
			if w := waits[wk]; w != "" {
				a.WaitMS += ms(w, e.TS)
				delete(waits, wk)
				if st := finished[wk]; st != "" { // the waiting attempt already finished: now it's done
					attempts[wk].State = st
					delete(finished, wk)
				}
			}
		case "attempt.finished":
			if !terminal(a.State) && a.Ended == nil {
				a.Ended = ptr(e.TS)
				if waits[k] != "" { // e.g. a plan finishes while its review is pending: still waiting for a human
					finished[k] = p.Status
				} else {
					a.State = p.Status
				}
			}
		case "pr.linked":
			a.PRURL = ptr(p.PRURL)
		case "gate":
			d.Gates = append(d.Gates, gate{Factory: e.Factory, Attempt: a.ID, TS: e.TS, Name: p.Name, Passed: *p.Passed, Reason: str(p.Reason)})
		case "usage":
			runs.see(e, p.AgentID, p.ParentAgentID, p.Agent, false)
			d.Usage = append(d.Usage, priced(usage{Factory: e.Factory, Attempt: a.ID, Model: p.Model, TS: e.TS, Input: *p.Input,
				Output: *p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite, Reasoning: p.Reasoning, Duration: p.DurationMS,
				Phase: str(p.Phase), Agent: str(p.AgentID)}, p.ReportedCostUSD))
		case "activity":
			r := runs.see(e, p.AgentID, p.ParentAgentID, p.Agent, false)
			if r != nil && r.Phase == nil && contract.Phases[p.Phase] {
				r.Phase = ptr(p.Phase)
			}
			if p.Tool != "" {
				var ok *bool
				if p.Failed != nil {
					ok = ptr(!*p.Failed)
				}
				d.ToolCalls = append(d.ToolCalls, toolCall{Factory: e.Factory, Attempt: a.ID, Agent: str(p.AgentID), TS: e.TS,
					Tool: p.Tool, Success: ok, Duration: p.DurationMS})
			}
		}
	}

	byURL := map[string]*pr{}
	for _, k := range prOrder {
		pr := prs[k]
		for _, c := range gh.commits[k] {
			if c.Factory != nil {
				pr.FactoryCommits++
			} else {
				pr.Humans++
			}
		}
		byURL[pr.URL] = pr
		d.PRs = append(d.PRs, *pr)
	}
	for _, k := range gh.order { // every repo#pr with commits or reviews, first-seen order
		d.PRCommits = append(d.PRCommits, gh.commits[k]...)
		d.Reviews = append(d.Reviews, gh.reviews[k]...)
	}
	for _, id := range gh.checkOrder {
		d.Checks = append(d.Checks, gh.checks[id])
	}
	// A usage row without a phase of its own takes its subagent's, if an activity event gave it one. This is how a
	// factory that runs every stage in one Claude Code process (one OTEL_RESOURCE_ATTRIBUTES for all its subagents)
	// phases its OTLP tokens. It is applied after all events, so it doesn't matter which arrived first.
	for i := range d.Usage {
		if u := &d.Usage[i]; u.Phase == nil && u.Agent != nil {
			if r := runs.runs[u.Factory+"\x00"+u.Attempt+"\x00"+*u.Agent]; r != nil {
				u.Phase = r.Phase
			}
		}
	}
	d.AgentRuns = runs.finish(attempts, d.ToolCalls)
	prStates := map[string]map[string]bool{} // work item -> states of its linked PRs
	for _, k := range order {
		a := attempts[k]
		a.WaitingSince = str(waits[k])
		d.Attempts = append(d.Attempts, *a)
		if a.PRURL != nil && byURL[*a.PRURL] != nil {
			wi := a.Repo + "#" + itoa(a.Issue)
			if prStates[wi] == nil {
				prStates[wi] = map[string]bool{}
			}
			prStates[wi][*byURL[*a.PRURL].State] = true
		}
	}
	for _, k := range itemOrder {
		wi := items[k]
		if is, ok := gh.issues[k]; ok { // the GitHub issue overrides the labels the factory sent
			var labels []string
			for _, l := range is.Issue.Labels {
				labels = append(labels, l.Name)
			}
			wi.TaskType, wi.Title, wi.IssueState, wi.ClosedAt = taskType(labels), str(is.Issue.Title), str(is.Issue.State), is.Issue.ClosedAt
			if is.Issue.ClosedBy != nil {
				wi.ClosedBy = str(is.Issue.ClosedBy.Login)
			}
		}
		wi.Outcome = ptr(outcome.FromPRStates(prStates[k]))
		if *wi.Outcome == "merged" {
			wi.Survival = ptr("pending")
		}
		d.WorkItems = append(d.WorkItems, *wi)
	}
	return d
}

// priced sets u's cost: the reported one if any, else estimated from the price table.
func priced(u usage, reported *float64) usage {
	if reported != nil {
		u.Cost, u.Provenance = reported, ptr("reported")
	} else if usd, ver, ok := pricing.Estimate(u.TS, u.Model, u.Input, u.Output, u.CacheRead, u.CacheWrite); ok {
		u.Cost, u.Provenance, u.PriceVersion = &usd, ptr("estimated"), ptr(ver)
	}
	return u
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// taskType is the first label that isn't a namespaced workflow label (e.g. agent:ready).
func taskType(labels []string) *string {
	for _, l := range labels {
		if !strings.Contains(l, ":") {
			return ptr(l)
		}
	}
	return nil
}

// load reads accepted events in normalization order and the registered commit emails and GitHub logins.
func load(tx *sql.Tx) (evs []event, emails, logins map[string]string, err error) {
	// contract rows hold the whole raw event; github rows hold the reconciler's wrapper object;
	// OTLP rows otlp() ignores (other event names, outside a subagent) are about half of all rows, so they are not read
	err = store.Each(tx, `SELECT source, factory_id, id, type, ts,
		CASE source WHEN 'contract' THEN json_extract(payload, '$.payload') ELSE payload END,
		json_extract(payload, '$.attempt_id')
		FROM events WHERE rejected IS NULL AND (source <> 'otlp' OR json_extract(payload, '$.agent_id') IS NOT NULL OR type IN
		  ('otlp.api_request', 'otlp.tool_result', 'otlp.subagent_completed', 'otlp.api_error', 'otlp.api_retries_exhausted'))
		ORDER BY ts, source, factory_id, id`, func(r *sql.Rows) error {
		var e event
		var p string
		var att sql.NullString
		err := r.Scan(&e.Source, &e.Factory, &e.ID, &e.Type, &e.TS, &p, &att)
		e.AttemptID, e.Payload = att.String, []byte(p)
		evs = append(evs, e)
		return err
	})
	if err != nil {
		return nil, nil, nil, err
	}
	emails, logins = map[string]string{}, map[string]string{}
	err = store.Each(tx, `SELECT factory_id, coalesce(commit_email, ''), coalesce(github_login, '') FROM factories`, func(r *sql.Rows) error {
		var f, m, l string
		err := r.Scan(&f, &m, &l)
		if m != "" {
			emails[m] = f
		}
		if l != "" {
			logins[l] = f
		}
		return err
	})
	return evs, emails, logins, err
}

// Run truncates every derived table and rebuilds it from events. It is also `factorybench replay`.
func Run(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Write first so the transaction holds SQLite's write lock before reading: a concurrent
	// serve/reconcile rebuild waits instead of overwriting this one with stale events.
	for _, t := range tables {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	evs, emails, logins, err := load(tx)
	if err != nil {
		return err
	}
	d := derive(evs, emails, logins)
	ins := func(q string, args ...any) {
		if err == nil {
			_, err = tx.Exec(q, args...)
		}
	}
	for _, w := range d.WorkItems {
		ins(`INSERT INTO work_items (repo, issue, task_type, title, issue_state, closed_at, closed_by, first_seen, outcome, survival)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, w.Repo, w.Issue, w.TaskType, w.Title, w.IssueState, w.ClosedAt, w.ClosedBy, w.First, w.Outcome, w.Survival)
	}
	for _, a := range d.Attempts {
		ins(`INSERT INTO attempts VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.Factory, a.ID, a.Repo, a.Issue, a.Parent, a.Role,
			a.Config, a.State, a.Started, a.Ended, a.WaitMS, a.PRURL, a.WaitingSince)
	}
	for _, u := range d.Usage {
		ins(`INSERT INTO usage VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.Factory, u.Attempt, u.Model, u.TS, u.Input, u.Output,
			u.CacheRead, u.CacheWrite, u.Reasoning, u.Duration, u.Cost, u.Provenance, u.PriceVersion, u.Phase, u.Agent)
	}
	for _, g := range d.Gates {
		ins(`INSERT INTO gates VALUES (?,?,?,?,?,?)`, g.Factory, g.Attempt, g.TS, g.Name, g.Passed, g.Reason)
	}
	for _, p := range d.PRs {
		ins(`INSERT INTO prs VALUES (?,?,?,?,?,?,?,?,?,?,?)`, p.Repo, p.Number, p.URL, p.Branch, p.State, p.Merged, p.FactoryCommits,
			p.Humans, p.Additions, p.Deletions, p.Files)
	}
	for _, c := range d.Checks {
		ins(`INSERT INTO checks VALUES (?,?,?,?,?,?,?)`, c.Repo, c.PR, c.Name, c.HeadSHA, c.Conclusion, c.Started, c.Done)
	}
	for _, r := range d.Reviews {
		ins(`INSERT INTO reviews VALUES (?,?,?,?,?)`, r.Repo, r.PR, r.Reviewer, r.State, r.Submitted)
	}
	for _, c := range d.PRCommits {
		ins(`INSERT INTO pr_commits VALUES (?,?,?,?,?,?)`, c.Repo, c.PR, c.SHA, c.Email, c.Factory, c.Committed)
	}
	for _, c := range d.ToolCalls {
		ins(`INSERT INTO tool_calls VALUES (?,?,?,?,?,?,?,?)`, c.Factory, c.Attempt, c.Agent, c.TS, c.Tool, c.Success, c.Duration, c.ErrorType)
	}
	for _, r := range d.AgentRuns {
		ins(`INSERT INTO agent_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, r.Factory, r.Attempt, r.ID, r.Parent, r.Type, r.Async,
			r.Model, r.FinalModel, r.ToolCalls, r.Duration, r.Status, r.Started)
	}
	for _, x := range d.APIErrors {
		ins(`INSERT INTO api_errors VALUES (?,?,?,?,?,?,?,?,?)`, x.Factory, x.Attempt, x.Agent, x.TS, x.Kind, x.Model, x.Status, x.Error, x.Duration)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
