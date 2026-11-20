// Package fakeclaude stands in for the `claude` CLI in tests and the dry run: no model, no network. It accepts
// exactly the flags the harness passes (anything else is an error, so a flag typo in the harness is caught),
// records every call, and answers by the role named on the first line of the system prompt (`ROLE: <role>`).
//
// Its answers are deterministic and cheap: the subject walks a fixed script of moves and writes a valid change
// folder quoting what the person said; the person reveals the truth one item at a time under the six rules; the
// judge, tagger and builders compute their answers from the text of the prompt. Environment knobs make it
// misbehave on purpose so the harness's checks can be shown to work:
//
//	FACTORYBENCH_FAKE_LOG=FILE      append one JSON line per call (role, model, flags, prompt hash, prompt text)
//	FACTORYBENCH_FAKE_COST=USD      cost reported per call (default 0.01)
//	FACTORYBENCH_FAKE_FAIL=ROLE     calls of that role fail (an error result, exit status 1)
//	FACTORYBENCH_FAKE_SUBJECT_TURNS=N  the subject finishes on its turn N (default 8)
//	FACTORYBENCH_FAKE_VIOLATE=1     the person reveals a latent item after any move (a simulator violation)
//	FACTORYBENCH_FAKE_UNKNOWN_ID=1  the person reveals an id that is not in the truth
//	FACTORYBENCH_FAKE_INVENT=1      the judge reports the last requirement as invented and unmarked
//	FACTORYBENCH_FAKE_CONTRADICT=1  the judge reports requirement R1 as contradicting a hard limit
//	FACTORYBENCH_FAKE_DISAGREE=R1,R2  the comparison marks those requirements disagree
package fakeclaude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
)

const Name = "fake-claude"

// MaybeRun runs the fake and exits when the process was started as `fake-claude` (a symlink to this binary).
// Call it first thing in main and in TestMain.
func MaybeRun() {
	if filepath.Base(os.Args[0]) == Name {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
}

// Runner is the fake as an in-process claude.Runner. It goes through the same command line the real claude gets.
type Runner struct{}

func (Runner) Run(_ context.Context, req claude.Request) (claude.Response, error) {
	args, stdin, err := claude.Args(req)
	if err != nil {
		return claude.Response{}, err
	}
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &errb)
	var runErr error
	if code != 0 {
		runErr = fmt.Errorf("exit status %d", code)
	}
	return claude.Interpret(Name, req.Role, runErr, out.Bytes(), errb.Bytes())
}

type flags struct {
	prompt, outputFormat, model, system, tools, schema, budget, sources string
	noPersist                                                           bool
}

func parseArgs(args []string, stdin io.Reader) (flags, error) {
	var f flags
	if len(args) == 0 || args[0] != "-p" {
		return f, fmt.Errorf("the first argument must be -p")
	}
	args = args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
		f.prompt, args = args[0], args[1:]
	} else {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return f, err
		}
		f.prompt = string(b)
	}
	for len(args) > 0 {
		name := args[0]
		if name == "--no-session-persistence" {
			f.noPersist, args = true, args[1:]
			continue
		}
		if len(args) < 2 {
			return f, fmt.Errorf("flag %s needs a value", name)
		}
		val := args[1]
		args = args[2:]
		switch name {
		case "--output-format":
			f.outputFormat = val
		case "--model":
			f.model = val
		case "--system-prompt":
			f.system = val
		case "--tools":
			f.tools = val
		case "--json-schema":
			f.schema = val
		case "--max-budget-usd":
			f.budget = val
		case "--setting-sources":
			f.sources = val
		default:
			return f, fmt.Errorf("unknown flag %s", name)
		}
	}
	return f, nil
}

var roleRe = regexp.MustCompile(`^ROLE: (\S+)\n`)

