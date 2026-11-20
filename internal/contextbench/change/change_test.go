package change

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factorybench/internal/contextbench/scenario"
)

const fixtures = "../fixtures/changes"

func TestGatesOnKnownGoodAndBadFolders(t *testing.T) {
	vs, err := LoadVariants(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) < 10 {
		t.Fatalf("only %d fixtures", len(vs))
	}
	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			g := Check(v.Folder, true)
			got := Expect{Gate1: g.Gate1, Gate2: g.Gate2, Gate3: g.Gate3, VagueWarnings: len(g.VagueWarnings)}
			if got != v.Expect {
				t.Errorf("got %+v, want %+v\nfailures: %s", got, v.Expect, strings.Join(g.Failures, "\n  "))
			}
			if !got.Gate1 || !got.Gate2 || !got.Gate3 {
				if len(g.Failures) == 0 {
					t.Error("a failed gate has no message")
				}
			}
		})
	}
}

func TestGate1OnlyOnFirstRun(t *testing.T) {
	vs, _ := LoadVariants(fixtures)
	for _, v := range vs {
		if v.Name == "no-project" {
			if g := Check(v.Folder, false); !g.Gate1 {
				t.Error("gate 1 must not apply when first_run is false")
			}
			if g := Check(v.Folder, true); g.Gate1 {
				t.Error("gate 1 must fail without project.md on a first run")
			}
		}
	}
}

func TestVagueWordsAreWarningsOnly(t *testing.T) {
	vs, _ := LoadVariants(fixtures)
	for _, v := range vs {
		if v.Name != "good-vague" {
			continue
		}
		g := Check(v.Folder, true)
		if !g.Gate3 || len(g.VagueWarnings) != 1 || !strings.Contains(g.VagueWarnings[0], "properly") {
			t.Errorf("vague warnings: %v gate3 %v", g.VagueWarnings, g.Gate3)
		}
	}
	for _, w := range []string{"properly", "fast", "quickly", "easily", "etc.", "appropriate", "user-friendly", "supports", "handle"} {
		if !vagueRe.MatchString("- **THEN** the app is " + w + " here") {
			t.Errorf("vague word %q is not caught", w)
		}
	}
	if vagueRe.MatchString("- **THEN** the sender is shown") {
		t.Error("false positive")
	}
}

func TestRequirementsAreNumberedAcrossSpecs(t *testing.T) {
	f := New()
	spec := func(names ...string) string {
		s := "## ADDED Requirements\n\n"
		for _, n := range names {
			s += "### Requirement: " + n + "\nThe app SHALL x.\n**Scope fence:** y\n\n#### Scenario: s\n- **WHEN** a\n- **THEN** b\n\n"
		}
		return s
	}
	f.Set("specs/b/spec.md", spec("B1"))
	f.Set("specs/a/spec.md", spec("A1", "A2"))
	f.Set("specs/a/notes.md", spec("ignored"))
	reqs := f.Requirements()
	if len(reqs) != 3 || reqs[0].Name != "A1" || reqs[1].ID != "R2" || reqs[2].Name != "B1" || reqs[2].ID != "R3" {
		t.Errorf("%+v", reqs)
	}
	if !strings.Contains(Index(reqs), "R3: specs/b/spec.md :: ADDED :: B1") {
		t.Errorf("index:\n%s", Index(reqs))
	}
}

func scenarioFor(t *testing.T, first bool) *scenario.Scenario {
	t.Helper()
	s, err := scenario.Load("../fixtures/scenarios/tiny")
	if err != nil {
		t.Fatal(err)
	}
	s.FirstRun = first
	return s
}

func TestCeilingRenderedFromTruthPassesTheGates(t *testing.T) {
	s := scenarioFor(t, true)
	f, err := Ceiling(s)
	if err != nil {
		t.Fatal(err)
	}
	g := Check(f, true)
	if !g.Gate1 || !g.Gate2 || !g.Gate3 {
		t.Fatalf("the rendered ceiling fails its gates: %v", g.Failures)
	}
	all := f.Render()
	for _, it := range s.Truth.Items {
		if !strings.Contains(all, it.Text) {
			t.Errorf("item %s missing from the ceiling", it.ID)
		}
	}
	for _, n := range s.Truth.Noise {
		if strings.Contains(all, n.Text) {
			t.Errorf("noise %s is in the ceiling", n.ID)
		}
	}
	if len(f.Requirements()) != len(s.Truth.Items)+len(s.Truth.Traps) {
		t.Errorf("%d requirements for %d items and %d traps", len(f.Requirements()), len(s.Truth.Items), len(s.Truth.Traps))
	}
	if f2, _ := Ceiling(scenarioFor(t, false)); f2.Files["project.md"] != "" {
		t.Error("a non-first-run ceiling has no project.md")
	}
}

func TestCeilingFolderOfTheScenarioWins(t *testing.T) {
	s := scenarioFor(t, false)
	dir := t.TempDir()
	s.Dir, s.HasCeiling = dir, true
	if err := os.MkdirAll(filepath.Join(dir, "ceiling"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ceiling", "proposal.md"), []byte("hand written"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Ceiling(s)
	if err != nil || f.Files["proposal.md"] != "hand written" || len(f.Files) != 1 {
		t.Errorf("%v %v", f, err)
	}
}

func TestNoneIsBriefAndAttachments(t *testing.T) {
	f := None(scenarioFor(t, true))
	if f.Files["brief.md"] == "" || f.Files["attachments/notes.txt"] == "" || len(f.Files) != 2 {
		t.Errorf("%v", f.Paths())
	}
	g := Check(f, true)
	if g.Gate1 || g.Gate2 || g.Gate3 {
		t.Errorf("the none subject must fail every code gate: %+v", g)
	}
}

func TestRenderAndRoundTrip(t *testing.T) {
	f := New()
	f.Set("b.md", "two\n")
	f.Set("a.md", "one")
	f.Set("bin", string([]byte{0xff, 0xfe, 0}))
	r := f.Render()
	if !strings.Contains(r, "=== a.md ===\none\n") || !strings.Contains(r, "(binary file, 3 bytes)") || strings.Index(r, "a.md") > strings.Index(r, "b.md") {
		t.Errorf("render:\n%s", r)
	}
	dir := t.TempDir()
	if err := f.WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	g, err := ReadDir(dir)
	if err != nil || len(g.Files) != 3 || g.Files["a.md"] != "one" {
		t.Errorf("%v %v", g, err)
	}
}
