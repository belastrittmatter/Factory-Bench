package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/metrics"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
)

type ReplayOptions struct {
	Calibration string // an id under calibration/, or a path to a calibration file
	History     string // "sim": the person sees its own earlier answers; "real": the real ones
	Person      string
	Judge       string
	MaxCost     float64
	Out         string
	Yes         bool
}

type ReplayTurn struct {
	Turn           int     `json:"turn"`
	Question       string  `json:"question"`
	RealAnswer     string  `json:"real_answer"`
	SimAnswer      string  `json:"sim_answer"`
	Verdict        string  `json:"verdict"`
	RealDontKnow   bool    `json:"real_dont_know"`
	SimDontKnow    bool    `json:"sim_dont_know"`
	DontKnowMatch  bool    `json:"dont_know_match"`
	LengthRatio    float64 `json:"length_ratio"`
	SimFrustration int     `json:"sim_frustration"`
	Reason         string  `json:"reason"`
}

// CalibrationResult is calibration.json.
type CalibrationResult struct {
	Calibration       string       `json:"calibration"`
	Scenario          string       `json:"scenario"`
	Persona           string       `json:"persona"`
	History           string       `json:"history"`
	Models            ReplayModels `json:"models"`
	Questions         int          `json:"questions"`
	Same              int          `json:"same"`
	Partial           int          `json:"partial"`
	Different         int          `json:"different"`
	Agreement         float64      `json:"agreement"`
	SameRate          float64      `json:"same_rate"`
	DontKnowMatchRate float64      `json:"dont_know_match_rate"`
	LengthRatioMean   float64      `json:"length_ratio_mean"`
	LengthRatioMedian float64      `json:"length_ratio_median"`
	SimViolations     int          `json:"sim_violations"`
	Status            string       `json:"status"`
	Note              string       `json:"note"`
	CostUSD           ReplayCost   `json:"cost_usd"`
	Turns             []ReplayTurn `json:"turns"`
}

type ReplayModels struct {
	Person string `json:"person"`
	Judge  string `json:"judge"`
}

type ReplayCost struct {
	Person float64 `json:"person"`
	Judge  float64 `json:"judge"`
}

// LoadCalibration finds and loads a calibration by id or path.
func (e *Env) LoadCalibration(idOrPath string) (*scenario.Calibration, error) {
	path := filepath.Join(e.CalibrationDir, idOrPath+".json")
	if _, err := os.Stat(idOrPath); err == nil && strings.HasSuffix(idOrPath, ".json") {
		path = idOrPath
	}
	return scenario.LoadCalibration(path)
}

