package report

import (
	"database/sql"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// The `report factories` scorecard: units summed per factory (or factory+config), plus gate and CI breakdowns.

// card is the scorecard of one factory (or factory+config): its units summed.
type card struct {
	group, factory string
	units          []*unit

	items, merged, closed, prs, phasedPR                                  int
	attempts, failed, sessions, withUsage                                 int
	tokens, verifyTok                                                     int64
	cost                                                                  cost
	elapsed, active                                                       time.Duration
	human, escaped, escapedPRs, planN, planPass, prN, prPass, reviewedPRs int
	changesReq, rejected                                                  int
	hasLogin                                                              bool
	run                                                                   runStats
}

func buildCards(d *metricsData, byConfig bool, now time.Time) []*card {
	var cards []*card
	byGroup := map[string]*card{}
	for _, u := range buildUnits(d, byConfig, now) {
		c := byGroup[u.group]
		if c == nil {
			login := d.login[u.factory]
			c = &card{group: u.group, factory: u.factory, rejected: d.rejected[u.factory], hasLogin: login != ""}
			byGroup[u.group] = c
			cards = append(cards, c)
		}
		c.units = append(c.units, u)
		c.items++
		if u.outcome == "merged" {
			c.merged++
		}
		if login := d.login[u.factory]; login != "" && u.item.issueState.String == "closed" && strings.EqualFold(u.item.closedBy.String, login) {
			c.closed++
		}
		if len(u.prs) > 0 {
			c.prs += len(u.prs)
			c.verifyTok += u.verifyTok
			c.phasedPR += u.phased
		}
		c.attempts += u.attempts
		c.failed += u.failed
		c.sessions += u.sessions
		c.withUsage += u.withUsage
		c.tokens += u.tokens
		c.cost.merge(u.cost)
		c.elapsed += u.elapsed
		c.active += u.active
		c.human += u.human
		c.escaped += u.escaped
		c.escapedPRs += u.escapedPRs
		c.planN += u.planN
		c.planPass += u.planPass
		c.prN += u.prN
		c.prPass += u.prPass
		c.reviewedPRs += u.reviewedPRs
		c.changesReq += u.changesReq
		c.run.merge(u.run)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].group < cards[j].group })
	return cards
}

func avg(k, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f (%d/%d)", float64(k)/float64(n), k, n)
}

// noActivity hides a tool call metric: "" when the card has no tool calls (see Factories).
func noActivity(c *card) bool { return c.run.withActivity == 0 }

