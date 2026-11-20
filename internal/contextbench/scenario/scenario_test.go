package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tiny = "../fixtures/scenarios/tiny"

// scenarioDir copies the tiny scenario's brief and truth into a new directory and writes scenario.json there.
func scenarioDir(t *testing.T, meta string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"brief.md", "truth.json"} {
		b, _ := os.ReadFile(filepath.Join(tiny, f))
		os.WriteFile(filepath.Join(dir, f), b, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(meta), 0o644)
	return dir
}

const okMeta = `{"id":"x","kind":"feature","title":"t","work_item":{"repo":"a/b","issue":1},"first_run":true,"max_turns":2}`

func TestLoadAndHelpers(t *testing.T) {
	s, err := Load(tiny)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "tiny" || !s.FirstRun || s.MaxTurns != 6 {
		t.Errorf("%+v", s.Meta)
	}
	if s.WorkItem == nil || *s.WorkItem != (WorkItem{Repo: "bench/tiny-context", Issue: 1}) {
		t.Errorf("work item %+v", s.WorkItem)
	}
	if len(s.Truth.Items) != 8 || len(s.Truth.Traps) != 3 || len(s.Truth.Noise) != 2 {
		t.Errorf("truth: %d %d %d", len(s.Truth.Items), len(s.Truth.Traps), len(s.Truth.Noise))
	}
	if got := s.Truth.LatentIDs(); len(got) != 2 || got[0] != "T6" {
		t.Errorf("latent %v", got)
	}
	if !s.Truth.Known("N1") || s.Truth.RevealableKnown("N1") || !s.Truth.RevealableKnown("X2") || s.Truth.Known("ZZ") {
		t.Error("Known / RevealableKnown")
	}
	if len(s.Attachments) != 1 || s.Attachments[0].Path != "notes.txt" || len(s.LivingSpecs) != 1 || s.LivingSpecs[0].Path != "chat-picker/spec.md" {
		t.Errorf("%+v %+v", s.Attachments, s.LivingSpecs)
	}
	if len(s.TruthRaw) == 0 || s.Project != "" || s.HasCeiling {
		t.Error("optional files")
	}
	if _, ok := s.Attachments[0].Text(); !ok {
		t.Error("text attachment reported binary")
	}
	if _, ok := (File{Data: []byte{0xff, 0, 1}}).Text(); ok {
		t.Error("binary reported text")
	}
}

func TestResolve(t *testing.T) {
	scen := "../fixtures/scenarios"
	if d, err := Resolve(scen, "tiny"); err != nil || d != filepath.Join(scen, "tiny") {
		t.Errorf("%v %v", d, err)
	}
	if d, err := Resolve("/nonexistent", tiny); err != nil || d != tiny {
		t.Errorf("a directory path: %v %v", d, err)
	}
	if _, err := Resolve(scen, "nope"); err == nil {
		t.Error("unknown scenario")
	}
}

func TestLoadIsStrict(t *testing.T) {
	dir := scenarioDir(t, strings.Replace(okMeta, `"max_turns":2`, `"max_turns":2,"extra":1`, 1))
	if _, err := Load(dir); err == nil {
		t.Error("an unknown field in scenario.json was accepted")
	}
}

func TestLoadNeedsAValidWorkItem(t *testing.T) {
	for name, wi := range map[string]string{
		"missing":    ``,
		"no repo":    `"work_item":{"repo":"","issue":1},`,
		"not a repo": `"work_item":{"repo":"just-a-name","issue":1},`,
		"no issue":   `"work_item":{"repo":"a/b","issue":0},`,
	} {
		dir := scenarioDir(t, `{"id":"x","kind":"feature","title":"t",`+wi+`"first_run":true,"max_turns":2}`)
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "work_item") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLoadRefusesWhatARunCannotUse(t *testing.T) {
	truth := func(items, rest string) string {
		return `{"items":[` + items + `],"traps":[` + rest + `],"noise":[]}`
	}
	item := func(id, extra string) string {
		return `{"id":"` + id + `","area":"rules","text":"t","g4":false,"latent":false,"technical":false,"source":"s"` + extra + `}`
	}
	for name, tc := range map[string]struct{ truth, want string }{
		"no items":          {truth("", ""), "no items"},
		"duplicate id":      {truth(item("A", "")+","+item("A", ""), ""), "used twice"},
		"id shared by trap": {truth(item("A", ""), `{"id":"A","kind":"vague","items":["A"],"text":"t","resolution":"r"}`), "used twice"},
		"empty id":          {truth(item("", ""), ""), "empty"},
		"unknown area":      {strings.Replace(truth(item("A", ""), ""), `"rules"`, `"mood"`, 1), "area"},
		"latent no hint":    {strings.Replace(truth(item("A", ""), ""), `"latent":false`, `"latent":true`, 1), "latent_hint"},
		"unknown field":     {truth(item("A", `,"extra":1`), ""), "extra"},
	} {
		dir := scenarioDir(t, okMeta)
		os.WriteFile(filepath.Join(dir, "truth.json"), []byte(tc.truth), 0o644)
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	dir := scenarioDir(t, okMeta)
	os.WriteFile(filepath.Join(dir, "brief.md"), []byte(" \n"), 0o644)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "brief is empty") {
		t.Errorf("empty brief: %v", err)
	}
}
