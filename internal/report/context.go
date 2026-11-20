package report

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"factorybench/internal/store"
)

var contextMoves = []string{"capture", "mirror", "show", "suggest", "expand", "sharpen", "challenge", "narrow", "ask", "confirm"}

type mTurn struct {
	n                                   int64
	speaker, move, suggestion, taggedBy string
	words                               int64
	dontKnow, couldNot, already, inAtt  bool
	confirmed                           bool
	frustration                         sql.NullInt64
	revealed                            []string
}

type mScore struct {
	scenario, subject, persona                                      string
	items, g4, latent, traps, noise                                 int64
	full, partial, g4Full, g4Partial, capLatent                     int64
	latentByMove                                                    map[string]int64
	raised, followed, invented, unmarked, tagErrors, assumedMarked  int64
	requirements, untraced, noiseLeft, missingSlots, contradictions int64
	agree, total, vague, simViolations                              int64
	costPerson, costJudge, costBuilders, costBuild                  float64
	built                                                           bool
	hiddenPass, hiddenTotal, buildTasks, buildMerged, escalations   sql.NullInt64
	repairs                                                         sql.NullInt64
}

func loadContext(db *sql.DB, d *metricsData, factory, attempt string) error {
	where, args := "", []any{}
	if factory != "" {
		where, args = " WHERE factory_id = ? AND attempt_id = ?", []any{factory, attempt}
	}
	return firstErr(
		store.Each(db, `SELECT factory_id, attempt_id, n, speaker, coalesce(move, ''), words, dont_know, could_not_answer, already_said,
			in_attachments, coalesce(suggestion, ''), frustration, confirmed, revealed, coalesce(tagged_by, '')
			FROM context_turns`+where+` ORDER BY factory_id, attempt_id, n`, func(r *sql.Rows) error {
			var f, id, rev string
			var t mTurn
			err := r.Scan(&f, &id, &t.n, &t.speaker, &t.move, &t.words, &t.dontKnow, &t.couldNot, &t.already, &t.inAtt,
				&t.suggestion, &t.frustration, &t.confirmed, &rev, &t.taggedBy)
			_ = json.Unmarshal([]byte(rev), &t.revealed)
			d.turns[f+"\x00"+id] = append(d.turns[f+"\x00"+id], t)
			return err
		}, args...),
		store.Each(db, `SELECT factory_id, attempt_id, scenario, coalesce(subject, ''), coalesce(persona, ''),
			truth_items, truth_g4, truth_latent, truth_traps, truth_noise, full_items, partial_items, g4_full, g4_partial,
			latent_drawn, latent_by_move, traps_raised, traps_followed, invented, invented_unmarked, map_tag_errors,
			assumed_marked, requirements, untraced, noise_left, missing_slots, contradictions, builders_agree, builders_total,
			vague_warnings, sim_violations, cost_person, cost_judge, cost_builders, cost_build, built, hidden_pass, hidden_total,
			build_tasks, build_merged, build_escalations, build_repairs FROM context_scores`+where, func(r *sql.Rows) error {
			var f, id, by string
			var s mScore
			err := r.Scan(&f, &id, &s.scenario, &s.subject, &s.persona, &s.items, &s.g4, &s.latent, &s.traps, &s.noise,
				&s.full, &s.partial, &s.g4Full, &s.g4Partial, &s.capLatent, &by, &s.raised, &s.followed, &s.invented,
				&s.unmarked, &s.tagErrors, &s.assumedMarked, &s.requirements, &s.untraced, &s.noiseLeft, &s.missingSlots,
				&s.contradictions, &s.agree, &s.total, &s.vague, &s.simViolations, &s.costPerson, &s.costJudge, &s.costBuilders,
				&s.costBuild, &s.built, &s.hiddenPass, &s.hiddenTotal, &s.buildTasks, &s.buildMerged, &s.escalations, &s.repairs)
			_ = json.Unmarshal([]byte(by), &s.latentByMove)
			d.scores[f+"\x00"+id] = &s
			return err
		}, args...),
	)
}

