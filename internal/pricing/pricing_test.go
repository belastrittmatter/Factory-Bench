package pricing

import "testing"

func TestEstimate(t *testing.T) {
	cr, cw := int64(1_000_000), int64(1_000_000)
	if usd, ok := Estimate("claude-haiku-5-5", 1_000_000, 1_000_000, &cr, &cw); !ok || usd != 1+5+0.10+1.25 {
		t.Fatalf("haiku: %v %v", usd, ok)
	}
	if _, ok := Estimate("gemini-3-pro", 1, 1, nil, &cw); ok {
		t.Fatal("gemini has no cache_write price; must be unpriced")
	}
	if _, ok := Estimate("unknown-model", 1, 1, nil, nil); ok {
		t.Fatal("unknown model must be unpriced")
	}
	if Version != "2026-11-11" {
		t.Fatal(Version)
	}
}
