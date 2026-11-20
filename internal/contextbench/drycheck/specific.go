package drycheck

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"factorybench/internal/contextbench/bench"
	"factorybench/internal/contextbench/fakeclaude"
	"factorybench/internal/contract"
	"factorybench/internal/emit"
	"factorybench/internal/normalize"
	"factorybench/internal/report"
)

// specificChecks are the invariants of each kind of run: what the fake claude's script makes each of them do.
func (d *dry) specificChecks(got map[string]*finished) {
	need := func(n string) *finished {
		f := got[n]
		d.check(f != nil && f.score != nil || n == "unknown-id" || n == "judge-fails", "run %s did not finish", n)
		return f
	}
	allGates := func(f *finished, want bool) bool {
		return len(f.gates) == 4 && f.gates[bench.GateFrame].Passed == want && f.gates[bench.GateChange].Passed == want &&
			f.gates[bench.GateTestable].Passed == want && f.gates[bench.GateAgreement].Passed == want
	}
	if f := need("none"); f != nil && f.score != nil {
		s := f.score
		d.check(len(f.calls("subject")) == 0 && len(f.calls("person")) == 0 && len(f.calls("builder")) == 0 && len(f.calls("judge")) == 1, "none: wrong calls %d", len(f.log))
		d.check(allGates(f, false), "none: gates %v", f.gates)
		d.check(f.gates[bench.GateAgreement].Reason != "" && f.gates[bench.GateChange].Reason != "", "none: a failed gate has no reason: %v", f.gates)
		d.check(s.Captured.Full == 0 && s.Captured.Partial == 0 && s.InventedUnmarked == 0 && s.Requirements == 0 && s.Builders.Total == 0 && s.Persona == "", "none: score %+v", s)
		_, err := os.Stat(filepath.Join(f.dir, "change", "brief.md"))
		d.check(err == nil, "none: the change folder has no brief.md")
		_, err = os.Stat(filepath.Join(f.dir, "change", "attachments", "notes.txt"))
		d.check(err == nil, "none: the change folder lacks the attachment")
	}
	if f := need("ceiling"); f != nil && f.score != nil {
		s := f.score
		d.check(s.Captured.Full == s.Truth.Items && s.Captured.Partial == 0 && s.Captured.G4Full == s.Truth.G4, "ceiling: captured %+v", s.Captured)
		d.check(allGates(f, true), "ceiling: gates %v", f.gates)
		d.check(len(f.calls("builder")) == bench.DefaultBuilders && len(f.calls("comparison")) == 1, "ceiling: %d builder and %d comparison calls", len(f.calls("builder")), len(f.calls("comparison")))
		d.check(s.NoiseLeft == 0 && s.Builders.Agree == s.Builders.Total && s.Builders.Total == s.Requirements, "ceiling: noise %d builders %+v of %d requirements", s.NoiseLeft, s.Builders, s.Requirements)
		d.check(strings.Contains(f.calls("judge")[0].Prompt, "<truth>"), "ceiling: the judge was not given the truth")
	}
	if f := need("plain-unsure"); f != nil && f.score != nil {
		s := f.score
		d.check(len(f.factoryTurns()) == 6 && f.moveCount("ask") == 6, "plain-unsure: %d factory turns, %d asks", len(f.factoryTurns()), f.moveCount("ask"))
		last := f.turns[len(f.turns)-1]
		d.check(last.Speaker == "factory" && f.transcript[len(f.transcript)-1].Subject.Done && f.transcript[len(f.transcript)-1].Person == nil, "plain-unsure: the forced last turn was not a finish without a reply")
		d.check(len(f.calls("person")) == 5 && len(f.calls("subject")) == 6 && len(f.usages) == 6, "plain-unsure: %d subject, %d person calls, %d usage events", len(f.calls("subject")), len(f.calls("person")), len(f.usages))
		d.check(countTurns(f.personTurns(), func(t contract.ContextTurn) bool { return t.DontKnow }) >= 1 && s.Captured.Latent == 0 && f.recovered() == 0 && s.SimViolations == 0, "plain-unsure: interaction")
		d.check(strings.Contains(f.calls("subject")[5].Prompt, "<turns_left>0</turns_left>"), "plain-unsure: the last turn was not announced as final")
		d.check(allGates(f, true), "plain-unsure: gates %v", f.gates)
	}
	if f := need("file-talkative-build"); f != nil && f.score != nil {
		s := f.score
		d.check(s.NoiseLeft >= 1, "talkative: noise left %d", s.NoiseLeft)
		d.check(s.Build != nil && *s.Build == contract.ContextBuild{HiddenPass: 1, HiddenTotal: 2, Tasks: 1, Merged: 1, Repairs: 1} && near(s.BenchCostUSD.Build, 0.05), "talkative: build %+v cost %v", s.Build, s.BenchCostUSD.Build)
		_, err := os.Stat(filepath.Join(f.dir, "build", "build.json"))
		d.check(err == nil, "talkative: build.json was not kept")
		b, _ := os.ReadFile(filepath.Join(d.tmp, "build.log"))
		line := string(b)
		d.check(strings.Contains(line, "yes=1") && strings.Contains(line, "model=claude-sonnet-5-5") && strings.Contains(line, "maxcost=2") && strings.Contains(line, filepath.Join(f.dir, "change")) &&
			strings.Contains(line, "attempt="+f.info.Attempt) && strings.Contains(line, "labels="+strings.Join(f.started.Labels, ",")), "talkative: the build command was called as %q", line)
	}
	if f := need("file-nontechnical-14"); f != nil && f.score != nil {
		s := f.score
		d.check(countTurns(f.personTurns(), func(t contract.ContextTurn) bool { return t.CouldNotAnswer }) >= 1, "nontechnical: no could_not_answer")
		d.check(s.Build == nil && strings.Contains(f.fin.Reason, "build failed") && strings.Contains(f.fin.Reason, "could not be built") && f.fin.Status == bench.StatusCompleted && s.BenchCostUSD.Build == 0,
			"nontechnical: a failed build was recorded as %+v, finished %+v", s.Build, f.fin)
		d.check(s.Traps.Raised == 3 && s.Traps.Followed == 3 && s.Truth.Latent == 2 && s.Captured.Latent == 2, "nontechnical: traps %+v captured %+v", s.Traps, s.Captured)
		d.check(s.LatentByMove["show"] == 1 && s.LatentByMove["suggest"] == 1 && s.SimViolations == 0, "nontechnical: latent_by_move %v", s.LatentByMove)
		d.check(len(f.factoryTurns()) == 14, "nontechnical: %d factory turns", len(f.factoryTurns()))
	}
	if f := need("file-technical-10"); f != nil && f.score != nil {
		s := f.score
		d.check(len(f.factoryTurns()) == 8 && countTurns(f.personTurns(), func(t contract.ContextTurn) bool { return t.CouldNotAnswer }) == 0, "technical: %d factory turns", len(f.factoryTurns()))
		d.check(countTurns(f.personTurns(), func(t contract.ContextTurn) bool { return t.Suggestion == "accepted" }) == 2 &&
			countTurns(f.personTurns(), func(t contract.ContextTurn) bool { return t.Suggestion == "rejected" }) == 0, "technical: suggestions")
		confirmed := false
		for i, t := range f.turns {
			if t.Speaker == "factory" && t.Move == "confirm" && i+1 < len(f.turns) && f.turns[i+1].Confirmed {
				confirmed = true
			}
		}
		d.check(confirmed, "technical: the confirm turn was not confirmed")
		d.check(f.turns[len(f.turns)-1].Speaker == "factory", "technical: the conversation did not end on the Context creator's done")
		d.check(allGates(f, true) && s.Builders.Agree == s.Builders.Total && s.Untraced == 0, "technical: gates %v builders %+v untraced %d", f.gates, s.Builders, s.Untraced)
		d.check(s.Captured.G4Full > 0, "technical: captured %+v", s.Captured)
	}
	if f := need("file-unsure-10"); f != nil && f.score != nil {
		d.check(countTurns(f.personTurns(), func(t contract.ContextTurn) bool { return t.DontKnow }) >= 1 && f.recovered() >= 1, "unsure: dont_know/recovered %d", f.recovered())
		d.check(f.maxFrustration() == 1, "unsure: max frustration %d", f.maxFrustration())
	}
	if f := need("file-human"); f != nil && f.score != nil {
		s := f.score
		d.check(s.Persona == "human" && slices.Contains(f.started.Labels, "persona:human"), "human: persona %q labels %v", s.Persona, f.started.Labels)
		d.check(len(f.calls("person")) == 0 && len(f.calls("tagger")) == 1, "human: %d person and %d tagger calls", len(f.calls("person")), len(f.calls("tagger")))
		d.check(strings.Contains(f.out, "Things you really want") && strings.Contains(f.out, "FAKE-SUBJECT-TURN-1") && strings.Contains(f.out, "end with a line containing only a dot"), "human: the truth or the Context creator's messages were not shown to the person")
		var words int64
		for _, t := range f.personTurns()[1:] {
			words += t.Words
		}
		d.check(words > 20, "human: person words %d", words)
		d.check(len(f.turns) > 2 && len(f.turns[2].Revealed) == 1 && f.turns[2].Revealed[0] == "T1", "human: the tagger did not fill the tags of the first answer: %+v", f.turns[2])
		d.check(s.SimViolations == 0 && s.LatentByMove["show"] == 1 && s.LatentByMove["suggest"] == 1, "human: latent_by_move %v violations %d", s.LatentByMove, s.SimViolations)
		d.check(s.Captured.Full == 5 && s.Captured.Partial == 0, "human: captured %+v", s.Captured)
		d.check(near(s.BenchCostUSD.Person, 0.01), "human: the tagger's cost %v is not under person", s.BenchCostUSD.Person)
	}
	if f := need("violate"); f != nil && f.score != nil {
		s := f.score
		d.check(s.SimViolations >= 1 && s.LatentByMove["ask"] >= 1, "violate: a latent reveal after ask was not counted (violations %d, latent_by_move %v)", s.SimViolations, s.LatentByMove)
		d.check(len(f.transcript) > 1 && len(f.transcript[1].Violations) == 1 && f.transcript[1].Subject.Move == "ask", "violate: turn 1 has no violation")
		d.check(f.fin.Status == bench.StatusCompleted, "violate: a violation must not make the run fail")
	}
	if f := need("unknown-id"); f != nil {
		d.check(f.fin.Status == bench.StatusFailed && strings.Contains(f.fin.Reason, "unknown id"), "unknown-id: %+v", f.fin)
		d.check(len(f.calls("judge")) == 0 && len(f.calls("builder")) == 0 && f.score == nil && len(f.gates) == 0, "unknown-id: an invalid run was still scored")
		d.check(len(f.turns) > 0 && len(f.usages) > 0, "unknown-id: the conversation up to the failure was not reported")
	}
	if f := need("disagree"); f != nil && f.score != nil {
		s := f.score
		g := f.gates[bench.GateAgreement]
		d.check(!g.Passed && strings.Contains(g.Reason, "R1") && s.Builders.Agree < s.Builders.Total && s.Builders.Agree > 0 && f.gateOK(bench.GateChange), "disagree: gate %+v builders %+v", g, s.Builders)
	}
	if f := need("invent"); f != nil && f.score != nil {
		s := f.score
		d.check(s.Invented == 1 && s.InventedUnmarked == 1 && s.AssumedMarked == 0, "invent: %+v", s)
	}
	if f := need("contradict"); f != nil && f.score != nil {
		g := f.gates[bench.GateChange]
		d.check(!g.Passed && strings.Contains(g.Reason, "contradict") && f.gateOK(bench.GateFrame) && f.gateOK(bench.GateTestable) && f.score.Contradictions == 1, "contradict: gates %v", f.gates)
		b, _ := os.ReadFile(filepath.Join(f.dir, "gates.json"))
		d.check(strings.Contains(string(b), "contradict"), "contradict: gates.json does not say why gate 2 failed")
	}
	if f := need("judge-fails"); f != nil {
		d.check(f.fin.Status == bench.StatusFailed && strings.Contains(f.fin.Reason, "judge") && f.score == nil, "judge-fails: %+v", f.fin)
		d.check(f.gateOK(bench.GateFrame) && f.gateOK(bench.GateChange) && f.gateOK(bench.GateTestable), "judge-fails: the gates decided by code were not reported: %v", f.gates)
		_, hasAgreement := f.gates[bench.GateAgreement]
		d.check(!hasAgreement, "judge-fails: gate 4 was reported without builders")
	}
	d.say("ok  baselines, personas, forced final turn, protocol checks, latent-after-ask violation, human run, build command and failures behave as docs/context-bench.md says")
}

