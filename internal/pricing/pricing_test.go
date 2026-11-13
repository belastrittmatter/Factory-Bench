package pricing

import (
	"math"
	"testing"
)

func TestEstimate(t *testing.T) {
	cr, cw := int64(1_000_000), int64(1_000_000)
	if usd, v, ok := Estimate("2026-11-11T09:00:00Z", "claude-haiku-5-5", 1_000_000, 1_000_000, &cr, &cw); !ok || usd != 1+5+0.10+1.25 || v != "2026-11-11" {
		t.Fatalf("haiku: %v %v %v", usd, v, ok)
	}
	// a Fable row Claude Code reported at $0.19128 (5-minute cache writes)
	zero, fcw := int64(0), int64(18282)
	if usd, v, ok := Estimate("2026-11-13T09:00:00Z", "claude-fable-5-2", 2, 211, &zero, &fcw); !ok || math.Abs(usd-0.19128) > 1e-5 || v != "2026-11-13" {
		t.Fatalf("fable: %v %v %v", usd, v, ok)
	}
	if _, _, ok := Estimate("2026-11-11T09:00:00Z", "gemini-3-pro", 1, 1, nil, &cw); ok {
		t.Fatal("gemini has no cache_write price; must be unpriced")
	}
	if _, _, ok := Estimate("2026-11-11T09:00:00Z", "unknown-model", 1, 1, nil, nil); ok {
		t.Fatal("unknown model must be unpriced")
	}
}

// A session is priced by the table in effect on its day, never by a newer one.
func TestAtPicksTableByDate(t *testing.T) {
	saved := versions
	defer func() { versions = saved }()
	versions = []version{{name: "2026-01-01"}, {name: "2026-06-01"}, {name: "2026-12-01"}}
	for ts, want := range map[string]string{
		"2025-05-05T00:00:00Z":     "2026-01-01", // before the first table: the earliest prices known
		"2026-05-31T23:59:59.000Z": "2026-01-01",
		"2026-06-01T00:00:00Z":     "2026-06-01",
		"2026-11-30T12:00:00Z":     "2026-06-01",
		"2027-01-01T00:00:00Z":     "2026-12-01",
	} {
		if got := at(ts).name; got != want {
			t.Errorf("at(%s) = %s, want %s", ts, got, want)
		}
	}
}

// Gemini is priced, and Google's announced increases apply from their dates.
func TestGemini(t *testing.T) {
	cr := int64(1_000_000)
	for ts, want := range map[string]float64{"2026-11-20T00:00:00Z": 2 + 10 + 0.10, "2026-12-01T00:00:00Z": 4 + 20 + 0.20} {
		if usd, _, ok := Estimate(ts, "gemini-4-carbon", 1_000_000, 1_000_000, &cr, nil); !ok || usd != want {
			t.Errorf("%s: %v %v, want %v", ts, usd, ok, want)
		}
	}
	if usd, _, ok := Estimate("2027-01-02T00:00:00Z", "gemini-3.8-flash", 1_000_000, 0, nil, &cr); !ok || usd != 3 {
		t.Errorf("3.8 flash in 2027 with cache_write: %v %v", usd, ok)
	}
	if usd, _, ok := Estimate("2026-11-13T04:00:00Z", "gemini-3.8-flash-high", 1_000_000, 0, nil, nil); !ok || usd != 0.75 {
		t.Errorf("thinking-level suffix: %v %v", usd, ok)
	}
	if _, _, ok := Estimate("2026-11-13T04:00:00Z", "gemini-3.8-flash-ultra", 1, 1, nil, nil); ok {
		t.Error("unknown suffix must stay unpriced")
	}
}
