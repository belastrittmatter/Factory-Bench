package drycheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"factorybench/internal/contextbench/bench"
	"factorybench/internal/contextbench/fakeclaude"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
	"factorybench/internal/contract"
)

type runSpec struct {
	name    string
	subject string
	person  string
	args    []string
	env     map[string]string
	stdin   string
}

type runFile struct {
	bench.RunInfo
	Status string `json:"status"`
	Note   string `json:"note"`
}

type usageP struct {
	Model           string   `json:"model"`
	Input           int64    `json:"input"`
	Output          int64    `json:"output"`
	CacheRead       int64    `json:"cache_read"`
	CacheWrite      int64    `json:"cache_write"`
	ReportedCostUSD *float64 `json:"reported_cost_usd"`
	DurationMS      int64    `json:"duration_ms"`
}

type startedP struct {
	WorkItem             contract.WorkItem `json:"work_item"`
	FactoryConfigVersion string            `json:"factory_config_version"`
	Labels               []string          `json:"labels"`
	Role                 string            `json:"role"`
}

type gateP struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason"`
}

type finishedP struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// finished is one run of the dry run with everything it left: its exit, its local evidence, the events it sent,
// the fake claude's calls.
type finished struct {
	spec       runSpec
	code       int
	out, errs  string
	dir        string
	info       runFile
	raw        [][]byte
	evs        []contract.Event
	started    startedP
	fin        finishedP
	usages     []usageP
	turns      []contract.ContextTurn
	score      *contract.ContextScore
	scoreKeys  map[string]json.RawMessage
	scores     int
	gates      map[string]gateP
	transcript []protocol.Turn
	log        []fakeclaude.LogEntry
	sc         *scenario.Scenario
}

func (d *dry) scenarioSpecs() []runSpec {
	creator := "file:" + filepath.Join(d.fx, "subjects", "fake-creator.md")
	script, _ := os.ReadFile(filepath.Join(d.fx, "human-script.txt"))
	build := filepath.Join(d.fx, "fake-build.sh")
	return []runSpec{
		{name: "none", subject: "none"},
		{name: "ceiling", subject: "ceiling"},
		{name: "plain-unsure", subject: "plain", person: "unsure"},
		{name: "plain-technical", subject: "plain", person: "decided-technical"},
		{name: "file-talkative-build", subject: creator, person: "talkative", args: []string{"--build-cmd", build}},
		{name: "file-nontechnical-14", subject: creator, person: "decided-nontechnical", args: []string{"--max-turns", "14", "--build-cmd", build},
			env: map[string]string{"FACTORYBENCH_FAKE_SUBJECT_TURNS": "14", "FACTORYBENCH_FAKE_BUILD_FAIL": "1"}},
		{name: "file-technical-10", subject: creator, person: "decided-technical", args: []string{"--max-turns", "10"}},
		{name: "file-unsure-10", subject: creator, person: "unsure", args: []string{"--max-turns", "10", "--no-text"}},
		{name: "file-human", subject: creator, person: "human", args: []string{"--max-turns", "10"}, stdin: string(script)},
		{name: "violate", subject: creator, person: "unsure", args: []string{"--max-turns", "10"}, env: map[string]string{"FACTORYBENCH_FAKE_VIOLATE": "1"}},
		{name: "unknown-id", subject: creator, person: "unsure", env: map[string]string{"FACTORYBENCH_FAKE_UNKNOWN_ID": "1"}},
		{name: "disagree", subject: creator, person: "decided-technical", args: []string{"--max-turns", "10"}, env: map[string]string{"FACTORYBENCH_FAKE_DISAGREE": "R1"}},
		{name: "invent", subject: creator, person: "decided-technical", args: []string{"--max-turns", "10"}, env: map[string]string{"FACTORYBENCH_FAKE_INVENT": "1"}},
		{name: "contradict", subject: creator, person: "decided-technical", args: []string{"--max-turns", "10"}, env: map[string]string{"FACTORYBENCH_FAKE_CONTRADICT": "1"}},
		{name: "judge-fails", subject: creator, person: "decided-technical", args: []string{"--max-turns", "10"}, env: map[string]string{"FACTORYBENCH_FAKE_FAIL": "judge"}},
	}
}

