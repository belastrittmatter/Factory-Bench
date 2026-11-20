// Package bench runs the Context creator bench: the conversation loop, scoring and the events.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	ScenariosDir string
	Runner       claude.Runner
	// Send delivers events; by default the way `factorybench emit` does (FACTORYBENCH_URL, spooled on failure),
	// with delivery problems written to Stderr.
	Send   func(evs ...[]byte)
	Stdout io.Writer
	Stderr io.Writer
}

func NewEnv(scenariosDir string, stdout, stderr io.Writer) *Env {
	return &Env{ScenariosDir: scenariosDir, Send: emit.Send, Stdout: stdout, Stderr: stderr}
}

func (e *Env) runner() claude.Runner {
	if e.Runner != nil {
		return e.Runner
	}
	return claude.Exec{Program: claude.Program()}
}

type Models struct {
	Subject, Person, Judge, Builder string
}

func DefaultModels() Models {
	return Models{DefaultSubjectModel, DefaultPersonModel, DefaultJudgeModel, DefaultBuilderModel}
}

type Options struct {
	Scenario string // an id under the scenarios directory, or a path to a scenario directory
	Subject  string
	Person   string
	MaxTurns int // 0: the scenario's max_turns
	Builders int
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
	if !sub.Converses() {
		if o.Person != "" && o.Person != "-" {
			fmt.Fprintf(e.Stderr, "factorybench: subject %s has no conversation: --person %s is ignored\n", sub.Name, o.Person)
		}
		return p, nil
	}
	if o.Person == "" {
		return nil, fmt.Errorf("--person is required for subject %s (one of %s)", sub.Name, strings.Join(prompts.Personas, ", "))
	}
	if p.PersonaText, err = prompts.Persona(o.Person); err != nil {
		return nil, err
	}
	p.PersonaID = o.Person
	if sameModel(o.Models.Person, o.Models.Subject) {
		return nil, fmt.Errorf("refusing to run: the person (%s) and the subject (%s) may be the same model; they must be different models so they do not understand each other more easily than a real person would", o.Models.Person, o.Models.Subject)
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
		out = append(out,
			SessionPlan{"subject", m.Subject, fmt.Sprintf("up to %d (one per turn, the last one forced to finish)", p.MaxTurns), p.MaxTurns},
			SessionPlan{"person", m.Person, fmt.Sprintf("up to %d", p.MaxTurns-1), p.MaxTurns - 1})
	}
	out = append(out,
		SessionPlan{"judge", m.Judge, "1", 1},
		SessionPlan{"builder", m.Builder, fmt.Sprint(p.Opts.Builders), p.Opts.Builders},
		SessionPlan{"comparison", m.Judge, "1", 1})
	for i := range out {
		out[i].Max *= TriesPerAsk
	}
	return out
}

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
	if url := os.Getenv("FACTORYBENCH_URL"); url != "" {
		fmt.Fprintf(w, "events    to %s\n", url)
	} else {
		fmt.Fprintln(w, "events    FACTORYBENCH_URL is not set: local evidence only")
	}
	fmt.Fprintf(w, "cap       $%g per session; at most %d sessions here, repairs included: $%.2f (an upper bound from the cap, not an estimate)\n",
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

// runInfo is run.json: how a run was started and how it ended.
type runInfo struct {
	Run         string   `json:"run"`
	Attempt     string   `json:"attempt"`
	Started     string   `json:"started"`
	ScenarioDir string   `json:"scenario_dir"`
	Subject     string   `json:"subject"`
	Persona     string   `json:"persona"`
	MaxTurns    int      `json:"max_turns"`
	Builders    int      `json:"builders"`
	MaxCostUSD  float64  `json:"max_cost_usd"`
	Models      Models   `json:"models"`
	Labels      []string `json:"labels"`
	Status      string   `json:"status,omitempty"`
	Note        string   `json:"note,omitempty"`
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func writeTranscript(path string, turns []protocol.Turn) error {
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
	info := runInfo{Run: name, Attempt: attempt, Started: started.Format(time.RFC3339), ScenarioDir: s.Dir, Subject: p.Subject.Name,
		Persona: p.PersonaID, MaxTurns: p.MaxTurns, Builders: o.Builders, MaxCostUSD: o.MaxCost, Models: o.Models, Labels: labels}
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
	evs.started(started, &contract.WorkItem{Repo: s.WorkItem.Repo, Issue: s.WorkItem.Issue}, ConfigVersion(p.Subject, o.Models.Subject), labels)
	evs.flush()

	finish := func(runErr error) (*Outcome, error) {
		out.SubjectCostUSD = subjectCost
		out.Status = StatusCompleted
		if runErr != nil {
			out.Status, out.Note = StatusFailed, runErr.Error()
			if ctx.Err() != nil {
				out.Status = StatusCancelled
			}
		}
		evs.finished(time.Now().UTC(), out.Status, out.Note)
		evs.flush()
		info.Status, info.Note = out.Status, out.Note
		if werr := writeJSON(filepath.Join(dir, "run.json"), info); werr != nil {
			return out, werr
		}
		if runErr != nil && !isInvalid(runErr) {
			return out, runErr
		}
		return out, nil
	}

	var turns []protocol.Turn
	var folder *change.Folder
	switch p.Subject.Kind {
	case SubjectNone:
		folder = change.None(s)
	case SubjectCeiling:
		if folder, err = change.Ceiling(s); err != nil {
			return finish(err)
		}
	default:
		person, err := newSimPerson(caller, o.Models.Person, s, p.PersonaID, p.PersonaText)
		if err != nil {
			return finish(err)
		}
		conv := &conversation{caller: caller, s: s, subject: p.Subject, model: o.Models.Subject, maxTurns: p.MaxTurns, person: person}
		cout, cerr := conv.run(ctx)
		turns, folder = cout.Turns, cout.Change
		for _, te := range TurnEvents(turns, o.NoText, started) {
			evs.add("context.turn", "turn:"+strconv.FormatInt(te.Turn.N, 10), te.At, te.Turn)
			if te.Turn.Speaker == "factory" {
				out.Turns++
			}
		}
		if werr := writeTranscript(filepath.Join(dir, "transcript.jsonl"), turns); werr != nil {
			return finish(werr)
		}
		evs.flush()
		if cerr != nil {
			return finish(cerr)
		}
	}
	if folder == nil || len(folder.Files) == 0 {
		return finish(invalid("the subject produced no change folder"))
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
		{Gate2Change, sc.Gates.Gate2, firstFailure(g.Failures, 2), true},
		{Gate3Testable, g.Gate3, firstFailure(g.Failures, 3), true},
		{Gate4Agreement, sc.Gate4 != nil && *sc.Gate4, sc.Gate4Reason, sc.Gate4 != nil},
	}
	for _, gt := range gates {
		if gt.sent && serr == nil {
			evs.gate(gt.name, gt.passed, gt.reason, now)
			out.Gates[gt.name] = gt.passed
		}
	}
	if err := writeJSON(filepath.Join(dir, "gates.json"), map[string]any{
		"gate1": g.Gate1, "gate2": sc.Gates.Gate2, "gate3": g.Gate3, "gate4": sc.Gate4,
		"requirements": g.Requirements, "failures": nonNil(g.Failures), "vague_warnings": nonNil(g.VagueWarnings),
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

	score := metrics.Score(metrics.ScoreInput{
		Truth: &s.Truth, Judge: sc.Judge, Turns: turns, Requirements: g.Requirements, Compare: sc.Compare, VagueWarnings: len(g.VagueWarnings),
		Cost: contract.ContextBenchCost{Person: meter.Get(BucketPerson), Judge: meter.Get(BucketJudge), Builders: meter.Get(BucketBuilders)},
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
