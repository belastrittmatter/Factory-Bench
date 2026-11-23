package dashboard

import (
	"sort"
	"strings"
)

// stepSQL is an OTLP event's factorybench.step as normalize stores it in usage.step: trimmed, at most 64 characters,
// NULL when absent or empty.
const stepSQL = `nullif(trim(substr(trim(json_extract(payload, '$."factorybench.step"')), 1, 64)), '')`

// stepUsageSQL is every usage row with a step of the attempts a matching filter, for stepData.
func stepUsageSQL(filter string) string {
	return `SELECT u.factory_id, u.attempt_id, u.step, u.ts, u.cost_usd, u.session, u.duration_ms, u.phase FROM usage u JOIN attempts a USING (factory_id, attempt_id)
	WHERE u.step IS NOT NULL AND ` + filter + ` ORDER BY u.ts`
}

// stepData derives the named-step views from sessions (sessionsSQL rows, oldest first), usage rows with a step
// (stepUsageSQL) and gate rows (factory_id, attempt_id, name, passed; oldest first), all of one issue or one attempt.
// A session is an OTLP session, or else the usage rows of one attempt that share a usage session id (a factory
// without OTLP); the time of those is the sum of their duration_ms, of an OTLP session the longer of its event span and
// that sum.
//   - steps: one row per session that has a step, with the cost of its step's model calls (its own, by session id;
//     by time window for usage rows without one)
//   - step_stats: per factory and step: sessions and ms (sum of session times; both nil when unknown, never 0),
//     cost (all its model calls) and sent_back
//   - step_runs: per factory: sent_back and re-runs, from the step sequence of each of its attempts
//   - gate_stats: per gate name: results, failed, and attempts whose first result for it passed
func stepData(sessions, usage, gates []map[string]any) map[string]any {
	type stat struct {
		row                     map[string]any
		ms, unpriced            int64
		cost                    float64
		priced, sessions, known int64 // known: sessions whose time is known
	}
	steps, stats, order, seq, runOrder := []map[string]any{}, map[string]*stat{}, []string{}, map[string][]string{}, []string{}
	statOf := func(f, step string) *stat {
		k := f + "\x00" + step
		if stats[k] == nil {
			stats[k] = &stat{row: map[string]any{"factory_id": f, "step": step}}
			order = append(order, k)
		}
		return stats[k]
	}
	type sess struct {
		f, att, id, step string
		phase, at, end   any
		ms               *int64
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
		step, ok := s["step"].(string)
		id, _ := s["session"].(string)
		k := s["factory_id"].(string) + "\x00" + s["attempt_id"].(string) + "\x00" + id
		seen[k] = true
		if !ok {
			continue
		}
		// an OTLP session's time is its event span, or its model calls' duration_ms when longer; nil when neither is known
		ms := dur[k]
		if span := parseTS(s["last_ts"]).Sub(parseTS(s["first_ts"])).Milliseconds(); span > deref(ms) {
			ms = &span
		}
		all = append(all, sess{s["factory_id"].(string), s["attempt_id"].(string), id, step, s["phase"], s["first_ts"], s["last_ts"], ms})
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
			all = append(all, sess{f: u["factory_id"].(string), att: u["attempt_id"].(string), id: id, step: u["step"].(string), phase: u["phase"], at: u["ts"]})
		}
		x := &all[i]
		x.end = u["ts"]
		if d, ok := u["duration_ms"].(int64); ok {
			x.ms = ptr(d + deref(x.ms))
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return parseTS(all[i].at).Before(parseTS(all[j].at)) })
	for _, s := range all {
		at, end := parseTS(s.at), parseTS(s.end)
		var cost float64
		var calls, priced int64
		for _, u := range usage {
			if u["factory_id"] != s.f || u["attempt_id"] != s.att || u["step"] != s.step {
				continue
			}
			// shortcut: usage rows without a session id (data from before usage.session) match by time window, so two overlapping sessions of one step share them
			if id, ok := u["session"].(string); ok && id != s.id {
				continue
			} else if t := parseTS(u["ts"]); !ok && (t.Before(at) || t.After(end)) {
				continue
			}
			calls++
			if c, ok := u["cost_usd"].(float64); ok {
				cost += c
				priced++
			}
		}
		steps = append(steps, map[string]any{"factory_id": s.f, "attempt_id": s.att, "step": s.step, "phase": s.phase,
			"at": s.at, "end": s.end, "calls": calls, "cost_usd": orNil(cost, priced), "unpriced": calls - priced})
		x := statOf(s.f, s.step)
		x.sessions++
		if s.ms != nil {
			x.ms += *s.ms
			x.known++
		}
		k := s.f + "\x00" + s.att
		if seq[k] == nil {
			runOrder = append(runOrder, k)
		}
		seq[k] = append(seq[k], s.step)
	}
	for _, u := range usage {
		x := statOf(u["factory_id"].(string), u["step"].(string))
		if c, ok := u["cost_usd"].(float64); ok {
			x.cost += c
			x.priced++
		} else {
			x.unpriced++
		}
	}
	runs := []map[string]any{}
	back, runOf := map[string]int{}, map[string]map[string]any{}
	for _, k := range runOrder { // per attempt, summed per factory: never across attempts or factories
		f := k[:strings.IndexByte(k, 0)]
		b, r, per := rework(seq[k])
		if runOf[f] == nil {
			runOf[f] = map[string]any{"factory_id": f, "sent_back": 0, "reruns": 0}
			runs = append(runs, runOf[f])
		}
		runOf[f]["sent_back"], runOf[f]["reruns"] = runOf[f]["sent_back"].(int)+b, runOf[f]["reruns"].(int)+r
		for step, n := range per {
			back[f+"\x00"+step] += n
		}
	}
	st := []map[string]any{}
	for _, k := range order {
		x := stats[k]
		x.row["sessions"], x.row["ms"], x.row["cost_usd"], x.row["unpriced"], x.row["sent_back"] = orNilInt(x.sessions, x.sessions), orNilInt(x.ms, x.known), orNil(x.cost, x.priced), x.unpriced, back[k]
		st = append(st, x.row)
	}
	type gstat struct{ results, failed, attempts, firstPassed int }
	gs, gorder, first := map[string]*gstat{}, []string{}, map[string]bool{}
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
		if k := g["factory_id"].(string) + "\x00" + g["attempt_id"].(string) + "\x00" + name; !first[k] {
			first[k] = true
			x.attempts++
			if passed {
				x.firstPassed++
			}
		}
	}
	gst := []map[string]any{}
	for _, n := range gorder {
		x := gs[n]
		gst = append(gst, map[string]any{"name": n, "results": x.results, "failed": x.failed, "attempts": x.attempts, "first_try_passed": x.firstPassed})
	}
	return map[string]any{"steps": steps, "step_stats": st, "step_runs": runs, "gate_stats": gst}
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