func (d *dry) scenarioRuns() map[string]*finished {
	sc, err := scenario.Load(filepath.Join(d.scen, "tiny"))
	if err != nil {
		d.check(false, "load the test scenario: %v", err)
		return nil
	}
	got := map[string]*finished{}
	for _, rs := range d.scenarioSpecs() {
		f := d.runOne(sc, rs)
		if f == nil {
			continue
		}
		got[rs.name] = f
		d.commonChecks(f)
	}
	d.say("ok  %d runs: events valid and complete, no truth reaches a subject or builder, builders never see the transcript, person and subject models differ, costs add up", len(got))
	d.specificChecks(got)
	return got
}

func (d *dry) runOne(sc *scenario.Scenario, rs runSpec) *finished {
	d.seq++
	logPath := filepath.Join(d.logs, fmt.Sprintf("%02d-%s.jsonl", d.seq, rs.name))
	env := map[string]string{"FACTORYBENCH_FAKE_LOG": logPath}
	for k, v := range rs.env {
		env[k] = v
	}
	before := dirNames(d.runs)
	args := []string{"run", "--scenarios", d.scen, "--out", d.runs, "--scenario", "tiny", "--subject", rs.subject, "--yes"}
	if rs.person != "" {
		args = append(args, "--person", rs.person)
	}
	args = append(args, rs.args...)
	code, out, errs := d.cli(env, rs.stdin, args...)
	f := &finished{spec: rs, code: code, out: out, errs: errs, sc: sc, gates: map[string]gateP{}}
	added := diff(dirNames(d.runs), before)
	if len(added) != 1 {
		d.check(false, "run %s: expected one new run directory, got %v (exit %d: %s%s)", rs.name, added, code, out, errs)
		return nil
	}
	f.dir = filepath.Join(d.runs, added[0])
	if !d.load(f) {
		return nil
	}
	if _, err := os.Stat(logPath); err == nil {
		f.log, _ = fakeclaude.ReadLog(logPath)
	}
	return f
}

// load reads the run's local evidence and decodes its events.
func (d *dry) load(f *finished) bool {
	name := f.spec.name
	b, err := os.ReadFile(filepath.Join(f.dir, "run.json"))
	if err != nil || json.Unmarshal(b, &f.info) != nil {
		d.check(false, "run %s: run.json: %v", name, err)
		return false
	}
	b, err = os.ReadFile(filepath.Join(f.dir, "events.jsonl"))
	if err != nil {
		d.check(false, "run %s: no events.jsonl: %v", name, err)
		return false
	}
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		ev, _, err := contract.Validate(line)
		if err != nil {
			d.check(false, "run %s: an event does not validate: %v: %s", name, err, line)
			continue
		}
		f.raw = append(f.raw, line)
		f.evs = append(f.evs, ev)
		switch ev.Type {
		case "attempt.started":
			_ = json.Unmarshal(ev.Payload, &f.started)
		case "attempt.finished":
			_ = json.Unmarshal(ev.Payload, &f.fin)
		case "usage":
			var u usageP
			_ = json.Unmarshal(ev.Payload, &u)
			f.usages = append(f.usages, u)
		case "context.turn":
			f.turns = append(f.turns, contract.ParseContextTurn(ev.Payload))
		case "gate":
			var g gateP
			_ = json.Unmarshal(ev.Payload, &g)
			f.gates[g.Name] = g
		case "context.score":
			f.scores++
			s := contract.ParseContextScore(ev.Payload)
			f.score = &s
			_ = json.Unmarshal(ev.Payload, &f.scoreKeys)
		}
	}
	f.transcript, _ = bench.ReadTranscript(filepath.Join(f.dir, "transcript.jsonl"))
	return true
}

