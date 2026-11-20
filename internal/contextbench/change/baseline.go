package change

import (
	"fmt"
	"strings"

	"factorybench/internal/contextbench/scenario"
)

// None is the change folder of the `none` subject: brief.md and the attachments, unchanged.
func None(s *scenario.Scenario) *Folder {
	f := New()
	f.Set("brief.md", s.Brief)
	for _, a := range s.Attachments {
		f.Set("attachments/"+a.Path, string(a.Data))
	}
	return f
}

// Ceiling is the change folder of the `ceiling` subject: scenarios/<id>/ceiling/ when it exists, else one
// rendered from truth.json with every item (and every trap resolution) as a requirement. Noise is left out.
func Ceiling(s *scenario.Scenario) (*Folder, error) {
	if s.HasCeiling {
		return ReadDir(s.Dir + "/ceiling")
	}
	return RenderCeiling(s), nil
}

var areaTitles = map[string]string{
	"actors": "Actors", "journeys": "Journeys", "data": "Data", "rules": "Rules",
	"screens": "Screens", "failures": "Failures", "limits": "Limits",
}

// RenderCeiling renders the change folder a perfect subject would write, straight from truth.json.
func RenderCeiling(s *scenario.Scenario) *Folder {
	f := New()
	t := s.Truth

	var spec strings.Builder
	spec.WriteString("## ADDED Requirements\n\n")
	for _, it := range t.Items {
		spec.WriteString(requirement(it.ID, it.Area, it.Text))
	}
	for _, tr := range t.Traps {
		if strings.TrimSpace(tr.Resolution) == "" || strings.EqualFold(strings.Trim(tr.Resolution, ". "), "I don't know") {
			continue
		}
		spec.WriteString(requirement(tr.ID, "decision", tr.Resolution))
	}
	f.Set("specs/ceiling/spec.md", spec.String())

	f.Set("proposal.md", fmt.Sprintf("## Why\n\n%s\n\n## What Changes\n\nEverything in specs/ceiling/spec.md.\n\n## Capabilities\n\n- ceiling\n\n## Impact\n\nThe whole change.\n", oneLine(s.Title)))

	var m strings.Builder
	for _, area := range scenario.Areas {
		m.WriteString("## " + areaTitles[area] + "\n")
		for _, it := range t.Items {
			if it.Area == area {
				fmt.Fprintf(&m, "- [said] %s\n", oneLine(it.Text))
			}
		}
		m.WriteString("\n")
	}
	f.Set("map.md", m.String())

	var d strings.Builder
	d.WriteString("# Decisions\n\n")
	if len(t.Traps) == 0 {
		d.WriteString("No factory decision was needed.\n")
	}
	for _, tr := range t.Traps {
		fmt.Fprintf(&d, "## %s\n\nProblem: %s\nOptions: as stated.\nChoice: %s\nReason: the person decided.\n\n", tr.ID, oneLine(tr.Text), oneLine(tr.Resolution))
	}
	f.Set("decisions.md", d.String())

	if s.FirstRun {
		f.Set("project.md", ceilingProject(s))
	}
	return f
}

func requirement(id, area, text string) string {
	name := oneLine(text)
	if len(name) > 60 {
		name = name[:60]
	}
	return fmt.Sprintf("### Requirement: %s %s\nThe product SHALL satisfy this: %s\n**Scope fence:** Only what this requirement states; nothing else changes.\n\n#### Scenario: %s holds\n- **WHEN** the case this requirement covers arises\n- **THEN** %s\n\n", id, name, oneLine(text), id, oneLine(text))
}

func ceilingProject(s *scenario.Scenario) string {
	by := map[string][]string{}
	for _, it := range s.Truth.Items {
		by[it.Area] = append(by[it.Area], oneLine(it.Text))
	}
	section := func(title string, lines []string) string {
		if len(lines) == 0 {
			lines = []string{"Nothing in this area was stated."}
		}
		return "## " + title + "\n" + "- " + strings.Join(lines, "\n- ") + "\n\n"
	}
	var b strings.Builder
	b.WriteString(section("Goal", []string{oneLine(s.Title)}))
	b.WriteString(section("Users", by["actors"]))
	b.WriteString(section("Platforms", by["limits"]))
	b.WriteString(section("Integrations", by["data"]))
	b.WriteString(section("Data and privacy", by["data"]))
	b.WriteString(section("Acceptance", by["journeys"]))
	b.WriteString(section("Hard limits", by["limits"]))
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
