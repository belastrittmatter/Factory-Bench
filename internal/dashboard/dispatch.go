package dashboard

import (
	"bufio"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A run manifest line is "[hh:mm:ss] text". A dispatch line starts one worker on a card ("dispatch lane=<lane> issue=#<n>
// pid=<p>"); the next line naming the same pid says how it ended. Lanes, states and outcome texts are taken as they are.

// dispatch is one worker the run manifest started on a card.
type dispatch struct {
	At      time.Time
	End     *time.Time // time of the next manifest line naming its pid; nil = not reported
	Lane    string
	Pid     string
	Outcome string // that line's text without the pid; "" = not reported
	File    string
}

// note is a manifest line that names a card but is not a dispatch (reaps, kills, halts).
type note struct {
	At   time.Time
	Text string
}

// move is a board state the manifest reported for a card ("card moved to '<state>'").
type move struct {
	At    time.Time
	State string
}

// runLog is what the run manifests say, per issue number.
type runLog struct {
	dispatches map[int64][]dispatch
	notes      map[int64][]note
	moves      map[int64][]move
}

func newRunLog() runLog {
	return runLog{map[int64][]dispatch{}, map[int64][]note{}, map[int64][]move{}}
}

var (
	pidRef   = regexp.MustCompile(`\(?\bpid=(\d+)\)?`)
	cardMove = regexp.MustCompile(`card moved to '([^']+)'`)
	issueRef = regexp.MustCompile(`#(\d+)\b`)
)

// parseManifest adds one manifest (its clock is local time on date) to out.
func parseManifest(date, name string, r io.Reader, out runLog) error {
	type ref struct {
		issue int64
		i     int
	}
	open := map[string]ref{} // pid -> its dispatch, until a later line names the pid
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, maxLine)
	for sc.Scan() {
		l := manifestLine.FindStringSubmatch(sc.Text())
		if l == nil {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+l[1], time.Local)
		if err != nil {
			continue
		}
		text, pid := l[2], ""
		if m := pidRef.FindStringSubmatch(text); m != nil {
			pid = m[1]
		}
		if x := dispatchLine.FindStringSubmatch(text); x != nil {
			issue, _ := strconv.ParseInt(x[2], 10, 64)
			out.dispatches[issue] = append(out.dispatches[issue], dispatch{At: at, Lane: x[1], Pid: pid, File: name})
			if pid != "" {
				open[pid] = ref{issue, len(out.dispatches[issue]) - 1}
			}
			continue
		}
		if p, ok := open[pid]; ok {
			d := &out.dispatches[p.issue][p.i]
			t := at
			d.End, d.Outcome = &t, strings.Join(strings.Fields(pidRef.ReplaceAllString(text, "")), " ")
			delete(open, pid)
		}
		if m := issueRef.FindStringSubmatch(text); m != nil {
			issue, _ := strconv.ParseInt(m[1], 10, 64)
			out.notes[issue] = append(out.notes[issue], note{at, text})
			if mv := cardMove.FindStringSubmatch(text); mv != nil {
				out.moves[issue] = append(out.moves[issue], move{at, mv[1]})
			}
		}
	}
	return sc.Err()
}

// dispatchSkew is how far a dispatch line may precede its attempt's start: the manifest has whole seconds and is
// written just before the factory sends attempt.started.
const dispatchSkew = time.Minute

// attemptWin is one attempt's time window.
type attemptWin struct {
	ID, State  string
	Start      time.Time
	End        *time.Time
	LastActive *time.Time // its last event; ends an open attempt that went idle or waits
}

// call is one model call (one usage row).
type call struct {
	TS         time.Time `json:"ts"`
	Attempt    string    `json:"attempt_id"`
	Model      string    `json:"model"`
	Phase      *string   `json:"phase"`
	Tokens     int64     `json:"tokens"`
	Cost       *float64  `json:"cost_usd"`
	DurationMS *int64    `json:"duration_ms"`
}

// dispatchRow is one dispatch with the model calls made during it. Lane "" collects an attempt's calls that fall
// outside every reported dispatch (no manifest, or calls before the first dispatch).
type dispatchRow struct {
	Lane         string    `json:"lane"`
	At           time.Time `json:"at"`
	End          time.Time `json:"end"`
	EndReported  bool      `json:"end_reported"` // false: End is derived (next dispatch, attempt end, last event)
	Outcome      string    `json:"outcome"`
	File         string    `json:"file"`
	Attempt      string    `json:"attempt_id"` // "" = before any attempt
	AttemptState string    `json:"attempt_state"`
	Retry        bool      `json:"retry"` // its attempt failed or was cancelled
	Calls        []call    `json:"calls"`
	Tokens       int64     `json:"tokens"`
	Cost         float64   `json:"cost_usd"`
	Unpriced     int       `json:"unpriced"`
}

// retry reports whether an attempt state means its work was thrown away.
func retry(state string) bool { return state == "failed" || state == "cancelled" }

// groupByDispatch assigns every dispatch to the attempt it ran in and every call to the latest dispatch of its
// attempt that started before it. It never drops a call.
func groupByDispatch(ds []dispatch, atts []attemptWin, calls []call, now time.Time) []dispatchRow {
	ds = append([]dispatch(nil), ds...)
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].At.Before(ds[j].At) })
	atts = append([]attemptWin(nil), atts...)
	sort.SliceStable(atts, func(i, j int) bool { return atts[i].Start.Before(atts[j].Start) })
	calls = append([]call(nil), calls...)
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].TS.Before(calls[j].TS) })
	byID := map[string]*attemptWin{}
	for i := range atts {
		byID[atts[i].ID] = &atts[i]
	}
	// attemptAt is the attempt a dispatch at t ran in: the latest one started by t, unless that one had already
	// ended and another starts within dispatchSkew after t (the dispatch line is written just before attempt.started).
	attemptAt := func(t time.Time) *attemptWin {
		var cur, next *attemptWin
		for i := range atts {
			switch a := &atts[i]; {
			case !a.Start.After(t):
				cur = a
			case next == nil && !a.Start.After(t.Add(dispatchSkew)):
				next = a
			}
		}
		if next != nil && (cur == nil || (cur.End != nil && !cur.End.After(t))) {
			return next
		}
		return cur
	}
	row := func(at time.Time, a *attemptWin) dispatchRow {
		r := dispatchRow{At: at, Calls: []call{}}
		if a != nil {
			r.Attempt, r.AttemptState, r.Retry = a.ID, a.State, retry(a.State)
		}
		return r
	}
	rows := []dispatchRow{}
	for _, d := range ds {
		r := row(d.At, attemptAt(d.At))
		r.Lane, r.Outcome, r.File = d.Lane, d.Outcome, d.File
		if d.End != nil {
			r.End, r.EndReported = *d.End, true
		}
		rows = append(rows, r)
	}
	n := len(rows)
	loose := map[string]int{} // attempt -> its row of calls outside every dispatch
	for _, c := range calls {
		idx := -1
		for i := 0; i < n; i++ {
			if rows[i].Attempt == c.Attempt && !rows[i].At.After(c.TS.Add(dispatchSkew)) {
				idx = i
			}
		}
		if idx < 0 {
			j, ok := loose[c.Attempt]
			if !ok {
				rows = append(rows, row(c.TS, byID[c.Attempt]))
				j = len(rows) - 1
				loose[c.Attempt] = j
			}
			idx = j
		}
		r := &rows[idx]
		r.Calls = append(r.Calls, c)
		r.Tokens += c.Tokens
		if c.Cost != nil {
			r.Cost += *c.Cost
		} else {
			r.Unpriced++
		}
	}
	for i := range rows {
		r := &rows[i]
		if r.EndReported {
			continue
		}
		if i >= n { // calls outside every dispatch: they span their own calls
			r.End = r.Calls[len(r.Calls)-1].TS
			continue
		}
		end := now
		if a := byID[r.Attempt]; a != nil && a.End != nil {
			end = *a.End
		} else if a != nil && a.LastActive != nil {
			end = *a.LastActive
		}
		if i+1 < n && rows[i+1].At.Before(end) {
			end = rows[i+1].At
		}
		if k := len(r.Calls); k > 0 && r.Calls[k-1].TS.After(end) {
			end = r.Calls[k-1].TS
		}
		if end.Before(r.At) {
			end = r.At
		}
		r.End = end
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })
	return rows
}

