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

// RoleContext is the role of a Context creator attempt. Normalize never infers its parent: bench runs of one
// scenario share a work item, and each is a run of its own.
const RoleContext = "context"

var (
	Speakers    = map[string]bool{"factory": true, "person": true}
	Moves       = map[string]bool{"capture": true, "mirror": true, "show": true, "suggest": true, "expand": true, "sharpen": true, "challenge": true, "narrow": true, "ask": true, "confirm": true}
	Suggestions = map[string]bool{"accepted": true, "rejected": true, "none": true}
	TaggedBy    = map[string]bool{"person": true, "tagger": true, "simulator": true}
)

// ContextTurn is the payload of a context.turn event: one turn of a Context creator conversation.
// N, Speaker and Words are required; Move is required on factory turns.
type ContextTurn struct {
	N              int64    `json:"n"`
	Speaker        string   `json:"speaker"`
	Move           string   `json:"move,omitempty"`
	Words          int64    `json:"words"`
	DontKnow       bool     `json:"dont_know,omitempty"`
	CouldNotAnswer bool     `json:"could_not_answer,omitempty"`
	AlreadySaid    bool     `json:"already_said,omitempty"`
	InAttachments  bool     `json:"in_attachments,omitempty"`
	Suggestion     string   `json:"suggestion,omitempty"`
	Frustration    *int64   `json:"frustration,omitempty"`
	Confirmed      bool     `json:"confirmed,omitempty"`
	Revealed       []string `json:"revealed,omitempty"`
	TaggedBy       string   `json:"tagged_by,omitempty"`
	Text           string   `json:"text,omitempty"`
}

// ContextScore is the payload of a context.score event: counts from scoring one Context creator attempt against
// the hidden truth. Scenario, Truth and Captured are required; absent counts are 0, and Build is nil when the run
// was not built. FactoryBench computes every rate from these counts.
type ContextScore struct {
	Scenario         string           `json:"scenario"`
	Subject          string           `json:"subject,omitempty"`
	Persona          string           `json:"persona,omitempty"`
	Truth            ContextTruth     `json:"truth"`
	Captured         ContextCaptured  `json:"captured"`
	LatentByMove     map[string]int64 `json:"latent_by_move,omitempty"`
	Traps            ContextTraps     `json:"traps"`
	Invented         int64            `json:"invented"`
	InventedUnmarked int64            `json:"invented_unmarked"`
	MapTagErrors     int64            `json:"map_tag_errors"`
	AssumedMarked    int64            `json:"assumed_marked"`
	Requirements     int64            `json:"requirements"`
	Untraced         int64            `json:"untraced"`
	NoiseLeft        int64            `json:"noise_left"`
	MissingSlots     int64            `json:"missing_slots"`
	Contradictions   int64            `json:"contradictions"`
	Builders         ContextBuilders  `json:"builders"`
	VagueWarnings    int64            `json:"vague_warnings"`
	SimViolations    int64            `json:"sim_violations"`
	BenchCostUSD     ContextBenchCost `json:"bench_cost_usd"`
	Build            *ContextBuild    `json:"build,omitempty"`
}

// ContextTruth is the size of the hidden truth: entries, G4 entries (a subset of items), latent entries
// (a subset of items), traps and noise entries.
type ContextTruth struct {
	Items  int64 `json:"items"`
	G4     int64 `json:"g4"`
	Latent int64 `json:"latent"`
	Traps  int64 `json:"traps"`
	Noise  int64 `json:"noise"`
}

// ContextCaptured is how many truth items the change folder captured fully or partly; G4 and Latent count the
// G4 and latent entries within those. Latent counts entries drawn out (full or partial is not distinguished).
type ContextCaptured struct {
	Full      int64 `json:"full"`
	Partial   int64 `json:"partial"`
	G4Full    int64 `json:"g4_full"`
	G4Partial int64 `json:"g4_partial"`
	Latent    int64 `json:"latent"`
}

// ContextTraps counts traps the Context creator raised and traps whose change followed the person's resolution.
type ContextTraps struct {
	Raised   int64 `json:"raised"`
	Followed int64 `json:"followed"`
}

// ContextBuilders is the gate 4 result: requirements the builder sessions agreed on, out of the compared total.
type ContextBuilders struct {
	Agree int64 `json:"agree"`
	Total int64 `json:"total"`
}

// ContextBenchCost is what the benchmark itself spent, in USD; never part of the factory's cost.
type ContextBenchCost struct {
	Person   float64 `json:"person"`
	Judge    float64 `json:"judge"`
	Builders float64 `json:"builders"`
	Build    float64 `json:"build"`
}

func (c ContextBenchCost) Total() float64 { return c.Person + c.Judge + c.Builders + c.Build }

// ContextBuild is the hidden-test result of building from the Context creator's change.
type ContextBuild struct {
	HiddenPass  int64 `json:"hidden_pass"`
	HiddenTotal int64 `json:"hidden_total"`
	Tasks       int64 `json:"tasks"`
	Merged      int64 `json:"merged"`
	Escalations int64 `json:"escalations"`
	Repairs     int64 `json:"repairs"`
}

