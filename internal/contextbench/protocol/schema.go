package protocol

import (
	"encoding/json"
	"strings"
)

func mustJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("bad schema: " + err.Error())
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// SubjectSchema is the JSON schema of the subject reply. plain restricts the move to ask.
func SubjectSchema(plain bool) string {
	enum := `"` + strings.Join(Moves, `","`) + `"`
	if plain {
		enum = `"ask"`
	}
	return mustJSON(`{
  "type": "object",
  "properties": {
    "move": {"type": "string", "enum": [` + enum + `]},
    "message": {"type": "string"},
    "done": {"type": "boolean"},
    "change": {
      "type": "object",
      "properties": {
        "files": {"type": "array", "items": {
          "type": "object",
          "properties": {"path": {"type": "string"}, "content": {"type": "string"}},
          "required": ["path", "content"], "additionalProperties": false}}
      },
      "required": ["files"], "additionalProperties": false
    }
  },
  "required": ["move", "message", "done"],
  "additionalProperties": false
}`)
}

const tagProps = `
    "revealed": {"type": "array", "items": {"type": "string"}},
    "dont_know": {"type": "boolean"},
    "could_not_answer": {"type": "boolean"},
    "already_said": {"type": "boolean"},
    "in_attachments": {"type": "boolean"},
    "suggestion": {"type": "string", "enum": ["accepted", "rejected", "none"]},
    "frustration": {"type": "integer", "minimum": 0, "maximum": 5},
    "confirmed": {"type": "boolean"}`

const tagRequired = `"revealed", "dont_know", "could_not_answer", "already_said", "in_attachments", "suggestion", "frustration", "confirmed"`

// PersonSchema is the JSON schema of the person reply.
var PersonSchema = mustJSON(`{
  "type": "object",
  "properties": {
    "reply": {"type": "string"},` + tagProps + `
  },
  "required": ["reply", ` + tagRequired + `],
  "additionalProperties": false
}`)

// TaggerSchema is the JSON schema of the tagger reply.
var TaggerSchema = mustJSON(`{
  "type": "object",
  "properties": {
    "turns": {"type": "array", "items": {
      "type": "object",
      "properties": {
        "turn": {"type": "integer"},` + tagProps + `
      },
      "required": ["turn", ` + tagRequired + `],
      "additionalProperties": false}}
  },
  "required": ["turns"],
  "additionalProperties": false
}`)

// JudgeSchema is the JSON schema of the judge reply.
var JudgeSchema = mustJSON(`{
  "type": "object",
  "properties": {
    "items": {"type": "array", "items": {
      "type": "object",
      "properties": {"id": {"type": "string"}, "captured": {"type": "string", "enum": ["full", "partial", "none"]}, "evidence": {"type": "string"}},
      "required": ["id", "captured", "evidence"], "additionalProperties": false}},
    "traps": {"type": "array", "items": {
      "type": "object",
      "properties": {"id": {"type": "string"}, "raised": {"type": "boolean"}, "turn": {"type": "integer", "minimum": 0},
                     "followed_resolution": {"type": "boolean"}, "evidence": {"type": "string"}},
      "required": ["id", "raised", "turn", "followed_resolution", "evidence"], "additionalProperties": false}},
    "invented": {"type": "array", "items": {
      "type": "object",
      "properties": {"requirement": {"type": "string"}, "marked_assumed": {"type": "boolean"}, "why": {"type": "string"}},
      "required": ["requirement", "marked_assumed", "why"], "additionalProperties": false}},
    "map_tag_errors": {"type": "array", "items": {
      "type": "object",
      "properties": {"bullet": {"type": "string"}, "tag": {"type": "string"}, "should_be": {"type": "string"}, "why": {"type": "string"}},
      "required": ["bullet", "tag", "should_be", "why"], "additionalProperties": false}},
    "untraced": {"type": "array", "items": {"type": "string"}},
    "noise_present": {"type": "array", "items": {"type": "string"}},
    "missing_slots": {"type": "array", "items": {
      "type": "object",
      "properties": {"requirement": {"type": "string"}, "slot": {"type": "integer", "enum": [4, 5]}, "why": {"type": "string"}},
      "required": ["requirement", "slot", "why"], "additionalProperties": false}},
    "contradictions": {"type": "array", "items": {
      "type": "object",
      "properties": {"requirement": {"type": "string"}, "with": {"type": "string"}, "why": {"type": "string"}},
      "required": ["requirement", "with", "why"], "additionalProperties": false}}
  },
  "required": ["items", "traps", "invented", "map_tag_errors", "untraced", "noise_present", "missing_slots", "contradictions"],
  "additionalProperties": false
}`)

// BuilderSchema is the JSON schema of a builder reply.
var BuilderSchema = mustJSON(`{
  "type": "object",
  "properties": {
    "requirements": {"type": "array", "items": {
      "type": "object",
      "properties": {
        "requirement": {"type": "string"},
        "tests": {"type": "array", "items": {
          "type": "object",
          "properties": {"name": {"type": "string"}, "given": {"type": "string"}, "when": {"type": "string"}, "then": {"type": "string"}},
          "required": ["name", "given", "when", "then"], "additionalProperties": false}}
      },
      "required": ["requirement", "tests"], "additionalProperties": false}}
  },
  "required": ["requirements"],
  "additionalProperties": false
}`)

// CompareSchema is the JSON schema of the comparison reply.
var CompareSchema = mustJSON(`{
  "type": "object",
  "properties": {
    "requirements": {"type": "array", "items": {
      "type": "object",
      "properties": {"requirement": {"type": "string"}, "verdict": {"type": "string", "enum": ["agree", "disagree"]}, "reason": {"type": "string"}},
      "required": ["requirement", "verdict", "reason"], "additionalProperties": false}}
  },
  "required": ["requirements"],
  "additionalProperties": false
}`)

// ReplaySchema is the JSON schema of the replay judge reply.
var ReplaySchema = mustJSON(`{
  "type": "object",
  "properties": {
    "turns": {"type": "array", "items": {
      "type": "object",
      "properties": {"turn": {"type": "integer"}, "verdict": {"type": "string", "enum": ["same", "partial", "different"]},
                     "real_dont_know": {"type": "boolean"}, "sim_dont_know": {"type": "boolean"}, "reason": {"type": "string"}},
      "required": ["turn", "verdict", "real_dont_know", "sim_dont_know", "reason"], "additionalProperties": false}}
  },
  "required": ["turns"],
  "additionalProperties": false
}`)
