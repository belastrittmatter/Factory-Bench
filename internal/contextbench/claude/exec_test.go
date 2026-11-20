package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func program(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

var judgeRequest = Request{Role: "judge", Model: "m", Prompt: "p", Schema: `{}`, MaxCost: 1}

func TestExecRunsTheProgramInAnEmptyDirectoryAndParsesItsResult(t *testing.T) {
	prog := program(t, `echo "{\"subtype\":\"success\",\"is_error\":false,\"total_cost_usd\":0.5,\"structured_output\":{\"entries\":$(ls -A | wc -l)}}"`)
	resp, err := Exec{Program: prog}.Run(context.Background(), judgeRequest)
	if err != nil || resp.Cost != 0.5 || string(resp.Output) != `{"entries":0}` {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestExecReportsAFailedCallWithItsCost(t *testing.T) {
	prog := program(t, `echo '{"subtype":"error_during_execution","is_error":true,"result":"it broke","total_cost_usd":0.25}'; exit 1`)
	resp, err := Exec{Program: prog}.Run(context.Background(), judgeRequest)
	if err == nil || resp.Cost != 0.25 || !strings.Contains(err.Error(), "it broke") {
		t.Errorf("%+v %v", resp, err)
	}
	if _, err := (Exec{Program: program(t, `echo oops >&2; exit 3`)}).Run(context.Background(), judgeRequest); err == nil || !strings.Contains(err.Error(), "oops") {
		t.Errorf("a program that prints no result: %v", err)
	}
	if _, err := (Exec{Program: "/nonexistent/claude"}).Run(context.Background(), judgeRequest); err == nil {
		t.Error("a missing program is an error")
	}
}

func TestProgramComesFromTheEnvironment(t *testing.T) {
	t.Setenv("FACTORYBENCH_CLAUDE", "")
	if Program() != "claude" {
		t.Error("default program")
	}
	t.Setenv("FACTORYBENCH_CLAUDE", "/x/fake")
	if Program() != "/x/fake" {
		t.Error("FACTORYBENCH_CLAUDE")
	}
}
