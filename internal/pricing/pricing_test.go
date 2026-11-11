package pricing

import "testing"

func TestEstimate(t *testing.T) {
	cr, cw := int64(1_000_000), int64(1_000_000)
	if usd, v, ok := Estimate("2026-11-11T09:00:00Z", "claude-haiku-5-5", 1_000_000, 1_000_000, &cr, &cw); !ok || usd != 1+5+0.10+1.25 || v != "2026-11-11" {
		t.Fatalf("haiku: %v %v %v", usd, v, ok)
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
