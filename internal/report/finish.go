package report

import (
	"regexp"
	"strconv"
)

// Lanes is what an attempt's finish reason says about its lanes (work packages): planned, merged, paused or blocked,
// and whether the bench's time cap stopped it, e.g. "bench fail: lanes=3/7 paused=0 blocked=0 timed-out flow-exit=none".
type Lanes struct {
	Merged   int  `json:"lanes_merged"`
	Planned  int  `json:"lanes_planned"`
	Paused   int  `json:"lanes_paused"`
	Blocked  int  `json:"lanes_blocked"`
	TimedOut bool `json:"timed_out"`
}

var (
	lanesRE   = regexp.MustCompile(`\blanes=(\d+)/(\d+)`)
	pausedRE  = regexp.MustCompile(`\bpaused=(\d+)`)
	blockedRE = regexp.MustCompile(`\bblocked=(\d+)`)
	timedRE   = regexp.MustCompile(`(?i)\btimed[ -]?out\b`)
)

// ParseLanes reads the lane counts out of a finish reason; ok is false when it names no lanes=merged/planned.
// shortcut: parses free text the t12 bench writes; replace with a structured attempt.finished field once the reporter sends one.
func ParseLanes(reason string) (l Lanes, ok bool) {
	m := lanesRE.FindStringSubmatch(reason)
	if m == nil {
		return l, false
	}
	n := func(re *regexp.Regexp) int {
		if x := re.FindStringSubmatch(reason); x != nil {
			v, _ := strconv.Atoi(x[1])
			return v
		}
		return 0
	}
	l.Merged, _ = strconv.Atoi(m[1])
	l.Planned, _ = strconv.Atoi(m[2])
	l.Paused, l.Blocked, l.TimedOut = n(pausedRE), n(blockedRE), timedRE.MatchString(reason)
	return l, true
}