// ---- matrix ----

func (d *dry) matrix() {
	out := filepath.Join(d.tmp, "matrix-runs")
	code, stdout, errs := d.cli(nil, "", "matrix", "--scenarios", d.scen, "--scenarios-list", "all", "--subjects", "none,ceiling", "--out", out, "--yes")
	d.check(code == 0 && strings.Contains(stdout, "2 runs, 0 not completed"), "matrix: exit %d: %s%s", code, stdout, errs)
	d.check(len(dirNames(out)) == 2, "matrix: %d run directories", len(dirNames(out)))
	d.say("ok  matrix runs every subject on every scenario")
}

// ---- offline: events are spooled, never block, and flush later ----

func (d *dry) offline(got map[string]*finished) {
	_ = os.Remove(d.spool)
	logger := log.Writer()
	log.SetOutput(io.Discard) // emit reports the failed delivery on stderr: expected here
	rs := runSpec{name: "offline", subject: "none", env: map[string]string{"FACTORYBENCH_URL": "http://127.0.0.1:1"}}
	f := d.runOne(nil, rs)
	log.SetOutput(logger)
	if f == nil {
		return
	}
	d.check(f.code == 0 && f.fin.Status == bench.StatusCompleted, "offline: the run did not complete without FactoryBench: exit %d, %+v", f.code, f.fin)
	b, err := os.ReadFile(d.spool)
	d.check(err == nil && bytes.Count(b, []byte("\n")) == len(f.raw) && len(f.raw) > 3, "offline: %d events not spooled (spool %v)", len(f.raw), err)
	before := d.postedEvents()
	logger = log.Writer()
	log.SetOutput(io.Discard)
	emit.Run([]string{"--flush"}, "")
	log.SetOutput(logger)
	d.check(d.postedEvents()-before == len(f.raw), "offline: flushing delivered %d of %d events", d.postedEvents()-before, len(f.raw))
	got["offline"] = f
	d.say("ok  an unreachable FactoryBench never blocks a run: its %d events are spooled and flush later", len(f.raw))
}