// Replay feeds every real question of a calibration to the simulated person as an `ask` turn and has a judge
// compare each simulated answer with the real one.
func (e *Env) Replay(ctx context.Context, o ReplayOptions) (*CalibrationResult, string, error) {
	cal, err := e.LoadCalibration(o.Calibration)
	if err != nil {
		return nil, "", err
	}
	if len(cal.Turns) == 0 {
		return nil, "", fmt.Errorf("calibration %s has no turns", cal.ID)
	}
	if o.History != "sim" && o.History != "real" {
		return nil, "", fmt.Errorf("--history must be sim or real")
	}
	dir, err := scenario.Resolve(e.ScenariosDir, cal.Scenario)
	if err != nil {
		return nil, "", err
	}
	s, err := scenario.Load(dir)
	if err != nil {
		return nil, "", err
	}
	text, err := prompts.Persona(cal.Persona)
	if err != nil {
		return nil, "", err
	}
	if !o.Yes {
		fmt.Fprintln(e.Stdout, "plan (nothing is called without --yes):")
		fmt.Fprintf(e.Stdout, "calibration %s: scenario %s, persona %s, %d real questions\n", cal.ID, cal.Scenario, cal.Persona, len(cal.Turns))
		fmt.Fprintf(e.Stdout, "  starts  person     %d x %s (one per question, history: %s)\n", len(cal.Turns), o.Person, o.History)
		fmt.Fprintf(e.Stdout, "  starts  judge      1 x %s\n", o.Judge)
		total := (len(cal.Turns) + 1) * TriesPerAsk
		fmt.Fprintf(e.Stdout, "cap       $%g per session; at most %d sessions, repairs included: $%.2f [INFERENCE: a ceiling from the cap, not an estimate of the cost]\n", o.MaxCost, total, float64(total)*o.MaxCost)
		fmt.Fprintln(e.Stdout, "add --yes to start it")
		return nil, "", nil
	}

	meter := &Meter{}
	caller := &Caller{Runner: e.runner(), Meter: meter, MaxCost: o.MaxCost}
	person, err := newSimPerson(caller, o.Person, s, cal.Persona, text)
	if err != nil {
		return nil, "", err
	}
	out := &CalibrationResult{Calibration: cal.ID, Scenario: s.ID, Persona: cal.Persona, History: o.History,
		Models: ReplayModels{Person: o.Person, Judge: o.Judge}, Questions: len(cal.Turns), Status: "ok"}
	turns := []protocol.Turn{{Turn: 0, Person: &protocol.PersonReply{Reply: BriefMessage(s), PersonTags: protocol.PersonTags{Revealed: []string{}, Suggestion: "none"}}}}
	seen := map[string]bool{}
	var sim []ReplayTurn
	var runErr error
	for i, ct := range cal.Turns {
		n := i + 1
		pr, err := person.reply(ctx, turns, "ask", ct.Question)
		if err != nil {
			runErr = err
			break
		}
		viol, err := CheckPerson(&s.Truth, seen, "ask", &pr.PersonTags)
		if err != nil {
			runErr = err
			break
		}
		out.SimViolations += len(viol)
		sim = append(sim, ReplayTurn{Turn: n, Question: ct.Question, RealAnswer: ct.Answer, SimAnswer: pr.Reply, SimFrustration: pr.Frustration})
		recorded := pr
		if o.History == "real" {
			recorded.Reply = ct.Answer
		}
		turns = append(turns, protocol.Turn{Turn: n, Subject: &protocol.SubjectSaid{Move: "ask", Message: ct.Question}, Person: &recorded, Violations: viol})
	}
	if runErr == nil {
		runErr = e.judgeReplay(ctx, caller, o.Judge, sim)
	}
	out.CostUSD = ReplayCost{Person: meter.Get(BucketPerson), Judge: meter.Get(BucketJudge)}
	if runErr != nil {
		if !IsInvalid(runErr) {
			return nil, "", runErr
		}
		out.Status, out.Note = "invalid", runErr.Error()
	} else {
		summarize(out, sim)
	}
	out.Turns = sim

	id := time.Now().UTC().Format("20060102T150405") + "-calibration-" + slugRe.ReplaceAllString(cal.ID, "-")
	rd, _, err := uniqueDir(o.Out, id)
	if err != nil {
		return nil, "", err
	}
	if err := WriteTranscript(filepath.Join(rd, "transcript.jsonl"), turns); err != nil {
		return nil, "", err
	}
	if err := writeJSON(filepath.Join(rd, "calibration.json"), out); err != nil {
		return nil, "", err
	}
	return out, rd, nil
}

func (e *Env) judgeReplay(ctx context.Context, caller *Caller, model string, sim []ReplayTurn) error {
	type pair struct {
		Turn      int    `json:"turn"`
		Question  string `json:"question"`
		RealAns   string `json:"real_answer"`
		SimAnswer string `json:"simulated_answer"`
	}
	pairs := make([]pair, len(sim))
	for i, t := range sim {
		pairs[i] = pair{t.Turn, t.Question, t.RealAnswer, t.SimAnswer}
	}
	pj, _ := json.MarshalIndent(pairs, "", "  ")
	var reply protocol.ReplayJudgeReply
	req := claude.Request{Role: prompts.RoleReplayJudge, Model: model, System: prompts.System("replay-judge"), Schema: protocol.ReplaySchema}
	err := caller.Ask(ctx, BucketJudge, req,
		func(repair error) (string, error) {
			return prompts.Render("replay-judge-turn", prompts.ReplayJudgeTurn{Pairs: string(pj), N: len(sim), Repair: prompts.RepairNote(repair)})
		},
		func(raw []byte) error {
			r, err := protocol.ParseReplayJudge(raw, len(sim))
			reply = r
			return err
		})
	if err != nil {
		return err
	}
	for _, v := range reply.Turns {
		t := &sim[v.Turn-1]
		t.Verdict, t.RealDontKnow, t.SimDontKnow, t.Reason = v.Verdict, v.RealDontKnow, v.SimDontKnow, v.Reason
		t.DontKnowMatch = v.RealDontKnow == v.SimDontKnow
		t.LengthRatio = LengthRatio(t.SimAnswer, t.RealAnswer)
	}
	return nil
}

// LengthRatio is the words of the simulated answer over the words of the real one (0 when the real one is empty).
func LengthRatio(sim, real string) float64 {
	r := protocol.Words(real)
	if r == 0 {
		return 0
	}
	return float64(protocol.Words(sim)) / float64(r)
}

