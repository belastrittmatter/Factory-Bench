// Package contextbench is `factorybench context`: the Context creator benchmark. It plays a person from a
// scenario's hidden truth, runs the Context creator under test, scores what it hands over and reports the run to
// FactoryBench as an attempt with context.turn, gate and context.score events. See docs/context-bench.md.
package contextbench

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"factorybench/internal/contextbench/bench"
	"factorybench/internal/contextbench/drycheck"
	"factorybench/internal/contextbench/fakeclaude"
	"factorybench/internal/contextbench/lint"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/scenario"
)

const usage = `usage:
  factorybench context run      --scenarios DIR --scenario ID --subject none|plain|ceiling|file:PATH --person PERSONA|human [flags]
  factorybench context matrix   --scenarios DIR --scenarios-list LIST|all --subjects LIST --personas LIST|all [--turns LIST] [flags]
  factorybench context replay   --scenarios DIR --calibration ID [flags]
  factorybench context retag    --run DIR [flags]
  factorybench context lint     --scenarios DIR
  factorybench context dry-run  [--keep]

shared flags: --max-turns N --builders K --build-cmd PATH --subject-model M --person-model M --judge-model M
              --builder-model M --tagger-model M --max-cost USD --out DIR --no-text --yes
events go to FACTORYBENCH_URL with FACTORYBENCH_TOKEN (like 'factorybench emit'); FACTORYBENCH_CLAUDE names the claude program.
Everything that calls a model needs --yes; without it the command prints its plan and exits 0.
`

// Main runs the `context` subcommands (args are what follows "context") and returns the process exit code.
func Main(args []string) int { return Run(args, os.Stdin, os.Stdout, os.Stderr) }

// Run is Main with its terminal given.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, rest := args[0], args[1:]
	if cmd == "fake-claude" { // the program the dry run points FACTORYBENCH_CLAUDE at
		return fakeclaude.Main(rest, stdin, stdout, stderr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := &cli{ctx: ctx, stdin: stdin, stdout: stdout, stderr: stderr}
	switch cmd {
	case "run":
		return c.run(rest)
	case "matrix":
		return c.matrix(rest)
	case "retag":
		return c.retag(rest)
	case "replay":
		return c.replay(rest)
	case "lint":
		return c.lint(rest)
	case "dry-run":
		return c.dryRun(rest)
	}
	fmt.Fprintf(stderr, "factorybench context: unknown command %q\n\n%s", cmd, usage)
	return 2
}

type cli struct {
	ctx            context.Context
	stdin          io.Reader
	stdout, stderr io.Writer
	scenariosDir   string
	opts           bench.Options
	jobs           int
}

func (c *cli) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("factorybench context "+name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	return fs
}

func (c *cli) parse(fs *flag.FlagSet, args []string) (ok bool, code int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.stderr, "%s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return false, 2
	}
	return true, 0
}

func (c *cli) fail(err error) int {
	fmt.Fprintln(c.stderr, "factorybench context:", err)
	return 1
}

// addShared registers the flags run and matrix share.
func (c *cli) addShared(fs *flag.FlagSet) {
	m := bench.DefaultModels()
	fs.StringVar(&c.scenariosDir, "scenarios", "", "directory of scenarios (one directory each)")
	fs.IntVar(&c.opts.MaxTurns, "max-turns", 0, "cap on the Context creator's turns (default: the scenario's max_turns)")
	fs.IntVar(&c.opts.Builders, "builders", bench.DefaultBuilders, "number of builder sessions K (gate 4)")
	fs.StringVar(&c.opts.BuildCmd, "build-cmd", "", "build command run on the change folder of scenarios that have a build section")
	fs.StringVar(&c.opts.Models.Subject, "subject-model", m.Subject, "model of the subject")
	fs.StringVar(&c.opts.Models.Person, "person-model", m.Person, "model of the simulated person (must differ from the subject's)")
	fs.StringVar(&c.opts.Models.Judge, "judge-model", m.Judge, "model of the judge and the comparison")
	fs.StringVar(&c.opts.Models.Builder, "builder-model", m.Builder, "model of the builders (and passed to the build command)")
	fs.StringVar(&c.opts.Models.Tagger, "tagger-model", m.Tagger, "model of the tagger (--person human)")
	fs.Float64Var(&c.opts.MaxCost, "max-cost", bench.DefaultMaxCost, "USD cap per model session (claude --max-budget-usd)")
	fs.StringVar(&c.opts.Out, "out", bench.DefaultOut, "directory for the local evidence of the runs")
	fs.BoolVar(&c.opts.NoText, "no-text", false, "leave the text of the turns out of the context.turn events")
	fs.BoolVar(&c.opts.Yes, "yes", false, "really call the models (without it: print the plan and exit)")
}

