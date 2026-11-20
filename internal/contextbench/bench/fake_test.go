package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
)

// fakeRunner stands in for the claude CLI: no model, no network. It records every request and answers by role,
// deterministically and cheaply, from the text of the prompt: the subject walks a fixed script of moves and writes
// a valid change folder quoting what the person said; the person reveals the truth one item at a time under the
// six rules; the judge and the builders compute their answers from the change folder. The fields make it
// misbehave on purpose.
type fakeRunner struct {
	fail      string   // calls of this role fail
	unknownID bool     // the person reveals an id that is not in the truth
	violate   bool     // the person reveals a latent item after any move
	disagree  []string // requirements the comparison marks as disagree

	mu   sync.Mutex
	reqs []claude.Request
}

const fakeCost = 0.01

func (f *fakeRunner) Run(_ context.Context, req claude.Request) (claude.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	usage := claude.Usage{Input: 100, Output: 20, CacheRead: 10, CacheWrite: 5, DurationMS: 50, Cost: &[]float64{fakeCost}[0]}
	resp := claude.Response{Cost: fakeCost, Usage: usage}
	if f.fail == req.Role {
		return resp, fmt.Errorf("fake failure of %s", req.Role)
	}
	var reply any
	switch req.Role {
	case prompts.RoleSubject:
		reply = fakeSubject(req)
	case prompts.RolePerson:
		reply = f.person(req)
	case prompts.RoleJudge:
		reply = fakeJudge(req)
	case prompts.RoleBuilder:
		reply = fakeBuilder(req)
	case prompts.RoleComparison:
		reply = f.comparison(req)
	default:
		return resp, fmt.Errorf("fake: unknown role %q", req.Role)
	}
	resp.Output, _ = json.Marshal(reply)
	return resp, nil
}

// role lists the requests of one role.
func (f *fakeRunner) role(role string) []claude.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []claude.Request
	for _, q := range f.reqs {
		if q.Role == role {
			out = append(out, q)
		}
	}
	return out
}

