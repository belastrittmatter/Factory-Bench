package bench

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contract"
)

const (
	Gate1Frame     = "context-frame"
	Gate2Change    = "context-change"
	Gate3Testable  = "context-testable"
	Gate4Agreement = "context-agreement"
)

func NewAttemptID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "ctx-" + hex.EncodeToString(b[:])
}

// ConfigVersion is the factory_config_version of a subject: its name, the hash of its prompt and its model, so
// a change to either shows as a different configuration. A subject without a conversation is just its name.
func ConfigVersion(s SubjectSpec, model string) string {
	if !s.Converses() {
		return s.Name
	}
	sum := sha256.Sum256([]byte(s.Prompt))
	return s.Slug() + "@" + hex.EncodeToString(sum[:])[:12] + "/" + model
}

// Labels are the labels of the attempt: scenario, persona and the turn cap (the last two only when there is a
// conversation).
func Labels(scenarioID string, p *Prepared) []string {
	labels := []string{"scenario:" + scenarioID}
	if p.Subject.Converses() {
		labels = append(labels, "persona:"+p.PersonaID, "max-turns:"+strconv.Itoa(p.MaxTurns))
	}
	return labels
}

type startedPayload struct {
	WorkItem             contract.WorkItem `json:"work_item"`
	FactoryConfigVersion string            `json:"factory_config_version"`
	Labels               []string          `json:"labels"`
	Role                 string            `json:"role"`
}

type usagePayload struct {
	Model           string   `json:"model"`
	Input           int64    `json:"input"`
	Output          int64    `json:"output"`
	CacheRead       int64    `json:"cache_read,omitempty"`
	CacheWrite      int64    `json:"cache_write,omitempty"`
	ReportedCostUSD *float64 `json:"reported_cost_usd,omitempty"`
	DurationMS      int64    `json:"duration_ms,omitempty"`
}

type gatePayload struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

type finishedPayload struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

type eventLog struct {
	attempt string
	dir     string
	send    func(evs ...[]byte)
	stderr  func(format string, a ...any)

	mu      sync.Mutex
	pending [][]byte
	usages  int
}

func newEventLog(attempt, dir string, env *Env) *eventLog {
	return &eventLog{attempt: attempt, dir: dir, send: env.Send, stderr: func(f string, a ...any) { fmt.Fprintf(env.Stderr, "contextbench: "+f+"\n", a...) }}
}

func (l *eventLog) add(typ, key string, ts time.Time, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		l.stderr("event %s: %v", key, err)
		return
	}
	raw, err := json.Marshal(contract.Event{Spec: contract.Spec, ID: l.attempt + ":" + key, Type: typ,
		TS: ts.UTC().Format(time.RFC3339Nano), AttemptID: l.attempt, Payload: body})
	if err != nil {
		l.stderr("event %s: %v", key, err)
		return
	}
	if _, _, err := contract.Validate(raw); err != nil {
		l.stderr("event %s is invalid and was not sent: %v", key, err)
		return
	}
	l.mu.Lock()
	l.pending = append(l.pending, raw)
	l.mu.Unlock()
}

func (l *eventLog) flush() {
	l.mu.Lock()
	evs := l.pending
	l.pending = nil
	l.mu.Unlock()
	if len(evs) == 0 {
		return
	}
	var buf []byte
	for _, e := range evs {
		buf = append(append(buf, e...), '\n')
	}
	f, err := os.OpenFile(filepath.Join(l.dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		l.stderr("events.jsonl: %v", err)
	} else {
		if _, err := f.Write(buf); err != nil {
			l.stderr("events.jsonl: %v", err)
		}
		f.Close()
	}
	l.send(evs...)
}

func (l *eventLog) started(ts time.Time, wi *contract.WorkItem, version string, labels []string) {
	l.add("attempt.started", "started", ts, startedPayload{WorkItem: *wi, FactoryConfigVersion: version, Labels: labels, Role: contract.RoleContext})
}

func (l *eventLog) usage(s Session) {
	u := s.Resp.Usage
	if u.Cost == nil && u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
		return
	}
	l.mu.Lock()
	l.usages++
	n := l.usages
	l.mu.Unlock()
	l.add("usage", "usage:"+strconv.Itoa(n), s.At, usagePayload{Model: s.Model, Input: u.Input, Output: u.Output,
		CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, ReportedCostUSD: u.Cost, DurationMS: u.DurationMS})
}

func (l *eventLog) gate(name string, passed bool, reason string, ts time.Time) {
	l.add("gate", "gate:"+name, ts, gatePayload{Name: name, Passed: passed, Reason: reason})
}

func (l *eventLog) finished(ts time.Time, status, reason string) {
	l.add("attempt.finished", "finished", ts, finishedPayload{Status: status, Reason: reason})
}

type TurnEvent struct {
	At   time.Time
	Turn contract.ContextTurn
}

// TurnEvents numbers the turns of a transcript in conversation order (the person's brief is turn 1, each subject
// message and the person's answer after it follow) and turns each into a context.turn payload. The person's tags
// are the simulator's; text is left out when noText. A turn without a time of its own gets the time of the one
// before it (start for the first).
func TurnEvents(turns []protocol.Turn, noText bool, start time.Time) []TurnEvent {
	var out []TurnEvent
	at := start
	text := func(s string) string {
		if noText {
			return ""
		}
		return s
	}
	for _, t := range turns {
		if s := t.Subject; s != nil {
			if !s.At.IsZero() {
				at = s.At
			}
			out = append(out, TurnEvent{at, contract.ContextTurn{N: int64(len(out) + 1), Speaker: "factory", Move: s.Move,
				Words: int64(protocol.Words(s.Message)), Text: text(s.Message)}})
		}
		if p := t.Person; p != nil {
			if !p.At.IsZero() {
				at = p.At
			}
			ev := contract.ContextTurn{N: int64(len(out) + 1), Speaker: "person", Words: int64(protocol.Words(p.Reply)),
				TaggedBy: "simulator", Text: text(p.Reply)}
			if t.Turn > 0 {
				fr := int64(p.Frustration)
				ev.DontKnow, ev.CouldNotAnswer, ev.AlreadySaid, ev.InAttachments, ev.Confirmed =
					p.DontKnow, p.CouldNotAnswer, p.AlreadySaid, p.InAttachments, p.Confirmed
				ev.Suggestion, ev.Frustration, ev.Revealed = p.Suggestion, &fr, p.Revealed
			}
			out = append(out, TurnEvent{at, ev})
		}
	}
	return out
}