// ---- replay and retag ----

func (d *dry) replayAndRetag(got map[string]*finished) {
	env := map[string]string{"FACTORYBENCH_FAKE_LOG": filepath.Join(d.logs, "replay.jsonl")}
	before := d.postedEvents()
	dirsBefore := dirNames(d.runs)
	code, out, errs := d.cli(env, "", "replay", "--scenarios", d.scen, "--out", d.runs, "--calibration", "tiny-q", "--yes")
	d.check(code == 0, "replay exit %d: %s%s", code, out, errs)
	var calDir string
	for _, n := range diff(dirNames(d.runs), dirsBefore) {
		if strings.Contains(n, "-calibration-") {
			calDir = filepath.Join(d.runs, n)
		}
	}
	if calDir == "" {
		d.check(false, "replay wrote no calibration directory")
		return
	}
	b, err := os.ReadFile(filepath.Join(calDir, "calibration.json"))
	d.check(err == nil, "replay: no calibration.json")
	var c bench.CalibrationResult
	_ = json.Unmarshal(b, &c)
	d.check(c.Status == "ok" && c.Questions == 3 && c.Same == 2 && c.Different == 1 && near(c.Agreement, 2.0/3) && near(c.DontKnowMatchRate, 1), "replay: %+v", c)
	d.check(len(c.Turns) == 3 && near(c.Turns[0].LengthRatio, 1) && c.Turns[2].LengthRatio > 0, "replay: length ratios %+v", c.Turns)
	d.check(strings.Contains(out, "agreement 0.67") && strings.Contains(out, "calibration.json"), "replay printed %q", out)
	log, _ := fakeclaude.ReadLog(env["FACTORYBENCH_FAKE_LOG"])
	persons := 0
	for _, e := range log {
		if e.Role == "person" {
			persons++
			d.check(strings.Contains(e.Prompt, "The assistant's last move: ask"), "replay: a question was not fed as an ask turn")
		}
	}
	d.check(persons == 3, "replay: %d person calls for 3 questions", persons)

	// retag a simulated run: the fake tagger and the fake person apply the same rules, so they agree
	target := got["file-unsure-10"]
	if target == nil {
		d.check(false, "retag: the unsure run is missing")
		return
	}
	code, out, errs = d.cli(map[string]string{"FACTORYBENCH_FAKE_LOG": filepath.Join(d.logs, "retag.jsonl")}, "", "retag", "--run", target.dir, "--yes")
	d.check(code == 0, "retag exit %d: %s%s", code, out, errs)
	var rt bench.RetagFile
	b, err = os.ReadFile(filepath.Join(target.dir, "retag.json"))
	d.check(err == nil, "retag: no retag.json")
	_ = json.Unmarshal(b, &rt)
	d.check(rt.Agreement.Turns > 0 && near(rt.Agreement.Overall, 1), "retag: agreement %+v", rt.Agreement)
	if h := got["file-human"]; h != nil {
		code, _, errs = d.cli(nil, "", "retag", "--run", h.dir, "--yes")
		d.check(code != 0 && strings.Contains(errs, "real person"), "retag of a human run was not refused: %s", errs)
	}
	d.check(d.postedEvents() == before, "replay and retag sent %d events: they stay local", d.postedEvents()-before)
	d.say("ok  replay (agreement %.2f over 3 real questions) and retag (agreement %.2f) stay local", c.Agreement, rt.Agreement.Overall)
}

