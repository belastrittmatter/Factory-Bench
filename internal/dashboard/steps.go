package dashboard

// stepSQL is an OTLP event's factorybench.step as normalize stores it in usage.step: trimmed, at most 64 characters,
// NULL when absent or empty.
const stepSQL = `nullif(trim(substr(trim(json_extract(payload, '$."factorybench.step"')), 1, 64)), '')`

// stepUsageSQL is every usage row with a step of the attempts a matching filter, for stepData.
func stepUsageSQL(filter string) string {
	return `SELECT u.factory_id, u.attempt_id, u.step, u.ts, u.cost_usd FROM usage u JOIN attempts a USING (factory_id, attempt_id)
	WHERE u.step IS NOT NULL AND ` + filter + ` ORDER BY u.ts`
}

// stepData derives the named-step views from sessions (sessionsSQL rows, oldest first), usage rows with a step
// (stepUsageSQL) and gate rows (factory_id, attempt_id, name, passed; oldest first), all of one issue or one attempt:
//   - steps: one row per session that has a step, with the cost of its step's model calls in its time window
//   - step_stats: per factory and step: sessions, ms (sum of session spans), cost (all its model calls) and sent_back
//   - step_runs: per factory (a run, as the scope is one issue): sent_back and re-runs, from the step sequence
//   - gate_stats: per gate name: results, failed, and attempts whose first result for it passed
func stepData(sessions, usage, gates []map[string]any) map[string]any {
	type stat struct {
		row              map[string]any
		ms, unpriced     int64
		cost             float64
		priced, sessions int64
	}
	steps, stats, order, seq, facs := []map[string]any{}, map[string]*stat{}, []string{}, map[string][]string{}, []string{}
	statOf := func(f, step string) *stat {
		k := f + "\x00" + step
		if stats[k] == nil {
			stats[k] = &stat{row: map[string]any{"factory_id": f, "step": step}}
			order = append(order, k)
		}
		return stats[k]
	}
	for _, s := range sessions {
		step, ok := s["step"].(string)
		if !ok {
			continue
		}
		f, att := s["factory_id"].(string), s["attempt_id"]
		at, end := parseTS(s["first_ts"]), parseTS(s["last_ts"])
		var cost float64
		var calls, priced int64
		// shortcut: usage rows carry no session id, so two overlapping sessions of one step share their calls; fine while a step runs one session at a time
		for _, u := range usage {
			if t := parseTS(u["ts"]); u["factory_id"] == f && u["attempt_id"] == att && u["step"] == step && !t.Before(at) && !t.After(end) {
				calls++
				if c, ok := u["cost_usd"].(float64); ok {
					cost += c
					priced++
				}
			}
		}
		steps = append(steps, map[string]any{"factory_id": f, "attempt_id": att, "step": step, "phase": s["phase"],
			"at": s["first_ts"], "end": s["last_ts"], "calls": calls, "cost_usd": orNil(cost, priced), "unpriced": calls - priced})
		x := statOf(f, step)
		x.sessions++
		x.ms += end.Sub(at).Milliseconds()
		if seq[f] == nil {
			facs = append(facs, f)
		}
		seq[f] = append(seq[f], step)
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
	back := map[string]int{}
	for _, f := range facs {
		b, r, per := rework(seq[f])
		runs = append(runs, map[string]any{"factory_id": f, "sent_back": b, "reruns": r})
		for step, n := range per {
			back[f+"\x00"+step] = n
		}
	}
	st := []map[string]any{}
	for _, k := range order {
		x := stats[k]
		x.row["sessions"], x.row["ms"], x.row["cost_usd"], x.row["unpriced"], x.row["sent_back"] = x.sessions, x.ms, orNil(x.cost, x.priced), x.unpriced, back[k]
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

// orNil is a summed cost, or nil when none of its calls was priced: unknown stays unknown, never 0.
func orNil(cost float64, priced int64) any {
	if priced == 0 {
		return nil
	}
	return cost
}
