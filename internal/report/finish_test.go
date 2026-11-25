package report

import "testing"

// The finish reasons the t12 bench sends, as they are in its database.
func TestParseLanes(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Lanes
		ok   bool
	}{
		{"bench fail: lanes=0/3 paused=3 blocked=0 flow-exit=1", Lanes{0, 3, 3, 0, false}, true},
		{"bench fail: lanes=1/2 paused=0 blocked=1 flow-exit=1", Lanes{1, 2, 0, 1, false}, true},
		{"bench fail: lanes=1/7 paused=6 blocked=0", Lanes{1, 7, 6, 0, false}, true},
		{"bench fail: lanes=3/7 paused=0 blocked=0 timed-out flow-exit=none", Lanes{3, 7, 0, 0, true}, true},
		{"bench pass: lanes=9/9 paused=0 blocked=0", Lanes{9, 9, 0, 0, false}, true},
		{"lanes=2/4", Lanes{Merged: 2, Planned: 4}, true},
		{"network error", Lanes{}, false},
		{"", Lanes{}, false},
	} {
		if got, ok := ParseLanes(c.in); got != c.want || ok != c.ok {
			t.Errorf("ParseLanes(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
