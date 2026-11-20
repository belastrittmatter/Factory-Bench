// Package prompts holds the prompt of every model role, embedded in the binary, and fills them in.
//
// The system prompts that are not files the user supplies (person, tagger, judge, builder, comparison,
// replay judge) are the *-system.md files; each message is the matching *-turn.md template. The sections of a
// message are wrapped in XML-style tags (<truth>, <transcript>, ...) so a reader, a model or the fake claude,
// finds them; Section extracts one.
package prompts

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed *.md personas/*.md subjects/*.md
var files embed.FS

var Personas = []string{"decided-technical", "decided-nontechnical", "unsure", "talkative"}

func Persona(id string) (string, error) {
	b, err := files.ReadFile("personas/" + id + ".md")
	if err != nil {
		return "", fmt.Errorf("persona %q is not one of %s, human", id, strings.Join(Personas, ", "))
	}
	return string(b), nil
}

func Plain() string {
	b, err := files.ReadFile("subjects/plain.md")
	if err != nil {
		panic("prompts: no plain subject")
	}
	return string(b)
}

// Roles of the model sessions. The system prompt of each starts with `ROLE: <role>`.
const (
	RoleSubject     = "subject"
	RolePerson      = "person"
	RoleTagger      = "tagger"
	RoleJudge       = "judge"
	RoleBuilder     = "builder"
	RoleComparison  = "comparison"
	RoleReplayJudge = "replay-judge"
)

func System(name string) string {
	b, err := files.ReadFile(name + "-system.md")
	if err != nil {
		panic("prompts: no system prompt " + name)
	}
	return string(b)
}

var tmpl = template.Must(template.New("").ParseFS(files, "*-turn.md", "person-system.md"))

// Render fills the template `<name>.md` (for example "judge-turn" or "person-system") with data.
func Render(name string, data any) (string, error) {
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, name+".md", data); err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}
	return b.String(), nil
}

// Section returns the text between <tag> and </tag> in a message, trimmed of its surrounding newlines.
func Section(text, tag string) (string, bool) {
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(text, open)
	if i < 0 {
		return "", false
	}
	rest := text[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return "", false
	}
	return strings.Trim(rest[:j], "\n"), true
}

func RepairNote(err error) string {
	if err == nil {
		return ""
	}
	return "\nYour previous reply was rejected: " + err.Error() + "\nFix exactly that and answer again."
}

type PersonSystem struct{ PersonaID, PersonaText string }

type PersonTurn struct {
	Truth, Attachments, SubjectFiles, Transcript, LastMove, LastMessage, Repair string
}

type SubjectTurn struct {
	FirstRun                                                     bool
	Brief, Attachments, Project, LivingSpecs, Transcript, Repair string
	TurnsLeft                                                    int
}

type TaggerTurn struct {
	Truth, Attachments, SubjectFiles, Transcript, PersonTurns, Repair string
}

type JudgeTurn struct {
	Truth, Change, Requirements, Transcript, Project, LivingSpecs string
	ItemIDs, TrapIDs, NoiseIDs, Repair                            string
}

type BuilderTurn struct{ Change, Requirements, LivingSpecs, Repair string }

type CompareTurn struct{ Change, Requirements, Writers, Repair string }

type ReplayJudgeTurn struct {
	Pairs, Repair string
	N             int
}
