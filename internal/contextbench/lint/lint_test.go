package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const td = "../fixtures"

func dirs(root string) Dirs {
	return Dirs{Scenarios: filepath.Join(root, "scenarios"), Calibration: filepath.Join(root, "calibration")}
}

func TestTestdataIsClean(t *testing.T) {
	r := Run(dirs(td))
	if len(r.Errors) != 0 {
		t.Fatalf("%s", strings.Join(r.Errors, "\n"))
	}
}

// bench makes a scratch scenarios directory with one scenario copied from testdata and patched.
func bench(t *testing.T, patch func(dir string)) Dirs {
	t.Helper()
	root := t.TempDir()
	dst := filepath.Join(root, "scenarios", "tiny")
	err := filepath.WalkDir(filepath.Join(td, "scenarios", "tiny"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join(td, "scenarios", "tiny"), p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if patch != nil {
		patch(dst)
	}
	d := dirs(root)
	d.Calibration = filepath.Join(root, "calibration")
	return d
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func truth(items, traps string) string {
	return `{"items":[` + items + `],"traps":[` + traps + `],"noise":[{"id":"N1","text":"n"}]}`
}

const item = `{"id":"T1","area":"rules","text":"x","g4":false,"latent":false,"technical":false,"source":"s"}`

func TestLintMessages(t *testing.T) {
	cases := []struct {
		name  string
		patch func(dir string)
		want  string
	}{
		{"duplicate id across kinds", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(item, `{"id":"T1","kind":"person-only","items":[],"text":"t","resolution":"r"}`))
		}, "ids are unique across items, traps and noise"},
		{"noise id clashes", func(d string) {
			write(t, filepath.Join(d, "truth.json"), `{"items":[`+item+`],"traps":[],"noise":[{"id":"T1","text":"n"}]}`)
		}, "id T1 is used by a item and by a noise"},
		{"latent without hint", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(strings.Replace(item, `"latent":false`, `"latent":true`, 1), ""))
		}, "latent but has no latent_hint"},
		{"trap references a missing item", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(item, `{"id":"X1","kind":"person-only","items":["T9"],"text":"t","resolution":"r"}`))
		}, `references "T9"`},
		{"trap references a trap", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(item, `{"id":"X1","kind":"person-only","items":[],"text":"t","resolution":"r"},{"id":"X2","kind":"person-only","items":["X1"],"text":"t","resolution":"r"}`))
		}, `references "X1", which is not an item`},
		{"bad trap kind", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(item, `{"id":"X1","kind":"mystery","items":[],"text":"t","resolution":"r"}`))
		}, `kind "mystery"`},
		{"empty resolution", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(item, `{"id":"X1","kind":"person-only","items":[],"text":"t","resolution":""}`))
		}, "resolution is empty"},
		{"vague trap needs an item", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(item, `{"id":"X1","kind":"vague","items":[],"text":"t","resolution":"r"}`))
		}, "names the items whose precise values are hidden"},
		{"bad area", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(strings.Replace(item, "rules", "colours", 1), ""))
		}, `area "colours"`},
		{"item without source", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(strings.Replace(item, `"source":"s"`, `"source":""`, 1), ""))
		}, "has no source"},
		{"unknown field", func(d string) {
			write(t, filepath.Join(d, "truth.json"), truth(strings.Replace(item, `"g4":false`, `"g4":false,"surprise":1`, 1), ""))
		}, "surprise"},
		{"missing brief", func(d string) { os.Remove(filepath.Join(d, "brief.md")) }, "brief.md"},
		{"missing truth", func(d string) { os.Remove(filepath.Join(d, "truth.json")) }, "truth.json"},
		{"id differs from directory", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"other","kind":"feature","title":"t","work_item":{"repo":"bench/tiny-context","issue":1},"first_run":true,"max_turns":3}`)
		}, `id "other" differs`},
		{"missing first_run", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"tiny","kind":"feature","title":"t","work_item":{"repo":"bench/tiny-context","issue":1},"max_turns":3}`)
		}, `no "first_run"`},
		{"bad kind", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"tiny","kind":"epic","title":"t","work_item":{"repo":"bench/tiny-context","issue":1},"first_run":true,"max_turns":3}`)
		}, `kind "epic"`},
		{"build without interface.md", func(d string) { os.Remove(filepath.Join(d, "interface.md")) }, "has build but no interface.md"},
		{"empty interface.md", func(d string) { write(t, filepath.Join(d, "interface.md"), "\n") }, "interface.md is empty"},
		{"build is not an object", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"tiny","kind":"feature","title":"t","work_item":{"repo":"bench/tiny-context","issue":1},"first_run":true,"max_turns":3,"build":null}`)
		}, "build must be an object"},
		{"missing work_item", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"tiny","kind":"feature","title":"t","first_run":true,"max_turns":3}`)
		}, `no "work_item"`},
		{"work_item repo is not owner/name", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"tiny","kind":"feature","title":"t","work_item":{"repo":"tiny","issue":1},"first_run":true,"max_turns":3}`)
		}, `work_item.repo "tiny" must be owner/name`},
		{"work_item issue is not positive", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"tiny","kind":"feature","title":"t","work_item":{"repo":"a/b","issue":0},"first_run":true,"max_turns":3}`)
		}, "work_item.issue must be a positive integer"},
		{"bug scenario without reproduction", func(d string) {
			write(t, filepath.Join(d, "scenario.json"), `{"id":"tiny","kind":"bug","title":"t","work_item":{"repo":"bench/tiny-context","issue":1},"first_run":false,"max_turns":3}`)
			write(t, filepath.Join(d, "truth.json"), truth(item, ""))
		}, "failures or journeys"},
		{"living spec without spec.md", func(d string) { write(t, filepath.Join(d, "living-specs", "empty", "notes.md"), "x") }, "living-specs/empty has no spec.md"},
		{"ceiling that fails the gates", func(d string) { write(t, filepath.Join(d, "ceiling", "proposal.md"), "## Why\nx\n") }, "ceiling/ fails its own gates"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Run(bench(t, c.patch))
			if !strings.Contains(strings.Join(r.Errors, "\n"), c.want) {
				t.Errorf("want an error containing %q, got:\n%s", c.want, strings.Join(r.Errors, "\n"))
			}
		})
	}
}

func TestLintCalibration(t *testing.T) {
	var d Dirs
	d = bench(t, nil)
	write(t, filepath.Join(d.Calibration, "c1.json"), `{"id":"c2","scenario":"ghost","persona":"grumpy","turns":[{"question":"q","answer":""}]}`)
	write(t, filepath.Join(d.Calibration, "c3.json"), `{"id":"c3","scenario":"tiny","persona":"unsure","turns":[]}`)
	msgs := strings.Join(Run(d).Errors, "\n")
	for _, want := range []string{`id "c2" differs`, `scenario "ghost" does not exist`, `persona "grumpy"`, "turn 1 has an empty question or answer", "calibration c3: no turns"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in\n%s", want, msgs)
		}
	}
}

func TestNoScenariosIsAnError(t *testing.T) {
	d := dirs(t.TempDir())
	if r := Run(d); len(r.Errors) == 0 {
		t.Error("an empty bench passed")
	}
}

func TestTwoScenariosCannotShareAWorkItem(t *testing.T) {
	d := bench(t, nil)
	other := filepath.Join(d.Scenarios, "other")
	for _, f := range []string{"brief.md", "truth.json"} {
		b, _ := os.ReadFile(filepath.Join(td, "scenarios", "tiny", f))
		write(t, filepath.Join(other, f), string(b))
	}
	write(t, filepath.Join(other, "scenario.json"), `{"id":"other","kind":"feature","title":"t","work_item":{"repo":"bench/tiny-context","issue":1},"first_run":false,"max_turns":3}`)
	msgs := strings.Join(Run(d).Errors, "\n")
	if !strings.Contains(msgs, "work_item bench/tiny-context#1 is also scenario") {
		t.Errorf("%s", msgs)
	}
	write(t, filepath.Join(other, "scenario.json"), `{"id":"other","kind":"feature","title":"t","work_item":{"repo":"bench/tiny-context","issue":2},"first_run":false,"max_turns":3}`)
	if r := Run(d); len(r.Errors) != 0 {
		t.Errorf("%v", r.Errors)
	}
}
