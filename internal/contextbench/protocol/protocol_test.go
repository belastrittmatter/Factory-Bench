package protocol

import (
	"encoding/json"
	"factorybench/internal/contract"
	"strings"
	"testing"
)

func TestSchemasAreJSON(t *testing.T) {
	for name, s := range map[string]string{"subject": SubjectSchema(false), "plain": SubjectSchema(true), "person": PersonSchema,
		"judge": JudgeSchema, "builder": BuilderSchema, "compare": CompareSchema} {
		var v map[string]any
		if err := json.Unmarshal([]byte(s), &v); err != nil || v["type"] != "object" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !strings.Contains(SubjectSchema(true), `"enum":["ask"]`) || strings.Contains(SubjectSchema(false), `"enum":["ask"]`) {
		t.Error("only the plain schema is restricted to ask")
	}
	for _, m := range contract.Moves {
		if !strings.Contains(SubjectSchema(false), `"`+m+`"`) {
			t.Errorf("move %s missing from the schema", m)
		}
	}
}

func TestParseSubject(t *testing.T) {
	good := `{"move":"ask","message":"who?","done":false}`
	done := `{"move":"confirm","message":"bye","done":true,"change":{"files":[{"path":"proposal.md","content":"x"}]}}`
	cases := []struct {
		name, in     string
		final, plain bool
		wantErr      string
	}{
		{"ask", good, false, false, ""},
		{"done with change", done, false, false, ""},
		{"done on the final turn", done, true, false, ""},
		{"not done on the final turn", good, true, false, "no turns are left"},
		{"unknown move", `{"move":"shout","message":"x","done":false}`, false, false, "not one of"},
		{"plain may only ask", `{"move":"show","message":"x","done":false}`, false, true, "only use the move ask"},
		{"empty message", `{"move":"ask","message":" ","done":false}`, false, false, "message is empty"},
		{"missing done", `{"move":"ask","message":"x"}`, false, false, `missing field "done"`},
		{"done without change", `{"move":"ask","message":"x","done":true}`, false, false, "change.files is missing"},
		{"done with empty change", `{"move":"ask","message":"x","done":true,"change":{"files":[]}}`, false, false, "change.files is missing"},
		{"path escapes", `{"move":"ask","message":"x","done":true,"change":{"files":[{"path":"../x","content":""}]}}`, false, false, "clean"},
		{"absolute path", `{"move":"ask","message":"x","done":true,"change":{"files":[{"path":"/x","content":""}]}}`, false, false, "relative"},
		{"duplicate path", `{"move":"ask","message":"x","done":true,"change":{"files":[{"path":"a","content":""},{"path":"a","content":""}]}}`, false, false, "twice"},
		{"file is also a folder", `{"move":"ask","message":"x","done":true,"change":{"files":[{"path":"specs","content":""},{"path":"specs/a/spec.md","content":""}]}}`, false, false, "also the folder"},
		{"sibling folders", `{"move":"ask","message":"x","done":true,"change":{"files":[{"path":"specs/a.md","content":""},{"path":"specs/b/c.md","content":""}]}}`, false, false, ""},
		{"change ignored when not done", `{"move":"ask","message":"x","done":false,"change":{"files":[{"path":"../x","content":""}]}}`, false, false, ""},
		{"not an object", `[1]`, false, false, "not a JSON object"},
	}
	for _, c := range cases {
		_, err := ParseSubject([]byte(c.in), c.final, c.plain)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: error %v, want %q", c.name, err, c.wantErr)
		}
	}
}

const person = `{"reply":"hi","revealed":["T1"],"dont_know":false,"could_not_answer":false,"already_said":false,"in_attachments":false,"suggestion":"none","frustration":2,"confirmed":false}`

