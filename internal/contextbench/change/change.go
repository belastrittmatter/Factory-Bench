// Package change reads a change folder, parses its spec deltas and runs gates 1 to 3 (docs/context-bench.md "Change
// folder" and "Scoring").
package change

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Folder is a change folder: path (forward slashes, relative) to content.
type Folder struct {
	Files map[string]string
}

// New returns an empty folder.
func New() *Folder { return &Folder{Files: map[string]string{}} }

// Set adds or replaces a file.
func (f *Folder) Set(path, content string) { f.Files[path] = content }

// Paths lists the paths sorted.
func (f *Folder) Paths() []string {
	out := make([]string, 0, len(f.Files))
	for p := range f.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ReadDir reads every regular file under dir.
func ReadDir(dir string) (*Folder, error) {
	f := New()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		f.Files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return f, err
}

// WriteDir writes the folder under dir.
func (f *Folder) WriteDir(dir string) error {
	for _, p := range f.Paths() {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(f.Files[p]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Render shows the folder to a model: every file under a header, binary files by size only.
func (f *Folder) Render() string {
	var b strings.Builder
	for _, p := range f.Paths() {
		c := f.Files[p]
		b.WriteString("=== " + p + " ===\n")
		if !utf8.ValidString(c) || strings.ContainsRune(c, 0) {
			fmt.Fprintf(&b, "(binary file, %d bytes)\n\n", len(c))
			continue
		}
		b.WriteString(strings.TrimRight(c, "\n") + "\n\n")
	}
	if b.Len() == 0 {
		return "(empty)\n"
	}
	return b.String()
}

// Scenario is one #### Scenario block of a requirement.
type Scenario struct {
	Name string
	When []string // text of every WHEN line
	Then []string
	Raw  []string // the WHEN, THEN and AND lines, for the vague-word check
}

// Requirement is one ### Requirement block of a spec delta.
type Requirement struct {
	ID        string // R1, R2, ... in spec path order then file order
	Spec      string // path of the delta file
	Section   string // ADDED, MODIFIED, REMOVED, or "" when outside a delta section
	Name      string
	Body      string // text before the first scenario
	Scenarios []Scenario
	Fields    map[string]string // **Scope fence:**, **Failure:**, **Visual:**, **Reason:**, ... by lower-case name
}

var (
	sectionRe  = regexp.MustCompile(`^##\s+(ADDED|MODIFIED|REMOVED)\s+Requirements\s*$`)
	reqRe      = regexp.MustCompile(`^###\s+Requirement:\s*(.*?)\s*$`)
	scenRe     = regexp.MustCompile(`^####\s+Scenario:\s*(.*?)\s*$`)
	stepRe     = regexp.MustCompile(`(?i)^\s*(?:[-*+]\s*)?\*\*(WHEN|THEN|AND|GIVEN)\*\*:?\s*(.*?)\s*$`)
	fieldRe    = regexp.MustCompile(`^\s*\*\*([A-Za-z][A-Za-z ]*?):?\*\*:?\s*(.*?)\s*$`)
	shallRe    = regexp.MustCompile(`\b(SHALL|MUST)\b`)
	skipSpecRe = regexp.MustCompile(`skip_specs:\s*true`)
	specPathRe = regexp.MustCompile(`^specs/[^/]+/spec\.md$`)
)

// SkipSpecs reports whether proposal.md says skip_specs: true.
func (f *Folder) SkipSpecs() bool { return skipSpecRe.MatchString(f.Files["proposal.md"]) }

// Requirements parses every requirement of every specs/<capability>/spec.md and numbers them R1, R2, ...
func (f *Folder) Requirements() []Requirement {
	var out []Requirement
	for _, p := range f.Paths() {
		if !specPathRe.MatchString(p) {
			continue
		}
		out = append(out, parseDelta(p, f.Files[p])...)
	}
	for i := range out {
		out[i].ID = fmt.Sprintf("R%d", i+1)
	}
	return out
}

func parseDelta(path, content string) []Requirement {
	var out []Requirement
	section := ""
	var cur *Requirement
	var curScen *Scenario
	var body []string
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(body, "\n"))
			out = append(out, *cur)
		}
		cur, curScen, body = nil, nil, nil
	}
	for _, line := range strings.Split(content, "\n") {
		trim := strings.TrimRight(line, " \t\r")
		if m := sectionRe.FindStringSubmatch(trim); m != nil {
			flush()
			section = m[1]
			continue
		}
		if strings.HasPrefix(trim, "## ") {
			flush()
			section = ""
			continue
		}
		if m := reqRe.FindStringSubmatch(trim); m != nil {
			flush()
			cur = &Requirement{Spec: path, Section: section, Name: m[1], Fields: map[string]string{}}
			continue
		}
		if cur == nil {
			continue
		}
		if m := scenRe.FindStringSubmatch(trim); m != nil {
			cur.Scenarios = append(cur.Scenarios, Scenario{Name: m[1]})
			curScen = &cur.Scenarios[len(cur.Scenarios)-1]
			continue
		}
		if curScen == nil {
			body = append(body, trim)
			if m := fieldRe.FindStringSubmatch(trim); m != nil {
				key := strings.ToLower(strings.TrimSpace(m[1]))
				if _, dup := cur.Fields[key]; !dup {
					cur.Fields[key] = m[2]
				}
			}
			continue
		}
		if m := stepRe.FindStringSubmatch(trim); m != nil {
			curScen.Raw = append(curScen.Raw, trim)
			switch strings.ToUpper(m[1]) {
			case "WHEN":
				curScen.When = append(curScen.When, m[2])
			case "THEN":
				curScen.Then = append(curScen.Then, m[2])
			}
		}
	}
	flush()
	return out
}

// Index renders the requirement ids for a prompt: one line per requirement.
func Index(reqs []Requirement) string {
	if len(reqs) == 0 {
		return "(no requirements)\n"
	}
	var b strings.Builder
	for _, r := range reqs {
		sec := r.Section
		if sec == "" {
			sec = "?"
		}
		fmt.Fprintf(&b, "%s: %s :: %s :: %s\n", r.ID, r.Spec, sec, r.Name)
	}
	return b.String()
}

// IDs lists the requirement ids.
func IDs(reqs []Requirement) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.ID
	}
	return out
}

