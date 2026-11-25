package dashboard

import (
	"cmp"
	"sort"
	"strings"
	"time"

	"factorybench/internal/contract"
	"factorybench/internal/store"
)

// stepSQL is an OTLP event's factorybench.step as normalize stores it in usage.step: trimmed, at most 64 characters,
// NULL when absent or empty.
const stepSQL = `nullif(trim(substr(trim(json_extract(payload, '$."factorybench.step"')), 1, 64)), '')`

// stepUsageSQL is every usage row with a step or a session of the attempts a matching filter, for stepData.
func stepUsageSQL(filter string) string {
	return `SELECT u.factory_id, u.attempt_id, u.step, u.ts, u.cost_usd, u.session, u.duration_ms, u.phase, u.harness, u.status, u.turns, u.lane,
	u.input + u.output + coalesce(u.cache_read, 0) + coalesce(u.cache_write, 0) AS tokens FROM usage u JOIN attempts a USING (factory_id, attempt_id)
	WHERE (u.step IS NOT NULL OR u.session IS NOT NULL) AND ` + filter + ` ORDER BY u.ts`
}

// stepToolsSQL is every tool call of the attempts a matching filter, for the tool counts of stepData's sessions.
func stepToolsSQL(filter string) string {
	return `SELECT t.factory_id, t.attempt_id, t.ts, t.success FROM tool_calls t JOIN attempts a USING (factory_id, attempt_id) WHERE ` + filter + ` ORDER BY t.ts`
}

// sendBacksSQL is every send_back event of the attempts a matching filter, oldest first.
func sendBacksSQL(filter string) string {
	return `SELECT s.factory_id, s.attempt_id, s.ts, s.from_step, s.to_step, s.reason, s.kind FROM send_backs s JOIN attempts a USING (factory_id, attempt_id)
	WHERE ` + filter + ` ORDER BY s.ts, s.rowid`
}

