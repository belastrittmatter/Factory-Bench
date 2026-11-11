package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExamplesPass(t *testing.T) {
	files, _ := filepath.Glob("../../spec/examples/*.json")
	seen := map[string]bool{}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		e, _, err := Validate(b)
		if err != nil {
			t.Errorf("%s: %v", f, err)
		}
		seen[e.Type] = true
	}
	for typ := range types {
		if !seen[typ] {
			t.Errorf("no example for %s", typ)
		}
	}
}

// The published schema and the Go validator must agree on the event types.
func TestSchemaTypesMatch(t *testing.T) {
	b, _ := os.ReadFile("../../spec/events.schema.json")
	var s struct {
		Properties struct {
			Type struct {
				Enum []string `json:"enum"`
			} `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Properties.Type.Enum) != len(types) {
		t.Fatalf("schema has %d types, validator %d", len(s.Properties.Type.Enum), len(types))
	}
	for _, typ := range s.Properties.Type.Enum {
		if !types[typ] {
			t.Errorf("schema type %s unknown to validator", typ)
		}
	}
}

func TestRejects(t *testing.T) {
	base := `{"spec":"factorybench/v1","id":"a:1","type":"%s","ts":"2026-11-11T09:00:00Z","attempt_id":"a","payload":%s}`
	cases := map[string][2]string{
		"payload.input missing":                     {"usage", `{"model":"m","output":1}`},
		"payload.output must be >= 0":               {"usage", `{"model":"m","input":1,"output":-1}`},
		"payload.work_item missing":                 {"attempt.started", `{"factory_config_version":"v"}`},
		"payload.work_item.repo must be owner/name": {"attempt.started", `{"work_item":{"repo":"x","issue":1},"factory_config_version":"v"}`},
		"payload.status must be":                    {"attempt.finished", `{"status":"done"}`},
		"unknown type":                              {"nope", `{}`},
		"payload.passed missing":                    {"gate", `{"name":"test"}`},
		"payload.phase must be":                     {"usage", `{"model":"m","input":1,"output":1,"phase":"plan"}`},
		"payload must be an object":                 {"activity", `[]`},
		"cannot unmarshal":                          {"usage", `{"model":"m","input":1.5,"output":1}`},
	}
	for want, c := range cases {
		_, _, err := Validate([]byte(strings.Replace(strings.Replace(base, "%s", c[0], 1), "%s", c[1], 1)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}