// Gates is the outcome of gates 1 to 3.
type Gates struct {
	Gate1, Gate2, Gate3 bool
	Failures            []string // why a gate failed, each prefixed "gate N:"
	VagueWarnings       []string // WHEN/THEN/AND lines with a vague word; warnings, not failures
	Notes               []string // format observations that do not fail a gate
	Requirements        int
}

var vagueRe = regexp.MustCompile(`(?i)\b(properly|fast|quickly|easily|appropriate|appropriately|user-friendly|supports?|supported|supporting|handles?|handled|handling)\b|\betc\.`)

var projectSections = []string{"Goal", "Users", "Platforms", "Integrations", "Data and privacy", "Acceptance", "Hard limits"}
var requiredFiles = []string{"proposal.md", "map.md", "decisions.md"}
var proposalSections = []string{"Why", "What Changes", "Capabilities", "Impact"}
var mapSections = []string{"Actors", "Journeys", "Data", "Rules", "Screens", "Failures", "Limits"}

// Check runs gates 1 to 3 on the folder. firstRun is the scenario's first_run.
func Check(f *Folder, firstRun bool) Gates {
	g := Gates{Gate1: true, Gate2: true, Gate3: true}
	fail := func(n int, format string, a ...any) {
		g.Failures = append(g.Failures, fmt.Sprintf("gate %d: ", n)+fmt.Sprintf(format, a...))
		switch n {
		case 1:
			g.Gate1 = false
		case 2:
			g.Gate2 = false
		case 3:
			g.Gate3 = false
		}
	}

	// gate 1
	if firstRun {
		pm, ok := f.Files["project.md"]
		if !ok {
			fail(1, "first run but project.md is missing")
		} else {
			secs := sections(pm, 2)
			for _, want := range projectSections {
				body, found := lookup(secs, want)
				switch {
				case !found:
					fail(1, "project.md has no ## %s section", want)
				case strings.TrimSpace(body) == "":
					fail(1, "project.md section ## %s is empty", want)
				}
			}
		}
	}

	// gate 2
	for _, p := range requiredFiles {
		if c, ok := f.Files[p]; !ok {
			fail(2, "required file %s is missing", p)
		} else if strings.TrimSpace(c) == "" {
			fail(2, "required file %s is empty", p)
		}
	}
	if prop, ok := f.Files["proposal.md"]; ok {
		secs := sections(prop, 2)
		for _, want := range proposalSections {
			if _, found := lookup(secs, want); !found {
				g.Notes = append(g.Notes, fmt.Sprintf("proposal.md has no ## %s section", want))
			}
		}
	}
	if m, ok := f.Files["map.md"]; ok {
		secs := sections(m, 2)
		for _, want := range mapSections {
			if _, found := lookup(secs, want); !found {
				g.Notes = append(g.Notes, fmt.Sprintf("map.md has no ## %s section", want))
			}
		}
		for _, line := range strings.Split(m, "\n") {
			t := strings.TrimSpace(line)
			if (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")) && !tagRe.MatchString(t[2:]) {
				g.Notes = append(g.Notes, "map.md bullet without a source tag: "+t)
			}
		}
	}
	reqs := f.Requirements()
	g.Requirements = len(reqs)
	skip := f.SkipSpecs()
	if len(reqs) == 0 && !skip {
		fail(2, "no requirement in any specs/<capability>/spec.md and proposal.md has no skip_specs: true")
	}
	for _, r := range reqs {
		label := fmt.Sprintf("%s (%s, %q)", r.ID, r.Spec, r.Name)
		switch r.Section {
		case "":
			fail(2, "requirement %s is outside an ## ADDED/MODIFIED/REMOVED Requirements section", label)
			continue
		case "REMOVED":
			for _, field := range []string{"reason", "migration", "effect on existing data"} {
				if strings.TrimSpace(r.Fields[field]) == "" {
					fail(2, "REMOVED requirement %s has no **%s:** line", label, fieldTitle(field))
				}
			}
			continue
		}
		if !shallRe.MatchString(r.Body) {
			fail(2, "requirement %s has neither SHALL nor MUST", label)
		}
		if strings.TrimSpace(r.Fields["scope fence"]) == "" {
			fail(2, "requirement %s has no **Scope fence:**", label)
		}
		if len(r.Scenarios) == 0 {
			fail(2, "requirement %s has no #### Scenario:", label)
		}
	}

	// gate 3
	for _, r := range reqs {
		if r.Section == "REMOVED" {
			continue
		}
		for _, s := range r.Scenarios {
			label := fmt.Sprintf("%s scenario %q", r.ID, s.Name)
			if !nonEmpty(s.When) {
				fail(3, "%s has no non-empty **WHEN** line", label)
			}
			if !nonEmpty(s.Then) {
				fail(3, "%s has no non-empty **THEN** line", label)
			}
			for _, raw := range s.Raw {
				if vagueRe.MatchString(raw) {
					g.VagueWarnings = append(g.VagueWarnings, fmt.Sprintf("%s: %s", label, raw))
				}
			}
		}
	}
	if len(reqs) == 0 && !skip {
		fail(3, "the change has no requirement, so no scenarios to check")
	}
	return g
}

var tagRe = regexp.MustCompile(`^\[(said|accepted|assumed|open)\]`)

func fieldTitle(k string) string {
	switch k {
	case "effect on existing data":
		return "Effect on existing data"
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

func nonEmpty(xs []string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return true
		}
	}
	return false
}

// sections splits markdown at headings of exactly the given level; it returns heading -> body.
func sections(md string, level int) []section {
	var out []section
	prefix := strings.Repeat("#", level) + " "
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimRight(line, " \t\r")
		if strings.HasPrefix(t, prefix) {
			out = append(out, section{title: strings.TrimSpace(t[len(prefix):])})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].body += line + "\n"
		}
	}
	return out
}

type section struct{ title, body string }

// lookup finds a section whose title is want or starts with want (case-insensitive: "Hard limits (P1 to P7)").
func lookup(secs []section, want string) (string, bool) {
	w := strings.ToLower(want)
	for _, s := range secs {
		t := strings.ToLower(s.title)
		if t == w || strings.HasPrefix(t, w+" ") || strings.HasPrefix(t, w+"(") {
			return s.body, true
		}
	}
	return "", false
}
