package bench

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"factorybench/internal/contextbench/claude"
)

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

func (m *Meter) Add(bucket string, usd float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]float64{}
	}
	m.m[bucket] += usd
}

func (m *Meter) Get(bucket string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.m[bucket]
}

// invalidError is a run that cannot be scored: a model call failed, or the subject, the person or a builder broke
// its protocol. The attempt is reported failed; that is an outcome, not an error of the bench.
type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }

func invalid(format string, a ...any) error {
	return &invalidError{fmt.Sprintf(format, a...)}
}

func isInvalid(err error) bool {
	var ie *invalidError
	return errors.As(err, &ie)
}

type Session struct {
	Bucket string
	Model  string
	At     time.Time
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

// TriesPerAsk is how many sessions one Ask may run: the first, and one repair when the reply breaks its protocol.
const TriesPerAsk = 2

// Ask runs one session. render builds the user message (repair is nil on the first try, else the rejection of
// the previous reply); check validates the structured reply and may keep what it parsed.
func (c *Caller) Ask(ctx context.Context, bucket string, req claude.Request, render func(repair error) (string, error), check func(raw []byte) error) error {
	req.MaxCost = c.MaxCost
	var last error
	for range TriesPerAsk {
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
			return invalid("%v", err)
		}
		if last = check(resp.Output); last == nil {
			return nil
		}
	}
	return invalid("%s broke its protocol: %v", req.Role, last)
}
