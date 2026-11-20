package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tiny = "../fixtures/scenarios/tiny"

func TestLoadAndHelpers(t *testing.T) {
	s, err := Load(tiny)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "tiny" || !s.FirstRun || s.MaxTurns != 6 || !s.Buildable() || string(s.Build) != `{"chain": "tiny-chain", "base": "abc1234"}` {
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
	if s.InterfaceMD == "" || len(s.TruthRaw) == 0 || s.Project != "" {
		t.Error("optional files")
	}
	if _, ok := s.Attachments[0].Text(); !ok {
		t.Error("text attachment reported binary")
	}
	if _, ok := (File{Data: []byte{0xff, 0, 1}}).Text(); ok {
		t.Error("binary reported text")
	}
}

func TestResolveAndAllIDs(t *testing.T) {
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
	ids, err := AllIDs(scen)
	if err != nil || len(ids) != 1 || ids[0] != "tiny" {
		t.Errorf("%v %v", ids, err)
	}
}

func TestLoadIsStrict(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"scenario.json", "brief.md", "truth.json"} {
		b, _ := os.ReadFile(filepath.Join(tiny, f))
		os.WriteFile(filepath.Join(dir, f), b, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(`{"id":"x","kind":"feature","title":"t","work_item":{"repo":"a/b","issue":1},"first_run":true,"max_turns":2,"extra":1}`), 0o644)
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
		dir := t.TempDir()
		for _, f := range []string{"brief.md", "truth.json"} {
			b, _ := os.ReadFile(filepath.Join(tiny, f))
			os.WriteFile(filepath.Join(dir, f), b, 0o644)
		}
		os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(`{"id":"x","kind":"feature","title":"t",`+wi+`"first_run":true,"max_turns":2}`), 0o644)
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "work_item") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBuildIsOpaque(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"brief.md", "truth.json"} {
		b, _ := os.ReadFile(filepath.Join(tiny, f))
		os.WriteFile(filepath.Join(dir, f), b, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(`{"id":"x","kind":"feature","title":"t","work_item":{"repo":"a/b","issue":1},"first_run":true,"max_turns":2,"build":{"anything":[1,2],"goes":null}}`), 0o644)
	s, err := Load(dir)
	if err != nil || !s.Buildable() || !strings.Contains(string(s.Build), `"anything"`) {
		t.Errorf("%+v %v", s, err)
	}
	os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(`{"id":"x","kind":"feature","title":"t","work_item":{"repo":"a/b","issue":1},"first_run":true,"max_turns":2}`), 0o644)
	if s, err := Load(dir); err != nil || s.Buildable() {
		t.Errorf("no build section: %+v %v", s, err)
	}
}

func TestCalibration(t *testing.T) {
	c, err := LoadCalibration("../fixtures/calibration/tiny-q.json")
	if err != nil || c.ID != "tiny-q" || len(c.Turns) != 3 || c.Persona != "decided-technical" {
		t.Errorf("%+v %v", c, err)
	}
}
