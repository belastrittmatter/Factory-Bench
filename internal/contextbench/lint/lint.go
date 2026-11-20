// Package lint checks scenarios and calibration files against docs/context-bench.md without calling a model.
package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"factorybench/internal/contextbench/change"
	"factorybench/internal/contextbench/prompts"
	"factorybench/internal/contextbench/scenario"
)

type Dirs struct {
	Scenarios   string
	Calibration string // calibration/<id>.json; a missing directory is fine
}

type Report struct {
	Errors   []string
	Warnings []string
}

func (r *Report) errf(where, format string, a ...any) {
	r.Errors = append(r.Errors, where+": "+fmt.Sprintf(format, a...))
}

func (r *Report) warnf(where, format string, a ...any) {
	r.Warnings = append(r.Warnings, where+": "+fmt.Sprintf(format, a...))
}

func Run(d Dirs) Report {
	var r Report
	ids, err := scenario.AllIDs(d.Scenarios)
	if err != nil {
		r.errf(d.Scenarios, "cannot list scenarios: %v", err)
	}
	if err == nil && len(ids) == 0 {
		r.errf(d.Scenarios, "no scenario (a directory with scenario.json) found")
	}
	known := map[string]bool{}
	workItems := map[scenario.WorkItem]string{}
	for _, id := range ids {
		known[id] = true
		if wi := checkScenario(&r, filepath.Join(d.Scenarios, id), id); wi != nil {
			if prev, dup := workItems[*wi]; dup {
				r.errf("scenario "+id, "work_item %s#%d is also scenario %s's: the runs of one scenario are grouped by work item", wi.Repo, wi.Issue, prev)
			}
			workItems[*wi] = id
		}
	}
	checkCalibration(&r, d, known)
	sort.Strings(r.Warnings)
	return r
}

var required = []string{"id", "kind", "title", "work_item", "first_run", "max_turns"}

func checkScenario(r *Report, dir, id string) *scenario.WorkItem {
	where := "scenario " + id
	raw, err := os.ReadFile(filepath.Join(dir, "scenario.json"))
	if err != nil {
		r.errf(where, "scenario.json: %v", err)
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		r.errf(where, "scenario.json is not a JSON object: %v", err)
		return nil
	}
	for _, k := range required {
		if _, ok := keys[k]; !ok {
			r.errf(where, "scenario.json has no %q", k)
		}
	}
	var meta scenario.Meta
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&meta); err != nil {
		r.errf(where, "scenario.json: %v", err)
		return nil
	}
	if meta.ID != id {
		r.errf(where, "scenario.json id %q differs from the directory name", meta.ID)
	}
	if meta.Kind != "feature" && meta.Kind != "bug" {
		r.errf(where, "kind %q is not feature or bug", meta.Kind)
	}
	if strings.TrimSpace(meta.Title) == "" {
		r.errf(where, "title is empty")
	}
	if meta.MaxTurns < 1 {
		r.errf(where, "max_turns must be at least 1")
	}
	var wi *scenario.WorkItem
	if msg := meta.WorkItem.Check(); msg != "" {
		if _, ok := keys["work_item"]; ok {
			r.errf(where, "%s", msg)
		}
	} else {
		wi = meta.WorkItem
	}
	if _, ok := keys["build"]; ok && !meta.Buildable() {
		r.errf(where, "build must be an object (it is passed to the build command untouched), not null")
	}

	if b, err := os.ReadFile(filepath.Join(dir, "brief.md")); err != nil {
		r.errf(where, "brief.md: %v", err)
	} else if strings.TrimSpace(string(b)) == "" {
		r.errf(where, "brief.md is empty")
	}

	var truth scenario.Truth
	traw, err := os.ReadFile(filepath.Join(dir, "truth.json"))
	if err != nil {
		r.errf(where, "truth.json: %v", err)
	} else {
		td := json.NewDecoder(bytes.NewReader(traw))
		td.DisallowUnknownFields()
		if err := td.Decode(&truth); err != nil {
			r.errf(where, "truth.json: %v", err)
		} else {
			checkTruth(r, where, meta, &truth)
		}
	}

	if st, err := os.Stat(filepath.Join(dir, "living-specs")); err == nil && st.IsDir() {
		ents, _ := os.ReadDir(filepath.Join(dir, "living-specs"))
		for _, e := range ents {
			if e.IsDir() {
				if _, err := os.Stat(filepath.Join(dir, "living-specs", e.Name(), "spec.md")); err != nil {
					r.errf(where, "living-specs/%s has no spec.md", e.Name())
				}
			} else {
				r.errf(where, "living-specs/%s: living specs are living-specs/<capability>/spec.md", e.Name())
			}
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "ceiling")); err == nil && st.IsDir() {
		if f, err := change.ReadDir(filepath.Join(dir, "ceiling")); err != nil {
			r.errf(where, "ceiling: %v", err)
		} else {
			g := change.Check(f, meta.FirstRun)
			for _, fl := range g.Failures {
				r.errf(where, "ceiling/ fails its own gates: %s", fl)
			}
		}
	}
	checkInterface(r, where, dir, meta)
	return wi
}