func summarize(out *CalibrationResult, sim []ReplayTurn) {
	var dk int
	var ratios []float64
	for _, t := range sim {
		switch t.Verdict {
		case protocol.Same:
			out.Same++
		case protocol.PartialV:
			out.Partial++
		default:
			out.Different++
		}
		if t.DontKnowMatch {
			dk++
		}
		ratios = append(ratios, t.LengthRatio)
	}
	n := float64(len(sim))
	out.Agreement = (float64(out.Same) + 0.5*float64(out.Partial)) / n
	out.SameRate = float64(out.Same) / n
	out.DontKnowMatchRate = float64(dk) / n
	var sum float64
	for _, r := range ratios {
		sum += r
	}
	out.LengthRatioMean = sum / n
	sort.Float64s(ratios)
	if m := len(ratios); m%2 == 1 {
		out.LengthRatioMedian = ratios[m/2]
	} else {
		out.LengthRatioMedian = (ratios[m/2-1] + ratios[m/2]) / 2
	}
}

// RetagFile is retag.json.
type RetagFile struct {
	Run         string               `json:"run"`
	TaggerModel string               `json:"tagger_model"`
	CostUSD     float64              `json:"cost_usd"`
	Agreement   metrics.TagAgreement `json:"agreement"`
	Turns       []RetagTurn          `json:"turns"`
}

type RetagTurn struct {
	Turn      int                 `json:"turn"`
	Simulator protocol.PersonTags `json:"simulator"`
	Tagger    protocol.PersonTags `json:"tagger"`
}

// Retag has the tagger re-tag a simulated run's transcript and writes retag.json with the agreement between
// the tagger's tags and the simulator's own.
func (e *Env) Retag(ctx context.Context, runDir, taggerModel string, maxCost float64, yes bool) (*RetagFile, error) {
	var info RunInfo
	b, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, fmt.Errorf("%s/run.json: %v", runDir, err)
	}
	if info.ScenarioDir == "" {
		return nil, fmt.Errorf("%s/run.json names no scenario_dir", runDir)
	}
	if info.Persona == scenario.HumanPerson {
		return nil, fmt.Errorf("run %s had a real person: its tags are already the tagger's, so there is nothing to compare them with", info.Run)
	}
	if !slices.Contains(prompts.Personas, info.Persona) {
		return nil, fmt.Errorf("run %s has no simulated person (persona %q)", info.Run, info.Persona)
	}
	s, err := scenario.Load(info.ScenarioDir)
	if err != nil {
		return nil, err
	}
	turns, err := ReadTranscript(filepath.Join(runDir, "transcript.jsonl"))
	if err != nil {
		return nil, err
	}
	want := PersonTurns(turns)
	if !yes {
		fmt.Fprintln(e.Stdout, "plan (nothing is called without --yes):")
		fmt.Fprintf(e.Stdout, "re-tag run %s (%d person turns)\n  starts  tagger     1 x %s\ncap       $%g per session; at most %d sessions, repairs included: $%.2f [INFERENCE: a ceiling from the cap, not an estimate of the cost]\nadd --yes to start it\n",
			info.Run, len(want), taggerModel, maxCost, TriesPerAsk, float64(TriesPerAsk)*maxCost)
		return nil, nil
	}
	meter := &Meter{}
	caller := &Caller{Runner: e.runner(), Meter: meter, MaxCost: maxCost}
	tags, err := TagTranscript(ctx, caller, BucketPerson, taggerModel, s, turns)
	if err != nil {
		return nil, err
	}
	tagged := make([]protocol.Turn, len(turns))
	copy(tagged, turns)
	for i := range tagged {
		if tagged[i].Person != nil {
			p := *tagged[i].Person
			tagged[i].Person = &p
		}
	}
	if err := ApplyTags(s, tagged, tags); err != nil {
		return nil, err
	}
	simTags, taggerTags := map[int]protocol.PersonTags{}, map[int]protocol.PersonTags{}
	out := &RetagFile{Run: info.Run, TaggerModel: taggerModel, CostUSD: meter.Get(BucketPerson)}
	for i, t := range turns {
		if !slices.Contains(want, t.Turn) {
			continue
		}
		simTags[t.Turn], taggerTags[t.Turn] = t.Person.PersonTags, tagged[i].Person.PersonTags
		out.Turns = append(out.Turns, RetagTurn{Turn: t.Turn, Simulator: t.Person.PersonTags, Tagger: tagged[i].Person.PersonTags})
	}
	out.Agreement = metrics.Agreement(simTags, taggerTags)
	if err := writeJSON(filepath.Join(runDir, "retag.json"), out); err != nil {
		return nil, err
	}
	return out, nil
}
