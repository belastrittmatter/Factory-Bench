package claude

import (
	"strings"
	"testing"
)

func TestArgsIsTheContractCommandLine(t *testing.T) {
	args, stdin, err := Args(Request{Role: "judge", Model: "m", System: "sys", Prompt: "hello", Schema: `{"type":"object"}`, MaxCost: 2})
	if err != nil || stdin != "" {
		t.Fatal(err, stdin)
	}
	want := []string{"-p", "hello", "--output-format", "json", "--model", "m", "--system-prompt", "ROLE: judge\n\nsys", "--tools", "",
		"--json-schema", `{"type":"object"}`, "--max-budget-usd", "2", "--no-session-persistence", "--setting-sources", "project"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%q", args)
	}
	args, _, _ = Args(Request{Role: "x", Prompt: "p", MaxCost: 0.25})
	if args[13] != "0.25" && !contains(args, "0.25") {
		t.Errorf("%q", args)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestLongOrDashPromptsGoToStdin(t *testing.T) {
	long := strings.Repeat("x", MaxArgBytes+1)
	args, stdin, err := Args(Request{Role: "x", Prompt: long, MaxCost: 1})
	if err != nil || stdin != long || args[1] != "--output-format" {
		t.Errorf("%v %d %q", err, len(stdin), args[:2])
	}
	if _, stdin, _ := Args(Request{Role: "x", Prompt: "-dash", MaxCost: 1}); stdin != "-dash" {
		t.Error("a prompt starting with - must not be an argument")
	}
	if _, _, err := Args(Request{Role: "x", System: long, MaxCost: 1}); err == nil {
		t.Error("an oversized system prompt was accepted")
	}
}

func TestParseResult(t *testing.T) {
	r, err := ParseResult([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"text","structured_output":{"a":1},"total_cost_usd":0.5}`))
	if err != nil || string(r.Output) != `{"a":1}` || r.Cost != 0.5 {
		t.Errorf("%+v %v", r, err)
	}
	r, err = ParseResult([]byte(`{"subtype":"success","is_error":false,"result":"Here you go:\n` + "```json\\n{\\\"b\\\":2}\\n```" + `","total_cost_usd":0.1}`))
	if err != nil || string(r.Output) != `{"b":2}` {
		t.Errorf("fenced: %q %v", r.Output, err)
	}
	r, err = ParseResult([]byte(`{"subtype":"success","is_error":false,"result":"{\"c\":3}"}`))
	if err != nil || string(r.Output) != `{"c":3}` {
		t.Errorf("bare: %q %v", r.Output, err)
	}
	for name, in := range map[string]string{
		"error":     `{"subtype":"error_max_budget_usd","is_error":true,"result":"over budget","total_cost_usd":2.1}`,
		"is_error":  `{"subtype":"success","is_error":true,"result":"boom"}`,
		"no output": `{"subtype":"success","is_error":false,"result":"just words"}`,
		"not json":  `hello`,
		"empty":     ``,
	} {
		if _, err := ParseResult([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r, err = ParseResult([]byte(`{"subtype":"error_max_budget_usd","is_error":true,"result":"over budget","total_cost_usd":2.1}`))
	if err == nil || r.Cost != 2.1 || !strings.Contains(err.Error(), "over budget") {
		t.Errorf("the cost of a failed call must survive: %+v %v", r, err)
	}
}

func TestParseResultReadsTheSessionUsage(t *testing.T) {
	r, err := ParseResult([]byte(`{"type":"result","subtype":"success","is_error":false,"structured_output":{"a":1},"total_cost_usd":0.25,"duration_ms":1500,
		"usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":4000,"cache_creation_input_tokens":500,"service_tier":"standard"}}`))
	u := r.Usage
	if err != nil || u.Input != 120 || u.Output != 30 || u.CacheRead != 4000 || u.CacheWrite != 500 || u.DurationMS != 1500 || u.Cost == nil || *u.Cost != 0.25 || r.Cost != 0.25 {
		t.Errorf("%+v %v", r, err)
	}
	// a session that reported no cost has none: it is not a reported zero
	r, err = ParseResult([]byte(`{"subtype":"success","is_error":false,"structured_output":{"a":1},"usage":{"input_tokens":1,"output_tokens":2}}`))
	if err != nil || r.Usage.Cost != nil || r.Cost != 0 || r.Usage.Input != 1 {
		t.Errorf("%+v %v", r, err)
	}
	// the usage of a failed session survives the error
	r, err = ParseResult([]byte(`{"subtype":"error_max_budget_usd","is_error":true,"result":"over","total_cost_usd":2.1,"usage":{"input_tokens":9,"output_tokens":8}}`))
	if err == nil || r.Usage.Input != 9 || r.Usage.Cost == nil || *r.Usage.Cost != 2.1 {
		t.Errorf("%+v %v", r, err)
	}
}