func (f *fakeRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// section returns the text between <tag> and </tag> of a message.
func section(text, tag string) string {
	_, rest, ok := strings.Cut(text, "<"+tag+">")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "</"+tag+">")
	return strings.Trim(body, "\n")
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

var lineRe = regexp.MustCompile(`(?m)^\[turn (\d+)\] (SUBJECT \((\w+)\)|PERSON): (.*)$`)

// personSaid lists what the person said in the transcript, the brief first.
func personSaid(transcript string) []string {
	var out []string
	for _, m := range lineRe.FindAllStringSubmatch(transcript, -1) {
		if m[2] == "PERSON" {
			out = append(out, squash(m[4]))
		}
	}
	return out
}

func subjectTurns(transcript string) int {
	n := 0
	for _, m := range lineRe.FindAllStringSubmatch(transcript, -1) {
		if strings.HasPrefix(m[2], "SUBJECT") {
			n++
		}
	}
	return n
}

func truthOf(req claude.Request) scenario.Truth {
	var t scenario.Truth
	_ = json.Unmarshal([]byte(section(req.Prompt, "truth")), &t)
	return t
}

var fakeMoves = []string{"ask", "ask", "show", "suggest", "challenge", "sharpen", "confirm", "mirror"}

func fakeSubject(req claude.Request) any {
	said := personSaid(section(req.Prompt, "transcript"))
	n := subjectTurns(section(req.Prompt, "transcript")) + 1
	move := fakeMoves[(n-1)%len(fakeMoves)]
	if strings.Contains(req.Schema, `"enum":["ask"]`) {
		move = "ask"
	}
	done := strings.Contains(req.Prompt, "you must finish now") || n >= len(fakeMoves)
	reply := protocol.SubjectReply{Move: move, Done: done, Message: fmt.Sprintf("FAKE-SUBJECT-TURN-%d %s: tell me more", n, move)}
	if done {
		reply.Message = fmt.Sprintf("FAKE-SUBJECT-TURN-%d %s: here is the change folder", n, move)
		reply.Change = &protocol.Change{Files: fakeChange(req, said)}
	}
	return reply
}

func fakeChange(req claude.Request, said []string) []protocol.File {
	var spec, mp strings.Builder
	spec.WriteString("## ADDED Requirements\n\n")
	mp.WriteString("## Actors\n- [assumed] The people in the brief\n## Journeys\n## Data\n## Rules\n")
	points := 0
	for _, p := range said {
		if slices.ContainsFunc([]string{"I don't know", "That is all I have", "Yes, please start", "I can't say"}, func(e string) bool { return strings.HasPrefix(p, e) }) {
			continue
		}
		points++
		fmt.Fprintf(&spec, "### Requirement: Point %d\nThe app SHALL satisfy: %s\n**Scope fence:** Only what the person said.\n\n#### Scenario: Point %d holds\n- **WHEN** case %d arises\n- **THEN** %s\n\n", points, p, points, points, p)
		fmt.Fprintf(&mp, "- [said] %s\n", p)
	}
	if points == 0 {
		spec.WriteString("### Requirement: Nothing yet\nThe app SHALL do what the brief says.\n**Scope fence:** The brief.\n\n#### Scenario: The brief holds\n- **WHEN** the brief applies\n- **THEN** it is done\n\n")
	}
	mp.WriteString("## Screens\n## Failures\n## Limits\n")
	files := []protocol.File{
		{Path: "proposal.md", Content: "## Why\nThe person asked.\n\n## What Changes\nWhat is in the spec.\n\n## Capabilities\n- feature\n\n## Impact\nThe whole product.\n"},
		{Path: "specs/feature/spec.md", Content: spec.String()},
		{Path: "map.md", Content: mp.String()},
		{Path: "decisions.md", Content: "# Decisions\n\nProblem: nothing needed deciding. Options: none. Choice: none. Reason: all was said.\n"},
	}
	if section(req.Prompt, "first_run") == "true" {
		files = append(files, protocol.File{Path: "project.md", Content: "## Goal\nThe brief.\n## Users\nThe people in the brief.\n## Platforms\nAs stated.\n## Integrations\nAs stated.\n## Data and privacy\nAs stated.\n## Acceptance\nThe scenarios.\n## Hard limits\nNone stated.\n"})
	}
	return files
}

func (f *fakeRunner) person(req claude.Request) any {
	truth := truthOf(req)
	persona := ""
	if _, rest, ok := strings.Cut(req.System, "PERSONA: "); ok {
		persona, _, _ = strings.Cut(rest, "\n")
	}
	said := strings.Join(personSaid(section(req.Prompt, "transcript")), " ")
	isSaid := func(text string) bool { return strings.Contains(said, squash(text)) }
	_, rest, _ := strings.Cut(req.Prompt, "The assistant's last move: ")
	move, _, _ := strings.Cut(rest, "\n")

	out := protocol.PersonReply{PersonTags: protocol.PersonTags{Revealed: []string{}, Suggestion: "none"}}
	if protocol.IsShowOrSuggest(move) {
		out.Suggestion = "accepted"
	}
	out.Confirmed = move == "confirm"
	noise := ""
	if persona == "talkative" {
		for _, nz := range truth.Noise {
			if !isSaid(nz.Text) {
				noise = " By the way, " + nz.Text
				break
			}
		}
	}
	reply := func(text string, revealed ...string) any {
		out.Revealed = append(out.Revealed, revealed...)
		out.Reply = text + noise
		return out
	}

	if f.unknownID {
		return reply("Something nobody knows.", "ZZ99")
	}
	if protocol.IsShowOrSuggest(move) || f.violate {
		for _, it := range truth.Items {
			if it.Latent && !isSaid(it.Text) {
				return reply(it.Text, it.ID)
			}
		}
	}
	if move == "challenge" || move == "sharpen" {
		for _, tr := range truth.Traps {
			if isSaid(tr.Resolution) {
				continue
			}
			out.DontKnow = strings.HasPrefix(tr.Resolution, "I don't know")
			text, ids := tr.Resolution, []string{tr.ID}
			if tr.Kind == "vague" && len(tr.Items) == 1 {
				if it, ok := truth.Item(tr.Items[0]); ok {
					text, ids = text+" "+it.Text, append(ids, it.ID)
				}
			}
			return reply(text, ids...)
		}
	}
	blocked := false
	for _, it := range truth.Items {
		if it.Latent || isSaid(it.Text) {
			continue
		}
		if it.Technical && persona != "decided-technical" {
			blocked = true
			continue
		}
		return reply(it.Text, it.ID)
	}
	if blocked {
		out.CouldNotAnswer = true
		return reply("I can't say that technically, it is a question for your engineers.")
	}
	if move == "confirm" {
		return reply("Yes, please start.")
	}
	return reply("That is all I have.")
}

var reqIDRe = regexp.MustCompile(`(?m)^(R\d+):`)

func requirementIDs(req claude.Request) []string {
	var ids []string
	for _, m := range reqIDRe.FindAllStringSubmatch(section(req.Prompt, "requirements"), -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func fakeJudge(req claude.Request) any {
	truth := truthOf(req)
	change := squash(section(req.Prompt, "change"))
	said := strings.Join(personSaid(section(req.Prompt, "transcript")), " ")
	out := protocol.Judge{Items: []protocol.JudgeItem{}, Traps: []protocol.JudgeTrap{}, Invented: []protocol.Invented{},
		MapTagErrors: []protocol.MapTagError{}, Untraced: []string{}, NoisePresent: []string{}, MissingSlots: []protocol.MissingSlot{}, Contradictions: []protocol.Contradiction{}}
	for _, it := range truth.Items {
		c := protocol.None
		if strings.Contains(change, squash(it.Text)) {
			c = protocol.Full
		}
		out.Items = append(out.Items, protocol.JudgeItem{ID: it.ID, Captured: c, Evidence: "fake"})
	}
	for i, tr := range truth.Traps {
		jt := protocol.JudgeTrap{ID: tr.ID, Evidence: "fake"}
		if strings.Contains(said, squash(tr.Resolution)) {
			jt.Raised, jt.Turn = true, i+1
		}
		jt.FollowedResolution = jt.Raised && strings.Contains(change, squash(tr.Resolution))
		out.Traps = append(out.Traps, jt)
	}
	for _, nz := range truth.Noise {
		if strings.Contains(change, squash(nz.Text)) {
			out.NoisePresent = append(out.NoisePresent, nz.ID)
		}
	}
	return out
}

func fakeBuilder(req claude.Request) any {
	out := protocol.BuilderReply{Requirements: []protocol.BuilderRequirement{}}
	for _, id := range requirementIDs(req) {
		out.Requirements = append(out.Requirements, protocol.BuilderRequirement{Requirement: id,
			Tests: []protocol.BuilderTest{{Name: "fake " + id, Given: "a product", When: "the case of " + id + " arises", Then: "the visible result of " + id}}})
	}
	return out
}

func (f *fakeRunner) comparison(req claude.Request) any {
	out := protocol.CompareReply{Requirements: []protocol.CompareVerdict{}}
	for _, id := range requirementIDs(req) {
		v := protocol.CompareVerdict{Requirement: id, Verdict: protocol.Agree, Reason: "fake"}
		if slices.Contains(f.disagree, id) {
			v.Verdict = protocol.Disagree
		}
		out.Requirements = append(out.Requirements, v)
	}
	return out
}