func TestParsePerson(t *testing.T) {
	r, err := ParsePerson([]byte(person))
	if err != nil || r.Reply != "hi" || r.Revealed[0] != "T1" || r.Frustration != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	bad := map[string]string{
		"suggestion":     strings.Replace(person, `"none"`, `"maybe"`, 1),
		"frustration 6":  strings.Replace(person, `"frustration":2`, `"frustration":6`, 1),
		"frustration -1": strings.Replace(person, `"frustration":2`, `"frustration":-1`, 1),
		"missing field":  strings.Replace(person, `"confirmed":false`, `"x":1`, 1),
		"empty reply":    strings.Replace(person, `"reply":"hi"`, `"reply":""`, 1),
		"null revealed":  strings.Replace(person, `["T1"]`, `null`, 1),
	}
	for name, in := range bad {
		if _, err := ParsePerson([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseJudge(t *testing.T) {
	judge := func(items, traps string) []byte {
		return []byte(`{"items":` + items + `,"traps":` + traps + `,"invented":[{"requirement":"R1","marked_assumed":false,"why":"w"}],"map_tag_errors":[],"untraced":["R1"],"noise_present":["N1"],"missing_slots":[],"contradictions":[]}`)
	}
	items := `[{"id":"T1","captured":"full","evidence":""},{"id":"T2","captured":"none","evidence":""}]`
	traps := `[{"id":"X1","raised":true,"turn":3,"followed_resolution":true,"evidence":""}]`
	args := func(raw []byte) error {
		_, err := ParseJudge(raw, []string{"T1", "T2"}, []string{"X1"}, []string{"N1"}, []string{"R1"})
		return err
	}
	if err := args(judge(items, traps)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"missing item":      judge(`[{"id":"T1","captured":"full","evidence":""}]`, traps),
		"unknown item":      judge(strings.Replace(items, "T2", "T9", 1), traps),
		"duplicate item":    judge(strings.Replace(items, "T2", "T1", 1), traps),
		"bad captured":      judge(strings.Replace(items, "none", "half", 1), traps),
		"raised no turn":    judge(items, strings.Replace(traps, `"turn":3`, `"turn":0`, 1)),
		"followed unraised": judge(items, strings.Replace(traps, `"raised":true`, `"raised":false`, 1)),
	} {
		if args(raw) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseJudge(judge(items, traps), []string{"T1", "T2"}, []string{"X1"}, []string{"N1"}, []string{"R7"}); err == nil {
		t.Error("an invented requirement id that is not in the change was accepted")
	}
}

func TestParseBuilderAndCompare(t *testing.T) {
	b := `{"requirements":[{"requirement":"R1","tests":[{"name":"n","given":"g","when":"w","then":"t"}]}]}`
	if _, err := ParseBuilder([]byte(b), []string{"R1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseBuilder([]byte(b), []string{"R1", "R2"}); err == nil {
		t.Error("a builder that skipped a requirement was accepted")
	}
	if _, err := ParseBuilder([]byte(strings.Replace(b, `[{"name":"n","given":"g","when":"w","then":"t"}]`, `[]`, 1)), []string{"R1"}); err == nil {
		t.Error("a requirement with no test was accepted")
	}
	c := `{"requirements":[{"requirement":"R1","verdict":"agree","reason":""}]}`
	if _, err := ParseCompare([]byte(c), []string{"R1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCompare([]byte(strings.Replace(c, "agree", "maybe", 1)), []string{"R1"}); err == nil {
		t.Error("a verdict other than agree or disagree was accepted")
	}
}

func TestRenderTranscript(t *testing.T) {
	turns := []Turn{
		{Turn: 0, Person: &PersonReply{Reply: "the brief\nsecond line"}},
		{Turn: 1, Subject: &SubjectSaid{Move: "show", Message: "a screen:\n- one\n- two"}, Person: &PersonReply{Reply: "yes"}},
		{Turn: 2, Subject: &SubjectSaid{Move: "ask", Message: "q", Done: true}},
	}
	full := RenderTranscript(turns, ViewFull)
	if want := "[turn 0] PERSON: the brief\nsecond line\n[turn 1] SUBJECT (show): a screen:\n- one\n- two\n[turn 1] PERSON: yes\n[turn 2] SUBJECT (ask): q\n"; full != want {
		t.Errorf("%q", full)
	}
	sub := RenderTranscript(turns, ViewSubject)
	if strings.Contains(sub, "the brief") || !strings.Contains(sub, "[turn 1] SUBJECT (show)") {
		t.Errorf("the subject's view starts after the brief:\n%s", sub)
	}
	if RenderTranscript(nil, ViewSubject) != "(nothing yet)\n" {
		t.Error("empty transcript")
	}
	if Words("  a b\nc ") != 3 {
		t.Error("words")
	}
}
