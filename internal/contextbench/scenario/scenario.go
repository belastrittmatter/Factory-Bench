// Package scenario loads scenarios (docs/context-bench.md "Scenarios").
package scenario

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
	"unicode/utf8"
)

var Areas = []string{"actors", "journeys", "data", "rules", "screens", "failures", "limits"}

// WorkItem is how FactoryBench groups the runs of one scenario. It never has to be a real GitHub issue.
type WorkItem struct {
	Repo  string `json:"repo"`
	Issue int64  `json:"issue"`
}

var repoRe = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

// Check says what is wrong with a work item, "" when nothing is.
func (w *WorkItem) Check() string {
	switch {
	case w == nil:
		return "work_item is missing"
	case !repoRe.MatchString(w.Repo):
		return fmt.Sprintf("work_item.repo %q must be owner/name", w.Repo)
	case w.Issue < 1:
		return "work_item.issue must be a positive integer"
	}
	return ""
}

// Meta is scenario.json.
type Meta struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	WorkItem *WorkItem `json:"work_item"`
	FirstRun bool      `json:"first_run"`
	MaxTurns int       `json:"max_turns"`
	Build    any       `json:"build"` // for a build command run after the bench; run ignores it
}

type Item struct {
	ID         string `json:"id"`
	Area       string `json:"area"`
	Text       string `json:"text"`
	G4         bool   `json:"g4"`
	Latent     bool   `json:"latent"`
	LatentHint string `json:"latent_hint,omitempty"`
	Technical  bool   `json:"technical"`
	Source     string `json:"source"`
}

type Trap struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Items      []string `json:"items"`
	Text       string   `json:"text"`
	Resolution string   `json:"resolution"`
	Source     string   `json:"source,omitempty"`
}

type Noise struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Truth is truth.json.
type Truth struct {
	Items []Item  `json:"items"`
	Traps []Trap  `json:"traps"`
	Noise []Noise `json:"noise"`
}

// ItemIDs, TrapIDs and NoiseIDs list the ids in file order.
func (t *Truth) ItemIDs() []string  { return ids(t.Items, func(i Item) string { return i.ID }) }
func (t *Truth) TrapIDs() []string  { return ids(t.Traps, func(i Trap) string { return i.ID }) }
func (t *Truth) NoiseIDs() []string { return ids(t.Noise, func(i Noise) string { return i.ID }) }

var idRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func ids[T any](xs []T, id func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = id(x)
	}
	return out
}

// check reports what makes a truth unusable: no items, an empty or reused id, an item outside the areas of the
// map, or a latent item without the hint that tells the person when to state it.
func (t *Truth) check() error {
	if len(t.Items) == 0 {
		return fmt.Errorf("no items")
	}
	seen := map[string]bool{}
	claim := func(id string) error {
		if strings.TrimSpace(id) == "" || seen[id] {
			return fmt.Errorf("id %q is empty or used twice (ids are unique across items, traps and noise)", id)
		}
		seen[id] = true
		return nil
	}
	for _, it := range t.Items {
		if err := claim(it.ID); err != nil {
			return err
		}
		if !slices.Contains(Areas, it.Area) {
			return fmt.Errorf("item %s: area %q is not one of %s", it.ID, it.Area, strings.Join(Areas, ", "))
		}
		if it.Latent && strings.TrimSpace(it.LatentHint) == "" {
			return fmt.Errorf("item %s is latent but has no latent_hint", it.ID)
		}
	}
	for _, tr := range t.Traps {
		if err := claim(tr.ID); err != nil {
			return err
		}
	}
	for _, n := range t.Noise {
		if err := claim(n.ID); err != nil {
			return err
		}
	}
	return nil
}

func (t *Truth) Item(id string) (Item, bool) {
	for _, i := range t.Items {
		if i.ID == id {
			return i, true
		}
	}
	return Item{}, false
}