// rework counts how often a card went back to a lane it had already visited after another lane (bounces) and how
// often the same lane was dispatched twice in a row (re-dispatches). lanes are in dispatch order.
func rework(lanes []string) (bounces, redispatches int) {
	seen := map[string]bool{}
	for i, l := range lanes {
		switch {
		case i > 0 && l == lanes[i-1]:
			redispatches++
		case seen[l]:
			bounces++
		}
		seen[l] = true
	}
	return bounces, redispatches
}

// barEnd is where an attempt's timeline bar stops and why: its end ("ended"), the start of the human wait it is in
// ("waiting"), its last event when idle ("idle"), or now while it runs ("running").
func barEnd(status string, ended, waitingSince, lastEvent *time.Time, now time.Time) (time.Time, string) {
	switch {
	case ended != nil:
		return *ended, "ended"
	case status == "waiting_for_human" && waitingSince != nil:
		return *waitingSince, "waiting"
	case status == "idle" && lastEvent != nil:
		return *lastEvent, "idle"
	}
	return now, "running"
}

// laneAt is the lane of the dispatch that was running at ts: the latest one started by ts (within dispatchSkew) that
// had not ended more than dispatchSkew before ts. "" = no dispatch was running.
func laneAt(rows []dispatchRow, ts time.Time) string {
	lane := ""
	for _, r := range rows {
		if r.Lane != "" && !r.At.After(ts.Add(dispatchSkew)) {
			lane = ""
			if !ts.After(r.End.Add(dispatchSkew)) {
				lane = r.Lane
			}
		}
	}
	return lane
}

