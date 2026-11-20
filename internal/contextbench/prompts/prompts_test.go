package prompts

import (
	"errors"
	"strings"
	"testing"
)

func TestEveryTemplateRendersAndLeavesNoPlaceholder(t *testing.T) {
	cases := map[string]any{
		"subject-turn": SubjectTurn{FirstRun: true, Brief: "b", TurnsLeft: 0},
		"person-turn":  PersonTurn{Truth: "{}"},
		"judge-turn":   JudgeTurn{},
		"builder-turn": BuilderTurn{},
		"compare-turn": CompareTurn{},
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
	for _, n := range []string{"person", "judge", "builder", "compare"} {
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
	if _, err := Persona("grumpy"); err == nil {
		t.Error("an unknown persona was accepted")
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