// Main is the fake's command line. It returns the exit status.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fl, err := parseArgs(args, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "fake-claude:", err)
		return 2
	}
	m := roleRe.FindStringSubmatch(fl.system)
	if m == nil {
		fmt.Fprintln(stderr, "fake-claude: the system prompt does not start with `ROLE: <role>`")
		return 2
	}
	role := m[1]
	if fl.outputFormat != "json" || fl.tools != "" || fl.schema == "" || !fl.noPersist || fl.sources != "project" {
		fmt.Fprintln(stderr, "fake-claude: the harness must pass --output-format json --tools \"\" --json-schema S --no-session-persistence --setting-sources project")
		return 2
	}
	if _, err := strconv.ParseFloat(fl.budget, 64); err != nil {
		fmt.Fprintln(stderr, "fake-claude: --max-budget-usd must be a number")
		return 2
	}
	record(fl, role)
	cost := 0.01
	if v := os.Getenv("FACTORYBENCH_FAKE_COST"); v != "" {
		cost, _ = strconv.ParseFloat(v, 64)
	}
	if os.Getenv("FACTORYBENCH_FAKE_FAIL") == role {
		writeResult(stdout, true, "fake failure", nil, cost)
		return 1
	}
	var reply any
	switch role {
	case prompts.RoleSubject:
		reply = subject(fl)
	case prompts.RolePerson:
		reply = person(fl)
	case prompts.RoleTagger:
		reply = tagger(fl)
	case prompts.RoleJudge:
		reply = judge(fl)
	case prompts.RoleBuilder:
		reply = builder(fl)
	case prompts.RoleComparison:
		reply = comparison(fl)
	case prompts.RoleReplayJudge:
		reply = replayJudge(fl)
	default:
		fmt.Fprintln(stderr, "fake-claude: unknown role", role)
		return 2
	}
	writeResult(stdout, false, "", reply, cost)
	return 0
}

func writeResult(w io.Writer, isErr bool, msg string, reply any, cost float64) {
	out := map[string]any{"type": "result", "subtype": "success", "is_error": isErr, "total_cost_usd": cost, "session_id": "fake",
		"duration_ms": 50, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 10, "cache_creation_input_tokens": 5}}
	if isErr {
		out["subtype"] = "error_during_execution"
		out["result"] = msg
	} else {
		b, _ := json.Marshal(reply)
		out["result"] = string(b)
		out["structured_output"] = json.RawMessage(b)
	}
	b, _ := json.Marshal(out)
	fmt.Fprintln(w, string(b))
}

type LogEntry struct {
	Role         string            `json:"role"`
	Model        string            `json:"model"`
	Flags        map[string]string `json:"flags"`
	PromptSHA256 string            `json:"prompt_sha256"`
	SystemSHA256 string            `json:"system_sha256"`
	Prompt       string            `json:"prompt"`
	System       string            `json:"system"`
	EmptyDir     bool              `json:"empty_dir"`
}

