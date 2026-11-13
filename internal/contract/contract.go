// Package contract is the factorybench/v1 event contract (spec/events.schema.json), validated in Go.
package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Spec is the contract version every event carries.
const Spec = "factorybench/v1"

// Phases are the allowed usage phases.
var Phases = map[string]bool{"implement": true, "verify": true, "review": true}

// Event is the envelope of one contract event.
type Event struct {
	Spec      string          `json:"spec"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	TS        string          `json:"ts"`
	AttemptID string          `json:"attempt_id"`
	Payload   json.RawMessage `json:"payload"`
}

// WorkItem identifies an issue: repo is owner/name.
type WorkItem struct {
	Repo  string `json:"repo"`
	Issue int64  `json:"issue"`
}

// Payload is the union of the event payload fields FactoryBench reads; each type uses its own.
// Other fields (e.g. base_commit, summary, branch) are allowed and stay in the raw payload.
type Payload struct {
	// attempt.started
	WorkItem             *WorkItem `json:"work_item"`
	FactoryConfigVersion string    `json:"factory_config_version"`
	ParentAttemptID      string    `json:"parent_attempt_id"`
	Labels               []string  `json:"labels"`
	Role                 string    `json:"role"`
	// usage
	Model           string   `json:"model"`
	Input           *int64   `json:"input"`
	Output          *int64   `json:"output"`
	CacheRead       *int64   `json:"cache_read"`
	CacheWrite      *int64   `json:"cache_write"`
	Reasoning       *int64   `json:"reasoning"`
	ReportedCostUSD *float64 `json:"reported_cost_usd"`
	DurationMS      *int64   `json:"duration_ms"`
	Phase           string   `json:"phase"`
	// usage and activity: which subagent, if any (none = the main agent). On activity, Phase (with AgentID) is the
	// phase of that subagent's usage rows that have none of their own.
	Agent         string `json:"agent"`
	AgentID       string `json:"agent_id"`
	ParentAgentID string `json:"parent_agent_id"`
	// activity
	Tool   string `json:"tool"`
	Failed *bool  `json:"failed"`
	// gate
	Name   string `json:"name"`
	Passed *bool  `json:"passed"`
	// human.*, gate, attempt.finished
	Reason string `json:"reason"`
	Source string `json:"source"`
	// pr.linked
	PRURL string `json:"pr_url"`
	// attempt.finished
	Status string `json:"status"`
}

var types = map[string]bool{
	"attempt.started": true, "usage": true, "activity": true, "human.waiting": true,
	"human.resumed": true, "human.steering": true, "pr.linked": true, "attempt.finished": true, "gate": true,
}

var repoRe = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

// Validate parses one raw event. On error, Event holds whatever could be read so it can still be stored.
func Validate(raw []byte) (Event, Payload, error) {
	var e Event
	var p Payload
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, p, fmt.Errorf("invalid event: %v", err)
	}
	switch {
	case e.Spec != Spec:
		return e, p, fmt.Errorf("spec must be %q", Spec)
	case e.ID == "":
		return e, p, errors.New("id missing")
	case !types[e.Type]:
		return e, p, fmt.Errorf("unknown type %q", e.Type)
	case e.AttemptID == "":
		return e, p, errors.New("attempt_id missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.TS); err != nil {
		return e, p, errors.New("ts must be RFC 3339")
	}
	if len(e.Payload) == 0 || e.Payload[0] != '{' {
		return e, p, errors.New("payload must be an object")
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return e, p, fmt.Errorf("payload: %v", err)
	}
	for i, v := range []*int64{p.Input, p.Output, p.CacheRead, p.CacheWrite, p.Reasoning, p.DurationMS} {
		if v != nil && *v < 0 {
			return e, p, fmt.Errorf("payload.%s must be >= 0",
				[]string{"input", "output", "cache_read", "cache_write", "reasoning", "duration_ms"}[i])
		}
	}
	switch e.Type {
	case "attempt.started":
		switch {
		case p.WorkItem == nil:
			return e, p, errors.New("payload.work_item missing")
		case !repoRe.MatchString(p.WorkItem.Repo):
			return e, p, errors.New("payload.work_item.repo must be owner/name")
		case p.WorkItem.Issue < 1:
			return e, p, errors.New("payload.work_item.issue must be a positive integer")
		case p.FactoryConfigVersion == "":
			return e, p, errors.New("payload.factory_config_version missing")
		}
	case "usage":
		switch {
		case p.Model == "":
			return e, p, errors.New("payload.model missing")
		case p.Input == nil:
			return e, p, errors.New("payload.input missing")
		case p.Output == nil:
			return e, p, errors.New("payload.output missing")
		case p.ReportedCostUSD != nil && *p.ReportedCostUSD < 0:
			return e, p, errors.New("payload.reported_cost_usd must be >= 0")
		case p.Phase != "" && !Phases[p.Phase]:
			return e, p, errors.New("payload.phase must be implement, verify, or review")
		}
	case "activity":
		if p.Phase != "" && !Phases[p.Phase] {
			return e, p, errors.New("payload.phase must be implement, verify, or review")
		}
	case "gate":
		switch {
		case p.Name == "":
			return e, p, errors.New("payload.name missing")
		case p.Passed == nil:
			return e, p, errors.New("payload.passed missing")
		}
	case "pr.linked":
		if p.PRURL == "" {
			return e, p, errors.New("payload.pr_url missing")
		}
	case "attempt.finished":
		if p.Status != "completed" && p.Status != "failed" && p.Status != "cancelled" {
			return e, p, errors.New("payload.status must be completed, failed, or cancelled")
		}
	case "human.steering":
		if p.Source != "" && p.Source != "comment" && p.Source != "chat" && p.Source != "label" {
			return e, p, errors.New("payload.source must be comment, chat, or label")
		}
	}
	return e, p, nil
}

// ParsePayload decodes an already-validated payload.
func ParsePayload(raw []byte) Payload {
	var p Payload
	_ = json.Unmarshal(raw, &p)
	return p
}