type ctxStats struct {
	runs    int
	convs   int
	scored  int
	built   int
	simRuns int

	factoryTurns, personTurns, personWords int64
	scoredFactoryTurns                     int64
	moves                                  map[string]int
	couldNot, already, inAtt               int
	accepted, rejected                     int
	dontKnow, recovered                    int
	frustN, frustSum, frustMax             int64

	items, g4, latent, traps                           int64
	full, partial, g4Full, g4Partial, capLatent        int64
	latentByMove                                       map[string]int64
	raised, followed                                   int64
	invented, unmarked, tagErrors                      int64
	noiseLeft, missingSlots, contradictions            int64
	requirements, untraced, agree, total, simViolation int64
	hiddenPass, hiddenTotal, escalations, repairs      int64
	costPerson, costJudge, costBuilders, costBuild     float64
}

func (s *ctxStats) has() bool { return s.runs > 0 }

func (s *ctxStats) add(turns []mTurn, sc *mScore) {
	if len(turns) == 0 && sc == nil {
		return
	}
	s.runs++
	if s.moves == nil {
		s.moves, s.latentByMove = map[string]int{}, map[string]int64{}
	}
	var fturns int64
	if len(turns) > 0 {
		s.convs++
	}
	for i, t := range turns {
		if t.speaker == "factory" {
			fturns++
			s.moves[t.move]++
			continue
		}
		s.personTurns++
		s.personWords += t.words
		for _, c := range []struct {
			on bool
			n  *int
		}{{t.couldNot, &s.couldNot}, {t.already, &s.already}, {t.inAtt, &s.inAtt}, {t.dontKnow, &s.dontKnow}} {
			if c.on {
				*c.n++
			}
		}
		switch t.suggestion {
		case "accepted":
			s.accepted++
		case "rejected":
			s.rejected++
		}
		if t.frustration.Valid {
			s.frustN++
			s.frustSum += t.frustration.Int64
			s.frustMax = max(s.frustMax, t.frustration.Int64)
		}
		if t.dontKnow && recovered(turns, i) {
			s.recovered++
		}
	}
	s.factoryTurns += fturns
	if sc == nil {
		return
	}
	s.scored++
	s.scoredFactoryTurns += fturns
	s.items += sc.items
	s.g4 += sc.g4
	s.latent += sc.latent
	s.traps += sc.traps
	s.full += sc.full
	s.partial += sc.partial
	s.g4Full += sc.g4Full
	s.g4Partial += sc.g4Partial
	s.capLatent += sc.capLatent
	for m, n := range sc.latentByMove {
		s.latentByMove[m] += n
	}
	s.raised += sc.raised
	s.followed += sc.followed
	s.invented += sc.invented
	s.unmarked += sc.unmarked
	s.tagErrors += sc.tagErrors
	s.noiseLeft += sc.noiseLeft
	s.missingSlots += sc.missingSlots
	s.contradictions += sc.contradictions
	s.requirements += sc.requirements
	s.untraced += sc.untraced
	s.agree += sc.agree
	s.total += sc.total
	s.simViolation += sc.simViolations
	if sc.simViolations > 0 {
		s.simRuns++
	}
	if sc.built {
		s.built++
		s.hiddenPass += sc.hiddenPass.Int64
		s.hiddenTotal += sc.hiddenTotal.Int64
		s.escalations += sc.escalations.Int64
		s.repairs += sc.repairs.Int64
	}
	s.costPerson += sc.costPerson
	s.costJudge += sc.costJudge
	s.costBuilders += sc.costBuilders
	s.costBuild += sc.costBuild
}

