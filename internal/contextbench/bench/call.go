// Package bench runs the Context creator bench: the conversation loop, scoring, the replay and the retag.
package bench

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"factorybench/internal/contextbench/claude"
)

// Cost buckets: the roles whose sessions are metered apart.
const (
	BucketSubject  = "subject"
	BucketPerson   = "person"
	BucketJudge    = "judge"
	BucketBuilders = "builders"
)

// Meter adds up the money spent per bucket. Safe for concurrent use.
type Meter struct {
	mu sync.Mutex
	m  map[string]float64
}

// Add records cost in a bucket.
func (m *Meter) Add(bucket string, usd float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]float64{}
	}
	m.m[bucket] += usd
}

// Get returns a bucket's total.
func (m *Meter) Get(bucket string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.m[bucket]
}

// Kinds of failure that make a run invalid.
const (
	KindCall     = "call"     // a model call failed
	KindProtocol = "protocol" // a model broke its protocol beyond repair
)

// InvalidError is a failure that makes a run `invalid`.
type InvalidError struct {
	Kind string
	Msg  string
}

func (e *InvalidError) Error() string { return e.Msg }

func invalid(kind, format string, a ...any) error {
	return &InvalidError{Kind: kind, Msg: fmt.Sprintf(format, a...)}
}

// IsInvalid reports whether err is an InvalidError.
func IsInvalid(err error) bool {
	var ie *InvalidError
	return errors.As(err, &ie)
}

// Session is one finished model session, as OnSession sees it.
type Session struct {
	Bucket string
	Model  string
	At     time.Time // when it finished
	Resp   claude.Response
}

// Caller makes model sessions on behalf of a run: it meters cost, and gives a reply that fails its check one
// repair attempt (the same session again with the problem named) before the run is invalid.
type Caller struct {
	Runner  claude.Runner
	Meter   *Meter
	MaxCost float64
	// OnSession, when set, sees every session that ran, failed ones included. Calls are serialised.
	OnSession func(Session)

	mu sync.Mutex
}

// Ask runs one session. render builds the user message (repair is nil on the first try, else the rejection of
// the previous reply); check validates the structured reply and may keep what it parsed.
func (c *Caller) Ask(ctx context.Context, bucket string, req claude.Request, render func(repair error) (string, error), check func(raw []byte) error) error {
	req.MaxCost = c.MaxCost
	var last error
	for range 2 {
		prompt, err := render(last)
		if err != nil {
			return err
		}
		req.Prompt = prompt
		resp, err := c.Runner.Run(ctx, req)
		c.Meter.Add(bucket, resp.Cost)
		if c.OnSession != nil {
			c.mu.Lock()
			c.OnSession(Session{Bucket: bucket, Model: req.Model, At: time.Now().UTC(), Resp: resp})
			c.mu.Unlock()
		}
		if err != nil {
			return invalid(KindCall, "%v", err)
		}
		if last = check(resp.Output); last == nil {
			return nil
		}
	}
	return invalid(KindProtocol, "%s broke its protocol: %v", req.Role, last)
}
