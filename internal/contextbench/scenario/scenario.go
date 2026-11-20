// Package scenario loads scenarios and calibration files (docs/context-bench.md "Scenarios").
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

// Areas, trap kinds and persona ids are fixed by docs/context-bench.md.
var (
	Areas       = []string{"actors", "journeys", "data", "rules", "screens", "failures", "limits"}
	TrapKinds   = []string{"contradiction", "person-only", "vague"}
	PersonaIDs  = []string{"decided-technical", "decided-nontechnical", "unsure", "talkative"}
	HumanPerson = "human"
)

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

// Meta is scenario.json. Build is opaque: it is handed to the build command untouched (the command reads
// scenario.json itself), and its presence says the scenario can be built.
type Meta struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Title    string          `json:"title"`
	WorkItem *WorkItem       `json:"work_item"`
	FirstRun bool            `json:"first_run"`
	MaxTurns int             `json:"max_turns"`
	Build    json.RawMessage `json:"build,omitempty"`
}

// Buildable reports whether scenario.json has a build section.
func (m Meta) Buildable() bool { return len(m.Build) > 0 && string(m.Build) != "null" }

// Item is one thing the person really wants.
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

// Trap is a seeded trap.
type Trap struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Items      []string `json:"items"`
	Text       string   `json:"text"`
	Resolution string   `json:"resolution"`
	Source     string   `json:"source,omitempty"`
}

// Noise is unrelated material the talkative persona volunteers.
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

func ids[T any](xs []T, id func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = id(x)
	}
	return out
}

// Item returns the item with the id.
func (t *Truth) Item(id string) (Item, bool) {
	for _, i := range t.Items {
		if i.ID == id {
			return i, true
		}
	}
	return Item{}, false
}

// Known reports whether id is an item, a trap or a noise id.
func (t *Truth) Known(id string) bool {
	return slices.Contains(t.ItemIDs(), id) || slices.Contains(t.TrapIDs(), id) || slices.Contains(t.NoiseIDs(), id)
}

// RevealableKnown reports whether id is an item or a trap id (what `revealed` may hold).
func (t *Truth) RevealableKnown(id string) bool {
	return slices.Contains(t.ItemIDs(), id) || slices.Contains(t.TrapIDs(), id)
}

// LatentIDs lists the latent items.
func (t *Truth) LatentIDs() []string {
	var out []string
	for _, i := range t.Items {
		if i.Latent {
			out = append(out, i.ID)
		}
	}
	return out
}

// File is a text or binary file of a scenario.
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

// Scenario is a loaded scenario directory.
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
	InterfaceMD string
}

// Load reads a scenario directory. It does not lint: Lint reports what is wrong with the files.
func Load(dir string) (*Scenario, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Scenario{Dir: dir}
	if err := readJSONStrict(filepath.Join(dir, "scenario.json"), &s.Meta); err != nil {
		return nil, err
	}
	if msg := s.WorkItem.Check(); msg != "" {
		return nil, fmt.Errorf("%s: %s", filepath.Join(dir, "scenario.json"), msg)
	}
	b, err := os.ReadFile(filepath.Join(dir, "brief.md"))
	if err != nil {
		return nil, err
	}
	s.Brief = string(b)
	if s.TruthRaw, err = os.ReadFile(filepath.Join(dir, "truth.json")); err != nil {
		return nil, err
	}
	if err := readJSONStrict(filepath.Join(dir, "truth.json"), &s.Truth); err != nil {
		return nil, err
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
	if b, err := os.ReadFile(filepath.Join(dir, "interface.md")); err == nil {
		s.InterfaceMD = string(b)
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

// AllIDs lists the scenario ids (directories holding scenario.json) under scenariosDir.
func AllIDs(scenariosDir string) ([]string, error) {
	ents, err := os.ReadDir(scenariosDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(scenariosDir, e.Name(), "scenario.json")); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func readJSONStrict(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	if dec.More() {
		return fmt.Errorf("%s: trailing data after the JSON value", path)
	}
	return nil
}

// readTree lists the regular files under dir (missing dir: none), sorted by path.
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

// CalTurn is one real question and the real answer to it.
type CalTurn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Calibration is calibration/<id>.json: a real conversation to replay against the simulated person.
type Calibration struct {
	ID       string    `json:"id"`
	Scenario string    `json:"scenario"`
	Persona  string    `json:"persona"`
	Source   string    `json:"source,omitempty"`
	Turns    []CalTurn `json:"turns"`
}

// LoadCalibration reads a calibration file, strictly.
func LoadCalibration(path string) (*Calibration, error) {
	var c Calibration
	if err := readJSONStrict(path, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
