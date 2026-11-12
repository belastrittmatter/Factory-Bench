package normalize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type toolCall struct {
	Factory, Attempt string
	Agent            *string
	TS, Tool         string
	Success          *bool
	Duration         *int64
	ErrorType        *string
}

type agentRun struct {
	Factory, Attempt, ID string
	Parent, Type         *string
	Async                *bool
	Model, FinalModel    *string
	ToolCalls            int64
	Duration             *int64
	Status, Started      string
	otlp, completed      bool
}

type apiError struct {
	Factory, Attempt string
	Agent            *string
	TS, Kind         string
	Model            *string
	Status           *int64
	Error            *string
	Duration         *int64
}

// attrs is an OTLP event payload: the record's attributes, as intake stored them.
type attrs map[string]any

func parseAttrs(b []byte) attrs {
	var a attrs
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	_ = d.Decode(&a)
	return a
}

func (a attrs) s(k string) string { s, _ := a[k].(string); return s }

// n, f and b also accept the value as a string: Claude Code sends e.g. tool_result success "true", duration_ms "3852".
func (a attrs) n(k string) *int64 {
	v, err := strconv.ParseInt(fmt.Sprint(a[k]), 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

func (a attrs) f(k string) *float64 {
	v, err := strconv.ParseFloat(fmt.Sprint(a[k]), 64)
	if err != nil {
		return nil
	}
	return &v
}

func (a attrs) b(k string) *bool {
	v, err := strconv.ParseBool(fmt.Sprint(a[k]))
	if err != nil {
		return nil
	}
	return &v
}

// agents collects the subagents seen on an attempt's events, keyed factory \x00 attempt \x00 agent_id.
type agents struct {
	runs  map[string]*agentRun
	order []string
}

// see records that agent id (none = the main agent) was active at e; parent and typ fill in once known.
func (g *agents) see(e event, id, parent, typ string, otlp bool) *agentRun {
	if id == "" {
		return nil
	}
	k := e.Factory + "\x00" + e.AttemptID + "\x00" + id
	r := g.runs[k]
	if r == nil {
		r = &agentRun{Factory: e.Factory, Attempt: e.AttemptID, ID: id, Started: e.TS}
		g.runs[k] = r
		g.order = append(g.order, k)
	}
	if r.Parent == nil {
		r.Parent = str(parent)
	}
	if r.Type == nil {
		r.Type = str(typ)
	}
	r.otlp = r.otlp || otlp
	return r
}

// finish counts each agent's tool calls and sets its status. An OTLP subagent without subagent_completed
// was killed once its attempt has ended; a contract-only one has no completion signal, so it ends with the attempt.
func (g *agents) finish(attempts map[string]*attempt, calls []toolCall) []agentRun {
	n := map[string]int64{}
	for _, c := range calls {
		if c.Agent != nil {
			n[c.Factory+"\x00"+c.Attempt+"\x00"+*c.Agent]++
		}
	}
	var out []agentRun
	for _, k := range g.order {
		r := g.runs[k]
		r.ToolCalls = n[k]
		switch {
		case r.completed:
			r.Status = "completed"
		case attempts[r.Factory+"\x00"+r.Attempt].Ended == nil:
			r.Status = "running"
		case r.otlp:
			r.Status = "killed"
		default:
			r.Status = "completed"
		}
		out = append(out, *r)
	}
	return out
}

// otlp derives rows from one OTLP event (Claude Code telemetry) of a started attempt. Unknown event names are ignored.
func (d *result) otlp(e event, g *agents) {
	at := parseAttrs(e.Payload)
	agent := str(at.s("agent_id"))
	typ := at.s("agent_type")
	if typ == "" {
		typ = at.s("agent.name")
	}
	r := g.see(e, at.s("agent_id"), at.s("parent_agent_id"), typ, true)
	switch e.Type {
	case "otlp.api_request":
		in, out, model := at.n("input_tokens"), at.n("output_tokens"), at.s("model")
		if in == nil || out == nil || model == "" {
			return
		}
		// subagent tokens come only from here: subagent_completed.total_tokens is the last request's size
		d.Usage = append(d.Usage, priced(usage{Factory: e.Factory, Attempt: e.AttemptID, Model: model, TS: e.TS, Input: *in,
			Output: *out, CacheRead: at.n("cache_read_tokens"), CacheWrite: at.n("cache_creation_tokens"),
			Duration: at.n("duration_ms"), Agent: agent}, at.f("cost_usd")))
		if r != nil && r.Model == nil {
			r.Model = ptr(model)
		}
	case "otlp.tool_result":
		d.ToolCalls = append(d.ToolCalls, toolCall{Factory: e.Factory, Attempt: e.AttemptID, Agent: agent, TS: e.TS,
			Tool: at.s("tool_name"), Success: at.b("success"), Duration: at.n("duration_ms"), ErrorType: str(at.s("error_type"))})
	case "otlp.subagent_completed":
		if r == nil {
			return
		}
		r.completed, r.Async, r.Duration, r.FinalModel = true, at.b("is_async"), at.n("duration_ms"), str(at.s("final_model"))
		if m := at.s("model"); m != "" {
			r.Model = ptr(m)
		}
	case "otlp.api_error", "otlp.api_retries_exhausted":
		// shape not verified yet (no capture): read the likely fields and tolerate their absence
		msg := at.s("error_type")
		for _, k := range []string{"error.type", "error"} {
			if msg == "" {
				msg = at.s(k)
			}
		}
		d.APIErrors = append(d.APIErrors, apiError{Factory: e.Factory, Attempt: e.AttemptID, Agent: agent, TS: e.TS,
			Kind: strings.TrimPrefix(e.Type, "otlp."), Model: str(at.s("model")), Status: at.n("status_code"),
			Error: str(msg), Duration: at.n("duration_ms")})
	}
}
