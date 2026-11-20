package bench

import (
	"context"
	"fmt"
	"strings"

	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
)

// PersonTurns lists the turn numbers whose person reply carries tags (every turn after the brief that has one).
func PersonTurns(turns []protocol.Turn) []int {
	var out []int
	for _, t := range turns {
		if t.Turn > 0 && t.Person != nil && t.Subject != nil {
			out = append(out, t.Turn)
		}
	}
	return out
}

// TagTranscript has the tagger fill the person-protocol tags of every person turn of the transcript.
func TagTranscript(ctx context.Context, c *Caller, bucket, model string, s *scenario.Scenario, turns []protocol.Turn) (map[int]protocol.PersonTags, error) {
	want := PersonTurns(turns)
	out := map[int]protocol.PersonTags{}
	if len(want) == 0 {
		return out, nil
	}
	nums := make([]string, len(want))
	for i, n := range want {
		nums[i] = fmt.Sprint(n)
	}
	var reply protocol.TaggerReply
	req := claude.Request{Role: prompts.RoleTagger, Model: model, System: prompts.System("tagger"), Schema: protocol.TaggerSchema}
	err := c.Ask(ctx, bucket, req,
		func(repair error) (string, error) {
			return prompts.Render("tagger-turn", prompts.TaggerTurn{
				Truth: string(s.TruthRaw), Attachments: fileBlock(s.Attachments, "attachments/"),
				SubjectFiles: subjectFiles(s), Transcript: protocol.RenderTranscript(turns, protocol.ViewFull),
				PersonTurns: strings.Join(nums, ", "), Repair: prompts.RepairNote(repair),
			})
		},
		func(raw []byte) error {
			r, err := protocol.ParseTagger(raw, want)
			if err != nil {
				return err
			}
			for _, t := range r.Turns {
				for _, id := range t.Revealed {
					if !s.Truth.Known(id) {
						return fmt.Errorf("turn %d: revealed id %q is not in the truth", t.Turn, id)
					}
				}
			}
			reply = r
			return nil
		})
	if err != nil {
		return nil, err
	}
	for _, t := range reply.Turns {
		out[t.Turn] = t.PersonTags
	}
	return out, nil
}

// ApplyTags writes tagger tags into the turns of a transcript: first-time reveals only, no noise ids, and no
// simulator violations (a real person cannot break the simulator's rules).
func ApplyTags(s *scenario.Scenario, turns []protocol.Turn, tags map[int]protocol.PersonTags) error {
	seen := map[string]bool{}
	for i := range turns {
		t := &turns[i]
		tg, ok := tags[t.Turn]
		if !ok || t.Person == nil || t.Subject == nil {
			continue
		}
		if _, err := CheckPerson(&s.Truth, seen, t.Subject.Move, &tg); err != nil {
			return err
		}
		t.Person.PersonTags = tg
	}
	return nil
}
