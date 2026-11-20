package fakeclaude

import (
	"bytes"
	"strings"
	"testing"

	"factorybench/internal/contextbench/claude"
)

func call(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

var good = []string{"-p", "<requirements>\nR1: a\n</requirements>", "--output-format", "json", "--model", "m", "--system-prompt", "ROLE: builder\n\nx",
	"--tools", "", "--json-schema", "{}", "--max-budget-usd", "2", "--no-session-persistence", "--setting-sources", "project"}

func TestTheFakeIsStrictAboutTheFlags(t *testing.T) {
	if code, out, errs := call(good...); code != 0 || !strings.Contains(out, `"structured_output"`) {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	mutate := func(drop, add string) []string {
		var a []string
		for i := 0; i < len(good); i++ {
			if good[i] == drop {
				if drop != "--no-session-persistence" {
					i++
				}
				continue
			}
			a = append(a, good[i])
		}
		if add != "" {
			a = append(a, add, "1")
		}
		return a
	}
	for name, args := range map[string][]string{
		"no role line":    append(append([]string{}, good[:7]...), append([]string{"no role"}, good[8:]...)...),
		"tools not empty": append(append([]string{}, good[:9]...), append([]string{"Read"}, good[10:]...)...),
		"missing schema":  mutate("--json-schema", ""),
		"missing persist": mutate("--no-session-persistence", ""),
		"missing sources": mutate("--setting-sources", ""),
		"unknown flag":    mutate("", "--frobnicate"),
		"no -p":           good[1:],
		"unknown role":    append(append([]string{}, good[:7]...), append([]string{"ROLE: wizard\n\nx"}, good[8:]...)...),
	} {
		if code, _, _ := call(args...); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheFakeReportsUsageLikeClaudeDoes(t *testing.T) {
	_, out, _ := call(good...)
	r, err := claude.ParseResult([]byte(out))
	u := r.Usage
	if err != nil || r.Cost != 0.01 || u.Cost == nil || *u.Cost != 0.01 || u.Input != 100 || u.Output != 20 || u.CacheRead != 10 || u.CacheWrite != 5 || u.DurationMS != 50 {
		t.Errorf("%+v %v", r, err)
	}
}