func (s *ctxStats) merge(o *ctxStats) {
	if s.moves == nil {
		s.moves, s.latentByMove = map[string]int{}, map[string]int64{}
	}
	s.runs += o.runs
	s.convs += o.convs
	s.scored += o.scored
	s.built += o.built
	s.simRuns += o.simRuns
	s.factoryTurns += o.factoryTurns
	s.personTurns += o.personTurns
	s.personWords += o.personWords
	s.scoredFactoryTurns += o.scoredFactoryTurns
	for m, n := range o.moves {
		s.moves[m] += n
	}
	s.couldNot += o.couldNot
	s.already += o.already
	s.inAtt += o.inAtt
	s.accepted += o.accepted
	s.rejected += o.rejected
	s.dontKnow += o.dontKnow
	s.recovered += o.recovered
	s.frustN += o.frustN
	s.frustSum += o.frustSum
	s.frustMax = max(s.frustMax, o.frustMax)
	s.items += o.items
	s.g4 += o.g4
	s.latent += o.latent
	s.traps += o.traps
	s.full += o.full
	s.partial += o.partial
	s.g4Full += o.g4Full
	s.g4Partial += o.g4Partial
	s.capLatent += o.capLatent
	for m, n := range o.latentByMove {
		s.latentByMove[m] += n
	}
	s.raised += o.raised
	s.followed += o.followed
	s.invented += o.invented
	s.unmarked += o.unmarked
	s.tagErrors += o.tagErrors
	s.noiseLeft += o.noiseLeft
	s.missingSlots += o.missingSlots
	s.contradictions += o.contradictions
	s.requirements += o.requirements
	s.untraced += o.untraced
	s.agree += o.agree
	s.total += o.total
	s.simViolation += o.simViolation
	s.hiddenPass += o.hiddenPass
	s.hiddenTotal += o.hiddenTotal
	s.escalations += o.escalations
	s.repairs += o.repairs
	s.costPerson += o.costPerson
	s.costJudge += o.costJudge
	s.costBuilders += o.costBuilders
	s.costBuild += o.costBuild
}

func nextAfter(turns []mTurn, i int, speaker string) int {
	for j := i + 1; j < len(turns); j++ {
		if turns[j].speaker == speaker {
			return j
		}
	}
	return -1
}

func recovered(turns []mTurn, i int) bool {
	f := nextAfter(turns, i, "factory")
	if f < 0 || (turns[f].move != "show" && turns[f].move != "suggest") {
		return false
	}
	p := nextAfter(turns, f, "person")
	return p >= 0 && len(turns[p].revealed) > 0
}

func num(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return fmt.Sprintf("%.1f", v)
}

func ratef(k float64, n int64) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%% (%s/%d)", 100*k/float64(n), num(k), n)
}

func ratio(k float64, n int64) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f (%s/%d)", k/float64(n), num(k), n)
}

func credit(full, partial int64) float64 { return float64(full) + 0.5*float64(partial) }

func topCounts(m map[string]int, n int) string {
	names := slices.Collect(maps.Keys(m))
	sort.Slice(names, func(i, j int) bool {
		if m[names[i]] != m[names[j]] {
			return m[names[i]] > m[names[j]]
		}
		return names[i] < names[j]
	})
	var top []string
	for _, k := range names {
		if m[k] > 0 && len(top) < n {
			top = append(top, fmt.Sprintf("%s %d", k, m[k]))
		}
	}
	if len(top) == 0 {
		return "n/a"
	}
	return strings.Join(top, ", ")
}

type ctxMetric struct {
	label string
	dir   int
	val   func(s *ctxStats) string
	warn  func(s *ctxStats) bool // nil, or whether a value needs a warning mark
}

