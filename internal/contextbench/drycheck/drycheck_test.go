package drycheck

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

func TestDryRunPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("the dry run starts the fake claude a few hundred times")
	}
	exe, _ := os.Executable()
	fake := filepath.Join(t.TempDir(), fakeclaude.Name)
	if err := os.Symlink(exe, fake); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(Config{Fake: fake, Stdout: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "dry-run ok") {
		t.Errorf("%s", out.String())
	}
}