// ---- the FactoryBench side: replay the database and read it with report ----

var runsHeadRe = regexp.MustCompile(`(\d+) runs? with context data · (\d+) scored`)

func (d *dry) database(got map[string]*finished) {
	// what every run sent, run by run: local evidence of the runs under d.runs and the matrix's
	var sent, turns, scored, runs int
	for _, root := range []string{d.runs, filepath.Join(d.tmp, "matrix-runs")} {
		for _, n := range dirNames(root) {
			b, err := os.ReadFile(filepath.Join(root, n, "events.jsonl"))
			if err != nil {
				continue
			}
			sent += bytes.Count(b, []byte("\n"))
			t, s := bytes.Count(b, []byte(`"type":"context.turn"`)), bytes.Count(b, []byte(`"type":"context.score"`))
			turns += t
			scored += s
			if t+s > 0 {
				runs++
			}
		}
	}
	if err := normalize.Run(d.db); err != nil {
		d.check(false, "replay of the database: %v", err)
		return
	}
	count := func(q string) int {
		var n int
		if err := d.db.QueryRow(q).Scan(&n); err != nil {
			d.check(false, "%s: %v", q, err)
		}
		return n
	}
	d.check(count(`SELECT count(*) FROM events`) == sent && count(`SELECT count(*) FROM events WHERE rejected IS NOT NULL`) == 0, "the database holds %d events, the runs sent %d", count(`SELECT count(*) FROM events`), sent)
	d.check(count(`SELECT count(*) FROM context_turns`) == turns && count(`SELECT count(*) FROM context_scores`) == scored, "context rows after replay: %d turns of %d, %d scores of %d",
		count(`SELECT count(*) FROM context_turns`), turns, count(`SELECT count(*) FROM context_scores`), scored)
	d.check(count(`SELECT count(*) FROM work_items WHERE repo = 'bench/tiny-context' AND issue = 1`) == 1, "the scenario's work item is not one work item")
	d.check(count(`SELECT count(*) FROM attempts WHERE role = 'context'`) == runs, "%d context attempts for %d runs", count(`SELECT count(*) FROM attempts WHERE role = 'context'`), runs)

	// per attempt, through the report package the dashboard shares
	for name, f := range got {
		ca, err := report.ContextOfAttempt(d.db, factoryID, f.info.Attempt)
		d.check(err == nil && ca != nil, "%s: no context data in the report: %v", name, err)
		if ca == nil {
			continue
		}
		d.check(len(ca.Turns) == len(f.turns) && ca.Scored == (f.score != nil), "%s: report has %d turns (sent %d), scored %v", name, len(ca.Turns), len(f.turns), ca.Scored)
	}
	if f := got["file-technical-10"]; f != nil {
		ca, _ := report.ContextOfAttempt(d.db, factoryID, f.info.Attempt)
		if ca != nil {
			d.check(metric(ca.Metrics, "recall") != "" && strings.Contains(metric(ca.Metrics, "recall"), "/"), "recall of the technical run shows no counts: %q", metric(ca.Metrics, "recall"))
		}
	}
	ci, err := report.ContextOfWorkItem(d.db, "bench/tiny-context", 1)
	d.check(err == nil && ci != nil && len(ci.Units) == 1 && ci.Units[0].Factory == factoryID && ci.Units[0].Runs == strconv.Itoa(runs)+" ("+strconv.Itoa(scored)+" scored)" && len(ci.Runs) == runs,
		"context of the work item: %+v %v", ci, err)

	var all, unsure bytes.Buffer
	d.check(report.Context(d.db, &all, factoryID, nil) == nil, "report context failed")
	m := runsHeadRe.FindStringSubmatch(all.String())
	d.check(m != nil && m[1] == strconv.Itoa(runs) && m[2] == strconv.Itoa(scored), "report context head: %q (want %d runs, %d scored)", firstLine(all.String()), runs, scored)
	for _, want := range []string{"Effort curve", "max-turns", "10", "14"} {
		d.check(strings.Contains(all.String(), want), "report context lacks %q", want)
	}
	d.check(report.Context(d.db, &unsure, factoryID, []string{"persona:unsure"}) == nil, "report context --label failed")
	mu := runsHeadRe.FindStringSubmatch(unsure.String())
	d.check(mu != nil && mu[1] == "4", "report context --label persona:unsure: %q", firstLine(unsure.String()))
	d.say("ok  the database replays the events into %d turns and %d scores over %d attempts; report reads them", turns, scored, runs)
}

func metric(ms []report.ContextMetric, label string) string {
	for _, m := range ms {
		if m.Label == label {
			return m.Value
		}
	}
	return ""
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }
