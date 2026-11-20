package contextbench

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtures = "fixtures"

func cmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("FACTORYBENCH_URL", "")
	t.Setenv("FACTORYBENCH_SPOOL", filepath.Join(t.TempDir(), "spool.jsonl"))
	var out, errb bytes.Buffer
	code = Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageAndUnknownCommand(t *testing.T) {
	if code, out, _ := cmd(t); code != 0 || !strings.Contains(out, "factorybench context run") {
		t.Errorf("%d %s", code, out)
	}
}

func TestRunNeedsItsFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"no scenario":  {"run", "--scenarios", fixtures + "/scenarios", "--subject", "none"},
		"no subject":   {"run", "--scenarios", fixtures + "/scenarios", "--scenario", "tiny"},
		"no scenarios": {"run", "--scenario", "tiny", "--subject", "none"},
		"extra arg":    {"run", "--scenarios", fixtures + "/scenarios", "--scenario", "tiny", "--subject", "none", "stray"},
		"bad flag":     {"run", "--frobnicate"},
	} {
		if code, _, _ := cmd(t, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}

func TestRunWithoutYesPrintsThePlanAndCallsNothing(t *testing.T) {
	t.Setenv("FACTORYBENCH_CLAUDE", "/nonexistent/claude")
	out := t.TempDir()
	code, stdout, _ := cmd(t, "run", "--scenarios", fixtures+"/scenarios", "--scenario", "tiny", "--subject", "plain", "--person", "unsure", "--out", filepath.Join(out, "runs"))
	if code != 0 || !strings.Contains(stdout, "plan") || !strings.Contains(stdout, "add --yes") {
		t.Fatalf("%d %s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(out, "runs")); !os.IsNotExist(err) {
		t.Error("a plan wrote the output directory")
	}
}

func TestRunReportsWhatItCannotUse(t *testing.T) {
	code, _, errs := cmd(t, "run", "--scenarios", fixtures+"/scenarios", "--scenario", "nope", "--subject", "none")
	if code != 1 || !strings.Contains(errs, "nope") {
		t.Errorf("%d %s", code, errs)
	}
}