func record(fl flags, role string) {
	path := os.Getenv("FACTORYBENCH_FAKE_LOG")
	if path == "" {
		return
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	entries, derr := os.ReadDir(".")
	e := LogEntry{Role: role, Model: fl.model, PromptSHA256: sum(fl.prompt), SystemSHA256: sum(fl.system), Prompt: fl.prompt, System: fl.system,
		Flags: map[string]string{"output-format": fl.outputFormat, "tools": fl.tools, "max-budget-usd": fl.budget,
			"setting-sources": fl.sources, "no-session-persistence": "true", "json-schema-bytes": strconv.Itoa(len(fl.schema))},
		EmptyDir: derr == nil && len(entries) == 0}
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func ReadLog(path string) ([]LogEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []LogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

var emptyReplies = []string{"I don't know", "I can't say that", "That is all I have", "Yes, please start", "Hmm, not sure", "It is in the attachments"}

func isEmptyReply(s string) bool {
	for _, p := range emptyReplies {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func section(fl flags, tag string) string {
	s, _ := prompts.Section(fl.prompt, tag)
	return s
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

func truthOf(fl flags) scenario.Truth {
	var t scenario.Truth
	_ = json.Unmarshal([]byte(section(fl, "truth")), &t)
	return t
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func personLines(ls []protocol.Line, fromTurn int) []protocol.Line {
	var out []protocol.Line
	for _, l := range ls {
		if l.Who == "PERSON" && l.Turn >= fromTurn {
			out = append(out, l)
		}
	}
	return out
}

func count(ls []protocol.Line, who string) int {
	n := 0
	for _, l := range ls {
		if l.Who == who {
			n++
		}
	}
	return n
}

var subjectMoves = []string{"ask", "ask", "show", "suggest", "challenge", "sharpen", "confirm", "mirror"}

func subject(fl flags) any {
	var schema struct {
		Properties struct {
			Move struct {
				Enum []string `json:"enum"`
			} `json:"move"`
		} `json:"properties"`
	}
	_ = json.Unmarshal([]byte(fl.schema), &schema)
	plain := slices.Equal(schema.Properties.Move.Enum, []string{"ask"})
	lines := protocol.ParseTranscript(section(fl, "transcript"))
	n := count(lines, "SUBJECT") + 1
	left, _ := strconv.Atoi(section(fl, "turns_left"))
	move := subjectMoves[(n-1)%len(subjectMoves)]
	if plain {
		move = "ask"
	}
	done := left == 0 || n >= envInt("FACTORYBENCH_FAKE_SUBJECT_TURNS", 8)
	reply := protocol.SubjectReply{Move: move, Done: done, Message: fmt.Sprintf("FAKE-SUBJECT-TURN-%d %s: tell me more", n, move)}
	if done {
		reply.Message = fmt.Sprintf("FAKE-SUBJECT-TURN-%d %s: here is the change folder", n, move)
		reply.Change = &protocol.Change{Files: changeFiles(fl, personLines(lines, 1))}
	}
	return reply
}

func changeFiles(fl flags, said []protocol.Line) []protocol.File {
	var points []string
	for _, l := range said {
		if t := squash(l.Text); t != "" && !isEmptyReply(t) {
			points = append(points, t)
		}
	}
	if len(points) == 0 {
		points = []string{"The person has not said anything yet"}
	}
	var spec, mp strings.Builder
	spec.WriteString("## ADDED Requirements\n\n")
	mp.WriteString("## Actors\n- [assumed] The people in the brief\n## Journeys\n## Data\n## Rules\n")
	for i, p := range points {
		fmt.Fprintf(&spec, "### Requirement: Point %d\nThe app SHALL satisfy: %s\n**Scope fence:** Only what the person said.\n\n#### Scenario: Point %d holds\n- **WHEN** case %d arises\n- **THEN** %s\n\n", i+1, p, i+1, i+1, p)
		fmt.Fprintf(&mp, "- [said] %s\n", p)
	}
	mp.WriteString("## Screens\n## Failures\n## Limits\n")
	files := []protocol.File{
		{Path: "proposal.md", Content: "## Why\nThe person asked.\n\n## What Changes\nWhat is in the spec.\n\n## Capabilities\n- feature\n\n## Impact\nThe whole product.\n"},
		{Path: "specs/feature/spec.md", Content: spec.String()},
		{Path: "map.md", Content: mp.String()},
		{Path: "decisions.md", Content: "# Decisions\n\nProblem: nothing needed deciding. Options: none. Choice: none. Reason: all was said.\n"},
	}
	if section(fl, "first_run") == "true" {
		files = append(files, protocol.File{Path: "project.md", Content: "## Goal\nThe brief.\n## Users\nThe people in the brief.\n## Platforms\nAs stated.\n## Integrations\nAs stated.\n## Data and privacy\nAs stated.\n## Acceptance\nThe scenarios.\n## Hard limits\nNone stated.\n"})
	}
	return files
}

func personaOf(system string) string {
	for _, l := range strings.Split(system, "\n") {
		if strings.HasPrefix(l, "PERSONA: ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "PERSONA: "))
		}
	}
	return ""
}

func person(fl flags) any {
	truth := truthOf(fl)
	persona := personaOf(fl.system)
	lines := protocol.ParseTranscript(section(fl, "transcript"))
	said := ""
	for _, l := range personLines(lines, 0) {
		said += " " + squash(l.Text)
	}
	move := strings.TrimSpace(strings.TrimPrefix(lastMove(fl.prompt), "The assistant's last move:"))
	n := count(lines, "SUBJECT") + 1
	isSaid := func(text string) bool { return strings.Contains(said, squash(text)) }
	reveal := func(ids ...string) []string { return ids }

	out := protocol.PersonReply{PersonTags: protocol.PersonTags{Revealed: []string{}, Suggestion: "none"}}
	if protocol.IsShowOrSuggest(move) {
		out.Suggestion = "accepted"
	}
	if move == "confirm" {
		out.Confirmed = true
	}
	technicalOK := persona == "decided-technical"
	var noise string
	if persona == "talkative" {
		for _, nz := range truth.Noise {
			if !isSaid(nz.Text) {
				noise = " By the way, " + nz.Text
				break
			}
		}
	}
	finish := func(reply string) any {
		out.Reply = reply + noise
		return out
	}

	if os.Getenv("FACTORYBENCH_FAKE_UNKNOWN_ID") == "1" {
		out.Revealed = reveal("ZZ99")
		return finish("Something nobody knows.")
	}
	latentOK := protocol.IsShowOrSuggest(move) || os.Getenv("FACTORYBENCH_FAKE_VIOLATE") == "1"
	if latentOK {
		for _, it := range truth.Items {
			if it.Latent && !isSaid(it.Text) {
				out.Revealed = reveal(it.ID)
				return finish(it.Text)
			}
		}
	}
	if move == "challenge" || move == "sharpen" {
		for _, tr := range truth.Traps {
			if isSaid(tr.Resolution) {
				continue
			}
			out.Revealed = reveal(tr.ID)
			reply := tr.Resolution
			if strings.HasPrefix(tr.Resolution, "I don't know") {
				out.DontKnow = true
			}
			if tr.Kind == "vague" && len(tr.Items) == 1 {
				if it, ok := truth.Item(tr.Items[0]); ok {
					out.Revealed = reveal(tr.ID, it.ID)
					reply += " " + it.Text
				}
			}
			return finish(reply)
		}
	}
	if persona == "unsure" && n%2 == 0 && !latentOK {
		out.DontKnow, out.Frustration = true, 1
		return finish("I don't know.")
	}
	blocked := false
	for _, it := range truth.Items {
		if it.Latent || isSaid(it.Text) {
			continue
		}
		if it.Technical && !technicalOK {
			blocked = true
			continue
		}
		out.Revealed = reveal(it.ID)
		return finish(it.Text)
	}
	if blocked {
		out.CouldNotAnswer = true
		return finish("I can't say that technically, it is a question for your engineers.")
	}
	if move == "confirm" {
		return finish("Yes, please start.")
	}
	return finish("That is all I have.")
}

func lastMove(prompt string) string {
	for _, l := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(l, "The assistant's last move:") {
			return l
		}
	}
	return ""
}

func tagger(fl flags) any {
	truth := truthOf(fl)
	lines := protocol.ParseTranscript(section(fl, "transcript"))
	moves := map[int]string{}
	for _, l := range lines {
		if l.Who == "SUBJECT" {
			moves[l.Turn] = l.Move
		}
	}
	seen := map[string]bool{}
	for _, it := range truth.Items {
		_ = it
	}
	var out protocol.TaggerReply
	for _, l := range personLines(lines, 1) {
		text := squash(l.Text)
		tg := protocol.PersonTags{Revealed: []string{}, Suggestion: "none"}
		for _, it := range truth.Items {
			if !seen[it.ID] && strings.Contains(text, squash(it.Text)) {
				seen[it.ID] = true
				tg.Revealed = append(tg.Revealed, it.ID)
			}
		}
		for _, tr := range truth.Traps {
			if !seen[tr.ID] && !strings.HasPrefix(tr.Resolution, "I don't know") && strings.Contains(text, squash(tr.Resolution)) {
				seen[tr.ID] = true
				tg.Revealed = append(tg.Revealed, tr.ID)
			}
		}
		tg.DontKnow = strings.Contains(text, "don't know")
		tg.CouldNotAnswer = strings.Contains(text, "can't say that technically")
		if protocol.IsShowOrSuggest(moves[l.Turn]) {
			tg.Suggestion = "accepted"
		}
		if tg.DontKnow {
			tg.Frustration = 1
		}
		tg.Confirmed = moves[l.Turn] == "confirm"
		out.Turns = append(out.Turns, protocol.TaggedTurn{Turn: l.Turn, PersonTags: tg})
	}
	return out
}

var reqIDRe = regexp.MustCompile(`(?m)^(R\d+):`)

func requirementIDs(fl flags) []string {
	var ids []string
	for _, m := range reqIDRe.FindAllStringSubmatch(section(fl, "requirements"), -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func judge(fl flags) any {
	truth := truthOf(fl)
	change := squash(section(fl, "change"))
	lines := protocol.ParseTranscript(section(fl, "transcript"))
	reqs := requirementIDs(fl)
	out := protocol.Judge{Items: []protocol.JudgeItem{}, Traps: []protocol.JudgeTrap{}, Invented: []protocol.Invented{},
		MapTagErrors: []protocol.MapTagError{}, Untraced: []string{}, NoisePresent: []string{}, MissingSlots: []protocol.MissingSlot{}, Contradictions: []protocol.Contradiction{}}
	for _, it := range truth.Items {
		c := protocol.None
		if strings.Contains(change, squash(it.Text)) {
			c = protocol.Full
		}
		out.Items = append(out.Items, protocol.JudgeItem{ID: it.ID, Captured: c, Evidence: "text found in the change folder: " + strconv.FormatBool(c == protocol.Full)})
	}
	for _, tr := range truth.Traps {
		jt := protocol.JudgeTrap{ID: tr.ID, Evidence: "fake"}
		for _, l := range personLines(lines, 1) {
			if strings.Contains(squash(l.Text), squash(tr.Resolution)) {
				jt.Raised, jt.Turn = true, l.Turn
				break
			}
		}
		jt.FollowedResolution = jt.Raised && strings.Contains(change, squash(tr.Resolution))
		out.Traps = append(out.Traps, jt)
	}
	for _, nz := range truth.Noise {
		if strings.Contains(change, squash(nz.Text)) {
			out.NoisePresent = append(out.NoisePresent, nz.ID)
		}
	}
	if len(personLines(lines, 1)) == 0 {
		out.Untraced = append(out.Untraced, reqs...)
	}
	if os.Getenv("FACTORYBENCH_FAKE_INVENT") == "1" && len(reqs) > 0 {
		out.Invented = append(out.Invented, protocol.Invented{Requirement: reqs[len(reqs)-1], MarkedAssumed: false, Why: "fake"})
	}
	if os.Getenv("FACTORYBENCH_FAKE_CONTRADICT") == "1" && len(reqs) > 0 {
		out.Contradictions = append(out.Contradictions, protocol.Contradiction{Requirement: reqs[0], With: "a hard limit", Why: "fake"})
	}
	return out
}

func builder(fl flags) any {
	out := protocol.BuilderReply{Requirements: []protocol.BuilderRequirement{}}
	for _, id := range requirementIDs(fl) {
		out.Requirements = append(out.Requirements, protocol.BuilderRequirement{Requirement: id,
			Tests: []protocol.BuilderTest{{Name: "fake " + id, Given: "a product", When: "the case of " + id + " arises", Then: "the visible result of " + id}}})
	}
	return out
}

func comparison(fl flags) any {
	disagree := strings.Split(os.Getenv("FACTORYBENCH_FAKE_DISAGREE"), ",")
	out := protocol.CompareReply{Requirements: []protocol.CompareVerdict{}}
	for _, id := range requirementIDs(fl) {
		v := protocol.CompareVerdict{Requirement: id, Verdict: protocol.Agree, Reason: "fake: same expectations"}
		if slices.Contains(disagree, id) {
			v.Verdict, v.Reason = protocol.Disagree, "fake: different expectations"
		}
		out.Requirements = append(out.Requirements, v)
	}
	return out
}

func replayJudge(fl flags) any {
	var pairs []struct {
		Turn      int    `json:"turn"`
		RealAns   string `json:"real_answer"`
		SimAnswer string `json:"simulated_answer"`
	}
	_ = json.Unmarshal([]byte(section(fl, "pairs")), &pairs)
	out := protocol.ReplayJudgeReply{Turns: []protocol.ReplayVerdict{}}
	for _, p := range pairs {
		v := protocol.ReplayVerdict{Turn: p.Turn, Verdict: protocol.Different, Reason: "fake",
			RealDontKnow: strings.Contains(p.RealAns, "don't know"), SimDontKnow: strings.Contains(p.SimAnswer, "don't know")}
		switch {
		case squash(p.RealAns) == squash(p.SimAnswer):
			v.Verdict = protocol.Same
		case overlap(p.RealAns, p.SimAnswer) >= 0.3:
			v.Verdict = protocol.PartialV
		}
		out.Turns = append(out.Turns, v)
	}
	return out
}

func overlap(a, b string) float64 {
	wa := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(a)) {
		wa[w] = true
	}
	if len(wa) == 0 {
		return 0
	}
	hit := 0
	for _, w := range strings.Fields(strings.ToLower(b)) {
		if wa[w] {
			hit++
			delete(wa, w)
		}
	}
	return float64(hit) / float64(hit+len(wa))
}
