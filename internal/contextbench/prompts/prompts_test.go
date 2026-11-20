package prompts

import (
	"errors"
	"strings"
	"testing"
)

func TestPersonPromptEnforcesTheSixRules(t *testing.T) {
	s, err := Render("person-system", PersonSystem{PersonaID: "unsure", PersonaText: "PERSONA TEXT HERE"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"PERSONA: unsure", "PERSONA TEXT HERE",
		"Answer only from the truth", `"I don't know" or "you decide"`,
		"LAST move is `show` or `suggest`", "`latent_hint`",
		"Vague traps stay vague until sharpened", "only then give the precise value",
		"Non-technical personas cannot answer technical questions", "`could_not_answer`",
		"A trap gets its resolution once it is raised", "Do not volunteer a resolution",
		"Never reveal that you have a truth file", "Never mention truth",
		"`revealed`", "`dont_know`", "`already_said`", "`in_attachments`", "`suggestion`", "`frustration`", "`confirmed`",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("person prompt lacks %q", want)
		}
	}
	if strings.Count(s, "\n1. **")+strings.Count(s, "\n2. **")+strings.Count(s, "\n3. **")+strings.Count(s, "\n4. **")+strings.Count(s, "\n5. **")+strings.Count(s, "\n6. **") != 6 {
		t.Error("the six rules are not numbered 1 to 6")
	}
}

func TestJudgePromptDefinesTheVerdicts(t *testing.T) {
	s := System("judge")
	for _, want := range []string{"`full`", "`partial`", "`none`", "invented", "marked_assumed", "`[said]`", "`[accepted]`", "`[assumed]`", "`[open]`",
		"untraced", "noise_present", "missing_slots", "contradictions", "followed_resolution", "raised"} {
		if !strings.Contains(s, want) {
			t.Errorf("judge prompt lacks %q", want)
		}
	}
}

func TestBuilderPromptForbidsGuessing(t *testing.T) {
	s := System("builder")
	for _, want := range []string{"only the change folder and the living specs", "Never guess", "cannot ask questions", "independently"} {
		if !strings.Contains(s, want) {
			t.Errorf("builder prompt lacks %q", want)
		}
	}
	for _, bad := range []string{"truth.json", "latent", "transcript"} {
		if strings.Contains(strings.ToLower(System("builder")+System("compare")), bad) {
			t.Errorf("a builder or comparison prompt mentions %q", bad)
		}
	}
}

func TestEveryTemplateRendersAndLeavesNoPlaceholder(t *testing.T) {
	cases := map[string]any{
		"subject-turn":      SubjectTurn{FirstRun: true, Brief: "b", TurnsLeft: 0},
		"person-turn":       PersonTurn{Truth: "{}"},
		"tagger-turn":       TaggerTurn{},
		"judge-turn":        JudgeTurn{},
		"builder-turn":      BuilderTurn{},
		"compare-turn":      CompareTurn{},
		"replay-judge-turn": ReplayJudgeTurn{N: 3},
	}
	for name, data := range cases {
		s, err := Render(name, data)
		if err != nil || strings.Contains(s, "{{") || strings.Contains(s, "<no value>") {
			t.Errorf("%s: %v", name, err)
		}
		if strings.HasPrefix(s, "-") {
			t.Errorf("%s starts with a dash", name)
		}
	}
	for _, n := range []string{"person", "tagger", "judge", "builder", "compare", "replay-judge"} {
		if strings.TrimSpace(System(n)) == "" {
			t.Errorf("%s system prompt is empty", n)
		}
	}
}

func TestSubjectTurnAnnouncesTheFinalTurn(t *testing.T) {
	last, _ := Render("subject-turn", SubjectTurn{TurnsLeft: 0})
	more, _ := Render("subject-turn", SubjectTurn{TurnsLeft: 3})
	if !strings.Contains(last, "you must finish now") || strings.Contains(more, "you must finish now") || !strings.Contains(more, "3 more turn(s)") {
		t.Error("turn announcements")
	}
	if v, ok := Section(last, "turns_left"); !ok || v != "0" {
		t.Errorf("turns_left section %q", v)
	}
}

func TestSection(t *testing.T) {
	text := "x\n<a>\none\ntwo\n</a>\n<b></b>"
	if v, ok := Section(text, "a"); !ok || v != "one\ntwo" {
		t.Errorf("%q %v", v, ok)
	}
	if v, ok := Section(text, "b"); !ok || v != "" {
		t.Errorf("%q %v", v, ok)
	}
	if _, ok := Section(text, "c"); ok {
		t.Error("missing section found")
	}
}

func TestRepairNote(t *testing.T) {
	if RepairNote(nil) != "" || !strings.Contains(RepairNote(errors.New("bad thing")), "bad thing") {
		t.Error("repair note")
	}
}

func TestThePersonasAndThePlainSubjectAreEmbedded(t *testing.T) {
	if len(Personas) != 4 {
		t.Fatalf("%v", Personas)
	}
	for _, id := range Personas {
		if txt, err := Persona(id); err != nil || len(txt) < 200 || !strings.Contains(txt, "# Persona") {
			t.Errorf("%s: %v (%d bytes)", id, err, len(txt))
		}
	}
	if _, err := Persona("human"); err == nil {
		t.Error("human has no persona file")
	}
	if _, err := Persona("../prompts/judge-system"); err == nil {
		t.Error("a persona id is not a path")
	}
	plain := Plain()
	for _, want := range []string{"only the move `ask`", "proposal.md", "map.md", "decisions.md", "project.md", "## ADDED Requirements", "`[assumed]`"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain subject lacks %q", want)
		}
	}
	for _, bad := range []string{"truth", "latent", "persona"} {
		if strings.Contains(strings.ToLower(plain), bad) {
			t.Errorf("the plain subject mentions %q", bad)
		}
	}
}