func dirNames(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func diff(after, before []string) []string {
	var out []string
	for _, a := range after {
		if !slices.Contains(before, a) {
			out = append(out, a)
		}
	}
	return out
}

func (f *finished) calls(role string) []fakeclaude.LogEntry {
	var out []fakeclaude.LogEntry
	for _, e := range f.log {
		if e.Role == role {
			out = append(out, e)
		}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// ---- views of the turn events, the way a reader of the events would count them ----

func (f *finished) factoryTurns() []contract.ContextTurn { return f.bySpeaker("factory") }

func (f *finished) personTurns() []contract.ContextTurn { return f.bySpeaker("person") }

func (f *finished) bySpeaker(s string) []contract.ContextTurn {
	var out []contract.ContextTurn
	for _, t := range f.turns {
		if t.Speaker == s {
			out = append(out, t)
		}
	}
	return out
}

func countTurns(ts []contract.ContextTurn, pred func(contract.ContextTurn) bool) int {
	n := 0
	for _, t := range ts {
		if pred(t) {
			n++
		}
	}
	return n
}

func (f *finished) moveCount(move string) int {
	return countTurns(f.factoryTurns(), func(t contract.ContextTurn) bool { return t.Move == move })
}

// recovered counts person turns with dont_know whose next factory turn is show or suggest and whose next person
// turn reveals something: the definition of "don't-know recovered".
func (f *finished) recovered() int {
	n := 0
	for i, t := range f.turns {
		if t.Speaker != "person" || !t.DontKnow {
			continue
		}
		if i+2 < len(f.turns) && f.turns[i+1].Speaker == "factory" && (f.turns[i+1].Move == "show" || f.turns[i+1].Move == "suggest") &&
			f.turns[i+2].Speaker == "person" && len(f.turns[i+2].Revealed) > 0 {
			n++
		}
	}
	return n
}

func (f *finished) maxFrustration() int64 {
	m := int64(-1)
	for _, t := range f.personTurns() {
		if t.Frustration != nil {
			m = max(m, *t.Frustration)
		}
	}
	return m
}

func (f *finished) gateOK(name string) bool { g, ok := f.gates[name]; return ok && g.Passed }

// ---- the invariants of every run ----

var configRe = regexp.MustCompile(`^(plain|file-fake-creator)@[0-9a-f]{12}/claude-sonnet-5-5$`)

var scoreKeys = []string{"scenario", "subject", "persona", "truth", "captured", "traps", "invented", "invented_unmarked", "map_tag_errors",
	"assumed_marked", "requirements", "untraced", "noise_left", "missing_slots", "contradictions", "builders", "vague_warnings",
	"sim_violations", "bench_cost_usd"}

func (d *dry) commonChecks(f *finished) {
	name := f.spec.name
	okRun := f.fin.Status == bench.StatusCompleted
	for _, file := range []string{"run.json", "transcript.jsonl", "events.jsonl"} {
		_, err := os.Stat(filepath.Join(f.dir, file))
		d.check(err == nil, "run %s: %s missing", name, file)
	}
	_, err := os.Stat(filepath.Join(f.dir, "gates.json"))
	d.check((err == nil) == (len(f.gates) > 0), "run %s: gates.json (%v) and the gate events (%d) disagree", name, err, len(f.gates))
	d.check(regexp.MustCompile(`-tiny-`+regexp.QuoteMeta(f.info.Subject2())+`(-\d+)?$`).MatchString(f.info.Run), "run %s: bad run id %q", name, f.info.Run)
	d.check(f.info.Status == f.fin.Status, "run %s: run.json says %q, attempt.finished %q", name, f.info.Status, f.fin.Status)
	if okRun {
		d.check(f.code == 0, "run %s: exit %d", name, f.code)
	} else {
		d.check(f.code == 1 && f.fin.Status == bench.StatusFailed && f.fin.Reason != "", "run %s: a failed run exited %d with %+v", name, f.code, f.fin)
	}

	// the envelope: one started first, one finished last, ids unique and keyed by the attempt, only the types of the spec
	ids := map[string]bool{}
	for i, ev := range f.evs {
		d.check(ev.AttemptID == f.info.Attempt && strings.HasPrefix(ev.ID, f.info.Attempt+":") && !ids[ev.ID], "run %s: event %d has a bad id or attempt: %s %s", name, i, ev.ID, ev.AttemptID)
		ids[ev.ID] = true
		d.check(slices.Contains([]string{"attempt.started", "usage", "context.turn", "gate", "context.score", "attempt.finished"}, ev.Type), "run %s: unexpected event type %s", name, ev.Type)
	}
	d.check(len(f.evs) > 2 && f.evs[0].Type == "attempt.started" && f.evs[len(f.evs)-1].Type == "attempt.finished", "run %s: the events do not run from attempt.started to attempt.finished", name)
	d.check(f.started.Role == "context" && f.started.WorkItem == contract.WorkItem{Repo: "bench/tiny-context", Issue: 1}, "run %s: attempt.started is %+v", name, f.started)
	converses := f.spec.subject != "none" && f.spec.subject != "ceiling"
	if converses {
		d.check(configRe.MatchString(f.started.FactoryConfigVersion), "run %s: factory_config_version %q", name, f.started.FactoryConfigVersion)
		maxTurns := "6"
		if i := slices.Index(f.spec.args, "--max-turns"); i >= 0 {
			maxTurns = f.spec.args[i+1]
		}
		want := []string{"scenario:tiny", "persona:" + f.spec.person, "max-turns:" + maxTurns}
		d.check(slices.Equal(f.started.Labels, want), "run %s: labels %v, want %v", name, f.started.Labels, want)
	} else {
		d.check(f.started.FactoryConfigVersion == f.spec.subject && slices.Equal(f.started.Labels, []string{"scenario:tiny"}), "run %s: attempt.started is %+v", name, f.started)
	}

	// the Context creator's own sessions are the only usage events
	subj := f.calls("subject")
	d.check(len(f.usages) == len(subj), "run %s: %d usage events for %d subject sessions", name, len(f.usages), len(subj))
	for _, u := range f.usages {
		d.check(u.Model == "claude-sonnet-5-5" && u.Input == 100 && u.Output == 20 && u.CacheRead == 10 && u.CacheWrite == 5 && u.ReportedCostUSD != nil && near(*u.ReportedCostUSD, 0.01),
			"run %s: usage %+v", name, u)
	}

	// the turns: numbered from 1 in conversation order, the text of the transcript unless --no-text
	for i, t := range f.turns {
		d.check(t.N == int64(i+1), "run %s: turn event %d is numbered %d", name, i+1, t.N)
	}
	nSub, nPer := 0, 0
	for _, t := range f.transcript {
		if t.Subject != nil {
			nSub++
		}
		if t.Person != nil {
			nPer++
		}
	}
	d.check(len(f.factoryTurns()) == nSub && len(f.personTurns()) == nPer, "run %s: %d/%d factory/person turn events for a transcript of %d/%d", name, len(f.factoryTurns()), len(f.personTurns()), nSub, nPer)
	if converses {
		d.check(len(f.turns) > 0 && f.turns[0].Speaker == "person" && f.turns[0].N == 1, "run %s: the person's brief is not turn 1", name)
	} else {
		d.check(len(f.turns) == 0, "run %s: a subject without a conversation has turn events", name)
	}
	taggedBy := "simulator"
	if f.spec.person == "human" {
		taggedBy = "tagger"
	}
	noText := slices.Contains(f.spec.args, "--no-text")
	k := 0
	for _, tt := range f.transcript {
		for _, text := range []string{subjectText(tt), personText(tt)} {
			if text == "" || k >= len(f.turns) {
				continue
			}
			if noText {
				d.check(f.turns[k].Text == "", "run %s: turn %d carries text under --no-text", name, k+1)
			} else {
				d.check(f.turns[k].Text == text, "run %s: turn %d text differs from the transcript", name, k+1)
			}
			k++
		}
	}
	for _, t := range f.turns {
		switch t.Speaker {
		case "factory":
			d.check(slices.Contains([]string{"capture", "mirror", "show", "suggest", "expand", "sharpen", "challenge", "narrow", "ask", "confirm"}, t.Move) && t.Words > 0, "run %s: factory turn %+v", name, t)
		case "person":
			d.check(t.TaggedBy == taggedBy && t.Words > 0, "run %s: person turn %d tagged_by %q", name, t.N, t.TaggedBy)
			d.check((t.N == 1) == (t.Frustration == nil), "run %s: person turn %d frustration %v", name, t.N, t.Frustration)
		}
	}

	// every call: the flags the spec names, and a role
	for _, e := range f.log {
		d.check(e.Flags["tools"] == "" && e.Flags["max-budget-usd"] == "2" && e.Flags["setting-sources"] == "project" && e.Flags["no-session-persistence"] == "true" && e.Flags["output-format"] == "json",
			"run %s: a %s call had flags %v", name, e.Role, e.Flags)
		d.check(strings.HasPrefix(e.System, "ROLE: "+e.Role+"\n"), "run %s: %s system prompt does not start with its ROLE line", name, e.Role)
	}
	d.noTruthToSubject(f)
	d.buildersBlind(f)
	d.modelsDiffer(f)

	// the gates and the score exist exactly when they could be decided
	d.check(f.spec.name == "unknown-id" || len(f.gates) >= 3, "run %s: %d gate events", name, len(f.gates))
	if okRun {
		d.check(f.scores == 1 && f.score != nil, "run %s: %d context.score events", name, f.scores)
		d.checkScore(f)
	} else {
		d.check(f.scores == 0, "run %s: a failed run sent a context.score", name)
	}
}

// Subject2 is the subject-and-persona part of a run id, for the dry run's id check.
func (i runFile) Subject2() string { return subjectSlug(i.Subject) + "-" + i.Persona }

func subjectText(t protocol.Turn) string {
	if t.Subject != nil {
		return t.Subject.Message
	}
	return ""
}

func personText(t protocol.Turn) string {
	if t.Person != nil {
		return t.Person.Reply
	}
	return ""
}

func (d *dry) checkScore(f *finished) {
	name, s := f.spec.name, f.score
	for _, key := range scoreKeys {
		if key == "persona" && (f.spec.subject == "none" || f.spec.subject == "ceiling") {
			continue
		}
		_, ok := f.scoreKeys[key]
		d.check(ok, "run %s: context.score has no %q", name, key)
	}
	d.check(s.Scenario == "tiny" && s.Persona == personaOf(f) && s.Subject == subjectSlug(f.spec.subject), "run %s: score names %q %q %q", name, s.Scenario, s.Subject, s.Persona)
	d.check(s.Truth.Items == 8 && s.Truth.G4 == 3 && s.Truth.Latent == 2 && s.Truth.Traps == 3 && s.Truth.Noise == 2, "run %s: truth %+v", name, s.Truth)
	d.check(s.Captured.Full+s.Captured.Partial <= s.Truth.Items && s.Captured.G4Full+s.Captured.G4Partial <= s.Truth.G4 && s.Captured.Latent <= s.Truth.Latent, "run %s: captured %+v", name, s.Captured)
	d.check(s.Builders.Agree <= s.Builders.Total, "run %s: builders %+v", name, s.Builders)
	// what the bench spent, by who: the fake charged $0.01 a call; the subject's calls are not in it
	c := s.BenchCostUSD
	d.check(near(c.Person, 0.01*float64(len(f.calls("person"))+len(f.calls("tagger")))) && near(c.Judge, 0.01*float64(len(f.calls("judge")))) &&
		near(c.Builders, 0.01*float64(len(f.calls("builder"))+len(f.calls("comparison")))), "run %s: bench cost %+v for %d person, %d tagger, %d judge, %d builder, %d comparison calls",
		name, c, len(f.calls("person")), len(f.calls("tagger")), len(f.calls("judge")), len(f.calls("builder")), len(f.calls("comparison")))
	d.check((s.Build != nil) == (c.Build > 0) && (s.Build == nil || near(c.Build, 0.05)), "run %s: build %+v cost %v", name, s.Build, c.Build)
	var subjectCost float64
	for _, u := range f.usages {
		subjectCost += *u.ReportedCostUSD
	}
	d.check(near(subjectCost, 0.01*float64(len(f.calls("subject")))), "run %s: subject usage cost %v for %d calls", name, subjectCost, len(f.calls("subject")))
	if f.spec.name != "none" && f.spec.name != "ceiling" {
		d.check(s.Requirements > 0 && s.Builders.Total <= s.Requirements, "run %s: %d requirements, %d compared", name, s.Requirements, s.Builders.Total)
	}
}

func subjectSlug(s string) string {
	if strings.HasPrefix(s, "file:") {
		base := filepath.Base(strings.TrimPrefix(s, "file:"))
		return "file-" + strings.TrimSuffix(base, filepath.Ext(base))
	}
	return s
}

func personaOf(f *finished) string {
	if f.spec.person == "" || f.spec.subject == "none" || f.spec.subject == "ceiling" {
		return ""
	}
	return f.spec.person
}

// said returns what the person had said by a subject call: the brief, the attachments, the replies before turn k.
func said(sc *scenario.Scenario, turns []protocol.Turn, upToTurn int) string {
	var b strings.Builder
	b.WriteString(bench.BriefMessage(sc))
	for _, a := range sc.Attachments {
		b.Write(a.Data)
	}
	for _, t := range turns {
		if t.Person != nil && t.Turn <= upToTurn {
			b.WriteString("\n" + t.Person.Reply)
		}
	}
	return squash(b.String())
}

// secrets lists every string of truth.json that only the person and the judges may know.
func secrets(sc *scenario.Scenario) []string {
	var out []string
	for _, it := range sc.Truth.Items {
		out = append(out, it.Text)
		if it.LatentHint != "" {
			out = append(out, it.LatentHint)
		}
	}
	for _, t := range sc.Truth.Traps {
		out = append(out, t.Text, t.Resolution)
	}
	for _, n := range sc.Truth.Noise {
		out = append(out, n.Text)
	}
	return out
}

var truthKeys = []string{`"latent_hint"`, `"g4"`, `"resolution"`, `"technical"`, `truth.json`, `<truth>`}

// noTruthToSubject: no subject prompt carries truth.json, or any truth string the person has not yet said.
func (d *dry) noTruthToSubject(f *finished) {
	name := f.spec.name
	for i, e := range f.calls("subject") {
		all := e.System + "\n" + e.Prompt
		for _, k := range truthKeys {
			d.check(!strings.Contains(all, k), "run %s: subject call %d contains %q", name, i+1, k)
		}
		d.check(!strings.Contains(all, string(f.sc.TruthRaw)), "run %s: subject call %d contains truth.json", name, i+1)
		have := squash(all)
		heard := said(f.sc, f.transcript, i) // the subject's call k sees person turns up to k-1
		for _, s := range secrets(f.sc) {
			if strings.Contains(have, squash(s)) {
				d.check(strings.Contains(heard, squash(s)), "run %s: subject call %d contains %q before the person said it", name, i+1, s)
			}
		}
		d.check(strings.Contains(e.Prompt, "<brief>") && strings.Contains(e.Prompt, "living-specs/chat-picker/spec.md") && strings.Contains(e.Prompt, "notes.txt"),
			"run %s: subject call %d lacks the brief, the attachments or the living specs", name, i+1)
	}
}

// buildersBlind: builders and the comparison get only the change folder and the living specs.
func (d *dry) buildersBlind(f *finished) {
	name := f.spec.name
	var all []fakeclaude.LogEntry
	all = append(all, f.calls("builder")...)
	all = append(all, f.calls("comparison")...)
	ceiling := f.spec.subject == "ceiling"
	for i, e := range all {
		text := e.System + "\n" + e.Prompt
		for _, k := range []string{"<transcript>", "[turn ", "FAKE-SUBJECT-TURN", "<truth>", "PERSONA:", `"latent_hint"`, "truth.json"} {
			d.check(!strings.Contains(text, k), "run %s: builder/comparison call %d contains %q", name, i+1, k)
		}
		have := squash(text)
		heard := said(f.sc, f.transcript, 1<<20)
		for _, s := range secrets(f.sc) {
			if strings.Contains(have, squash(s)) && !ceiling {
				d.check(strings.Contains(heard, squash(s)), "run %s: builder/comparison call %d contains %q, which was never said", name, i+1, s)
			}
		}
		d.check(strings.Contains(e.Prompt, "living-specs/chat-picker/spec.md") || e.Role == "comparison", "run %s: builder call %d lacks the living specs", name, i+1)
	}
}

func (d *dry) modelsDiffer(f *finished) {
	name := f.spec.name
	subj, pers := map[string]bool{}, map[string]bool{}
	for _, e := range f.log {
		switch e.Role {
		case "subject":
			subj[e.Model] = true
		case "person":
			pers[e.Model] = true
		}
	}
	for m := range subj {
		d.check(!pers[m], "run %s: the person and the subject both ran on %s", name, m)
	}
	if len(subj) > 0 && f.spec.person != "human" {
		d.check(len(pers) > 0, "run %s: no person session", name)
	}
}

func count(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}
