package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"factorybench/internal/contextbench/change"
	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
)

type scoreInput struct {
	S         *scenario.Scenario
	Folder    *change.Folder
	Turns     []protocol.Turn
	Talked    bool
	JudgeMdl  string
	BuilderMd string
	Builders  int
	Caller    *Caller
}

type scoreOutput struct {
	Gates        change.Gates
	Gate2        bool
	Gate4        *bool
	Gate4Reason  string
	Judge        protocol.Judge
	Builders     *buildersFile
	Compare      *protocol.CompareReply
	Requirements int
}

type buildersFile struct {
	Skipped    string                  `json:"skipped,omitempty"`
	Writers    []protocol.BuilderReply `json:"writers,omitempty"`
	Comparison *protocol.CompareReply  `json:"comparison,omitempty"`
}

func scoreRun(ctx context.Context, in scoreInput) (scoreOutput, error) {
	var out scoreOutput

	g := change.Check(in.Folder, in.S.FirstRun)
	out.Gates, out.Gate2 = g, g.Gate2
	reqs := in.Folder.Requirements()
	reqIDs := change.IDs(reqs)
	out.Requirements = len(reqs)

	transcript := "(there was no conversation: the subject never talked to anyone)\n"
	if in.Talked {
		transcript = protocol.RenderTranscript(in.Turns, protocol.ViewFull)
	}
	var judge protocol.Judge
	req := claude.Request{Role: prompts.RoleJudge, Model: in.JudgeMdl, System: prompts.System("judge"), Schema: protocol.JudgeSchema}
	err := in.Caller.Ask(ctx, BucketJudge, req,
		func(repair error) (string, error) {
			return prompts.Render("judge-turn", prompts.JudgeTurn{
				Truth: string(in.S.TruthRaw), Change: in.Folder.Render(), Requirements: change.Index(reqs),
				Transcript: transcript, Project: projectText(in.S),
				LivingSpecs: fileBlock(in.S.LivingSpecs, "living-specs/"),
				ItemIDs:     strings.Join(in.S.Truth.ItemIDs(), ", "), TrapIDs: strings.Join(in.S.Truth.TrapIDs(), ", "),
				NoiseIDs: strings.Join(in.S.Truth.NoiseIDs(), ", "), Repair: prompts.RepairNote(repair),
			})
		},
		func(raw []byte) error {
			j, err := protocol.ParseJudge(raw, in.S.Truth.ItemIDs(), in.S.Truth.TrapIDs(), in.S.Truth.NoiseIDs(), reqIDs)
			judge = j
			return err
		})
	if err != nil {
		return out, err
	}
	out.Judge = judge
	if len(judge.Contradictions) > 0 {
		out.Gate2 = false
		out.Gates.Failures = append(out.Gates.Failures, "gate 2: the judge found requirements that contradict a living spec or a hard limit (judge.json)")
	}

	if len(reqs) == 0 {
		g4 := in.Folder.SkipSpecs()
		out.Gate4 = &g4
		if !g4 {
			out.Gate4Reason = "the change folder has no requirement for builders to write scenarios for"
		}
		out.Builders = &buildersFile{Skipped: "the change folder has no requirement for builders to write scenarios for"}
		return out, nil
	}
	bf, err := runBuilders(ctx, in, reqs)
	out.Builders = bf
	if err != nil {
		return out, err
	}
	out.Compare = bf.Comparison
	var disagree []string
	for _, r := range bf.Comparison.Requirements {
		if r.Verdict != protocol.Agree {
			disagree = append(disagree, r.Requirement)
		}
	}
	g4 := len(disagree) == 0
	out.Gate4 = &g4
	if !g4 {
		out.Gate4Reason = "the builders disagree on " + strings.Join(disagree, ", ")
	}
	return out, nil
}

func runBuilders(ctx context.Context, in scoreInput, reqs []change.Requirement) (*buildersFile, error) {
	reqIDs := change.IDs(reqs)
	k := max(in.Builders, 1)
	bf := &buildersFile{Writers: make([]protocol.BuilderReply, k)}
	errs := make([]error, k)
	var wg sync.WaitGroup
	for i := range k {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := claude.Request{Role: prompts.RoleBuilder, Model: in.BuilderMd, System: prompts.System("builder"), Schema: protocol.BuilderSchema}
			errs[i] = in.Caller.Ask(ctx, BucketBuilders, req,
				func(repair error) (string, error) {
					return prompts.Render("builder-turn", prompts.BuilderTurn{
						Change: in.Folder.Render(), Requirements: change.Index(reqs),
						LivingSpecs: fileBlock(in.S.LivingSpecs, "living-specs/"), Repair: prompts.RepairNote(repair),
					})
				},
				func(raw []byte) error {
					r, err := protocol.ParseBuilder(raw, reqIDs)
					bf.Writers[i] = r
					return err
				})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			bf.Writers = nil
			return bf, err
		}
	}
	var writers strings.Builder
	for i, w := range bf.Writers {
		b, _ := json.MarshalIndent(w, "", "  ")
		fmt.Fprintf(&writers, "=== writer %d ===\n%s\n\n", i+1, b)
	}
	var cmp protocol.CompareReply
	req := claude.Request{Role: prompts.RoleComparison, Model: in.JudgeMdl, System: prompts.System("compare"), Schema: protocol.CompareSchema}
	err := in.Caller.Ask(ctx, BucketBuilders, req,
		func(repair error) (string, error) {
			return prompts.Render("compare-turn", prompts.CompareTurn{
				Change: in.Folder.Render(), Requirements: change.Index(reqs), Writers: writers.String(),
				Repair: prompts.RepairNote(repair),
			})
		},
		func(raw []byte) error {
			r, err := protocol.ParseCompare(raw, reqIDs)
			cmp = r
			return err
		})
	if err != nil {
		return bf, err
	}
	bf.Comparison = &cmp
	return bf, nil
}