var contextMetrics = []ctxMetric{
	{"context turns / conversation", 0, func(s *ctxStats) string { return avg(int(s.factoryTurns), s.convs) }, nil},
	{"person words / conversation", 0, func(s *ctxStats) string { return avg(int(s.personWords), s.convs) }, nil},
	{"moves", 0, func(s *ctxStats) string { return topCounts(s.moves, 3) }, nil},
	{"could not answer", -1, func(s *ctxStats) string { return rate(s.couldNot, int(s.personTurns)) }, nil},
	{"asked again", -1, func(s *ctxStats) string { return per(avg(s.already, s.convs), "conversation") }, nil},
	{"asked what was given", -1, func(s *ctxStats) string { return per(avg(s.inAtt, s.convs), "conversation") }, nil},
	{"suggestions accepted", 1, func(s *ctxStats) string { return rate(s.accepted, s.accepted+s.rejected) }, nil},
	{"don't-know recovered", 1, func(s *ctxStats) string { return rate(s.recovered, s.dontKnow) }, nil},
	{"frustration", -1, func(s *ctxStats) string {
		if s.frustN == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f mean, %d max (%s)", float64(s.frustSum)/float64(s.frustN), s.frustMax, plural(int(s.frustN), "turn"))
	}, nil},
	{"recall", 1, func(s *ctxStats) string { return ratef(credit(s.full, s.partial), s.items) }, nil},
	{"weighted recall", 1, func(s *ctxStats) string {
		return ratef(float64(s.full)+2*float64(s.g4Full)+0.5*(float64(s.partial)+2*float64(s.g4Partial)), s.items+2*s.g4)
	}, nil},
	{"G4 recall", 1, func(s *ctxStats) string { return ratef(credit(s.g4Full, s.g4Partial), s.g4) }, nil},
	{"items per turn", 1, func(s *ctxStats) string { return ratio(credit(s.full, s.partial), s.scoredFactoryTurns) }, nil},
	{"latent drawn", 1, func(s *ctxStats) string {
		v := ratef(float64(s.capLatent), s.latent)
		if by := intCounts(s.latentByMove); by != "" && s.latent > 0 {
			v += " · " + by
		}
		return v
	}, nil},
	{"traps raised", 1, func(s *ctxStats) string { return ratef(float64(s.raised), s.traps) }, nil},
	{"traps followed", 1, func(s *ctxStats) string { return ratef(float64(s.followed), s.traps) }, nil},
	{"invented", -1, func(s *ctxStats) string {
		v := per(avg(int(s.invented), s.scored), "run")
		if s.scored > 0 {
			v += fmt.Sprintf(" · %d not marked assumed", s.unmarked)
		}
		return v
	}, nil},
	{"map tag errors", -1, func(s *ctxStats) string { return per(avg(int(s.tagErrors), s.scored), "run") }, nil},
	{"noise left", -1, func(s *ctxStats) string { return per(avg(int(s.noiseLeft), s.scored), "run") }, nil},
	{"missing slots", -1, func(s *ctxStats) string { return per(avg(int(s.missingSlots), s.scored), "run") }, nil},
	{"contradictions", -1, func(s *ctxStats) string { return per(avg(int(s.contradictions), s.scored), "run") }, nil},
	{"traceability", 1, func(s *ctxStats) string { return ratef(float64(s.requirements-s.untraced), s.requirements) }, nil},
	{"builder agreement", 1, func(s *ctxStats) string { return ratef(float64(s.agree), s.total) }, nil},
	{"hidden tests", 1, func(s *ctxStats) string {
		v := ratef(float64(s.hiddenPass), s.hiddenTotal)
		if s.built > 0 {
			v += fmt.Sprintf(" · escalations %s · repairs %s", avg(int(s.escalations), s.built), per(avg(int(s.repairs), s.built), "built run"))
		}
		return v
	}, nil},
	{"simulator violations", -1, func(s *ctxStats) string {
		v := per(avg(int(s.simViolation), s.scored), "run")
		if s.simRuns > 0 {
			v += fmt.Sprintf(" ⚠ %s marked", plural(s.simRuns, "run"))
		}
		return v
	}, func(s *ctxStats) bool { return s.simRuns > 0 }},
	{"bench cost (not factory cost)", 0, func(s *ctxStats) string {
		if s.scored == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%s (person %s, judge %s, builders %s, build %s)", usd(s.costPerson+s.costJudge+s.costBuilders+s.costBuild),
			usd(s.costPerson), usd(s.costJudge), usd(s.costBuilders), usd(s.costBuild))
	}, nil},
}

func per(v, unit string) string {
	if v == "n/a" {
		return v
	}
	return v + " / " + unit
}

func intCounts(m map[string]int64) string {
	im := map[string]int{}
	for k, v := range m {
		im[k] = int(v)
	}
	if s := topCounts(im, len(im)); s != "n/a" {
		return s
	}
	return ""
}

