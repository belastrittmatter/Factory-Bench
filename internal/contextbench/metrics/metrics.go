// Package metrics turns a judge verdict and a tagged transcript into the counts of a context.score event.
// Everything here is pure; the rates are FactoryBench's report's.
package metrics

import (
	"slices"

	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
	"factorybench/internal/contract"
)

type Reveal struct {
	ID   string
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
				out = append(out, Reveal{ID: id, Move: t.Subject.Move})
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
}

// Score returns the context.score payload of a run, without its scenario, subject and persona.
func Score(in ScoreInput) contract.ContextScore {
	t, j := in.Truth, in.Judge
	sc := contract.ContextScore{
		Truth:         contract.ContextTruth{Items: int64(len(t.Items)), Traps: int64(len(t.Traps)), Noise: int64(len(t.Noise)), Latent: int64(len(t.LatentIDs()))},
		Requirements:  int64(in.Requirements),
		VagueWarnings: int64(in.VagueWarnings),
		BenchCostUSD:  in.Cost,
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
