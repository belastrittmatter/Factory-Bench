// Package metrics turns a judge verdict and a tagged transcript into the counts of a context.score event, and
// compares two taggings of the same transcript. Everything here is pure; the rates are FactoryBench's report's.
package metrics

import (
	"slices"

	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
	"factorybench/internal/contract"
)

type Reveal struct {
	ID   string
	Turn int
	Move string
}

// Reveals lists the first-time reveals of the transcript, in order. A later repeat of an id is not a reveal.
func Reveals(turns []protocol.Turn) []Reveal {
	seen := map[string]bool{}
	var out []Reveal
	for _, t := range turns {
		if t.Person == nil || t.Subject == nil {
			continue
		}
		for _, id := range t.Person.Revealed {
			if !seen[id] {
				seen[id] = true
				out = append(out, Reveal{ID: id, Turn: t.Turn, Move: t.Subject.Move})
			}
		}
	}
	return out
}

type ScoreInput struct {
	Truth         *scenario.Truth
	Judge         protocol.Judge
	Turns         []protocol.Turn
	Requirements  int
	Compare       *protocol.CompareReply // nil when no builder compared anything
	VagueWarnings int
	Cost          contract.ContextBenchCost
	Build         *contract.ContextBuild // nil when the run was not built
}

// Score returns the context.score payload of a run, without its scenario, subject and persona.
func Score(in ScoreInput) contract.ContextScore {
	t, j := in.Truth, in.Judge
	sc := contract.ContextScore{
		Truth:         contract.ContextTruth{Items: int64(len(t.Items)), Traps: int64(len(t.Traps)), Noise: int64(len(t.Noise)), Latent: int64(len(t.LatentIDs()))},
		Requirements:  int64(in.Requirements),
		VagueWarnings: int64(in.VagueWarnings),
		BenchCostUSD:  in.Cost,
		Build:         in.Build,
	}
	captured := map[string]string{}
	for _, it := range j.Items {
		captured[it.ID] = it.Captured
	}
	for _, it := range t.Items {
		if it.G4 {
			sc.Truth.G4++
		}
		switch captured[it.ID] {
		case protocol.Full:
			sc.Captured.Full++
			if it.G4 {
				sc.Captured.G4Full++
			}
		case protocol.Partial:
			sc.Captured.Partial++
			if it.G4 {
				sc.Captured.G4Partial++
			}
		}
	}
	latent := t.LatentIDs()
	for _, r := range Reveals(in.Turns) {
		if !slices.Contains(latent, r.ID) {
			continue
		}
		if sc.LatentByMove == nil {
			sc.LatentByMove = map[string]int64{}
		}
		sc.LatentByMove[r.Move]++
		if c := captured[r.ID]; c == protocol.Full || c == protocol.Partial {
			sc.Captured.Latent++
		}
	}
	for _, tr := range j.Traps {
		if tr.Raised {
			sc.Traps.Raised++
		}
		if tr.FollowedResolution {
			sc.Traps.Followed++
		}
	}
	for _, iv := range j.Invented {
		sc.Invented++
		if iv.MarkedAssumed {
			sc.AssumedMarked++
		} else {
			sc.InventedUnmarked++
		}
	}
	sc.MapTagErrors = int64(len(j.MapTagErrors))
	sc.Untraced = distinct(j.Untraced)
	sc.NoiseLeft = distinct(j.NoisePresent)
	sc.MissingSlots = int64(len(j.MissingSlots))
	sc.Contradictions = int64(len(j.Contradictions))
	if in.Compare != nil {
		for _, r := range in.Compare.Requirements {
			sc.Builders.Total++
			if r.Verdict == protocol.Agree {
				sc.Builders.Agree++
			}
		}
	}
	for _, turn := range in.Turns {
		sc.SimViolations += int64(len(turn.Violations))
	}
	return sc
}

func distinct(ids []string) int64 {
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	return int64(len(seen))
}

type TagAgreement struct {
	Turns              int                `json:"turns"`
	PerField           map[string]float64 `json:"per_field"`
	Overall            float64            `json:"overall"`
	RevealedJaccard    float64            `json:"revealed_jaccard"`
	FrustrationWithin1 float64            `json:"frustration_within_1"`
}

// Agreement compares tags a (the simulator's) with b (the tagger's) over the turns both have. Overall is the
// mean of the per-field agreements; revealed agrees when the id sets are equal, and frustration when equal.
func Agreement(a, b map[int]protocol.PersonTags) TagAgreement {
	out := TagAgreement{PerField: map[string]float64{}}
	var turns []int
	for t := range a {
		if _, ok := b[t]; ok {
			turns = append(turns, t)
		}
	}
	out.Turns = len(turns)
	if len(turns) == 0 {
		return out
	}
	count := map[string]int{}
	var jac, near float64
	for _, t := range turns {
		x, y := a[t], b[t]
		if sameSet(x.Revealed, y.Revealed) {
			count["revealed"]++
		}
		if x.DontKnow == y.DontKnow {
			count["dont_know"]++
		}
		if x.CouldNotAnswer == y.CouldNotAnswer {
			count["could_not_answer"]++
		}
		if x.AlreadySaid == y.AlreadySaid {
			count["already_said"]++
		}
		if x.InAttachments == y.InAttachments {
			count["in_attachments"]++
		}
		if x.Suggestion == y.Suggestion {
			count["suggestion"]++
		}
		if x.Frustration == y.Frustration {
			count["frustration"]++
		}
		if x.Confirmed == y.Confirmed {
			count["confirmed"]++
		}
		if d := x.Frustration - y.Frustration; d >= -1 && d <= 1 {
			near++
		}
		jac += jaccard(x.Revealed, y.Revealed)
	}
	var sum float64
	for _, f := range []string{"revealed", "dont_know", "could_not_answer", "already_said", "in_attachments", "suggestion", "frustration", "confirmed"} {
		v := float64(count[f]) / float64(len(turns))
		out.PerField[f] = v
		sum += v
	}
	out.Overall = sum / float64(len(out.PerField))
	out.RevealedJaccard = jac / float64(len(turns))
	out.FrustrationWithin1 = near / float64(len(turns))
	return out
}

func sameSet(a, b []string) bool {
	return len(a) == len(b) && jaccard(a, b) == 1
}

func jaccard(a, b []string) float64 {
	sa, sb := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		sa[x] = true
	}
	for _, x := range b {
		sb[x] = true
	}
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	inter := 0
	for x := range sa {
		if sb[x] {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}