// scorecard rows. A row whose value is "" for every card is left out, so factories without tool calls or
// PR size see no rows for them; a single "" among other values shows as n/a.
var scorecard = []struct {
	label string
	val   func(c *card) string
}{
	{"work items", func(c *card) string { return fmt.Sprint(c.items) }},
	{"attempts", func(c *card) string { return fmt.Sprintf("%d (%d failed)", c.attempts, c.failed) }},
	{"sessions", func(c *card) string { return fmt.Sprint(c.sessions) }},
	{"tokens / issue", func(c *card) string {
		if c.withUsage == 0 {
			return "?"
		}
		s := tok(sql.NullInt64{Int64: c.tokens / int64(c.items), Valid: true})
		if c.withUsage < c.attempts {
			s += fmt.Sprintf(" +? (%d attempt(s) without usage)", c.attempts-c.withUsage)
		}
		return s
	}},
	{"time / issue", func(c *card) string {
		n := time.Duration(c.items)
		return dur(c.active/n) + " active / " + dur(c.elapsed/n) + " elapsed"
	}},
	{"cost total", func(c *card) string {
		if c.cost.sessions == c.cost.nUnk {
			return "?"
		}
		s := usd(c.cost.rep + c.cost.est)
		switch nEst := c.cost.sessions - c.cost.nRep - c.cost.nUnk; {
		case c.cost.nRep > 0 && nEst > 0:
			s += fmt.Sprintf(" (reported %s + est. %s)", usd(c.cost.rep), usd(c.cost.est))
		case c.cost.nRep > 0:
			s += " reported"
		default:
			s += " est."
		}
		if n := c.attempts - c.withUsage; n > 0 {
			s += fmt.Sprintf(" +? (%d attempt(s) without usage)", n)
		} else if c.cost.nUnk > 0 {
			s += " +?"
		}
		return s
	}},
	{"cost / issue", func(c *card) string {
		return money((c.cost.rep+c.cost.est)/float64(c.items), c.cost, c.attempts-c.withUsage)
	}},
	{"cost / merged issue", func(c *card) string {
		if c.merged == 0 {
			return "n/a (0 merged)"
		}
		return money((c.cost.rep+c.cost.est)/float64(c.merged), c.cost, c.attempts-c.withUsage) + fmt.Sprintf(" (%d merged)", c.merged)
	}},
	{"verification tokens / PR", func(c *card) string {
		switch {
		case c.prs == 0:
			return "n/a (no PRs)"
		case c.phasedPR == 0:
			return "n/a (no phase data)"
		}
		return fmt.Sprintf("%s (%s / %d PRs)", tok(sql.NullInt64{Int64: c.verifyTok / int64(c.prs), Valid: true}),
			tok(sql.NullInt64{Int64: c.verifyTok, Valid: true}), c.prs)
	}},
	{"issues closed by agent", func(c *card) string {
		if !c.hasLogin {
			return "n/a (no github login)"
		}
		return rate(c.closed, c.items)
	}},
	{"first-pass plan approval", func(c *card) string { return rate(c.planPass, c.planN) }},
	{"first-pass PR approval", func(c *card) string { return rate(c.prPass, c.prN) }},
	{"review rounds / PR", func(c *card) string { return avg(c.changesReq, c.reviewedPRs) }},
	{"human interventions / issue", func(c *card) string { return avg(c.human, c.items) }},
	{"escaped defects", func(c *card) string {
		if c.prs == 0 {
			return "n/a (no PRs)"
		}
		return fmt.Sprintf("%d on %d/%d PRs", c.escaped, c.escapedPRs, c.prs)
	}},
	{"time to first PR", func(c *card) string {
		if c.run.nToPR == 0 {
			return ""
		}
		return fmt.Sprintf("%s (%d issues)", dur(c.run.toPR/time.Duration(c.run.nToPR)), c.run.nToPR)
	}},
	{"time to merge", func(c *card) string {
		if c.run.nToMerge == 0 {
			return ""
		}
		return fmt.Sprintf("%s (%d merged)", dur(c.run.toMerge/time.Duration(c.run.nToMerge)), c.run.nToMerge)
	}},
	{"CI passing on first push", func(c *card) string {
		if c.run.ciN == 0 {
			return ""
		}
		return rate(c.run.ciPass, c.run.ciN) + " PRs"
	}},
	{"PR size", func(c *card) string {
		n := int64(c.run.sizedPRs)
		if n == 0 {
			return ""
		}
		return fmt.Sprintf("+%d / -%d lines, %d files (avg of %d PRs)", c.run.added/n, c.run.deleted/n, c.run.files/n, n)
	}},
	{"cost / changed line", func(c *card) string {
		lines := c.run.added + c.run.deleted
		sc := c.run.sizedCost
		switch {
		case c.run.sizedPRs == 0:
			return ""
		case lines == 0:
			return "n/a (0 lines)"
		case sc.sessions == sc.nUnk:
			return "?"
		}
		s := fmt.Sprintf("$%.4f", (sc.rep+sc.est)/float64(lines)) // a cent per line is already a lot
		if sc.nUnk > 0 || c.run.sizedNoUsage > 0 {
			s += " +?"
		}
		return s + fmt.Sprintf(" (%d lines)", lines)
	}},
	{"tool calls / issue", func(c *card) string {
		if noActivity(c) {
			return ""
		}
		return avg(c.run.toolCalls, c.items)
	}},
	{"tool mix", func(c *card) string {
		if noActivity(c) {
			return ""
		}
		names := slices.Collect(maps.Keys(c.run.tools))
		sort.Slice(names, func(i, j int) bool {
			if a, b := c.run.tools[names[i]], c.run.tools[names[j]]; a != b {
				return a > b
			}
			return names[i] < names[j]
		})
		var top []string
		rest := c.run.toolCalls
		for i, n := range names {
			if i == 3 {
				top = append(top, fmt.Sprintf("%d other", rest))
				break
			}
			top = append(top, fmt.Sprintf("%s %d", n, c.run.tools[n]))
			rest -= c.run.tools[n]
		}
		return strings.Join(top, ", ")
	}},
	{"tool failure rate", func(c *card) string {
		if noActivity(c) {
			return ""
		}
		return rate(c.run.toolFailed, c.run.toolKnown)
	}},
	{"time in model / tool calls", func(c *card) string {
		if noActivity(c) {
			return ""
		}
		// summed per call: parallel subagents can make either exceed the elapsed time
		return dur(time.Duration(c.run.modelMS)*time.Millisecond) + " / " + dur(time.Duration(c.run.toolMS)*time.Millisecond)
	}},
	{"subagents / issue", func(c *card) string {
		if noActivity(c) {
			return ""
		}
		return avg(c.run.subagents, c.items)
	}},
	{"API errors / attempt", func(c *card) string {
		if noActivity(c) {
			return ""
		}
		return avg(c.run.apiErrors, c.attempts)
	}},
	{"usage coverage", func(c *card) string { return rate(c.withUsage, c.attempts) + " attempts" }},
	{"activity coverage", func(c *card) string { return rate(c.run.withActivity, c.attempts) + " attempts" }},
	{"unpriced sessions", func(c *card) string { return fmt.Sprint(c.cost.nUnk) }},
	{"rejected events", func(c *card) string { return fmt.Sprint(c.rejected) }},
}