// stepData derives the named-step and timeline views from sessions (sessionsSQL rows, oldest first), usage rows with a
// step or session (stepUsageSQL), gate rows (factory_id, attempt_id, ts, name, passed, reason; oldest first), tool calls
// (stepToolsSQL) and send_back events (sendBacksSQL), all of one issue or one attempt.
// A session is an OTLP session (first..last event), or else the usage rows of one attempt that share a usage session id
// (a factory without OTLP), which ran from the earliest ts - duration_ms of its rows to their latest ts. The time of
// those is the sum of their duration_ms, of an OTLP session the longer of its event span and that sum.
//   - sessions: every session, with its cost, tokens and tool calls (by time window; nil when the attempt has none or
//     another of its sessions overlaps the window)
//   - steps: one row per session that has a step, with the cost of its step's model calls (its own, by session id;
//     by time window for usage rows without one)
//   - step_stats: per attempt and step: sessions and ms (sum of session times; both nil when unknown, never 0),
//     cost (all its model calls) and sent_back (send-backs to it)
//   - send_backs: an attempt's send_back events or, only when it has none, the send-backs inferred from its step order
//   - step_runs: per factory: sent_back (its send_backs) and re-runs (from the step sequence of each of its attempts)
//   - checks: the gates, each in the row of the step of its attempt that has its name, else nil
//   - gate_stats: per gate name: results, failed, and attempts whose first result for it passed
func stepData(sessions, usage, gates, tools, sent []map[string]any) map[string]any {
	type stat struct {
		row                     map[string]any
		ms, unpriced            int64
		cost                    float64
		priced, sessions, known int64 // known: sessions whose time is known
	}
	steps, stats, order, seq, runOrder := []map[string]any{}, map[string]*stat{}, []string{}, map[string][]string{}, []string{}
	seqAt := map[string][]any{}                 // the start of each session in seq
	statOf := func(f, att, step string) *stat { // per run (attempt): two runs of one factory never merge
		k := f + "\x00" + att + "\x00" + step
		if stats[k] == nil {
			stats[k] = &stat{row: map[string]any{"factory_id": f, "attempt_id": att, "step": step}}
			order = append(order, k)
		}
		return stats[k]
	}
	type sess struct {
		row         map[string]any // the OTLP session row, nil for a usage session
		f, att, id  string
		step, phase any
		at, end     any
		start       time.Time
		ms          *int64
	}
	var all []sess
	seen := map[string]bool{}  // factory, attempt, session
	dur := map[string]*int64{} // factory, attempt, session -> sum of its usage rows' duration_ms
	for _, u := range usage {
		id, ok := u["session"].(string)
		if d, ok2 := u["duration_ms"].(int64); ok && ok2 {
			k := u["factory_id"].(string) + "\x00" + u["attempt_id"].(string) + "\x00" + id
			dur[k] = ptr(d + deref(dur[k]))
		}
	}
	for _, s := range sessions {
		id, _ := s["session"].(string)
		k := s["factory_id"].(string) + "\x00" + s["attempt_id"].(string) + "\x00" + id
		seen[k] = true
		// an OTLP session's time is its event span, or its model calls' duration_ms when longer; nil when neither is known
		ms := dur[k]
		if span := parseTS(s["last_ts"]).Sub(parseTS(s["first_ts"])).Milliseconds(); span > deref(ms) {
			ms = &span
		}
		all = append(all, sess{s, s["factory_id"].(string), s["attempt_id"].(string), id, s["step"], s["phase"], s["first_ts"], s["last_ts"], parseTS(s["first_ts"]), ms})
	}
	byUsage := map[string]int{} // a usage session's index in all
	for _, u := range usage {
		id, ok := u["session"].(string)
		k := u["factory_id"].(string) + "\x00" + u["attempt_id"].(string) + "\x00" + id
		if !ok || seen[k] {
			continue
		}
		i, ok := byUsage[k]
		if !ok {
			i = len(all)
			byUsage[k] = i
			all = append(all, sess{f: u["factory_id"].(string), att: u["attempt_id"].(string), id: id, step: u["step"], phase: u["phase"]})
		}
		x := &all[i]
		x.end = u["ts"]
		if x.step == nil {
			x.step = u["step"]
		}
		if x.phase == nil {
			x.phase = u["phase"]
		}
		d, ok := u["duration_ms"].(int64)
		if ok {
			x.ms = ptr(d + deref(x.ms))
		}
		// a usage row is sent when its session ended: the session started duration_ms before
		if t := parseTS(u["ts"]).Add(-time.Duration(d) * time.Millisecond); x.at == nil || t.Before(x.start) {
			x.at, x.start = u["ts"], t
			if d > 0 {
				x.at = store.FormatTS(t)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].start.Before(all[j].start) })
	hasTools := map[string]bool{} // factory, attempt
	for _, t := range tools {
		hasTools[t["factory_id"].(string)+"\x00"+t["attempt_id"].(string)] = true
	}
	stepOf := map[string]bool{} // factory, attempt, step: the attempt has a session of that step
	out := []map[string]any{}
	for _, s := range all {
		at, end := s.start, parseTS(s.end)
		var cost float64
		var calls, priced, tokens, turns, withTurns int64
		var harness, status, lane any // from its usage rows: the first harness and lane, the last status
		for _, u := range usage {
			if u["factory_id"] != s.f || u["attempt_id"] != s.att {
				continue
			}
			// shortcut: usage rows without a session id (data from before usage.session) match by step and time window, so two overlapping sessions of one step share them
			if id, ok := u["session"].(string); ok && id != s.id {
				continue
			} else if t := parseTS(u["ts"]); !ok && (s.step == nil || u["step"] != s.step || t.Before(at) || t.After(end)) {
				continue
			}
			calls++
			tokens += u["tokens"].(int64)
			if t, ok := u["turns"].(int64); ok {
				turns += t
				withTurns++
			}
			harness, lane = cmp.Or(harness, u["harness"]), cmp.Or(lane, u["lane"])
			if u["status"] != nil {
				status = u["status"]
			}
			if c, ok := u["cost_usd"].(float64); ok {
				cost += c
				priced++
			}
		}
		row := s.row
		if row == nil {
			// first_ts, last_ts and events as an OTLP session row has them, for readers of the older shape
			row = map[string]any{"factory_id": s.f, "attempt_id": s.att, "session": s.id, "step": s.step, "phase": s.phase,
				"first_ts": s.at, "last_ts": s.end, "events": calls}
		}
		row["start"], row["end"], row["cost_usd"], row["tokens"], row["tools"], row["failed_tools"] = s.at, s.end, orNil(cost, priced), orNilInt(tokens, calls), nil, nil
		row["harness"], row["status"], row["turns"], row["lane"] = harness, status, orNilInt(turns, withTurns), lane
		overlaps := false // another session of the attempt runs in this one's window: its tool calls cannot be told apart
		for _, o := range all {
			if o.f == s.f && o.att == s.att && o.id != s.id && !o.start.After(end) && !at.After(parseTS(o.end)) {
				overlaps = true
				break
			}
		}
		if hasTools[s.f+"\x00"+s.att] && !overlaps {
			// shortcut: tool calls carry no session id, so they are matched by time window and unknown (nil) when sessions overlap; upgrade when activity carries the session
			var n, failed int64
			for _, t := range tools {
				if ts := parseTS(t["ts"]); t["factory_id"] == s.f && t["attempt_id"] == s.att && !ts.Before(at) && !ts.After(end) {
					n++
					if t["success"] == int64(0) {
						failed++
					}
				}
			}
			row["tools"], row["failed_tools"] = n, failed
		}
		out = append(out, row)
		step, ok := s.step.(string)
		if !ok {
			continue
		}
		stepOf[s.f+"\x00"+s.att+"\x00"+step] = true
		steps = append(steps, map[string]any{"factory_id": s.f, "attempt_id": s.att, "step": step, "phase": s.phase,
			"at": s.at, "end": s.end, "calls": calls, "cost_usd": orNil(cost, priced), "unpriced": calls - priced,
			"harness": harness, "status": status, "turns": row["turns"], "lane": lane})
		x := statOf(s.f, s.att, step)
		x.sessions++
		if s.ms != nil {
			x.ms += *s.ms
			x.known++
		}
		k := s.f + "\x00" + s.att
		if seq[k] == nil {
			runOrder = append(runOrder, k)
		}
		seq[k], seqAt[k] = append(seq[k], step), append(seqAt[k], s.at)
	}
	for _, u := range usage {
		step, ok := u["step"].(string)
		if !ok {
			continue
		}
		x := statOf(u["factory_id"].(string), u["attempt_id"].(string), step)
		if c, ok := u["cost_usd"].(float64); ok {
			x.cost += c
			x.priced++
		} else {
			x.unpriced++
		}
	}
	// send-backs: an attempt's own send_back events win; only an attempt without any is inferred from its step order
	backs, explicit := []map[string]any{}, map[string]bool{}
	for _, b := range sent {
		k := b["factory_id"].(string) + "\x00" + b["attempt_id"].(string)
		if !explicit[k] && seq[k] == nil {
			runOrder = append(runOrder, k)
		}
		explicit[k] = true
		backs = append(backs, map[string]any{"factory_id": b["factory_id"], "attempt_id": b["attempt_id"], "at": b["ts"], "from": b["from_step"],
			"to": b["to_step"], "reason": b["reason"], "kind": b["kind"], "inferred": false})
	}
	runs := []map[string]any{}
	back, runOf := map[string]int{}, map[string]map[string]any{}
	for _, k := range runOrder { // per attempt, summed per factory: never across attempts or factories
		f, att := k[:strings.IndexByte(k, 0)], k[strings.IndexByte(k, 0)+1:]
		bounced, r := rework(seq[k])
		if !explicit[k] {
			for _, i := range bounced {
				backs = append(backs, map[string]any{"factory_id": f, "attempt_id": att, "at": seqAt[k][i], "from": seq[k][i-1], "to": seq[k][i],
					"reason": nil, "kind": nil, "inferred": true})
			}
		}
		if runOf[f] == nil {
			runOf[f] = map[string]any{"factory_id": f, "sent_back": 0, "reruns": 0}
			runs = append(runs, runOf[f])
		}
		runOf[f]["reruns"] = runOf[f]["reruns"].(int) + r
	}
	sort.SliceStable(backs, func(i, j int) bool { return parseTS(backs[i]["at"]).Before(parseTS(backs[j]["at"])) })
	for _, b := range backs {
		f := b["factory_id"].(string)
		runOf[f]["sent_back"] = runOf[f]["sent_back"].(int) + 1
		back[f+"\x00"+b["attempt_id"].(string)+"\x00"+b["to"].(string)]++
	}
	st := []map[string]any{}
	for _, k := range order {
		x := stats[k]
		x.row["sessions"], x.row["ms"], x.row["cost_usd"], x.row["unpriced"], x.row["sent_back"] = orNilInt(x.sessions, x.sessions), orNilInt(x.ms, x.known), orNil(x.cost, x.priced), x.unpriced, back[k]
		st = append(st, x.row)
	}
	type gstat struct{ results, failed, attempts, firstPassed int }
	gs, gorder, first, checks := map[string]*gstat{}, []string{}, map[string]bool{}, []map[string]any{}
	for _, g := range gates {
		name, _ := g["name"].(string)
		x := gs[name]
		if x == nil {
			x = &gstat{}
			gs[name] = x
			gorder = append(gorder, name)
		}
		passed := g["passed"] == int64(1)
		x.results++
		if !passed {
			x.failed++
		}
		k := g["factory_id"].(string) + "\x00" + g["attempt_id"].(string) + "\x00" + name
		if !first[k] {
			first[k] = true
			x.attempts++
			if passed {
				x.firstPassed++
			}
		}
		var step any
		if stepOf[k] {
			step = name
		}
		checks = append(checks, map[string]any{"factory_id": g["factory_id"], "attempt_id": g["attempt_id"], "name": name, "passed": passed,
			"at": g["ts"], "step": step, "reason": g["reason"]})
	}
	gst := []map[string]any{}
	for _, n := range gorder {
		x := gs[n]
		gst = append(gst, map[string]any{"name": n, "results": x.results, "failed": x.failed, "attempts": x.attempts, "first_try_passed": x.firstPassed})
	}
	res := map[string]any{"sessions": out, "steps": steps, "step_stats": st, "step_runs": runs, "gate_stats": gst, "checks": checks}
	if len(backs) > 0 {
		res["send_backs"] = backs
	}
	return res
}

