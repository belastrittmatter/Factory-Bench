package protocol

import (
	"fmt"
	"slices"
	"strings"
)

// Captured values of a truth item in a change folder.
const (
	Full    = "full"
	Partial = "partial"
	None    = "none"
)

// JudgeItem is the judge's verdict on one truth item.
type JudgeItem struct {
	ID       string `json:"id"`
	Captured string `json:"captured"`
	Evidence string `json:"evidence"`
}

// JudgeTrap is the judge's verdict on one trap. Turn is the transcript turn in which the subject raised it
// (0 when it never did).
type JudgeTrap struct {
	ID                 string `json:"id"`
	Raised             bool   `json:"raised"`
	Turn               int    `json:"turn"`
	FollowedResolution bool   `json:"followed_resolution"`
	Evidence           string `json:"evidence"`
}

// Invented is a requirement that nothing in the truth or the transcript supports.
type Invented struct {
	Requirement   string `json:"requirement"`
	MarkedAssumed bool   `json:"marked_assumed"`
	Why           string `json:"why"`
}

// MapTagError is a map bullet whose source tag is wrong.
type MapTagError struct {
	Bullet   string `json:"bullet"`
	Tag      string `json:"tag"`
	ShouldBe string `json:"should_be"`
	Why      string `json:"why"`
}

// MissingSlot is a requirement that lacked a slot 4 (Failure) or slot 5 (Visual) it needed.
type MissingSlot struct {
	Requirement string `json:"requirement"`
	Slot        int    `json:"slot"`
	Why         string `json:"why"`
}

// Contradiction is a requirement that contradicts a living spec or a hard limit.
type Contradiction struct {
	Requirement string `json:"requirement"`
	With        string `json:"with"`
	Why         string `json:"why"`
}

// Judge is the judge's whole verdict.
type Judge struct {
	Items          []JudgeItem     `json:"items"`
	Traps          []JudgeTrap     `json:"traps"`
	Invented       []Invented      `json:"invented"`
	MapTagErrors   []MapTagError   `json:"map_tag_errors"`
	Untraced       []string        `json:"untraced"`
	NoisePresent   []string        `json:"noise_present"`
	MissingSlots   []MissingSlot   `json:"missing_slots"`
	Contradictions []Contradiction `json:"contradictions"`
}

// ParseJudge decodes a judge reply and checks it against the truth ids and the requirement ids of the change.
func ParseJudge(raw []byte, itemIDs, trapIDs, noiseIDs, reqIDs []string) (Judge, error) {
	var j Judge
	if err := decodeStrict(raw, &j, "items", "traps", "invented", "map_tag_errors", "untraced", "noise_present",
		"missing_slots", "contradictions"); err != nil {
		return j, err
	}
	var errs []string
	if err := exactlyOnce("items", idsOf(j.Items, func(i JudgeItem) string { return i.ID }), itemIDs); err != nil {
		errs = append(errs, err.Error())
	}
	for _, it := range j.Items {
		if !slices.Contains([]string{Full, Partial, None}, it.Captured) {
			errs = append(errs, fmt.Sprintf("item %s: captured %q is not full, partial or none", it.ID, it.Captured))
		}
	}
	if err := exactlyOnce("traps", idsOf(j.Traps, func(t JudgeTrap) string { return t.ID }), trapIDs); err != nil {
		errs = append(errs, err.Error())
	}
	for _, t := range j.Traps {
		if t.Raised && t.Turn < 1 {
			errs = append(errs, fmt.Sprintf("trap %s: raised but turn is %d", t.ID, t.Turn))
		}
		if !t.Raised && t.FollowedResolution {
			errs = append(errs, fmt.Sprintf("trap %s: followed_resolution is true but raised is false", t.ID))
		}
	}
	for _, in := range j.Invented {
		if !slices.Contains(reqIDs, in.Requirement) {
			errs = append(errs, fmt.Sprintf("invented: %q is not a requirement id", in.Requirement))
		}
	}
	for _, id := range j.Untraced {
		if !slices.Contains(reqIDs, id) {
			errs = append(errs, fmt.Sprintf("untraced: %q is not a requirement id", id))
		}
	}
	for _, id := range j.NoisePresent {
		if !slices.Contains(noiseIDs, id) {
			errs = append(errs, fmt.Sprintf("noise_present: %q is not a noise id", id))
		}
	}
	for _, m := range j.MissingSlots {
		if !slices.Contains(reqIDs, m.Requirement) || (m.Slot != 4 && m.Slot != 5) {
			errs = append(errs, fmt.Sprintf("missing_slots: bad entry %+v", m))
		}
	}
	for _, c := range j.Contradictions {
		if !slices.Contains(reqIDs, c.Requirement) {
			errs = append(errs, fmt.Sprintf("contradictions: %q is not a requirement id", c.Requirement))
		}
	}
	if len(errs) > 0 {
		return j, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return j, nil
}

func idsOf[T any](xs []T, id func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = id(x)
	}
	return out
}

