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
	"strings"

	"factorybench/internal/contextbench/bench"
	"factorybench/internal/contextbench/prompts"
)

const usage = `usage:
  factorybench context run --scenarios DIR --scenario ID --subject none|plain|ceiling|file:PATH --person PERSONA [flags]

flags: --max-turns N --builders K --subject-model M --person-model M --judge-model M --builder-model M
       --max-cost USD --out DIR --no-text --yes
events go to FACTORYBENCH_URL with FACTORYBENCH_TOKEN (like 'factorybench emit'); FACTORYBENCH_CLAUDE names the claude program.
Everything that calls a model needs --yes; without it the command prints its plan and exits 0.
`

// Main runs the `context` subcommands (args are what follows "context") and returns the process exit code.
func Main(args []string) int { return Run(args, os.Stdout, os.Stderr) }

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if args[0] != "run" {
		fmt.Fprintf(stderr, "factorybench context: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	return run(args[1:], stdout, stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	var scenarios string
	var o bench.Options
	m := bench.DefaultModels()
	fs := flag.NewFlagSet("factorybench context run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&scenarios, "scenarios", "", "directory of scenarios (one directory each)")
	fs.StringVar(&o.Scenario, "scenario", "", "scenario id (under --scenarios) or a scenario directory")
	fs.StringVar(&o.Subject, "subject", "", "none, plain, ceiling or file:PATH")
	fs.StringVar(&o.Person, "person", "", strings.Join(prompts.Personas, ", "))
	fs.IntVar(&o.MaxTurns, "max-turns", 0, "cap on the Context creator's turns (default: the scenario's max_turns)")
	fs.IntVar(&o.Builders, "builders", bench.DefaultBuilders, "number of builder sessions K (gate 4)")
	fs.StringVar(&o.Models.Subject, "subject-model", m.Subject, "model of the subject")
	fs.StringVar(&o.Models.Person, "person-model", m.Person, "model of the simulated person (must differ from the subject's)")
	fs.StringVar(&o.Models.Judge, "judge-model", m.Judge, "model of the judge and the comparison")
	fs.StringVar(&o.Models.Builder, "builder-model", m.Builder, "model of the builders")
	fs.Float64Var(&o.MaxCost, "max-cost", bench.DefaultMaxCost, "USD cap per model session (claude --max-budget-usd)")
	fs.StringVar(&o.Out, "out", bench.DefaultOut, "directory for the local evidence of the run")
	fs.BoolVar(&o.NoText, "no-text", false, "leave the text of the turns out of the context.turn events")
	fs.BoolVar(&o.Yes, "yes", false, "really call the models (without it: print the plan and exit)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return 2
	}
	if scenarios == "" || o.Scenario == "" || o.Subject == "" {
		fmt.Fprintf(stderr, "%s: --scenarios, --scenario and --subject are required\n", fs.Name())
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	context.AfterFunc(ctx, stop) // after the first Ctrl-C, a second one ends the process
	out, err := bench.NewEnv(scenarios, stdout, stderr).Run(ctx, o)
	if err != nil {
		fmt.Fprintln(stderr, "factorybench context:", err)
		return 1
	}
	if out == nil {
		return 0
	}
	printOutcome(stdout, out)
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
	for _, g := range []string{bench.Gate1Frame, bench.Gate2Change, bench.Gate3Testable, bench.Gate4Agreement} {
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
		fmt.Fprintf(w, "  bench cost $%.4f (person %.4f, judge %.4f, builders %.4f); the Context creator's own sessions: $%.4f\n",
			s.BenchCostUSD.Person+s.BenchCostUSD.Judge+s.BenchCostUSD.Builders, s.BenchCostUSD.Person, s.BenchCostUSD.Judge, s.BenchCostUSD.Builders, o.SubjectCostUSD)
	}
}
