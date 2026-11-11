package claudecode

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"factorybench/internal/contract"
)

func TestTranslateCaptured(t *testing.T) {
	f, err := os.Open("../../testdata/captured/claude-code-2.1.322-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Translate(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"usage":[{"cache_read":63549,"cache_write":15626,"duration_ms":3053,"input":1189,"model":"claude-haiku-5-5","output":293,"reasoning":0,"reported_cost_usd":0.0040260899999999995}],"finished":{"status":"completed"}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	// The translated payloads must be valid contract events.
	valid(t, "attempt.finished", got.Finished)
	for _, u := range got.Usage {
		valid(t, "usage", u)
	}
}

func valid(t *testing.T, typ string, p map[string]any) {
	t.Helper()
	pb, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := json.Marshal(map[string]any{"spec": contract.Spec, "id": "x:" + typ, "type": typ,
		"ts": "2026-11-11T10:00:00Z", "attempt_id": "x", "payload": json.RawMessage(pb)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := contract.Validate(ev); err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
}

// Two models in modelUsage give two payloads, sorted by model, with duration only on the first;
// without modelUsage the top-level usage and total_cost_usd are used.
func TestTranslateModelUsage(t *testing.T) {
	two := `{"type":"result","duration_ms":10,"total_cost_usd":0.5,"usage":{"input_tokens":1,"output_tokens":2},` +
		`"modelUsage":{"claude-sonnet-5-5":{"inputTokens":5,"outputTokens":6,"costUSD":0.4},` +
		`"claude-haiku-5-5":{"inputTokens":3,"outputTokens":4,"thinkingTokens":7,"costUSD":0.1}}}`
	fallback := `{"type":"system","subtype":"init","model":"m"}` + "\n" +
		`{"type":"result","duration_ms":10,"total_cost_usd":0.5,"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":0}}`
	for in, want := range map[string]string{
		two: `[{"duration_ms":10,"input":3,"model":"claude-haiku-5-5","output":4,"reasoning":7,"reported_cost_usd":0.1},` +
			`{"input":5,"model":"claude-sonnet-5-5","output":6,"reported_cost_usd":0.4}]`,
		fallback: `[{"cache_read":0,"duration_ms":10,"input":1,"model":"m","output":2,"reported_cost_usd":0.5}]`,
	} {
		got, err := Translate(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(got.Usage); string(b) != want {
			t.Errorf("got  %s\nwant %s", b, want)
		}
		for _, u := range got.Usage {
			valid(t, "usage", u)
		}
	}
}

func TestTranslateNoResult(t *testing.T) {
	got, _ := Translate(strings.NewReader(`{"type":"system","subtype":"init","model":"m"}` + "\n"))
	if got.Usage != nil || got.Finished["status"] != "failed" {
		t.Fatalf("%+v", got)
	}
}