func (c *cli) env(scenarios string) *bench.Env {
	e := bench.NewEnv(scenarios)
	e.Stdin, e.Stdout, e.Stderr = c.stdin, c.stdout, c.stderr
	return e
}

func (c *cli) run(args []string) int {
	fs := c.flags("run")
	fs.StringVar(&c.opts.Scenario, "scenario", "", "scenario id (under --scenarios) or a scenario directory")
	fs.StringVar(&c.opts.Subject, "subject", "", "none, plain, ceiling or file:PATH")
	fs.StringVar(&c.opts.Person, "person", "", strings.Join(prompts.Personas, ", ")+" or human")
	c.addShared(fs)
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	if c.opts.Scenario == "" || c.opts.Subject == "" {
		fmt.Fprintln(c.stderr, "factorybench context run: --scenario and --subject are required")
		return 2
	}
	if c.scenariosDir == "" {
		if st, err := os.Stat(c.opts.Scenario); err != nil || !st.IsDir() {
			fmt.Fprintln(c.stderr, "factorybench context run: --scenarios is required (or give --scenario a scenario directory)")
			return 2
		}
		c.scenariosDir = filepath.Dir(filepath.Clean(c.opts.Scenario))
	}
	out, err := c.env(c.scenariosDir).Run(c.ctx, c.opts)
	if err != nil {
		return c.fail(err)
	}
	if out == nil {
		return 0
	}
	printOutcome(c.stdout, out)
	if out.Status != bench.StatusCompleted {
		return 1
	}
	return 0
}

