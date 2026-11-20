package contextbench

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factorybench/internal/contextbench/fakeclaude"
)

func TestMain(m *testing.M) {
	fakeclaude.MaybeRun()
	os.Exit(m.Run())
}

const fixtures = "fixtures"

func cmd(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("FACTORYBENCH_URL", "")
	t.Setenv("FACTORYBENCH_SPOOL", filepath.Join(t.TempDir(), "spool.jsonl"))
	var out, errb bytes.Buffer
	code = Run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
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

func TestUsageAndUnknownCommand(t *testing.T) {
	if code, out, _ := cmd(t, ""); code != 0 || !strings.Contains(out, "factorybench context run") || !strings.Contains(out, "dry-run") {
		t.Errorf("%d %s", code, out)
	}
	if code, _, errs := cmd(t, "", "frobnicate"); code != 2 || !strings.Contains(errs, "unknown command") {
		t.Errorf("%d %s", code, errs)
	}
}

func TestRunNeedsItsFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"no scenario":  {"run", "--scenarios", fixtures + "/scenarios", "--subject", "none"},
		"no subject":   {"run", "--scenarios", fixtures + "/scenarios", "--scenario", "tiny"},
		"no scenarios": {"run", "--scenario", "tiny", "--subject", "none"},
		"extra arg":    {"run", "--scenarios", fixtures + "/scenarios", "--scenario", "tiny", "--subject", "none", "stray"},
		"bad flag":     {"run", "--frobnicate"},
		"matrix human": {"matrix", "--scenarios", fixtures + "/scenarios", "--scenarios-list", "tiny", "--subjects", "plain", "--personas", "human"},
		"bad turns":    {"matrix", "--scenarios", fixtures + "/scenarios", "--scenarios-list", "tiny", "--subjects", "plain", "--personas", "unsure", "--turns", "x"},
		"retag":        {"retag"},
		"replay":       {"replay", "--scenarios", fixtures + "/scenarios"},
		"lint":         {"lint"},
	} {
		if code, _, _ := cmd(t, "", args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}

func TestRunWithoutYesPrintsThePlanAndCallsNothing(t *testing.T) {
	t.Setenv("FACTORYBENCH_CLAUDE", "/nonexistent/claude")
	out := t.TempDir()
	code, stdout, _ := cmd(t, "", "run", "--scenarios", fixtures+"/scenarios", "--scenario", "tiny", "--subject", "plain", "--person", "unsure", "--out", filepath.Join(out, "runs"))
	if code != 0 || !strings.Contains(stdout, "plan") || !strings.Contains(stdout, "add --yes") {
		t.Fatalf("%d %s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(out, "runs")); !os.IsNotExist(err) {
		t.Error("a plan wrote the output directory")
	}
	code, stdout, _ = cmd(t, "", "matrix", "--scenarios", fixtures+"/scenarios", "--scenarios-list", "all", "--subjects", "none,plain", "--personas", "all", "--turns", "4,8")
	if code != 0 || !strings.Contains(stdout, "plan (nothing is called without --yes): 9 runs") {
		t.Errorf("%d %s", code, stdout)
	}
	code, stdout, _ = cmd(t, "", "retag", "--run", t.TempDir())
	if code == 0 {
		t.Errorf("retag of an empty directory: %s", stdout)
	}
}

func TestLintTheFixtureScenario(t *testing.T) {
	code, out, _ := cmd(t, "", "lint", "--scenarios", fixtures+"/scenarios")
	if code != 0 || !strings.Contains(out, "lint ok: 1 scenarios") {
		t.Errorf("%d %s", code, out)
	}
	code, out, _ = cmd(t, "", "lint", "--scenarios", t.TempDir())
	if code != 1 || !strings.Contains(out, "no scenario") {
		t.Errorf("%d %s", code, out)
	}
}

func TestRunAndMatrixWithTheFakeClaude(t *testing.T) {
	t.Setenv("FACTORYBENCH_CLAUDE", fakeProgram(t))
	out := filepath.Join(t.TempDir(), "runs")
	code, stdout, errs := cmd(t, "", "run", "--scenarios", fixtures+"/scenarios", "--scenario", "tiny", "--subject", "ceiling", "--out", out, "--yes")
	if code != 0 || !strings.Contains(stdout, "status completed") || !strings.Contains(stdout, "bench cost") {
		t.Fatalf("%d %s %s", code, stdout, errs)
	}
	code, stdout, errs = cmd(t, "", "matrix", "--scenarios", fixtures+"/scenarios", "--scenarios-list", "tiny", "--subjects", "none,ceiling", "--out", out, "--yes")
	if code != 0 || !strings.Contains(stdout, "2 runs, 0 not completed") {
		t.Fatalf("%d %s %s", code, stdout, errs)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 3 {
		t.Errorf("%d run directories", len(ents))
	}
	code, stdout, errs = cmd(t, "", "replay", "--scenarios", fixtures+"/scenarios", "--calibration", "tiny-q", "--out", out, "--yes")
	if code != 0 || !strings.Contains(stdout, "calibration.json") || !strings.Contains(stdout, "agreement") {
		t.Fatalf("%d %s %s", code, stdout, errs)
	}
}
