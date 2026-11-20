// Package bench runs the Context creator bench: the conversation loop, scoring, the events, the replay and the retag.
package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"factorybench/internal/contextbench/change"
	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/metrics"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
	"factorybench/internal/contract"
	"factorybench/internal/emit"
)

const (
	DefaultSubjectModel = "claude-sonnet-5-5"
	DefaultPersonModel  = "claude-opus-5-5"
	DefaultJudgeModel   = "claude-opus-5-5"
	DefaultBuilderModel = "claude-sonnet-5-5"
	DefaultBuilders     = 3
	DefaultMaxCost      = 2.0
	DefaultOut          = "context-runs"
)

type Env struct {
	ScenariosDir   string
	CalibrationDir string // calibration/<id>.json; by default next to the scenarios directory
	Runner         claude.Runner
	// Send delivers events; by default the way `factorybench emit` does (FACTORYBENCH_URL, spooled on failure),
	// with delivery problems written to Stderr.
	Send        func(evs ...[]byte)
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	stdinReader *bufio.Reader
}

func NewEnv(scenariosDir string, stdin io.Reader, stdout, stderr io.Writer) *Env {
	lg := log.New(stderr, "", log.LstdFlags)
	return &Env{
		ScenariosDir: scenariosDir, CalibrationDir: filepath.Join(filepath.Dir(filepath.Clean(scenariosDir)), "calibration"),
		Send: func(evs ...[]byte) { emit.Send(lg, evs...) }, Stdin: stdin, Stdout: stdout, Stderr: stderr,
	}
}

func (e *Env) runner() claude.Runner {
	if e.Runner != nil {
		return e.Runner
	}
	return claude.Exec{Program: claude.Program()}
}

func (e *Env) stdin() *bufio.Reader {
	if e.stdinReader == nil {
		e.stdinReader = bufio.NewReader(e.Stdin)
	}
	return e.stdinReader
}

type Models struct {
	Subject, Person, Judge, Builder, Tagger string
}

func DefaultModels() Models {
	return Models{DefaultSubjectModel, DefaultPersonModel, DefaultJudgeModel, DefaultBuilderModel, DefaultJudgeModel}
}

type Options struct {
	Scenario string // an id under the scenarios directory, or a path to a scenario directory
	Subject  string
	Person   string
	MaxTurns int // 0: the scenario's max_turns
	Builders int
	BuildCmd string // "": the run is not built
	Models   Models
	MaxCost  float64
	Out      string
	NoText   bool
	Yes      bool
}

type Prepared struct {
	Opts        Options
	Scenario    *scenario.Scenario
	Subject     SubjectSpec
	PersonaID   string // "-" when the subject has no conversation
	PersonaText string
	Human       bool
	MaxTurns    int
}