// waitsSQL is every human.waiting and human.resumed of the attempts a matching filter, oldest first, for waits.
func waitsSQL(filter string) string {
	return `SELECT e.factory_id, a.attempt_id, a.parent_attempt_id, e.ts, e.type, json_extract(e.payload, '$.payload.reason') AS reason,
	json_extract(e.payload, '$.payload.kind') AS kind, json_extract(e.payload, '$.payload.by') AS by FROM events e JOIN attempts a ON a.factory_id = e.factory_id AND a.attempt_id = json_extract(e.payload, '$.attempt_id')
	WHERE e.source = 'contract' AND e.rejected IS NULL AND e.type IN ('human.waiting', 'human.resumed') AND ` + filter + ` ORDER BY e.ts, e.factory_id, e.id`
}

// waits is one row per human.waiting (waitsSQL rows): each human.resumed ends the oldest open wait of its attempt, or of
// its parent when it has none open (plan waits, build resumes), as contract.Closes says; end and by (person, observer or nil) are nil while open.
// attempt, when set, keeps only that attempt's.
func waits(evs []map[string]any, attempt string) []map[string]any {
	out, open := []map[string]any{}, map[string][]map[string]any{}
	for _, e := range evs {
		f, att := e["factory_id"].(string), e["attempt_id"].(string)
		k := f + "\x00" + att
		if e["type"] == "human.waiting" {
			w := map[string]any{"factory_id": f, "attempt_id": att, "start": e["ts"], "end": nil, "kind": e["kind"], "reason": e["reason"], "end_reason": nil, "by": nil, "auto_answered": false}
			open[k] = append(open[k], w)
			if attempt == "" || att == attempt {
				out = append(out, w)
			}
			continue
		}
		if p, ok := e["parent_attempt_id"].(string); ok && len(open[k]) == 0 {
			k = f + "\x00" + p
		}
		if ws := open[k]; len(ws) > 0 {
			n := contract.Closes(len(ws), func(i int) string { k, _ := ws[i]["kind"].(string); return k })
			for _, w := range ws[:n] {
				w["end"], w["end_reason"], w["by"] = e["ts"], e["reason"], e["by"]
				w["auto_answered"] = autoAnswered(w)
			}
			open[k] = ws[n:]
		}
	}
	return out
}

// autoAnswerMax is the quickest a person answers a question: a faster answer came from a script.
const autoAnswerMax = 5 * time.Second

// autoAnswered reports whether a closed wait (waits row) was answered within autoAnswerMax. A resume that says the
// question went unanswered (the bench ended the run) is not an answer.
func autoAnswered(w map[string]any) bool {
	if w["end"] == nil {
		return false
	}
	if unanswered(w) {
		return false
	}
	return parseTS(w["end"]).Sub(parseTS(w["start"])) < autoAnswerMax
}

// unanswered reports whether a wait (waits row) ended with a resume that says nobody answered: the bench ended the run.
func unanswered(w map[string]any) bool {
	r, _ := w["end_reason"].(string)
	return strings.Contains(strings.ToLower(r), "unanswered")
}

// orNilInt is n, or nil when none of its parts was known.
func orNilInt(n, known int64) any {
	if known == 0 {
		return nil
	}
	return n
}

func ptr[T any](v T) *T { return &v }

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// orNil is a summed cost, or nil when none of its calls was priced: unknown stays unknown, never 0.
func orNil(cost float64, priced int64) any {
	if priced == 0 {
		return nil
	}
	return cost
}
