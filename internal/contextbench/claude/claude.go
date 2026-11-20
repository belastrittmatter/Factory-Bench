// Package claude calls models through the `claude` CLI. Every call is one fresh session with no tools.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MaxArgBytes is the longest prompt passed as an argument; a longer one goes to claude's stdin (one argument is
// limited to 128 KiB on Linux).
const MaxArgBytes = 100_000

type Request struct {
	Role    string
	Model   string
	System  string
	Prompt  string
	Schema  string
	MaxCost float64 // USD cap for the session (claude --max-budget-usd)
}

// Usage is what claude reported a session used. Cost is nil when claude gave none.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int64
	DurationMS                           int64
	Cost                                 *float64
}

type Response struct {
	Output json.RawMessage
	Cost   float64 // total_cost_usd, set even when the call failed
	Usage  Usage   // set even when the call failed
}

type Runner interface {
	Run(ctx context.Context, req Request) (Response, error)
}

// SystemPrompt is the system prompt claude gets: the role line, a blank line, then the role's prompt.
func SystemPrompt(req Request) string { return "ROLE: " + req.Role + "\n\n" + req.System }

// Args builds the claude command line of a request: the arguments and the text for stdin ("" when the prompt
// goes in the arguments).
func Args(req Request) (args []string, stdin string, err error) {
	sys := SystemPrompt(req)
	if len(sys) > MaxArgBytes {
		return nil, "", fmt.Errorf("%s system prompt is %d bytes, over the %d argument limit", req.Role, len(sys), MaxArgBytes)
	}
	args = []string{"-p"}
	if len(req.Prompt) > MaxArgBytes || strings.HasPrefix(req.Prompt, "-") {
		stdin = req.Prompt
	} else {
		args = append(args, req.Prompt)
	}
	args = append(args, "--output-format", "json", "--model", req.Model, "--system-prompt", sys, "--tools", "",
		"--json-schema", req.Schema, "--max-budget-usd", strconv.FormatFloat(req.MaxCost, 'f', -1, 64),
		"--no-session-persistence", "--setting-sources", "project")
	return args, stdin, nil
}

// Program is the claude program: FACTORYBENCH_CLAUDE, else `claude`.
func Program() string {
	if p := os.Getenv("FACTORYBENCH_CLAUDE"); p != "" {
		return p
	}
	return "claude"
}

type Exec struct {
	Program string
	Timeout time.Duration // per session; 0 means 20 minutes
}

func (e Exec) Run(ctx context.Context, req Request) (Response, error) {
	args, stdin, err := Args(req)
	if err != nil {
		return Response{}, err
	}
	to := e.Timeout
	if to == 0 {
		to = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Program, args...)
	// an empty working directory: --setting-sources project then finds no project settings, hooks or CLAUDE.md, so
	// what a session knows does not depend on where the bench was started
	if cmd.Dir, err = os.MkdirTemp("", "factorybench-claude-"); err != nil {
		return Response{}, err
	}
	defer os.RemoveAll(cmd.Dir)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	return Interpret(e.Program, req.Role, runErr, out.Bytes(), errb.Bytes())
}

// Interpret turns what a claude process did (its exit error, stdout and stderr) into a Response or an error.
// The cost is set even when the call failed.
func Interpret(program, role string, runErr error, stdout, stderr []byte) (Response, error) {
	resp, perr := ParseResult(stdout)
	if runErr != nil && (perr == nil || errors.Is(perr, errNoJSON)) {
		perr = fmt.Errorf("%s exited: %v: %s", program, runErr, strings.TrimSpace(string(stderr)))
	}
	if perr != nil {
		return resp, fmt.Errorf("%s session: %w", role, perr)
	}
	return resp, nil
}

var errNoJSON = errors.New("no JSON result on stdout")

// ParseResult reads the JSON object `claude --output-format json` prints. The structured reply is
// `structured_output`; when that is absent the JSON in `result` is used.
func ParseResult(raw []byte) (Response, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Response{}, errNoJSON
	}
	var r struct {
		Type             string          `json:"type"`
		Subtype          string          `json:"subtype"`
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		TotalCostUSD     *float64        `json:"total_cost_usd"`
		DurationMS       int64           `json:"duration_ms"`
		Usage            struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Response{}, fmt.Errorf("%w: %v", errNoJSON, err)
	}
	resp := Response{Usage: Usage{Input: r.Usage.Input, Output: r.Usage.Output, CacheRead: r.Usage.CacheRead,
		CacheWrite: r.Usage.CacheWrite, DurationMS: r.DurationMS, Cost: r.TotalCostUSD}}
	if r.TotalCostUSD != nil {
		resp.Cost = *r.TotalCostUSD
	}
	if r.IsError || strings.HasPrefix(r.Subtype, "error") {
		msg := strings.TrimSpace(r.Result)
		if msg == "" {
			msg = r.Subtype
		}
		return resp, fmt.Errorf("claude reported an error (%s): %s", r.Subtype, msg)
	}
	if so := bytes.TrimSpace(r.StructuredOutput); len(so) > 0 && !bytes.Equal(so, []byte("null")) {
		resp.Output = so
		return resp, nil
	}
	if js, ok := extractJSON(r.Result); ok {
		resp.Output = js
		return resp, nil
	}
	return resp, fmt.Errorf("the reply has no structured output")
}

func extractJSON(s string) (json.RawMessage, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			s = strings.TrimSpace(rest[:j])
		}
	}
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	cand := []byte(s[start : end+1])
	if !json.Valid(cand) {
		return nil, false
	}
	return cand, true
}