func ParseSubject(arg string) (SubjectSpec, error) {
	switch {
	case arg == "none" || arg == "ceiling":
		return SubjectSpec{Kind: SubjectKind(arg), Name: arg}, nil
	case arg == "plain":
		return SubjectSpec{Kind: SubjectPlain, Name: arg, Prompt: prompts.Plain()}, nil
	case strings.HasPrefix(arg, "file:"):
		p := strings.TrimPrefix(arg, "file:")
		b, err := os.ReadFile(p)
		if err != nil {
			return SubjectSpec{}, fmt.Errorf("subject %s: %v", arg, err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return SubjectSpec{}, fmt.Errorf("subject %s: the file is empty", arg)
		}
		if n := len(claude.SystemPrompt(claude.Request{Role: prompts.RoleSubject, System: string(b)})); n > claude.MaxArgBytes {
			return SubjectSpec{}, fmt.Errorf("subject %s: the prompt is %d bytes, over the %d argument limit", arg, n, claude.MaxArgBytes)
		}
		return SubjectSpec{Kind: SubjectFile, Name: arg, Prompt: string(b)}, nil
	}
	return SubjectSpec{}, fmt.Errorf("subject %q is not none, plain, ceiling or file:<path>", arg)
}

// Prepare resolves and checks a run's options. It calls no model.
func (e *Env) Prepare(o Options) (*Prepared, error) {
	dir, err := scenario.Resolve(e.ScenariosDir, o.Scenario)
	if err != nil {
		return nil, err
	}
	s, err := scenario.Load(dir)
	if err != nil {
		return nil, err
	}
	sub, err := ParseSubject(o.Subject)
	if err != nil {
		return nil, err
	}
	p := &Prepared{Opts: o, Scenario: s, Subject: sub, PersonaID: "-", MaxTurns: o.MaxTurns}
	if p.MaxTurns == 0 {
		p.MaxTurns = s.MaxTurns
	}
	if p.MaxTurns < 1 {
		return nil, fmt.Errorf("max turns must be at least 1")
	}
	if o.Builders < 1 {
		return nil, fmt.Errorf("--builders must be at least 1")
	}
	if o.BuildCmd != "" && !s.Buildable() {
		fmt.Fprintf(e.Stderr, "factorybench: scenario %s has no build section: --build-cmd is ignored for it\n", s.ID)
	}
	if !sub.Converses() {
		if o.Person != "" && o.Person != "-" {
			fmt.Fprintf(e.Stderr, "factorybench: subject %s has no conversation: --person %s is ignored\n", sub.Name, o.Person)
		}
		return p, nil
	}
	switch o.Person {
	case "":
		return nil, fmt.Errorf("--person is required for subject %s (one of %s, %s)", sub.Name, strings.Join(prompts.Personas, ", "), scenario.HumanPerson)
	case scenario.HumanPerson:
		p.Human, p.PersonaID = true, scenario.HumanPerson
	default:
		txt, err := prompts.Persona(o.Person)
		if err != nil {
			return nil, err
		}
		p.PersonaID, p.PersonaText = o.Person, txt
		if sameModel(o.Models.Person, o.Models.Subject) {
			return nil, fmt.Errorf("refusing to run: the person (%s) and the subject (%s) may be the same model; they must be different models so they do not understand each other more easily than a real person would", o.Models.Person, o.Models.Subject)
		}
	}
	return p, nil
}

// sameModel reports whether a and b are, or may resolve to, one model: a bare alias such as "opus" names the newest
// model of its family, which may be the full name on the other side.
func sameModel(a, b string) bool {
	if a == b {
		return true
	}
	family := func(m string) string {
		for _, f := range []string{"opus", "sonnet", "haiku"} {
			if m == f || strings.Contains(m, "-"+f) {
				return f
			}
		}
		return ""
	}
	bare := func(m string) bool { return m == family(m) }
	return (bare(a) || bare(b)) && family(a) != "" && family(a) == family(b)
}

type SessionPlan struct {
	Role  string
	Model string
	Count string
	Max   int
}

func (p *Prepared) Sessions() []SessionPlan {
	m := p.Opts.Models
	var out []SessionPlan
	if p.Subject.Converses() {
		out = append(out, SessionPlan{"subject", m.Subject, fmt.Sprintf("up to %d (one per turn, the last one forced to finish)", p.MaxTurns), p.MaxTurns})
		if p.Human {
			out = append(out, SessionPlan{"tagger", m.Tagger, "1 (after the conversation)", 1})
		} else {
			out = append(out, SessionPlan{"person", m.Person, fmt.Sprintf("up to %d", p.MaxTurns-1), p.MaxTurns - 1})
		}
	}
	out = append(out, SessionPlan{"judge", m.Judge, "1", 1})
	out = append(out, SessionPlan{"builder", m.Builder, fmt.Sprint(p.Opts.Builders), p.Opts.Builders})
	out = append(out, SessionPlan{"comparison", m.Judge, "1", 1})
	for i := range out {
		out[i].Max *= TriesPerAsk
	}
	return out
}

func (p *Prepared) builds() bool { return p.Opts.BuildCmd != "" && p.Scenario.Buildable() }

func (p *Prepared) PrintPlan(w io.Writer) {
	s := p.Scenario
	fmt.Fprintf(w, "scenario  %s (%s, work item %s#%d, first_run=%v, %d turns max)\n", s.ID, s.Kind, s.WorkItem.Repo, s.WorkItem.Issue, s.FirstRun, p.MaxTurns)
	fmt.Fprintf(w, "subject   %s\n", p.Subject.Name)
	fmt.Fprintf(w, "person    %s\n", p.PersonaID)
	total := 0
	for _, sp := range p.Sessions() {
		fmt.Fprintf(w, "  starts  %-10s %s x %s\n", sp.Role, sp.Count, sp.Model)
		total += sp.Max
	}
	if p.builds() {
		fmt.Fprintf(w, "  starts  %-10s %s (its own sessions, each capped at $%g)\n", "build", p.Opts.BuildCmd, p.Opts.MaxCost)
	}
	if os.Getenv("FACTORYBENCH_URL") != "" {
		fmt.Fprintf(w, "events    to %s\n", os.Getenv("FACTORYBENCH_URL"))
	} else {
		fmt.Fprintln(w, "events    FACTORYBENCH_URL is not set: local evidence only")
	}
	fmt.Fprintf(w, "cap       $%g per session; at most %d sessions here, repairs included: $%.2f [INFERENCE: a ceiling from the cap, not an estimate of the cost]\n",
		p.Opts.MaxCost, total, float64(total)*p.Opts.MaxCost)
}

type Outcome struct {
	Dir     string
	Run     string
	Attempt string
	Status  string
	Note    string
	Score   *contract.ContextScore // nil when scoring did not finish
	Gates   map[string]bool
	Turns   int
	// SubjectCostUSD is what the Context creator's own sessions reported; the bench's spending is Score.BenchCostUSD.
	SubjectCostUSD float64
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func uniqueDir(out, id string) (string, string, error) {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", "", err
	}
	name := id
	for i := 2; ; i++ {
		err := os.Mkdir(filepath.Join(out, name), 0o755)
		if err == nil {
			return filepath.Join(out, name), name, nil
		}
		if !os.IsExist(err) {
			return "", "", err
		}
		name = fmt.Sprintf("%s-%d", id, i)
	}
}

// RunInfo is run.json: how a run was started, which is all a retag needs besides the transcript.
type RunInfo struct {
	Run         string   `json:"run"`
	Attempt     string   `json:"attempt"`
	Started     string   `json:"started"`
	ScenarioDir string   `json:"scenario_dir"`
	Subject     string   `json:"subject"`
	Persona     string   `json:"persona"`
	MaxTurns    int      `json:"max_turns"`
	Builders    int      `json:"builders"`
	BuildCmd    string   `json:"build_cmd,omitempty"`
	MaxCostUSD  float64  `json:"max_cost_usd"`
	Models      Models   `json:"models"`
	Labels      []string `json:"labels"`
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func WriteTranscript(path string, turns []protocol.Turn) error {
	var buf bytes.Buffer
	for _, t := range turns {
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func ReadTranscript(path string) ([]protocol.Turn, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var turns []protocol.Turn
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var t protocol.Turn
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		turns = append(turns, t)
	}
	return turns, nil
}

// Run executes one run. Without o.Yes it prints the plan and returns a nil outcome without calling a model or
// sending an event.
func (e *Env) Run(ctx context.Context, o Options) (*Outcome, error) {
	p, err := e.Prepare(o)
	if err != nil {
		return nil, err
	}
	if !o.Yes {
		fmt.Fprintln(e.Stdout, "plan (nothing is called without --yes):")
		p.PrintPlan(e.Stdout)
		fmt.Fprintln(e.Stdout, "add --yes to start it")
		return nil, nil
	}
	return e.execute(ctx, p)
}

func (e *Env) execute(ctx context.Context, p *Prepared) (*Outcome, error) {
	o, s := p.Opts, p.Scenario
	started := time.Now().UTC()
	id := started.Format("20060102T150405") + "-" + s.ID + "-" + p.Subject.Slug() + "-" + p.PersonaID
	dir, name, err := uniqueDir(o.Out, id)
	if err != nil {
		return nil, err
	}
	attempt := NewAttemptID()
	labels := Labels(s.ID, p)
	version := ConfigVersion(p.Subject, o.Models.Subject)
	info := RunInfo{Run: name, Attempt: attempt, Started: started.Format(time.RFC3339), ScenarioDir: s.Dir, Subject: p.Subject.Name,
		Persona: p.PersonaID, MaxTurns: p.MaxTurns, Builders: o.Builders, BuildCmd: o.BuildCmd, MaxCostUSD: o.MaxCost, Models: o.Models, Labels: labels}
	if err := writeJSON(filepath.Join(dir, "run.json"), info); err != nil {
		return nil, err
	}

	evs := newEventLog(attempt, dir, e)
	meter := &Meter{}
	caller := &Caller{Runner: e.runner(), Meter: meter, MaxCost: o.MaxCost}
	var subjectCost float64
	caller.OnSession = func(se Session) {
		if se.Bucket == BucketSubject {
			subjectCost += se.Resp.Cost
			evs.usage(se)
		}
	}
	out := &Outcome{Dir: dir, Run: name, Attempt: attempt, Gates: map[string]bool{}}
	evs.started(started, &contract.WorkItem{Repo: s.WorkItem.Repo, Issue: s.WorkItem.Issue}, version, labels)
	evs.flush()

	finish := func(runErr error) (*Outcome, error) {
		out.SubjectCostUSD = subjectCost
		status, note := StatusCompleted, out.Note
		if runErr != nil {
			status, note = StatusFailed, joinNote(note, runErr.Error())
		}
		out.Status, out.Note = status, note
		evs.finished(time.Now().UTC(), status, note)
		evs.flush()
		if werr := writeJSON(filepath.Join(dir, "run.json"), struct {
			RunInfo
			Status string `json:"status"`
			Note   string `json:"note,omitempty"`
		}{info, status, note}); werr != nil {
			return out, werr
		}
		if runErr != nil && !IsInvalid(runErr) {
			return out, runErr
		}
		return out, nil
	}

	var turns []protocol.Turn
	var folder *change.Folder
	taggedBy := TaggedBySimulator
	switch p.Subject.Kind {
	case SubjectNone:
		folder = change.None(s)
	case SubjectCeiling:
		if folder, err = change.Ceiling(s); err != nil {
			return finish(err)
		}
	default:
		var person personSource
		if p.Human {
			taggedBy = TaggedByTagger
			showTruth(e.Stdout, s)
			person = &humanPerson{in: e.stdin(), out: e.Stdout}
		} else if person, err = newSimPerson(caller, o.Models.Person, s, p.PersonaID, p.PersonaText); err != nil {
			return finish(err)
		}
		conv := &conversation{caller: caller, s: s, subject: p.Subject, model: o.Models.Subject, maxTurns: p.MaxTurns, person: person, out: e.Stdout}
		cout, cerr := conv.run(ctx)
		turns, folder = cout.Turns, cout.Change
		if cerr == nil && p.Human {
			var tags map[int]protocol.PersonTags
			if tags, cerr = TagTranscript(ctx, caller, BucketPerson, o.Models.Tagger, s, turns); cerr == nil {
				cerr = ApplyTags(s, turns, tags)
			}
		}
		for _, te := range TurnEvents(turns, taggedBy, o.NoText, started) {
			evs.add("context.turn", "turn:"+strconv.FormatInt(te.Turn.N, 10), te.At, te.Turn)
			if te.Turn.Speaker == "factory" {
				out.Turns++
			}
		}
		if werr := WriteTranscript(filepath.Join(dir, "transcript.jsonl"), turns); werr != nil {
			return finish(werr)
		}
		evs.flush()
		if cerr != nil {
			return finish(cerr)
		}
	}
	if !p.Subject.Converses() {
		if werr := WriteTranscript(filepath.Join(dir, "transcript.jsonl"), nil); werr != nil {
			return finish(werr)
		}
	}
	if folder == nil || len(folder.Files) == 0 {
		return finish(invalid(KindProtocol, "the subject produced no change folder"))
	}
	if err := folder.WriteDir(filepath.Join(dir, "change")); err != nil {
		return finish(err)
	}

	sc, serr := scoreRun(ctx, scoreInput{
		S: s, Folder: folder, Turns: turns, Talked: p.Subject.Converses(), JudgeMdl: o.Models.Judge,
		BuilderMd: o.Models.Builder, Builders: o.Builders, Caller: caller,
	})
	now := time.Now().UTC()
	g := sc.Gates
	gates := []struct {
		name   string
		passed bool
		reason string
		sent   bool
	}{
		{Gate1Frame, g.Gate1, firstFailure(g.Failures, 1), s.FirstRun},
		{Gate2Change, sc.Gate2, firstFailure(g.Failures, 2), true},
		{Gate3Testable, g.Gate3, firstFailure(g.Failures, 3), true},
		{Gate4Agreement, sc.Gate4 != nil && *sc.Gate4, sc.Gate4Reason, sc.Gate4 != nil},
	}
	for _, gt := range gates {
		if gt.sent {
			evs.gate(gt.name, gt.passed, gt.reason, now)
			out.Gates[gt.name] = gt.passed
		}
	}
	if err := writeJSON(filepath.Join(dir, "gates.json"), map[string]any{
		"gate1": g.Gate1, "gate2": sc.Gate2, "gate3": g.Gate3, "gate4": sc.Gate4,
		"requirements": g.Requirements, "failures": nonNil(g.Failures), "vague_warnings": nonNil(g.VagueWarnings), "notes": nonNil(g.Notes),
	}); err != nil {
		return finish(err)
	}
	if serr == nil || len(sc.Judge.Items) > 0 {
		if err := writeJSON(filepath.Join(dir, "judge.json"), sc.Judge); err != nil {
			return finish(err)
		}
	}
	if sc.Builders != nil {
		if err := writeJSON(filepath.Join(dir, "builders.json"), sc.Builders); err != nil {
			return finish(err)
		}
	}
	if serr != nil {
		evs.flush()
		return finish(serr)
	}

	var build *contract.ContextBuild
	var buildCost float64
	if p.builds() {
		b, cost, note, berr := e.runBuild(ctx, p, dir, attempt, labels)
		buildCost, build = cost, b
		if ctx.Err() != nil {
			return finish(ctx.Err())
		}
		if berr != nil {
			out.Note = joinNote(out.Note, "build failed: "+berr.Error())
		} else if note != "" {
			out.Note = joinNote(out.Note, "build: "+note)
		}
	}
	score := metrics.Score(metrics.ScoreInput{
		Truth: &s.Truth, Judge: sc.Judge, Turns: turns, Requirements: g.Requirements, Compare: sc.Compare, VagueWarnings: len(g.VagueWarnings),
		Cost:  contract.ContextBenchCost{Person: meter.Get(BucketPerson), Judge: meter.Get(BucketJudge), Builders: meter.Get(BucketBuilders), Build: buildCost},
		Build: build,
	})
	score.Scenario, score.Subject = s.ID, p.Subject.Slug()
	if p.Subject.Converses() {
		score.Persona = p.PersonaID
	}
	evs.add("context.score", "score", time.Now().UTC(), score)
	out.Score = &score
	return finish(nil)
}

func firstFailure(failures []string, gate int) string {
	prefix := "gate " + strconv.Itoa(gate) + ":"
	for _, f := range failures {
		if strings.HasPrefix(f, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(f, prefix))
		}
	}
	return ""
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

type buildJSON struct {
	HiddenPass  int64   `json:"hidden_pass"`
	HiddenTotal int64   `json:"hidden_total"`
	Tasks       int64   `json:"tasks"`
	Merged      int64   `json:"merged"`
	Escalations int64   `json:"escalations"`
	Repairs     int64   `json:"repairs"`
	CostUSD     float64 `json:"cost_usd"`
	Note        string  `json:"note"`
}

func (e *Env) runBuild(ctx context.Context, p *Prepared, runDir, attempt string, labels []string) (*contract.ContextBuild, float64, string, error) {
	o := p.Opts
	out := filepath.Join(runDir, "build")
	args := []string{"--scenario", p.Scenario.Dir, "--change", filepath.Join(runDir, "change"), "--out", out, "--yes",
		"--model", o.Models.Builder, "--max-cost", strconv.FormatFloat(o.MaxCost, 'f', -1, 64)}
	cmd := exec.CommandContext(ctx, o.BuildCmd, args...)
	cmd.Env = append(os.Environ(), "FACTORYBENCH_CONTEXT_ATTEMPT="+attempt, "FACTORYBENCH_CONTEXT_LABELS="+strings.Join(labels, ","))
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	runErr := cmd.Run()
	_ = os.WriteFile(filepath.Join(runDir, "build.log"), append(so.Bytes(), se.Bytes()...), 0o644)
	if runErr != nil {
		return nil, 0, "", errors.New(tail(runErr.Error()+": "+strings.TrimSpace(se.String()), 600))
	}
	raw, err := os.ReadFile(filepath.Join(out, "build.json"))
	if err != nil {
		return nil, 0, "", fmt.Errorf("the build command exited 0 but wrote no build.json: %s", tail(strings.TrimSpace(se.String()), 400))
	}
	var bj buildJSON
	if err := json.Unmarshal(raw, &bj); err != nil {
		return nil, 0, "", fmt.Errorf("build.json: %v", err)
	}
	b := &contract.ContextBuild{HiddenPass: bj.HiddenPass, HiddenTotal: bj.HiddenTotal, Tasks: bj.Tasks, Merged: bj.Merged, Escalations: bj.Escalations, Repairs: bj.Repairs}
	return b, bj.CostUSD, bj.Note, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
