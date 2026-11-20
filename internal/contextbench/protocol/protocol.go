// Package protocol holds the message shapes of docs/context-bench.md: the subject reply, the person reply and its tags,
// the judge, builder and comparison shapes, their JSON schemas, and the transcript text format the prompts use.
package protocol

import (
	"bytes"
	"encoding/json"
	"factorybench/internal/contract"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// IsShowOrSuggest reports whether a move is one that may draw a latent item out.
func IsShowOrSuggest(move string) bool { return move == "show" || move == "suggest" }

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Change struct {
	Files []File `json:"files"`
}

type SubjectReply struct {
	Move    string  `json:"move"`
	Message string  `json:"message"`
	Done    bool    `json:"done"`
	Change  *Change `json:"change,omitempty"`
}

// PersonTags are the person-protocol tags of one person turn, filled by the simulated person with its reply.
type PersonTags struct {
	Revealed       []string `json:"revealed"`
	DontKnow       bool     `json:"dont_know"`
	CouldNotAnswer bool     `json:"could_not_answer"`
	AlreadySaid    bool     `json:"already_said"`
	InAttachments  bool     `json:"in_attachments"`
	Suggestion     string   `json:"suggestion"`
	Frustration    int      `json:"frustration"`
	Confirmed      bool     `json:"confirmed"`
}

type PersonReply struct {
	Reply string `json:"reply"`
	PersonTags
	At time.Time `json:"at,omitzero"` // when the harness got it; never part of the model's reply
}

type SubjectSaid struct {
	Move    string    `json:"move"`
	Message string    `json:"message"`
	Done    bool      `json:"done"`
	At      time.Time `json:"at,omitzero"`
}

// Turn is one line of transcript.jsonl: turn 0 is the person's first message (the brief), every other turn is
// one subject message and the person's answer to it (nil when the subject finished with that message).
type Turn struct {
	Turn       int          `json:"turn"`
	Subject    *SubjectSaid `json:"subject,omitempty"`
	Person     *PersonReply `json:"person,omitempty"`
	Violations []string     `json:"violations,omitempty"`
}

func SafePath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("path %q must be relative with forward slashes", p)
	}
	if c := path.Clean(p); c != p || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("path %q must be clean and stay inside the folder", p)
	}
	return nil
}

func decodeStrict(raw []byte, v any, required ...string) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("not a JSON object: %v", err)
	}
	for _, k := range required {
		if v, ok := keys[k]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("missing field %q", k)
		}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("wrong field type: %v", err)
	}
	return nil
}

// ParseSubject decodes and validates a subject reply. final is true on the forced last turn (no turns left);
// plain restricts the move to ask.
func ParseSubject(raw []byte, final, plain bool) (SubjectReply, error) {
	var r SubjectReply
	if err := decodeStrict(raw, &r, "move", "message", "done"); err != nil {
		return r, err
	}
	if !slices.Contains(contract.Moves, r.Move) {
		return r, fmt.Errorf("move %q is not one of %s", r.Move, strings.Join(contract.Moves, ", "))
	}
	if plain && r.Move != "ask" {
		return r, fmt.Errorf("this subject may only use the move ask, got %q", r.Move)
	}
	if !r.Done && strings.TrimSpace(r.Message) == "" {
		return r, fmt.Errorf("message is empty")
	}
	if final && !r.Done {
		return r, fmt.Errorf("no turns are left: the reply must have done: true and a change folder")
	}
	if r.Done {
		if r.Change == nil || len(r.Change.Files) == 0 {
			return r, fmt.Errorf("done is true but change.files is missing or empty")
		}
		seen := map[string]bool{}
		for _, f := range r.Change.Files {
			if err := SafePath(f.Path); err != nil {
				return r, fmt.Errorf("change.files: %v", err)
			}
			if seen[f.Path] {
				return r, fmt.Errorf("change.files: %q appears twice", f.Path)
			}
			seen[f.Path] = true
		}
		for p := range seen {
			for d := path.Dir(p); d != "."; d = path.Dir(d) {
				if seen[d] {
					return r, fmt.Errorf("change.files: %q is a file and also the folder of %q", d, p)
				}
			}
		}
	}
	return r, nil
}

// ParsePerson decodes and structurally validates a person reply (ids are checked against the truth apart).
func ParsePerson(raw []byte) (PersonReply, error) {
	var r PersonReply
	if err := decodeStrict(raw, &r, "reply", "revealed", "dont_know", "could_not_answer", "already_said",
		"in_attachments", "suggestion", "frustration", "confirmed"); err != nil {
		return r, err
	}
	if !slices.Contains(contract.Suggestions, r.Suggestion) {
		return r, fmt.Errorf("suggestion %q is not one of accepted, rejected, none", r.Suggestion)
	}
	if r.Frustration < 0 || r.Frustration > 5 {
		return r, fmt.Errorf("frustration %d is outside 0..5", r.Frustration)
	}
	if r.Revealed == nil {
		r.Revealed = []string{}
	}
	if strings.TrimSpace(r.Reply) == "" {
		return r, fmt.Errorf("reply is empty")
	}
	return r, nil
}