func printOutcome(w io.Writer, o *bench.Outcome) {
	fmt.Fprintf(w, "%s  %s\n  attempt %s  status %s", o.Run, o.Dir, o.Attempt, o.Status)
	if o.Note != "" {
		fmt.Fprintf(w, " (%s)", o.Note)
	}
	fmt.Fprintln(w)
	var gates []string
	for _, g := range []string{bench.GateFrame, bench.GateChange, bench.GateTestable, bench.GateAgreement} {
		if passed, ok := o.Gates[g]; ok {
			gates = append(gates, fmt.Sprintf("%s %v", strings.TrimPrefix(g, "context-"), passed))
		}
	}
	fmt.Fprintf(w, "  turns %d  gates: %s\n", o.Turns, strings.Join(gates, ", "))
	if s := o.Score; s != nil {
		fmt.Fprintf(w, "  items %d: full %d, partial %d  g4 %d: full %d, partial %d  latent %d: drawn %d  traps %d: raised %d, followed %d\n",
			s.Truth.Items, s.Captured.Full, s.Captured.Partial, s.Truth.G4, s.Captured.G4Full, s.Captured.G4Partial,
			s.Truth.Latent, s.Captured.Latent, s.Truth.Traps, s.Traps.Raised, s.Traps.Followed)
		fmt.Fprintf(w, "  requirements %d: untraced %d, invented %d (unmarked %d)  builders agree %d of %d  violations %d  noise left %d\n",
			s.Requirements, s.Untraced, s.Invented, s.InventedUnmarked, s.Builders.Agree, s.Builders.Total, s.SimViolations, s.NoiseLeft)
		if b := s.Build; b != nil {
			fmt.Fprintf(w, "  build: hidden tests %d of %d, tasks %d, merged %d, escalations %d, repairs %d\n", b.HiddenPass, b.HiddenTotal, b.Tasks, b.Merged, b.Escalations, b.Repairs)
		}
		fmt.Fprintf(w, "  bench cost $%.4f (person %.4f, judge %.4f, builders %.4f, build %.4f); the Context creator's own sessions: $%.4f\n",
			s.BenchCostUSD.Total(), s.BenchCostUSD.Person, s.BenchCostUSD.Judge, s.BenchCostUSD.Builders, s.BenchCostUSD.Build, o.SubjectCostUSD)
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *cli) matrix(args []string) int {
	var scenarios, subjects, personas, turns string
	fs := c.flags("matrix")
	fs.StringVar(&scenarios, "scenarios-list", "", "comma-separated scenario ids, or all")
	fs.StringVar(&subjects, "subjects", "", "comma-separated: none, plain, ceiling, file:PATH")
	fs.StringVar(&personas, "personas", "", "comma-separated persona ids, or all")
	fs.StringVar(&turns, "turns", "", "comma-separated --max-turns values (default: each scenario's own)")
	fs.IntVar(&c.jobs, "jobs", 1, "runs in parallel")
	c.addShared(fs)
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	if c.scenariosDir == "" || scenarios == "" || subjects == "" {
		fmt.Fprintln(c.stderr, "factorybench context matrix: --scenarios, --scenarios-list and --subjects are required")
		return 2
	}
	env := c.env(c.scenariosDir)
	var sc []string
	if scenarios == "all" {
		ids, err := scenario.AllIDs(env.ScenariosDir)
		if err != nil {
			return c.fail(err)
		}
		sc = ids
	} else {
		sc = splitList(scenarios)
	}
	ps := splitList(personas)
	if personas == "all" {
		ps = prompts.Personas
	}
	subs := splitList(subjects)
	if slices.Contains(ps, scenario.HumanPerson) {
		fmt.Fprintln(c.stderr, "factorybench context matrix: a human run is interactive; use `factorybench context run --person human`")
		return 2
	}
	var turnList []int
	for _, t := range splitList(turns) {
		n, err := strconv.Atoi(t)
		if err != nil || n < 1 {
			fmt.Fprintf(c.stderr, "factorybench context matrix: bad --turns value %q\n", t)
			return 2
		}
		turnList = append(turnList, n)
	}
	if len(turnList) == 0 {
		turnList = []int{0}
	}
	if len(sc) == 0 || len(subs) == 0 {
		fmt.Fprintln(c.stderr, "factorybench context matrix: nothing to run")
		return 2
	}

	var runs []bench.Options
	for _, s := range sc {
		for _, sub := range subs {
			if sub != "plain" && !strings.HasPrefix(sub, "file:") { // none and ceiling: one run per scenario
				o := c.opts
				o.Scenario, o.Subject, o.Person = s, sub, ""
				runs = append(runs, o)
				continue
			}
			if len(ps) == 0 {
				fmt.Fprintf(c.stderr, "factorybench context matrix: --personas is required for subject %s\n", sub)
				return 2
			}
			for _, p := range ps {
				for _, t := range turnList {
					o := c.opts
					o.Scenario, o.Subject, o.Person, o.MaxTurns = s, sub, p, t
					runs = append(runs, o)
				}
			}
		}
	}
	// check every run before starting any
	prepared := make([]*bench.Prepared, len(runs))
	for i, o := range runs {
		p, err := env.Prepare(o)
		if err != nil {
			return c.fail(fmt.Errorf("run %d (%s, %s, %s): %v", i+1, o.Scenario, o.Subject, o.Person, err))
		}
		prepared[i] = p
	}
	if !c.opts.Yes {
		fmt.Fprintf(c.stdout, "plan (nothing is called without --yes): %d runs\n", len(runs))
		total := 0
		for i, p := range prepared {
			n := 0
			for _, sp := range p.Sessions() {
				n += sp.Max
			}
			total += n
			fmt.Fprintf(c.stdout, "  %2d. %s x %s x %s, %d turns max: up to %d sessions\n", i+1, p.Scenario.ID, p.Subject.Name, p.PersonaID, p.MaxTurns, n)
		}
		fmt.Fprintf(c.stdout, "cap       $%g per session; at most %d sessions: $%.2f [INFERENCE: a ceiling from the cap, not an estimate of the cost]\n",
			c.opts.MaxCost, total, float64(total)*c.opts.MaxCost)
		fmt.Fprintln(c.stdout, "add --yes to start them")
		return 0
	}

	outcomes := make([]*bench.Outcome, len(runs))
	errs := make([]error, len(runs))
	sem := make(chan struct{}, max(c.jobs, 1))
	var wg sync.WaitGroup
	for i := range runs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[i], errs[i] = env.Run(c.ctx, runs[i])
		}()
	}
	wg.Wait()
	bad := 0
	for i, o := range outcomes {
		switch {
		case errs[i] != nil:
			bad++
			fmt.Fprintf(c.stderr, "run %d (%s x %s x %s): %v\n", i+1, runs[i].Scenario, runs[i].Subject, runs[i].Person, errs[i])
		case o != nil:
			printOutcome(c.stdout, o)
			if o.Status != bench.StatusCompleted {
				bad++
			}
		}
	}
	fmt.Fprintf(c.stdout, "%d runs, %d not completed\n", len(runs), bad)
	if bad > 0 {
		return 1
	}
	return 0
}

