package bench

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"factorybench/internal/contextbench/change"
	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
)

const maxFileShown = 64 << 10

func fileBlock(files []scenario.File, dir string) string {
	if len(files) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString("=== " + dir + f.Path + " ===\n")
		txt, ok := f.Text()
		switch {
		case !ok:
			fmt.Fprintf(&b, "(binary file, %d bytes, not shown)\n\n", len(f.Data))
		case len(txt) > maxFileShown:
			b.WriteString(txt[:maxFileShown] + "\n(cut at 64 KiB)\n\n")
		default:
			b.WriteString(strings.TrimRight(txt, "\n") + "\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func projectText(s *scenario.Scenario) string {
	if strings.TrimSpace(s.Project) == "" {
		return "(none: there is no project frame yet)"
	}
	return strings.TrimRight(s.Project, "\n")
}

// BriefMessage is the person's first message: brief.md and the names of the attachments.
func BriefMessage(s *scenario.Scenario) string {
	msg := strings.TrimRight(s.Brief, "\n")
	if len(s.Attachments) > 0 {
		names := make([]string, len(s.Attachments))
		for i, a := range s.Attachments {
			names[i] = a.Path
		}
		msg += "\n\nAttachments: " + strings.Join(names, ", ")
	}
	return msg
}

// CheckPerson applies the harness's checks to a person reply. seen holds the ids revealed so far and is
// updated. Unknown ids (not in truth.json at all) are an error: the run is invalid. Noise ids are dropped (the
// tag lists items and traps only), ids revealed before are dropped, a suggestion tag after a move other than
// show or suggest becomes none. A latent item revealed after a move other than show or suggest is returned as a
// simulator violation (the reveal still counts).
func CheckPerson(truth *scenario.Truth, seen map[string]bool, move string, tags *protocol.PersonTags) (violations []string, err error) {
	var kept []string
	for _, id := range tags.Revealed {
		if !truth.Known(id) {
			return nil, invalid("the person revealed unknown id %q", id)
		}
		if !truth.RevealableKnown(id) || seen[id] || slices.Contains(kept, id) {
			continue
		}
		kept = append(kept, id)
	}
	for _, id := range kept {
		seen[id] = true
		if it, ok := truth.Item(id); ok && it.Latent && !protocol.IsShowOrSuggest(move) {
			violations = append(violations, fmt.Sprintf("latent %s revealed after move %s", id, move))
		}
	}
	if kept == nil {
		kept = []string{}
	}
	tags.Revealed = kept
	if !protocol.IsShowOrSuggest(move) {
		tags.Suggestion = "none"
	}
	return violations, nil
}

// simPerson plays the person from the scenario's truth.
type simPerson struct {
	caller *Caller
	model  string
	s      *scenario.Scenario
	system string
}

func newSimPerson(c *Caller, model string, s *scenario.Scenario, personaID, personaText string) (*simPerson, error) {
	sys, err := prompts.Render("person-system", prompts.PersonSystem{PersonaID: personaID, PersonaText: personaText})
	if err != nil {
		return nil, err
	}
	return &simPerson{caller: c, model: model, s: s, system: sys}, nil
}

func (p *simPerson) reply(ctx context.Context, turns []protocol.Turn, move, message string) (protocol.PersonReply, error) {
	var out protocol.PersonReply
	subjectFiles := "project.md:\n" + projectText(p.s) + "\n\nliving specs:\n" + fileBlock(p.s.LivingSpecs, "living-specs/")
	req := claude.Request{Role: prompts.RolePerson, Model: p.model, System: p.system, Schema: protocol.PersonSchema}
	err := p.caller.Ask(ctx, BucketPerson, req,
		func(repair error) (string, error) {
			return prompts.Render("person-turn", prompts.PersonTurn{
				Truth: string(p.s.TruthRaw), Attachments: fileBlock(p.s.Attachments, "attachments/"),
				SubjectFiles: subjectFiles, Transcript: protocol.RenderTranscript(turns, protocol.ViewFull),
				LastMove: move, LastMessage: message, Repair: prompts.RepairNote(repair),
			})
		},
		func(raw []byte) error {
			r, err := protocol.ParsePerson(raw)
			out = r
			return err
		})
	return out, err
}

type SubjectKind string

const (
	SubjectNone    SubjectKind = "none"
	SubjectPlain   SubjectKind = "plain"
	SubjectCeiling SubjectKind = "ceiling"
	SubjectFile    SubjectKind = "file"
)

type SubjectSpec struct {
	Kind   SubjectKind
	Name   string
	Prompt string
}

// Slug is the subject's name as used in run ids and scores: file subjects by their file name.
func (s SubjectSpec) Slug() string {
	if s.Kind == SubjectFile {
		base := strings.TrimSuffix(filepath.Base(strings.TrimPrefix(s.Name, "file:")), filepath.Ext(s.Name))
		return "file-" + slugRe.ReplaceAllString(base, "-")
	}
	return s.Name
}

func (s SubjectSpec) Converses() bool { return s.Kind == SubjectPlain || s.Kind == SubjectFile }

type conversation struct {
	caller   *Caller
	s        *scenario.Scenario
	subject  SubjectSpec
	model    string
	maxTurns int
	person   *simPerson
}

type convoOut struct {
	Turns  []protocol.Turn
	Change *change.Folder
}

func (c *conversation) run(ctx context.Context) (out convoOut, _ error) {
	start := time.Now()
	out = convoOut{Turns: []protocol.Turn{{Turn: 0, Person: &protocol.PersonReply{Reply: BriefMessage(c.s), PersonTags: protocol.PersonTags{Revealed: []string{}, Suggestion: "none"}, At: start.UTC()}}}}
	plain := c.subject.Kind == SubjectPlain
	seen := map[string]bool{}
	for n := 1; ; n++ {
		left := c.maxTurns - n
		var reply protocol.SubjectReply
		req := claude.Request{Role: prompts.RoleSubject, Model: c.model, System: c.subject.Prompt, Schema: protocol.SubjectSchema(plain)}
		err := c.caller.Ask(ctx, BucketSubject, req,
			func(repair error) (string, error) {
				return prompts.Render("subject-turn", prompts.SubjectTurn{
					FirstRun: c.s.FirstRun, Brief: strings.TrimRight(c.s.Brief, "\n"),
					Attachments: fileBlock(c.s.Attachments, "attachments/"), Project: projectText(c.s),
					LivingSpecs: fileBlock(c.s.LivingSpecs, "living-specs/"),
					Transcript:  protocol.RenderTranscript(out.Turns, protocol.ViewSubject), TurnsLeft: left,
					Repair: prompts.RepairNote(repair),
				})
			},
			func(raw []byte) error {
				r, err := protocol.ParseSubject(raw, left == 0, plain)
				reply = r
				return err
			})
		if err != nil {
			return out, err
		}
		turn := protocol.Turn{Turn: n, Subject: &protocol.SubjectSaid{Move: reply.Move, Message: reply.Message, Done: reply.Done, At: time.Now().UTC()}}
		if reply.Done {
			f := change.New()
			for _, file := range reply.Change.Files {
				f.Set(file.Path, file.Content)
			}
			out.Change = f
			out.Turns = append(out.Turns, turn)
			return out, nil
		}
		pr, err := c.person.reply(ctx, out.Turns, reply.Move, reply.Message)
		if err != nil {
			out.Turns = append(out.Turns, turn)
			return out, err
		}
		turn.Violations, err = CheckPerson(&c.s.Truth, seen, reply.Move, &pr.PersonTags)
		pr.At = time.Now().UTC()
		turn.Person = &pr
		out.Turns = append(out.Turns, turn)
		if err != nil {
			return out, err
		}
	}
}
