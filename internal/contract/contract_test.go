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
		"phase must be implement, verify":           {"activity", `{"agent_id":"a1","phase":"plan"}`},
		"payload must be an object":                 {"activity", `[]`},
		"cannot unmarshal":                          {"usage", `{"model":"m","input":1.5,"output":1}`},

		"payload.n missing":                                               {"context.turn", `{"speaker":"person","words":3}`},
		"payload.speaker missing":                                         {"context.turn", `{"n":1,"words":3}`},
		"payload.words missing":                                           {"context.turn", `{"n":1,"speaker":"person"}`},
		"payload.n must be >= 1":                                          {"context.turn", `{"n":0,"speaker":"person","words":3}`},
		"payload.speaker must be":                                         {"context.turn", `{"n":1,"speaker":"bot","words":3}`},
		"payload.words must be >= 0":                                      {"context.turn", `{"n":1,"speaker":"person","words":-1}`},
		"payload.move missing on a factory turn":                          {"context.turn", `{"n":2,"speaker":"factory","words":3}`},
		"payload.move must be":                                            {"context.turn", `{"n":2,"speaker":"factory","move":"probe","words":3}`},
		"payload.suggestion must be":                                      {"context.turn", `{"n":2,"speaker":"person","words":3,"suggestion":"maybe"}`},
		"payload.frustration must be 0 to 5 (6)":                          {"context.turn", `{"n":2,"speaker":"person","words":3,"frustration":6}`},
		"payload.frustration must be 0 to 5 (-1)":                         {"context.turn", `{"n":2,"speaker":"person","words":3,"frustration":-1}`},
		"payload.tagged_by must be":                                       {"context.turn", `{"n":2,"speaker":"person","words":3,"tagged_by":"robot"}`},
		"payload.scenario missing":                                        {"context.score", `{"truth":{},"captured":{}}`},
		"payload.truth missing":                                           {"context.score", `{"scenario":"s","captured":{}}`},
		"payload.captured missing":                                        {"context.score", `{"scenario":"s","truth":{}}`},
		"payload.truth.items must be >= 0":                                {"context.score", `{"scenario":"s","truth":{"items":-1},"captured":{}}`},
		"payload.captured.partial must be >= 0":                           {"context.score", `{"scenario":"s","truth":{},"captured":{"partial":-2}}`},
		"payload.invented must be >= 0":                                   {"context.score", `{"scenario":"s","truth":{},"captured":{},"invented":-1}`},
		"payload.builders.total must be >= 0":                             {"context.score", `{"scenario":"s","truth":{},"captured":{},"builders":{"total":-1}}`},
		"payload.build.hidden_pass must be >= 0":                          {"context.score", `{"scenario":"s","truth":{},"captured":{},"build":{"hidden_pass":-1}}`},
		"payload.latent_by_move: unknown move":                            {"context.score", `{"scenario":"s","truth":{},"captured":{},"latent_by_move":{"probe":1}}`},
		"payload.bench_cost_usd.judge must be >= 0":                       {"context.score", `{"scenario":"s","truth":{},"captured":{},"bench_cost_usd":{"judge":-0.1}}`},
		"payload.words missing (null)":                                    {"context.turn", `{"n":1,"speaker":"person","words":null}`},
		"payload.truth missing (null)":                                    {"context.score", `{"scenario":"s","truth":null,"captured":{}}`},
		"payload.captured.full + captured.partial must be <= truth.items": {"context.score", `{"scenario":"s","truth":{"items":2},"captured":{"full":2,"partial":1}}`},
		"payload.untraced must be <= requirements":                        {"context.score", `{"scenario":"s","truth":{},"captured":{},"requirements":2,"untraced":5}`},
		"payload.builders.agree must be <= builders.total":                {"context.score", `{"scenario":"s","truth":{},"captured":{},"builders":{"agree":9,"total":3}}`},
		"payload.traps.followed must be <= truth.traps":                   {"context.score", `{"scenario":"s","truth":{"traps":1},"captured":{},"traps":{"raised":1,"followed":2}}`},
		"payload.build.hidden_pass must be <= build.hidden_total":         {"context.score", `{"scenario":"s","truth":{},"captured":{},"build":{"hidden_pass":5,"hidden_total":4}}`},
	}
	for want, c := range cases {
		want, _, _ = strings.Cut(want, " (")
		_, _, err := Validate([]byte(strings.Replace(strings.Replace(base, "%s", c[0], 1), "%s", c[1], 1)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}

func TestContextAccepts(t *testing.T) {
	base := `{"spec":"factorybench/v1","id":"a:1","type":"%s","ts":"2026-11-11T09:00:00Z","attempt_id":"a","payload":%s}`
	for typ, payload := range map[string]string{
		"context.turn":  `{"n":1,"speaker":"person","words":0}`,
		"context.score": `{"scenario":"s","truth":{},"captured":{}}`,
	} {
		raw := strings.Replace(strings.Replace(base, "%s", typ, 1), "%s", payload, 1)
		if _, _, err := Validate([]byte(raw)); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
	for _, f := range []string{"0", "5"} {
		raw := strings.Replace(strings.Replace(base, "%s", "context.turn", 1), "%s", `{"n":3,"speaker":"person","words":2,"frustration":`+f+`}`, 1)
		if _, _, err := Validate([]byte(raw)); err != nil {
			t.Errorf("frustration %s: %v", f, err)
		}
	}
	ct := ParseContextTurn([]byte(`{"n":4,"speaker":"factory","move":"show","words":9,"revealed":["a1"]}`))
	if ct.N != 4 || ct.Move != "show" || ct.Words != 9 || len(ct.Revealed) != 1 {
		t.Errorf("ParseContextTurn: %+v", ct)
	}
	cs := ParseContextScore([]byte(`{"scenario":"s","truth":{"items":3},"captured":{"full":1},"build":{"hidden_total":2}}`))
	if cs.Truth.Items != 3 || cs.Captured.Full != 1 || cs.Build == nil || cs.Build.HiddenTotal != 2 {
		t.Errorf("ParseContextScore: %+v", cs)
	}
	if cs := ParseContextScore([]byte(`{"scenario":"s","truth":{},"captured":{}}`)); cs.Build != nil {
		t.Error("absent build must stay nil")
	}
}