// exactlyOnce checks that got lists every id of want once and nothing else.
func exactlyOnce(what string, got, want []string) error {
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	var bad []string
	for _, g := range got {
		if !slices.Contains(want, g) {
			bad = append(bad, fmt.Sprintf("unknown id %q", g))
		}
	}
	for _, w := range want {
		switch seen[w] {
		case 0:
			bad = append(bad, fmt.Sprintf("%s is missing", w))
		case 1:
		default:
			bad = append(bad, fmt.Sprintf("%s appears %d times", w, seen[w]))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s: %s", what, strings.Join(bad, ", "))
	}
	return nil
}

// BuilderTest is one scenario a builder would test.
type BuilderTest struct {
	Name  string `json:"name"`
	Given string `json:"given"`
	When  string `json:"when"`
	Then  string `json:"then"`
}

// BuilderRequirement is a builder's tests for one requirement.
type BuilderRequirement struct {
	Requirement string        `json:"requirement"`
	Tests       []BuilderTest `json:"tests"`
}

// BuilderReply is one builder's answer.
type BuilderReply struct {
	Requirements []BuilderRequirement `json:"requirements"`
}

// ParseBuilder decodes a builder reply: every requirement needs at least one test.
func ParseBuilder(raw []byte, reqIDs []string) (BuilderReply, error) {
	var r BuilderReply
	if err := decodeStrict(raw, &r, "requirements"); err != nil {
		return r, err
	}
	if err := exactlyOnce("requirements", idsOf(r.Requirements, func(b BuilderRequirement) string { return b.Requirement }), reqIDs); err != nil {
		return r, err
	}
	for _, b := range r.Requirements {
		if len(b.Tests) == 0 {
			return r, fmt.Errorf("requirement %s has no tests", b.Requirement)
		}
	}
	return r, nil
}

// Verdicts of the comparison.
const (
	Agree    = "agree"
	Disagree = "disagree"
)

// CompareVerdict is the comparison's verdict on one requirement.
type CompareVerdict struct {
	Requirement string `json:"requirement"`
	Verdict     string `json:"verdict"`
	Reason      string `json:"reason"`
}

// CompareReply is the comparison session's answer.
type CompareReply struct {
	Requirements []CompareVerdict `json:"requirements"`
}

// ParseCompare decodes a comparison reply.
func ParseCompare(raw []byte, reqIDs []string) (CompareReply, error) {
	var r CompareReply
	if err := decodeStrict(raw, &r, "requirements"); err != nil {
		return r, err
	}
	if err := exactlyOnce("requirements", idsOf(r.Requirements, func(c CompareVerdict) string { return c.Requirement }), reqIDs); err != nil {
		return r, err
	}
	for _, c := range r.Requirements {
		if c.Verdict != Agree && c.Verdict != Disagree {
			return r, fmt.Errorf("requirement %s: verdict %q is not agree or disagree", c.Requirement, c.Verdict)
		}
	}
	return r, nil
}

// Replay verdicts.
const (
	Same      = "same"
	PartialV  = "partial"
	Different = "different"
)

// ReplayVerdict compares one simulated answer with the real one.
type ReplayVerdict struct {
	Turn         int    `json:"turn"`
	Verdict      string `json:"verdict"`
	RealDontKnow bool   `json:"real_dont_know"`
	SimDontKnow  bool   `json:"sim_dont_know"`
	Reason       string `json:"reason"`
}

// ReplayJudgeReply is the replay judge's answer.
type ReplayJudgeReply struct {
	Turns []ReplayVerdict `json:"turns"`
}

// ParseReplayJudge decodes the replay judge's answer for turns 1..n.
func ParseReplayJudge(raw []byte, n int) (ReplayJudgeReply, error) {
	var r ReplayJudgeReply
	if err := decodeStrict(raw, &r, "turns"); err != nil {
		return r, err
	}
	seen := map[int]bool{}
	for _, t := range r.Turns {
		if t.Turn < 1 || t.Turn > n || seen[t.Turn] {
			return r, fmt.Errorf("turn %d is out of range or repeated", t.Turn)
		}
		seen[t.Turn] = true
		if !slices.Contains([]string{Same, PartialV, Different}, t.Verdict) {
			return r, fmt.Errorf("turn %d: verdict %q is not same, partial or different", t.Turn, t.Verdict)
		}
	}
	if len(seen) != n {
		return r, fmt.Errorf("got verdicts for %d of %d turns", len(seen), n)
	}
	return r, nil
}