func (c *cli) retag(args []string) int {
	var run, model string
	var maxCost float64
	var yes bool
	fs := c.flags("retag")
	fs.StringVar(&run, "run", "", "run directory (under --out) of a simulated run")
	fs.StringVar(&model, "tagger-model", bench.DefaultJudgeModel, "model of the tagger")
	fs.Float64Var(&maxCost, "max-cost", bench.DefaultMaxCost, "USD cap per model session")
	fs.BoolVar(&yes, "yes", false, "really call the model")
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	if run == "" {
		fmt.Fprintln(c.stderr, "factorybench context retag: --run is required")
		return 2
	}
	out, err := c.env("").Retag(c.ctx, run, model, maxCost, yes)
	if err != nil {
		return c.fail(err)
	}
	if out == nil {
		return 0
	}
	a := out.Agreement
	fmt.Fprintf(c.stdout, "run %s: tag agreement %.2f over %d person turns (revealed jaccard %.2f, frustration within 1: %.2f)\n  written to %s\n",
		out.Run, a.Overall, a.Turns, a.RevealedJaccard, a.FrustrationWithin1, filepath.Join(run, "retag.json"))
	return 0
}

func (c *cli) replay(args []string) int {
	var o bench.ReplayOptions
	fs := c.flags("replay")
	fs.StringVar(&c.scenariosDir, "scenarios", "", "directory of scenarios; calibration/<id>.json is looked up next to it")
	fs.StringVar(&o.Calibration, "calibration", "", "calibration id (calibration/<id>.json) or a path to the file")
	fs.StringVar(&o.Person, "person-model", bench.DefaultPersonModel, "model of the simulated person")
	fs.StringVar(&o.Judge, "judge-model", bench.DefaultJudgeModel, "model of the judge")
	fs.StringVar(&o.History, "history", "sim", "sim: the person sees its own earlier answers; real: the real ones")
	fs.Float64Var(&o.MaxCost, "max-cost", bench.DefaultMaxCost, "USD cap per model session")
	fs.StringVar(&o.Out, "out", bench.DefaultOut, "directory the replay goes to")
	fs.BoolVar(&o.Yes, "yes", false, "really call the models")
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	if o.Calibration == "" || c.scenariosDir == "" {
		fmt.Fprintln(c.stderr, "factorybench context replay: --scenarios and --calibration are required")
		return 2
	}
	res, dir, err := c.env(c.scenariosDir).Replay(c.ctx, o)
	if err != nil {
		return c.fail(err)
	}
	if res == nil {
		return 0
	}
	printReplay(c.stdout, res, dir)
	if res.Status != "ok" {
		return 1
	}
	return 0
}

func printReplay(w io.Writer, res *bench.CalibrationResult, dir string) {
	fmt.Fprintf(w, "%s  %s\n  status %s", res.Calibration, filepath.Join(dir, "calibration.json"), res.Status)
	if res.Note != "" {
		fmt.Fprintf(w, " (%s)", res.Note)
	}
	fmt.Fprintf(w, "\n  agreement %.2f over %d questions: same %d, partial %d, different %d; don't-know match %.2f; length ratio mean %.2f median %.2f; violations %d\n",
		res.Agreement, res.Questions, res.Same, res.Partial, res.Different, res.DontKnowMatchRate, res.LengthRatioMean, res.LengthRatioMedian, res.SimViolations)
	for _, t := range res.Turns {
		fmt.Fprintf(w, "  %2d. %-9s real: %s\n                  sim:  %s\n", t.Turn, t.Verdict, oneLine(t.RealAnswer), oneLine(t.SimAnswer))
	}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 100 {
		s = s[:100] + "..."
	}
	return s
}

func (c *cli) lint(args []string) int {
	fs := c.flags("lint")
	fs.StringVar(&c.scenariosDir, "scenarios", "", "directory of scenarios")
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	if c.scenariosDir == "" {
		fmt.Fprintln(c.stderr, "factorybench context lint: --scenarios is required")
		return 2
	}
	d := lint.Dirs{Scenarios: c.scenariosDir, Calibration: c.env(c.scenariosDir).CalibrationDir}
	r := lint.Run(d)
	for _, w := range r.Warnings {
		fmt.Fprintln(c.stdout, "warning:", w)
	}
	for _, e := range r.Errors {
		fmt.Fprintln(c.stdout, "error:", e)
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(c.stdout, "lint: %d errors, %d warnings\n", len(r.Errors), len(r.Warnings))
		return 1
	}
	ids, _ := scenario.AllIDs(d.Scenarios)
	fmt.Fprintf(c.stdout, "lint ok: %d scenarios, %d warnings\n", len(ids), len(r.Warnings))
	return 0
}

func (c *cli) dryRun(args []string) int {
	fs := c.flags("dry-run")
	keep := fs.Bool("keep", false, "keep the temporary directory and print its path")
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	exe, err := os.Executable()
	if err != nil {
		return c.fail(err)
	}
	if err := drycheck.Run(drycheck.Config{Exe: exe, Stdout: c.stdout, Keep: *keep, Run: Run}); err != nil {
		fmt.Fprintln(c.stderr, "dry-run FAILED:", err)
		return 1
	}
	return 0
}