func (t *Truth) Known(id string) bool {
	return slices.Contains(t.ItemIDs(), id) || slices.Contains(t.TrapIDs(), id) || slices.Contains(t.NoiseIDs(), id)
}

// RevealableKnown reports whether id is an item or a trap id (what `revealed` may hold).
func (t *Truth) RevealableKnown(id string) bool {
	return slices.Contains(t.ItemIDs(), id) || slices.Contains(t.TrapIDs(), id)
}

func (t *Truth) LatentIDs() []string {
	var out []string
	for _, i := range t.Items {
		if i.Latent {
			out = append(out, i.ID)
		}
	}
	return out
}

type File struct {
	Path string // relative to its folder, forward slashes
	Data []byte
}

// Text returns the content when it is text, else "".
func (f File) Text() (string, bool) {
	if utf8.Valid(f.Data) && !bytes.Contains(f.Data, []byte{0}) {
		return string(f.Data), true
	}
	return "", false
}

type Scenario struct {
	Meta
	Dir         string
	Brief       string
	Truth       Truth
	TruthRaw    []byte
	Attachments []File
	Project     string // project.md, "" when absent
	LivingSpecs []File // living-specs/<capability>/spec.md, path relative to living-specs/
	HasCeiling  bool
}

// Load reads a scenario directory and checks what a run needs: a valid scenario.json and work item, a brief, and
// a truth whose ids are unique and whose items can be played and scored.
func Load(dir string) (*Scenario, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Scenario{Dir: dir}
	metaPath := filepath.Join(dir, "scenario.json")
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}
	if err := decodeStrict(metaPath, b, &s.Meta); err != nil {
		return nil, err
	}
	if !idRe.MatchString(s.ID) {
		return nil, fmt.Errorf("%s: id %q must match %s", metaPath, s.ID, idRe)
	}
	if msg := s.WorkItem.Check(); msg != "" {
		return nil, fmt.Errorf("%s: %s", metaPath, msg)
	}
	briefPath := filepath.Join(dir, "brief.md")
	if b, err = os.ReadFile(briefPath); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, fmt.Errorf("%s: the brief is empty", briefPath)
	}
	s.Brief = string(b)
	truthPath := filepath.Join(dir, "truth.json")
	if s.TruthRaw, err = os.ReadFile(truthPath); err != nil {
		return nil, err
	}
	if err := decodeStrict(truthPath, s.TruthRaw, &s.Truth); err != nil {
		return nil, err
	}
	if err := s.Truth.check(); err != nil {
		return nil, fmt.Errorf("%s: %v", truthPath, err)
	}
	if s.Attachments, err = readTree(filepath.Join(dir, "attachments")); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, "project.md")); err == nil {
		s.Project = string(b)
	}
	if s.LivingSpecs, err = readTree(filepath.Join(dir, "living-specs")); err != nil {
		return nil, err
	}
	if st, err := os.Stat(filepath.Join(dir, "ceiling")); err == nil && st.IsDir() {
		s.HasCeiling = true
	}
	return s, nil
}

// Resolve finds a scenario directory from an id under scenariosDir, or from a path that is a directory.
func Resolve(scenariosDir, idOrDir string) (string, error) {
	if st, err := os.Stat(filepath.Join(scenariosDir, idOrDir)); err == nil && st.IsDir() && !strings.ContainsAny(idOrDir, `/\`) {
		return filepath.Join(scenariosDir, idOrDir), nil
	}
	if st, err := os.Stat(idOrDir); err == nil && st.IsDir() {
		return idOrDir, nil
	}
	return "", fmt.Errorf("scenario %q is neither an id under %s nor a directory", idOrDir, scenariosDir)
}

func decodeStrict(name string, b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	if dec.More() {
		return fmt.Errorf("%s: trailing data after the JSON value", name)
	}
	return nil
}

func readTree(dir string) ([]File, error) {
	var out []File
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, File{Path: filepath.ToSlash(rel), Data: data})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}