// Factories prints the scorecard (metrics as rows, factories as columns), then gate and CI breakdowns.
func Factories(db *sql.DB, w io.Writer, byConfig bool, now time.Time) error {
	d, err := loadMetrics(db)
	if err != nil {
		return err
	}
	cards := buildCards(d, byConfig, now)
	if len(cards) == 0 {
		_, err := fmt.Fprintln(w, "no attempts yet")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "metric")
	for _, c := range cards {
		fmt.Fprint(tw, "\t"+c.group)
	}
	fmt.Fprintln(tw)
	for _, m := range scorecard {
		vals, shown := make([]string, len(cards)), false
		for i, c := range cards {
			vals[i] = m.val(c)
			shown = shown || vals[i] != ""
		}
		if !shown {
			continue
		}
		fmt.Fprint(tw, m.label)
		for _, v := range vals {
			if v == "" {
				v = "n/a"
			}
			fmt.Fprint(tw, "\t"+v)
		}
		fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, legend); err != nil {
		return err
	}

	var gateRows, suite []string
	for _, c := range cards {
		gates := map[string][2]int{} // name -> failures, runs
		for _, u := range c.units {
			for _, a := range u.atts {
				for _, g := range d.gates[a.factory+"\x00"+a.id] {
					v := gates[g.name]
					if !g.passed {
						v[0]++
					}
					v[1]++
					gates[g.name] = v
				}
			}
		}
		for _, name := range slices.Sorted(maps.Keys(gates)) {
			gateRows = append(gateRows, fmt.Sprintf("  %s\tfactory\t%s\t%s\n", c.group, name, rate(gates[name][0], gates[name][1])))
		}

		checks := map[string][]mCheck{}
		seen := map[string]bool{}
		for _, u := range c.units {
			for _, p := range u.prs {
				if !seen[p.key] {
					seen[p.key] = true
					for _, ch := range d.checks[p.key] {
						checks[ch.name] = append(checks[ch.name], ch)
					}
				}
			}
		}
		for _, name := range slices.Sorted(maps.Keys(checks)) {
			cs := ciStats(checks[name])
			gateRows = append(gateRows, fmt.Sprintf("  %s\tCI\t%s\t%s\n", c.group, name, rate(cs.fails, cs.runs)))
			suite = append(suite, fmt.Sprintf("  %s\t%s\t%s\t%s\n", c.group, name, cs.median, rate(cs.flaky, cs.shas)))
		}
	}
	if err := section(w, "Gates (defects caught)", "  factory\tsource\tgate\tfailed / runs\n", gateRows); err != nil {
		return err
	}
	return section(w, "CI suite", "  factory\tcheck\tmedian runtime\tflake rate (sha pairs)\n", suite)
}

func section(w io.Writer, title, header string, rows []string) error {
	fmt.Fprintln(w, "\n"+title)
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "  none")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, header+strings.Join(rows, ""))
	return tw.Flush()
}

type checkStats struct {
	fails, runs int    // failed (incl. timed out) / finished runs
	median      string // median runtime of finished runs, "n/a" without one
	flaky, shas int    // head SHAs that both failed and succeeded / head SHAs with a finished run
}

// ciStats summarises every run of one check. Runs still going or never executed (skipped, neutral,
// cancelled, ...) say nothing about runtime or flakiness and are left out.
func ciStats(checks []mCheck) checkStats {
	var cs checkStats
	var times []time.Duration
	pairs := map[string][2]bool{} // head_sha -> failed, succeeded
	for _, ch := range checks {
		if ch.concl != "success" && ch.concl != "failure" && ch.concl != "timed_out" {
			continue
		}
		cs.runs++
		p := pairs[ch.sha]
		if ch.concl == "success" {
			p[1] = true
		} else {
			cs.fails++
			p[0] = true
		}
		pairs[ch.sha] = p
		if rt := parse(ch.done).Sub(parse(ch.started)); ch.started != "" && ch.done != "" && rt >= 0 {
			times = append(times, rt)
		}
	}
	for _, p := range pairs {
		if p[0] && p[1] {
			cs.flaky++
		}
	}
	cs.shas, cs.median = len(pairs), "n/a"
	if n := len(times); n > 0 {
		slices.Sort(times)
		cs.median = dur((times[(n-1)/2] + times[n/2]) / 2)
	}
	return cs
}
