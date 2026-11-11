// Package claudecode translates `claude -p --output-format stream-json --verbose` output into contract payloads.
// It is a test-harness helper (scripts/claude-code-run.sh), not part of FactoryBench proper.
package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"sort"
)

// Result is what one Claude Code run reports: usage payloads (one per model) and the attempt.finished payload.
type Result struct {
	Usage    []map[string]any `json:"usage"`    // contract usage payloads; nil when no result line was seen
	Finished map[string]any   `json:"finished"` // contract attempt.finished payload
}

// Translate reads a stream and returns the usage payloads from its result line and the finish status.
// modelUsage (per model, every call, the basis of total_cost_usd) gives one payload per model; without it,
// the top-level usage (main-loop turns only) and total_cost_usd give one payload for the init model.
func Translate(r io.Reader) (Result, error) {
	var model string
	var res *struct {
		IsError      bool     `json:"is_error"`
		Subtype      string   `json:"subtype"`
		TotalCostUSD *float64 `json:"total_cost_usd"`
		DurationMS   *int64   `json:"duration_ms"`
		Usage        struct {
			Input      *int64 `json:"input_tokens"`
			Output     *int64 `json:"output_tokens"`
			CacheRead  *int64 `json:"cache_read_input_tokens"`
			CacheWrite *int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		ModelUsage map[string]struct {
			Input      *int64   `json:"inputTokens"`
			Output     *int64   `json:"outputTokens"`
			CacheRead  *int64   `json:"cacheReadInputTokens"`
			CacheWrite *int64   `json:"cacheCreationInputTokens"`
			Thinking   *int64   `json:"thinkingTokens"`
			CostUSD    *float64 `json:"costUSD"`
		} `json:"modelUsage"`
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var line struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Model   string `json:"model"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Type == "system" && line.Subtype == "init" {
			model = line.Model
		}
		if line.Type == "result" {
			if err := json.Unmarshal(sc.Bytes(), &res); err != nil {
				return Result{}, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Result{}, err
	}
	if res == nil {
		return Result{Finished: map[string]any{"status": "failed", "reason": "no result line"}}, nil
	}
	// Optional fields are sent only when Claude Code reported them; unknown stays absent, never 0.
	set := func(u map[string]any, k string, v *int64) {
		if v != nil {
			u[k] = *v
		}
	}
	// model, input and output are required by the contract: without them a payload can't be sent,
	// and an unknown session is sent as no usage at all, never as zeros.
	payload := func(model string, in, out *int64) map[string]any {
		if model == "" || in == nil || out == nil {
			return nil
		}
		return map[string]any{"model": model, "input": *in, "output": *out}
	}
	var usage []map[string]any
	if len(res.ModelUsage) > 0 {
		models := make([]string, 0, len(res.ModelUsage))
		for m := range res.ModelUsage {
			models = append(models, m)
		}
		sort.Strings(models) // deterministic order; JSON object order isn't kept by Go maps
		for _, m := range models {
			mu := res.ModelUsage[m]
			u := payload(m, mu.Input, mu.Output)
			if u == nil {
				continue
			}
			set(u, "cache_read", mu.CacheRead)
			set(u, "cache_write", mu.CacheWrite)
			set(u, "reasoning", mu.Thinking)
			if mu.CostUSD != nil {
				u["reported_cost_usd"] = *mu.CostUSD
			}
			usage = append(usage, u)
		}
	} else if u := payload(model, res.Usage.Input, res.Usage.Output); u != nil {
		set(u, "cache_read", res.Usage.CacheRead)
		set(u, "cache_write", res.Usage.CacheWrite)
		if res.TotalCostUSD != nil {
			u["reported_cost_usd"] = *res.TotalCostUSD
		}
		usage = append(usage, u)
	}
	// duration_ms is the whole run's wall clock. It is one model's session time only when the run used one model;
	// with several it can't be split, so it stays unknown.
	if res.DurationMS != nil && len(usage) == 1 {
		usage[0]["duration_ms"] = *res.DurationMS
	}
	fin := map[string]any{"status": "completed"}
	if res.IsError {
		fin = map[string]any{"status": "failed", "reason": res.Subtype}
	}
	return Result{Usage: usage, Finished: fin}, nil
}