// ContextMetric is one preformatted Context metric. Dir is +1 when higher is better, -1 when lower is, 0 when neither.
type ContextMetric struct {
	Label, Value string
	Dir          int `json:",omitempty"`
}

func (s *ctxStats) metrics() []ContextMetric {
	out := make([]ContextMetric, len(contextMetrics))
	for i, m := range contextMetrics {
		out[i] = ContextMetric{m.label, m.val(s), m.dir}
	}
	return out
}

func contextSection(cards []*card) []ScoreRow {
	any := false
	for _, c := range cards {
		any = any || c.ctx.has()
	}
	if !any {
		return nil
	}
	rows := make([]ScoreRow, len(contextMetrics))
	for i, m := range contextMetrics {
		rows[i] = ScoreRow{Label: m.label, Values: make([]string, len(cards)), Section: "Context", Dir: m.dir}
		for j, c := range cards {
			rows[i].Values[j] = "n/a"
			if c.ctx.has() {
				rows[i].Values[j] = m.val(&c.ctx)
				if m.warn != nil && m.warn(&c.ctx) {
					if rows[i].Warn == nil {
						rows[i].Warn = make([]bool, len(cards))
					}
					rows[i].Warn[j] = true
				}
			}
		}
	}
	return rows
}

func printContextCards(w io.Writer, cards []*card) error {
	rows := contextSection(cards)
	if rows == nil {
		return nil
	}
	fmt.Fprintln(w, "\nContext")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "metric")
	for _, c := range cards {
		fmt.Fprint(tw, "\t"+c.group)
	}
	fmt.Fprintln(tw)
	for _, r := range rows {
		fmt.Fprint(tw, r.Label)
		for _, v := range r.Values {
			fmt.Fprint(tw, "\t"+v)
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

func printContextUnits(w io.Writer, units []*unit) error {
	var rows []*unit
	for _, u := range units {
		if u.ctx.has() {
			rows = append(rows, u)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	fmt.Fprintln(w, "\nContext")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "item\tfactory\truns")
	for _, m := range contextMetrics {
		fmt.Fprint(tw, "\t"+m.label)
	}
	fmt.Fprintln(tw)
	for _, u := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s", u.item.key, u.group, u.ctx.runsText())
		for _, m := range contextMetrics {
			fmt.Fprint(tw, "\t"+m.val(&u.ctx))
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

func (s *ctxStats) runsText() string { return fmt.Sprintf("%d (%d scored)", s.runs, s.scored) }

type ContextUnit struct {
	Factory string
	Runs    string
	Metrics []ContextMetric
}

// ContextTurnRow is one turn of a conversation, for the strip on the dashboard. Text is left out on purpose.
type ContextTurnRow struct {
	N           int64
	Speaker     string
	Move        string `json:",omitempty"`
	Words       int64
	Tags        []string // person tags that are set: dont_know, could_not_answer, already_said, in_attachments, confirmed
	Suggestion  string   `json:",omitempty"`
	Frustration *int64   `json:",omitempty"`
	Revealed    []string
	TaggedBy    string `json:",omitempty"`
}

type ContextAttempt struct {
	Turns   []ContextTurnRow
	Scored  bool
	Metrics []ContextMetric
}

type ContextRun struct {
	Factory, Attempt, Role string
	Labels                 []string
	ContextAttempt
}

type ContextOfItem struct {
	Units []ContextUnit
	Runs  []ContextRun
}

func (d *metricsData) conversation(k string) *ContextAttempt {
	var st ctxStats
	st.add(d.turns[k], d.scores[k])
	if !st.has() {
		return nil
	}
	out := &ContextAttempt{Turns: []ContextTurnRow{}, Scored: d.scores[k] != nil, Metrics: st.metrics()}
	for _, t := range d.turns[k] {
		r := ContextTurnRow{N: t.n, Speaker: t.speaker, Move: t.move, Words: t.words, Tags: []string{}, Suggestion: t.suggestion,
			Revealed: append([]string{}, t.revealed...), TaggedBy: t.taggedBy}
		if t.frustration.Valid {
			r.Frustration = &t.frustration.Int64
		}
		for _, tag := range []struct {
			on   bool
			name string
		}{{t.dontKnow, "dont_know"}, {t.couldNot, "could_not_answer"}, {t.already, "already_said"}, {t.inAtt, "in_attachments"}, {t.confirmed, "confirmed"}} {
			if tag.on {
				r.Tags = append(r.Tags, tag.name)
			}
		}
		out.Turns = append(out.Turns, r)
	}
	return out
}

// ContextOfAttempt returns the context data of one attempt, or nil when it has none.
func ContextOfAttempt(db *sql.DB, factory, attempt string) (*ContextAttempt, error) {
	d := &metricsData{turns: map[string][]mTurn{}, scores: map[string]*mScore{}}
	if err := loadContext(db, d, factory, attempt); err != nil {
		return nil, err
	}
	return d.conversation(factory + "\x00" + attempt), nil
}

// WorkItem returns the item rows of every factory on the work item (as ItemRows) and their Context metrics (the
// numbers of `report items`) with each of its context attempts' conversations, from one load of the database.
func WorkItem(db *sql.DB, repo string, issue int64, now time.Time) ([]ItemRow, *ContextOfItem, error) {
	d, err := loadMetrics(db, nil)
	if err != nil {
		return nil, nil, err
	}
	key := fmt.Sprintf("%s#%d", repo, issue)
	rows, out := []ItemRow{}, &ContextOfItem{Units: []ContextUnit{}, Runs: []ContextRun{}}
	for _, u := range buildUnits(d, false, now) {
		if u.item.key != key {
			continue
		}
		rows = append(rows, u.row())
		if u.ctx.has() {
			out.Units = append(out.Units, ContextUnit{u.factory, u.ctx.runsText(), u.ctx.metrics()})
		}
	}
	for _, a := range d.atts {
		if a.repo != repo || a.issue != issue {
			continue
		}
		if c := d.conversation(a.factory + "\x00" + a.id); c != nil {
			out.Runs = append(out.Runs, ContextRun{a.factory, a.id, a.role, append([]string{}, d.labels[a.factory+"\x00"+a.id]...), *c})
		}
	}
	return rows, out, nil
}

func runSource(turns []mTurn) string {
	sim, real := false, false
	for _, t := range turns {
		switch t.taggedBy {
		case "simulator":
			sim = true
		case "person", "tagger":
			real = true
		}
	}
	switch {
	case sim && real:
		return "mixed"
	case sim:
		return "simulated"
	case real:
		return "real"
	}
	return ""
}

func maxTurns(labels []string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, "max-turns:"); ok {
			return v
		}
	}
	return ""
}

// Context prints `report context`: the effort curve, the moves table and simulated against real, over the attempts of
// factory (all when empty) that carry every label.
func Context(db *sql.DB, w io.Writer, factory string, labels []string) error {
	d, err := loadMetrics(db, labels)
	if err != nil {
		return err
	}
	type run struct {
		a      *mAtt
		turns  []mTurn
		score  *mScore
		stats  ctxStats
		source string
	}
	var runs []*run
	var all ctxStats
	for _, a := range d.atts {
		if factory != "" && a.factory != factory {
			continue
		}
		k := a.factory + "\x00" + a.id
		r := &run{a: a, turns: d.turns[k], score: d.scores[k]}
		r.stats.add(r.turns, r.score)
		if !r.stats.has() {
			continue
		}
		r.source = runSource(r.turns)
		runs = append(runs, r)
		all.merge(&r.stats)
	}
	if len(runs) == 0 {
		_, err := fmt.Fprintln(w, "no context data yet")
		return err
	}
	fmt.Fprintf(w, "%s with context data · %d scored · %s\n", plural(len(runs), "run"), all.scored, plural(int(all.factoryTurns), "factory turn"))

	byTurns := map[string]*ctxStats{}
	for _, r := range runs {
		k := maxTurns(d.labels[r.a.factory+"\x00"+r.a.id])
		if byTurns[k] == nil {
			byTurns[k] = &ctxStats{}
		}
		byTurns[k].merge(&r.stats)
	}
	keys := slices.Collect(maps.Keys(byTurns))
	sort.Slice(keys, func(i, j int) bool {
		a, ea := strconv.Atoi(keys[i])
		b, eb := strconv.Atoi(keys[j])
		switch {
		case ea != nil || eb != nil:
			return (ea == nil) && eb != nil || (ea != nil) == (eb != nil) && keys[i] < keys[j]
		}
		return a < b
	})
	fmt.Fprintln(w, "\nEffort curve (runs by max-turns label)")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "max-turns\truns\tperson words / run\trecall\tbuilder agreement\thidden tests")
	for _, k := range keys {
		s := byTurns[k]
		label := k
		if label == "" {
			label = "none"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", label, s.runsText(), avg(int(s.personWords), s.convs),
			metricVal("recall", s), metricVal("builder agreement", s), metricVal("hidden tests", s))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	type moveStat struct{ used, revealed, accepted, rejected int }
	moves := map[string]*moveStat{}
	for _, r := range runs {
		for i, t := range r.turns {
			if t.speaker != "factory" {
				continue
			}
			m := moves[t.move]
			if m == nil {
				m = &moveStat{}
				moves[t.move] = m
			}
			m.used++
			if p := nextAfter(r.turns, i, "person"); p >= 0 {
				m.revealed += len(r.turns[p].revealed)
				switch r.turns[p].suggestion {
				case "accepted":
					m.accepted++
				case "rejected":
					m.rejected++
				}
			}
		}
	}
	fmt.Fprintln(w, "\nMoves")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "move\tused\tids revealed next turn\tlatent drawn\tsuggestion accepted")
	for _, m := range contextMoves {
		ms, latent := moves[m], all.latentByMove[m]
		if ms == nil && latent == 0 {
			continue
		}
		if ms == nil {
			ms = &moveStat{}
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\n", m, ms.used, ms.revealed, latent, rate(ms.accepted, ms.accepted+ms.rejected))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	scenarios := map[string]map[string]*ctxStats{}
	untagged := 0
	for _, r := range runs {
		scenario := ""
		if r.score != nil {
			scenario = r.score.scenario
		}
		if scenario == "" {
			for _, l := range d.labels[r.a.factory+"\x00"+r.a.id] {
				if v, ok := strings.CutPrefix(l, "scenario:"); ok {
					scenario = v
				}
			}
		}
		if r.source == "" {
			untagged++
			continue
		}
		if scenarios[scenario] == nil {
			scenarios[scenario] = map[string]*ctxStats{}
		}
		if scenarios[scenario][r.source] == nil {
			scenarios[scenario][r.source] = &ctxStats{}
		}
		scenarios[scenario][r.source].merge(&r.stats)
	}
	fmt.Fprintln(w, "\nSimulated against real (by who set the person tags)")
	if len(scenarios) == 0 {
		fmt.Fprintln(w, "  none: no run has tagged_by on its person turns")
		return nil
	}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "scenario\tpersons\truns\tperson words / run\tcould not answer\tasked again\tdon't-know recovered\trecall")
	for _, sc := range slices.Sorted(maps.Keys(scenarios)) {
		for _, src := range []string{"simulated", "real", "mixed"} {
			s := scenarios[sc][src]
			if s == nil {
				continue
			}
			name := sc
			if name == "" {
				name = "unknown"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, src, s.runsText(), avg(int(s.personWords), s.convs),
				metricVal("could not answer", s), metricVal("asked again", s), metricVal("don't-know recovered", s), metricVal("recall", s))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if untagged > 0 {
		fmt.Fprintf(w, "%s left out: no person turn has tagged_by\n", plural(untagged, "run"))
	}
	return nil
}

func metricVal(label string, s *ctxStats) string {
	for _, m := range contextMetrics {
		if m.label == label {
			return m.val(s)
		}
	}
	panic("no context metric " + label)
}