var types = map[string]bool{
	"attempt.started": true, "usage": true, "activity": true, "human.waiting": true,
	"human.resumed": true, "human.steering": true, "pr.linked": true, "attempt.finished": true, "gate": true,
	"context.turn": true, "context.score": true,
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
	case "context.turn":
		if err := validateContextTurn(e.Payload); err != nil {
			return e, p, err
		}
	case "context.score":
		if err := validateContextScore(e.Payload); err != nil {
			return e, p, err
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

// ParseContextTurn decodes an already-validated context.turn payload.
func ParseContextTurn(raw []byte) ContextTurn {
	var t ContextTurn
	_ = json.Unmarshal(raw, &t)
	return t
}

// ParseContextScore decodes an already-validated context.score payload.
func ParseContextScore(raw []byte) ContextScore {
	var s ContextScore
	_ = json.Unmarshal(raw, &s)
	return s
}

func required(raw []byte, keys ...string) string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	for _, k := range keys {
		if v, ok := m[k]; !ok || string(v) == "null" {
			return k
		}
	}
	return ""
}

func validateContextTurn(raw []byte) error {
	if k := required(raw, "n", "speaker", "words"); k != "" {
		return fmt.Errorf("payload.%s missing", k)
	}
	var t ContextTurn
	if err := json.Unmarshal(raw, &t); err != nil {
		return fmt.Errorf("payload: %v", err)
	}
	switch {
	case t.N < 1:
		return errors.New("payload.n must be >= 1")
	case !Speakers[t.Speaker]:
		return errors.New("payload.speaker must be factory or person")
	case t.Words < 0:
		return errors.New("payload.words must be >= 0")
	case t.Speaker == "factory" && t.Move == "":
		return errors.New("payload.move missing on a factory turn")
	case t.Move != "" && !Moves[t.Move]:
		return errors.New("payload.move must be capture, mirror, show, suggest, expand, sharpen, challenge, narrow, ask, or confirm")
	case t.Suggestion != "" && !Suggestions[t.Suggestion]:
		return errors.New("payload.suggestion must be accepted, rejected, or none")
	case t.Frustration != nil && (*t.Frustration < 0 || *t.Frustration > 5):
		return errors.New("payload.frustration must be 0 to 5")
	case t.TaggedBy != "" && !TaggedBy[t.TaggedBy]:
		return errors.New("payload.tagged_by must be person, tagger, or simulator")
	}
	return nil
}

func validateContextScore(raw []byte) error {
	if k := required(raw, "scenario", "truth", "captured"); k != "" {
		return fmt.Errorf("payload.%s missing", k)
	}
	var s ContextScore
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("payload: %v", err)
	}
	if s.Scenario == "" {
		return errors.New("payload.scenario missing")
	}
	counts := []struct {
		name string
		v    int64
	}{
		{"truth.items", s.Truth.Items}, {"truth.g4", s.Truth.G4}, {"truth.latent", s.Truth.Latent}, {"truth.traps", s.Truth.Traps}, {"truth.noise", s.Truth.Noise},
		{"captured.full", s.Captured.Full}, {"captured.partial", s.Captured.Partial}, {"captured.g4_full", s.Captured.G4Full},
		{"captured.g4_partial", s.Captured.G4Partial}, {"captured.latent", s.Captured.Latent},
		{"traps.raised", s.Traps.Raised}, {"traps.followed", s.Traps.Followed},
		{"invented", s.Invented}, {"invented_unmarked", s.InventedUnmarked}, {"map_tag_errors", s.MapTagErrors},
		{"assumed_marked", s.AssumedMarked}, {"requirements", s.Requirements}, {"untraced", s.Untraced},
		{"noise_left", s.NoiseLeft}, {"missing_slots", s.MissingSlots}, {"contradictions", s.Contradictions},
		{"builders.agree", s.Builders.Agree}, {"builders.total", s.Builders.Total},
		{"vague_warnings", s.VagueWarnings}, {"sim_violations", s.SimViolations},
	}
	if s.Build != nil {
		counts = append(counts, []struct {
			name string
			v    int64
		}{{"build.hidden_pass", s.Build.HiddenPass}, {"build.hidden_total", s.Build.HiddenTotal}, {"build.tasks", s.Build.Tasks},
			{"build.merged", s.Build.Merged}, {"build.escalations", s.Build.Escalations}, {"build.repairs", s.Build.Repairs}}...)
	}
	for m, n := range s.LatentByMove {
		counts = append(counts, struct {
			name string
			v    int64
		}{"latent_by_move." + m, n})
		if !Moves[m] {
			return fmt.Errorf("payload.latent_by_move: unknown move %q", m)
		}
	}
	for _, c := range counts {
		if c.v < 0 {
			return fmt.Errorf("payload.%s must be >= 0", c.name)
		}
	}
	parts := []struct {
		name        string
		part, whole int64
	}{
		{"captured.full + captured.partial must be <= truth.items", s.Captured.Full + s.Captured.Partial, s.Truth.Items},
		{"captured.g4_full + captured.g4_partial must be <= truth.g4", s.Captured.G4Full + s.Captured.G4Partial, s.Truth.G4},
		{"captured.latent must be <= truth.latent", s.Captured.Latent, s.Truth.Latent},
		{"traps.raised must be <= truth.traps", s.Traps.Raised, s.Truth.Traps},
		{"traps.followed must be <= truth.traps", s.Traps.Followed, s.Truth.Traps},
		{"invented_unmarked must be <= invented", s.InventedUnmarked, s.Invented},
		{"untraced must be <= requirements", s.Untraced, s.Requirements},
		{"builders.agree must be <= builders.total", s.Builders.Agree, s.Builders.Total},
	}
	if s.Build != nil {
		parts = append(parts, struct {
			name        string
			part, whole int64
		}{"build.hidden_pass must be <= build.hidden_total", s.Build.HiddenPass, s.Build.HiddenTotal})
	}
	for _, p := range parts {
		if p.part > p.whole {
			return errors.New("payload." + p.name)
		}
	}
	for name, c := range map[string]float64{"person": s.BenchCostUSD.Person, "judge": s.BenchCostUSD.Judge, "builders": s.BenchCostUSD.Builders, "build": s.BenchCostUSD.Build} {
		if c < 0 {
			return fmt.Errorf("payload.bench_cost_usd.%s must be >= 0", name)
		}
	}
	return nil
}
