package change

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Expect is what the gates must say about a fixture folder.
type Expect struct {
	Gate1         bool `json:"gate1"`
	Gate2         bool `json:"gate2"`
	Gate3         bool `json:"gate3"`
	VagueWarnings int  `json:"vague_warnings"`
}

// Variant is a change folder of the gate fixtures with the verdict the gates must give it.
type Variant struct {
	Name   string
	Folder *Folder
	Expect Expect
}

// LoadVariants builds the fixture folders under dir: `base/` is a good change folder, and every
// `variants/<name>/` holds `expect.json`, an optional `overlay/` (files that replace or add to the base) and an
// optional `delete.txt` (one base path per line to remove). Variants come back sorted by name.
func LoadVariants(dir string) ([]Variant, error) {
	ents, err := os.ReadDir(filepath.Join(dir, "variants"))
	if err != nil {
		return nil, err
	}
	var out []Variant
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		vdir := filepath.Join(dir, "variants", e.Name())
		f, err := ReadDir(filepath.Join(dir, "base"))
		if err != nil {
			return nil, err
		}
		if b, err := os.ReadFile(filepath.Join(vdir, "delete.txt")); err == nil {
			for _, p := range strings.Fields(string(b)) {
				delete(f.Files, p)
			}
		}
		if _, err := os.Stat(filepath.Join(vdir, "overlay")); err == nil {
			ov, err := ReadDir(filepath.Join(vdir, "overlay"))
			if err != nil {
				return nil, err
			}
			for p, c := range ov.Files {
				f.Files[p] = c
			}
		}
		delete(f.Files, ".keep")
		var ex Expect
		b, err := os.ReadFile(filepath.Join(vdir, "expect.json"))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &ex); err != nil {
			return nil, err
		}
		out = append(out, Variant{Name: e.Name(), Folder: f, Expect: ex})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
