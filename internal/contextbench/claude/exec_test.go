package claude_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/fakeclaude"
)

func TestMain(m *testing.M) {
	fakeclaude.MaybeRun()
	os.Exit(m.Run())
}

func fakeProgram(t *testing.T) string {
	t.Helper()
	exe, _ := os.Executable()
	p := filepath.Join(t.TempDir(), fakeclaude.Name)
	if err := os.Symlink(exe, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunsTheProgramAndParsesItsResult(t *testing.T) {
	prog := fakeProgram(t)
	log := filepath.Join(t.TempDir(), "log.jsonl")
	t.Setenv("FACTORYBENCH_FAKE_LOG", log)
	req := claude.Request{Role: "comparison", Model: "m1", System: "s", Prompt: "<requirements>\nR1: a\nR2: b\n</requirements>", Schema: `{"type":"object"}`, MaxCost: 1.5}
	resp, err := claude.Exec{Program: prog}.Run(context.Background(), req)
	if err != nil || resp.Cost != 0.01 || !strings.Contains(string(resp.Output), `"R2"`) {
		t.Fatalf("%+v %v", resp, err)
	}
	entries, _ := fakeclaude.ReadLog(log)
	if len(entries) != 1 || entries[0].Model != "m1" || entries[0].Flags["max-budget-usd"] != "1.5" || entries[0].Role != "comparison" || !strings.HasPrefix(entries[0].System, "ROLE: comparison\n") {
		t.Errorf("%+v", entries)
	}
}

func TestExecSendsALongPromptThroughStdin(t *testing.T) {
	prog := fakeProgram(t)
	log := filepath.Join(t.TempDir(), "log.jsonl")
	t.Setenv("FACTORYBENCH_FAKE_LOG", log)
	prompt := "<requirements>\nR1: a\n</requirements>\n" + strings.Repeat("padding ", claude.MaxArgBytes/8+10)
	if _, err := (claude.Exec{Program: prog}).Run(context.Background(), claude.Request{Role: "builder", Model: "m", Prompt: prompt, Schema: `{}`, MaxCost: 1}); err != nil {
		t.Fatal(err)
	}
	entries, _ := fakeclaude.ReadLog(log)
	if len(entries) != 1 || entries[0].Prompt != prompt {
		t.Error("the long prompt did not arrive intact")
	}
}

func TestExecReportsAFailedCallWithItsCost(t *testing.T) {
	prog := fakeProgram(t)
	t.Setenv("FACTORYBENCH_FAKE_FAIL", "judge")
	resp, err := claude.Exec{Program: prog}.Run(context.Background(), claude.Request{Role: "judge", Model: "m", Prompt: "p", Schema: `{}`, MaxCost: 1})
	if err == nil || resp.Cost != 0.01 || !strings.Contains(err.Error(), "fake failure") {
		t.Errorf("%+v %v", resp, err)
	}
	if _, err := (claude.Exec{Program: "/nonexistent/claude"}).Run(context.Background(), claude.Request{Role: "judge", Model: "m", Prompt: "p", Schema: `{}`, MaxCost: 1}); err == nil {
		t.Error("a missing program is an error")
	}
}

func TestProgramComesFromTheEnvironment(t *testing.T) {
	t.Setenv("FACTORYBENCH_CLAUDE", "")
	if claude.Program() != "claude" {
		t.Error("default program")
	}
	t.Setenv("FACTORYBENCH_CLAUDE", "/x/fake")
	if claude.Program() != "/x/fake" {
		t.Error("FACTORYBENCH_CLAUDE")
	}
}