var latentIDRe = regexp.MustCompile(`^\S+$`)

func checkTruth(r *Report, where string, meta scenario.Meta, t *scenario.Truth) {
	where += " truth.json"
	if len(t.Items) == 0 {
		r.errf(where, "no items")
	}
	seen := map[string]string{}
	claim := func(kind, id string) {
		if id == "" || !latentIDRe.MatchString(id) {
			r.errf(where, "%s has an empty or space-containing id %q", kind, id)
			return
		}
		if prev, dup := seen[id]; dup {
			r.errf(where, "id %s is used by a %s and by a %s: ids are unique across items, traps and noise", id, prev, kind)
		}
		seen[id] = kind
	}
	itemIDs := map[string]bool{}
	for _, it := range t.Items {
		claim("item", it.ID)
		itemIDs[it.ID] = true
		if !slices.Contains(scenario.Areas, it.Area) {
			r.errf(where, "item %s: area %q is not one of %s", it.ID, it.Area, strings.Join(scenario.Areas, ", "))
		}
		if strings.TrimSpace(it.Text) == "" {
			r.errf(where, "item %s: text is empty", it.ID)
		}
		if it.Latent && strings.TrimSpace(it.LatentHint) == "" {
			r.errf(where, "item %s is latent but has no latent_hint", it.ID)
		}
		if !it.Latent && it.LatentHint != "" {
			r.warnf(where, "item %s has a latent_hint but is not latent", it.ID)
		}
		if strings.TrimSpace(it.Source) == "" {
			r.errf(where, "item %s has no source", it.ID)
		}
	}
	for _, tr := range t.Traps {
		claim("trap", tr.ID)
		if !slices.Contains(scenario.TrapKinds, tr.Kind) {
			r.errf(where, "trap %s: kind %q is not one of %s", tr.ID, tr.Kind, strings.Join(scenario.TrapKinds, ", "))
		}
		if strings.TrimSpace(tr.Text) == "" {
			r.errf(where, "trap %s: text is empty", tr.ID)
		}
		if strings.TrimSpace(tr.Resolution) == "" {
			r.errf(where, `trap %s: resolution is empty (use "I don't know" when the person does not know)`, tr.ID)
		}
		for _, ref := range tr.Items {
			if !itemIDs[ref] {
				r.errf(where, "trap %s references %q, which is not an item", tr.ID, ref)
			}
		}
		switch tr.Kind {
		case "contradiction":
			if len(tr.Items) == 0 {
				r.errf(where, "trap %s: a contradiction names the items (or the item) that cannot both hold", tr.ID)
			}
		case "vague":
			if len(tr.Items) == 0 {
				r.errf(where, "trap %s: a vague trap names the items whose precise values are hidden", tr.ID)
			}
		}
	}
	for _, n := range t.Noise {
		claim("noise", n.ID)
		if strings.TrimSpace(n.Text) == "" {
			r.errf(where, "noise %s: text is empty", n.ID)
		}
	}
	if meta.Kind == "bug" {
		ok := false
		for _, it := range t.Items {
			if it.Area == "failures" || it.Area == "journeys" {
				ok = true
			}
		}
		if !ok {
			r.errf(where, "a bug scenario needs items in failures or journeys (reproduction steps, expected result, actual result)")
		}
	}
}

func checkInterface(r *Report, where, dir string, meta scenario.Meta) {
	b, err := os.ReadFile(filepath.Join(dir, "interface.md"))
	switch {
	case !meta.Buildable():
		if err == nil {
			r.warnf(where, "interface.md is only used with a build section")
		}
	case err != nil:
		r.errf(where, "has build but no interface.md")
	case strings.TrimSpace(string(b)) == "":
		r.errf(where, "interface.md is empty")
	}
}

func checkCalibration(r *Report, d Dirs, scenarios map[string]bool) {
	ents, err := os.ReadDir(d.Calibration)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		where := "calibration " + id
		c, err := scenario.LoadCalibration(filepath.Join(d.Calibration, e.Name()))
		if err != nil {
			r.errf(where, "%v", err)
			continue
		}
		if c.ID != id {
			r.errf(where, "id %q differs from the file name", c.ID)
		}
		if !scenarios[c.Scenario] {
			r.errf(where, "scenario %q does not exist under %s", c.Scenario, d.Scenarios)
		}
		if !slices.Contains(prompts.Personas, c.Persona) {
			r.errf(where, "persona %q is not one of %s", c.Persona, strings.Join(prompts.Personas, ", "))
		}
		if len(c.Turns) == 0 {
			r.errf(where, "no turns")
		}
		for i, t := range c.Turns {
			if strings.TrimSpace(t.Question) == "" || strings.TrimSpace(t.Answer) == "" {
				r.errf(where, "turn %d has an empty question or answer", i+1)
			}
		}
	}
}
