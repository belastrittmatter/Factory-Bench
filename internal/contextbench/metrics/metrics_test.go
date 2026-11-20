package metrics

import (
	"testing"

	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
)

func truth() *scenario.Truth {
	return &scenario.Truth{
		Items: []scenario.Item{
			{ID: "A", G4: true, Latent: true},
			{ID: "B", G4: true},
			{ID: "C"},
			{ID: "D", Latent: true},
		},
		Traps: []scenario.Trap{{ID: "X1"}, {ID: "X2"}},
		Noise: []scenario.Noise{{ID: "N1"}, {ID: "N2"}, {ID: "N3"}},
	}
}

func judged() protocol.Judge {
	return protocol.Judge{
		Items: []protocol.JudgeItem{{ID: "A", Captured: "full"}, {ID: "B", Captured: "partial"}, {ID: "C", Captured: "none"}, {ID: "D", Captured: "partial"}},
		Traps: []protocol.JudgeTrap{{ID: "X1", Raised: true, Turn: 5, FollowedResolution: true}, {ID: "X2"}},
		Invented: []protocol.Invented{
			{Requirement: "R1", MarkedAssumed: true}, {Requirement: "R2"}, {Requirement: "R3"},
		},
		MapTagErrors:   []protocol.MapTagError{{Bullet: "x"}},
		Untraced:       []string{"R2", "R3", "R3"},
		NoisePresent:   []string{"N1", "N1", "N2"},
		MissingSlots:   []protocol.MissingSlot{{Requirement: "R1", Slot: 4}},
		Contradictions: []protocol.Contradiction{{Requirement: "R2"}, {Requirement: "R3"}},
	}
}

func tags(revealed []string, dk bool, sugg string, fr int) *protocol.PersonReply {
	if revealed == nil {
		revealed = []string{}
	}
	return &protocol.PersonReply{Reply: "one two three", PersonTags: protocol.PersonTags{Revealed: revealed, DontKnow: dk, Suggestion: sugg, Frustration: fr}}
}

func transcript() []protocol.Turn {
	sub := func(move string) *protocol.SubjectSaid { return &protocol.SubjectSaid{Move: move, Message: "m"} }
	return []protocol.Turn{
		{Turn: 0, Person: tags(nil, false, "none", 0)},
		{Turn: 1, Subject: sub("ask"), Person: tags([]string{"A"}, false, "none", 0), Violations: []string{"latent A revealed after move ask"}},
		{Turn: 2, Subject: sub("ask"), Person: tags(nil, true, "none", 1)},
		{Turn: 3, Subject: sub("show"), Person: tags([]string{"D"}, false, "accepted", 0)},
		{Turn: 4, Subject: sub("ask"), Person: tags(nil, true, "none", 2)},
		{Turn: 5, Subject: sub("challenge"), Person: tags([]string{"X1"}, true, "none", 3)},
		{Turn: 6, Subject: sub("suggest"), Person: tags([]string{"B", "A"}, false, "rejected", 1)},
		{Turn: 7, Subject: &protocol.SubjectSaid{Move: "mirror", Done: true}},
	}
}

func TestScoreCountsTheTruthAndTheJudgesVerdict(t *testing.T) {
	sc := Score(ScoreInput{Truth: truth(), Judge: judged(), Turns: transcript(), Requirements: 5, VagueWarnings: 4})
	if sc.Truth.Items != 4 || sc.Truth.G4 != 2 || sc.Truth.Latent != 2 || sc.Truth.Traps != 2 || sc.Truth.Noise != 3 {
		t.Errorf("truth %+v", sc.Truth)
	}
	if c := sc.Captured; c.Full != 1 || c.Partial != 2 || c.G4Full != 1 || c.G4Partial != 1 || c.Latent != 2 {
		t.Errorf("captured %+v", c)
	}
	if want := map[string]int64{"show": 1, "ask": 1}; len(sc.LatentByMove) != 2 || sc.LatentByMove["show"] != want["show"] || sc.LatentByMove["ask"] != want["ask"] {
		t.Errorf("latent_by_move %v", sc.LatentByMove)
	}
	if sc.Traps.Raised != 1 || sc.Traps.Followed != 1 {
		t.Errorf("traps %+v", sc.Traps)
	}
	if sc.Invented != 3 || sc.InventedUnmarked != 2 || sc.AssumedMarked != 1 || sc.MapTagErrors != 1 {
		t.Errorf("invented %d unmarked %d assumed_marked %d tag errors %d", sc.Invented, sc.InventedUnmarked, sc.AssumedMarked, sc.MapTagErrors)
	}
	if sc.Requirements != 5 || sc.Untraced != 2 || sc.NoiseLeft != 2 || sc.MissingSlots != 1 || sc.Contradictions != 2 || sc.VagueWarnings != 4 {
		t.Errorf("requirement counts %+v", sc)
	}
	if sc.SimViolations != 1 {
		t.Errorf("sim_violations = %d", sc.SimViolations)
	}
	if sc.Builders.Total != 0 || sc.Build != nil {
		t.Errorf("builders %+v build %v", sc.Builders, sc.Build)
	}
}

func TestLatentCapturedNeedsTheChangeToHoldIt(t *testing.T) {
	j := judged()
	j.Items[0].Captured = "none"
	if sc := Score(ScoreInput{Truth: truth(), Judge: j, Turns: transcript()}); sc.Captured.Latent != 1 {
		t.Errorf("captured.latent = %d, want 1 (a revealed latent item the change dropped is not drawn)", sc.Captured.Latent)
	}
}

func TestBuilderAgreementCounts(t *testing.T) {
	c := &protocol.CompareReply{Requirements: []protocol.CompareVerdict{
		{Requirement: "R1", Verdict: "agree"}, {Requirement: "R2", Verdict: "agree"}, {Requirement: "R3", Verdict: "agree"}, {Requirement: "R4", Verdict: "disagree"},
	}}
	sc := Score(ScoreInput{Truth: truth(), Judge: judged(), Compare: c})
	if sc.Builders.Agree != 3 || sc.Builders.Total != 4 {
		t.Errorf("builders %+v", sc.Builders)
	}
}

func TestAnEmptyTruthScoresZeroCounts(t *testing.T) {
	sc := Score(ScoreInput{Truth: &scenario.Truth{}})
	if sc.Truth.Items != 0 || sc.Captured.Full != 0 || sc.LatentByMove != nil {
		t.Errorf("%+v", sc)
	}
}

func TestRevealsAreFirstTimeOnly(t *testing.T) {
	got := Reveals(transcript())
	want := []Reveal{{"A", "ask"}, {"D", "show"}, {"X1", "challenge"}, {"B", "suggest"}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reveal %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