// toolCall is one tool call, for the per-lane breakdown.
type toolCall struct {
	TS      time.Time
	Tool    string
	Success *bool
}

// laneTools is one lane's tool use; Lane "" = calls made outside every dispatch.
type laneTools struct {
	Lane  string      `json:"lane"`
	Calls int         `json:"calls"`
	Tools []toolCount `json:"tools"` // most used first
}

type toolCount struct {
	Tool   string `json:"tool"`
	Calls  int    `json:"calls"`
	Failed int    `json:"failed"`
}

// toolsByLane counts every tool call per lane (laneAt) and tool, lanes in order of first use.
func toolsByLane(rows []dispatchRow, calls []toolCall) []laneTools {
	out := []laneTools{}
	idx := map[string]int{}
	for _, c := range calls {
		l := laneAt(rows, c.TS)
		i, ok := idx[l]
		if !ok {
			out = append(out, laneTools{Lane: l})
			i = len(out) - 1
			idx[l] = i
		}
		lt := &out[i]
		lt.Calls++
		j := -1
		for k, t := range lt.Tools {
			if t.Tool == c.Tool {
				j = k
			}
		}
		if j < 0 {
			lt.Tools = append(lt.Tools, toolCount{Tool: c.Tool})
			j = len(lt.Tools) - 1
		}
		lt.Tools[j].Calls++
		if c.Success != nil && !*c.Success {
			lt.Tools[j].Failed++
		}
	}
	for _, lt := range out {
		sort.SliceStable(lt.Tools, func(a, b int) bool { return lt.Tools[a].Calls > lt.Tools[b].Calls })
	}
	return out
}
