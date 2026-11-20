package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"factorybench/internal/contextbench/claude"
	"factorybench/internal/contextbench/fakeclaude"
	"factorybench/internal/contextbench/protocol"
	"factorybench/internal/contextbench/scenario"
	"factorybench/internal/contract"
)

const (
	fixtures = "../fixtures"
	tinyDir  = fixtures + "/scenarios/tiny"
	creator  = "file:" + fixtures + "/subjects/fake-creator.md"
)

type recorder struct {
	mu   sync.Mutex
	reqs []claude.Request
}

func (r *recorder) Run(ctx context.Context, req claude.Request) (claude.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return fakeclaude.Runner{}.Run(ctx, req)
}

func (r *recorder) role(role string) []claude.Request {
	var out []claude.Request
	for _, q := range r.reqs {
		if q.Role == role {
			out = append(out, q)
		}
	}
	return out
}

type sink struct {
	mu  sync.Mutex
	raw [][]byte
}

func (s *sink) send(evs ...[]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = append(s.raw, evs...)
}

type event struct {
	contract.Event
	payload map[string]json.RawMessage
}

func (s *sink) events(t *testing.T) []event {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []event
	for _, raw := range s.raw {
		ev, _, err := contract.Validate(raw)
		if err != nil {
			t.Fatalf("an event the bench sent does not validate: %v: %s", err, raw)
		}
		e := event{Event: ev}
		_ = json.Unmarshal(ev.Payload, &e.payload)
		out = append(out, e)
	}
	return out
}

func ofType(evs []event, typ string) []event {
	var out []event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func testEnv(t *testing.T) (*Env, *recorder, *sink, *bytes.Buffer) {
	t.Helper()
	e := NewEnv(fixtures + "/scenarios")
	rec, sk := &recorder{}, &sink{}
	e.Runner, e.Send = rec, sk.send
	var out bytes.Buffer
	e.Stdout, e.Stderr = &out, &out
	return e, rec, sk, &out
}

func opts(t *testing.T, subject, person string) Options {
	return Options{Scenario: "tiny", Subject: subject, Person: person, Builders: 3, Models: DefaultModels(), MaxCost: 2, Out: t.TempDir(), Yes: true}
}

func loadTiny(t *testing.T) *scenario.Scenario {
	t.Helper()
	s, err := scenario.Load(tinyDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func run(t *testing.T, e *Env, o Options) *Outcome {
	t.Helper()
	oc, err := e.Run(context.Background(), o)
	if err != nil || oc == nil {
		t.Fatalf("%v %+v", err, oc)
	}
	return oc
}

func score(t *testing.T, sk *sink) contract.ContextScore {
	t.Helper()
	ss := ofType(sk.events(t), "context.score")
	if len(ss) != 1 {
		t.Fatalf("%d context.score events", len(ss))
	}
	return contract.ParseContextScore(ss[0].Payload)
}

func TestCheckPerson(t *testing.T) {
	s := loadTiny(t)
	tags := func(ids ...string) *protocol.PersonTags {
		return &protocol.PersonTags{Revealed: ids, Suggestion: "accepted"}
	}

	seen := map[string]bool{}
	tg := tags("T6", "T1")
	v, err := CheckPerson(&s.Truth, seen, "ask", tg)
	if err != nil || len(v) != 1 || !strings.Contains(v[0], "T6") || !seen["T6"] || !seen["T1"] {
		t.Errorf("violation: %v %v %v", v, err, seen)
	}
	if tg.Suggestion != "none" {
		t.Error("a suggestion tag after ask must become none")
	}

	for _, move := range []string{"show", "suggest"} {
		tg := tags("T7")
		v, err := CheckPerson(&s.Truth, map[string]bool{}, move, tg)
		if err != nil || len(v) != 0 || tg.Suggestion != "accepted" {
			t.Errorf("%s: %v %v %s", move, v, err, tg.Suggestion)
		}
	}
	for _, move := range []string{"capture", "mirror", "expand", "sharpen", "challenge", "narrow", "ask", "confirm"} {
		v, _ := CheckPerson(&s.Truth, map[string]bool{}, move, tags("T7"))
		if len(v) != 1 {
			t.Errorf("%s: latent reveal not counted: %v", move, v)
		}
	}
	if v, _ := CheckPerson(&s.Truth, map[string]bool{}, "ask", tags("T2")); len(v) != 0 {
		t.Errorf("non-latent: %v", v)
	}

	_, err = CheckPerson(&s.Truth, map[string]bool{}, "ask", tags("T2", "ZZ9"))
	if err == nil || !IsInvalid(err) || !strings.Contains(err.Error(), "ZZ9") {
		t.Fatalf("unknown id: %v", err)
	}

	seen = map[string]bool{"T2": true}
	tg = tags("T2", "N1", "T3", "T3", "X1")
	if _, err := CheckPerson(&s.Truth, seen, "ask", tg); err != nil || strings.Join(tg.Revealed, ",") != "T3,X1" {
		t.Errorf("revealed %v %v", tg.Revealed, err)
	}
	tg = tags()
	CheckPerson(&s.Truth, seen, "ask", tg)
	if tg.Revealed == nil {
		t.Error("revealed must serialise as [] not null")
	}
}

func TestNoTruthReachesTheSubjectOrTheBuilders(t *testing.T) {
	e, rec, _, _ := testEnv(t)
	s := loadTiny(t)
	o := opts(t, creator, "talkative")
	o.MaxTurns = 10
	out := run(t, e, o)
	if out.Status != StatusCompleted {
		t.Fatalf("%+v", out)
	}
	turns, _ := ReadTranscript(filepath.Join(out.Dir, "transcript.jsonl"))
	said := func(upTo int) string {
		var b strings.Builder
		b.WriteString(BriefMessage(s))
		for _, a := range s.Attachments {
			b.Write(a.Data)
		}
		for _, tn := range turns {
			if tn.Person != nil && tn.Turn <= upTo {
				b.WriteString(" " + tn.Person.Reply)
			}
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	var secrets []string
	for _, it := range s.Truth.Items {
		secrets = append(secrets, it.Text, it.LatentHint)
	}
	for _, tr := range s.Truth.Traps {
		secrets = append(secrets, tr.Text, tr.Resolution)
	}
	for _, n := range s.Truth.Noise {
		secrets = append(secrets, n.Text)
	}
	subj := rec.role("subject")
	if len(subj) != 8 {
		t.Fatalf("%d subject calls", len(subj))
	}
	for i, q := range subj {
		all := q.System + "\n" + q.Prompt
		if strings.Contains(all, string(s.TruthRaw)) || strings.Contains(all, "latent_hint") || strings.Contains(all, "truth.json") {
			t.Errorf("subject call %d holds truth.json", i+1)
		}
		heard := said(i)
		for _, sec := range secrets {
			if sec != "" && strings.Contains(strings.Join(strings.Fields(all), " "), sec) && !strings.Contains(heard, sec) {
				t.Errorf("subject call %d holds %q before the person said it", i+1, sec)
			}
		}
	}
	for _, role := range []string{"builder", "comparison"} {
		for i, q := range rec.role(role) {
			all := q.System + "\n" + q.Prompt
			for _, bad := range []string{"<transcript>", "[turn ", "FAKE-SUBJECT-TURN", "<truth>", "latent_hint"} {
				if strings.Contains(all, bad) {
					t.Errorf("%s call %d contains %q", role, i+1, bad)
				}
			}
		}
	}
	if len(rec.role("builder")) != 3 || len(rec.role("comparison")) != 1 || len(rec.role("judge")) != 1 {
		t.Errorf("builders %d comparison %d judge %d", len(rec.role("builder")), len(rec.role("comparison")), len(rec.role("judge")))
	}
	if !strings.Contains(rec.role("judge")[0].Prompt, "<truth>") || !strings.Contains(rec.role("person")[0].Prompt, "<truth>") {
		t.Error("the judge and the person must get the truth")
	}
	if !strings.Contains(rec.role("builder")[0].Prompt, "living-specs/chat-picker/spec.md") {
		t.Error("builders get the living specs")
	}
	for _, q := range rec.role("person") {
		if q.Model == rec.role("subject")[0].Model {
			t.Error("person and subject ran on the same model")
		}
	}
}

func TestRefusesPersonModelEqualToSubjectModel(t *testing.T) {
	e, rec, sk, _ := testEnv(t)
	o := opts(t, "plain", "unsure")
	o.Models.Person = o.Models.Subject
	if _, err := e.Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "different models") {
		t.Fatalf("%v", err)
	}
	if len(rec.reqs) != 0 || len(sk.raw) != 0 {
		t.Error("a refused run called a model or sent an event")
	}
}

func TestWithoutYesNothingIsCalledWrittenOrSent(t *testing.T) {
	e, rec, sk, out := testEnv(t)
	o := opts(t, "plain", "unsure")
	o.Yes = false
	oc, err := e.Run(context.Background(), o)
	if err != nil || oc != nil || len(rec.reqs) != 0 || len(sk.raw) != 0 {
		t.Fatalf("%v %v %d calls %d events", err, oc, len(rec.reqs), len(sk.raw))
	}
	for _, want := range []string{"plan", "scenario  tiny", "work item bench/tiny-context#1", "subject", "person", "judge", "builder", "comparison", "--yes", "[INFERENCE"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
	if entries, _ := os.ReadDir(o.Out); len(entries) != 0 {
		t.Error("a plan wrote files")
	}
}

func TestPrepareErrors(t *testing.T) {
	e, _, _, _ := testEnv(t)
	for name, mod := range map[string]func(*Options){
		"no person":       func(o *Options) { o.Person = "" },
		"unknown persona": func(o *Options) { o.Person = "grumpy" },
		"bad subject":     func(o *Options) { o.Subject = "smart" },
		"missing file":    func(o *Options) { o.Subject = "file:nope.md" },
		"no scenario":     func(o *Options) { o.Scenario = "nope" },
		"no builders":     func(o *Options) { o.Builders = 0 },
	} {
		o := opts(t, "plain", "unsure")
		mod(&o)
		if _, err := e.Prepare(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	o := opts(t, "none", "")
	if p, err := e.Prepare(o); err != nil || p.PersonaID != "-" {
		t.Errorf("none needs no person: %v", err)
	}
	o.Models.Person = o.Models.Subject
	if _, err := e.Prepare(o); err != nil {
		t.Error(err)
	}
}

func TestUnknownIDFailsTheAttemptButKeepsAndSendsTheConversation(t *testing.T) {
	t.Setenv("FACTORYBENCH_FAKE_UNKNOWN_ID", "1")
	e, rec, sk, _ := testEnv(t)
	oc := run(t, e, opts(t, "plain", "unsure"))
	if oc.Status != StatusFailed || !strings.Contains(oc.Note, "unknown id") || oc.Score != nil {
		t.Errorf("%s %q", oc.Status, oc.Note)
	}
	if len(rec.role("judge")) != 0 {
		t.Error("an invalid run was scored")
	}
	for _, f := range []string{"run.json", "transcript.jsonl", "events.jsonl"} {
		if _, err := os.Stat(filepath.Join(oc.Dir, f)); err != nil {
			t.Error(err)
		}
	}
	evs := sk.events(t)
	last := evs[len(evs)-1]
	var fin struct{ Status, Reason string }
	_ = json.Unmarshal(last.Payload, &fin)
	if last.Type != "attempt.finished" || fin.Status != "failed" || !strings.Contains(fin.Reason, "unknown id") {
		t.Errorf("last event %s %+v", last.Type, fin)
	}
	if len(ofType(evs, "context.turn")) == 0 || len(ofType(evs, "usage")) == 0 || len(ofType(evs, "context.score")) != 0 || len(ofType(evs, "gate")) != 0 {
		t.Error("a failed conversation must report its turns and usage and nothing it was not scored for")
	}
}

func TestSubjectThatNeverFinishesFails(t *testing.T) {
	e, _, sk, _ := testEnv(t)
	e.Runner = runnerFunc(func(ctx context.Context, req claude.Request) (claude.Response, error) {
		if req.Role == "subject" {
			return claude.Response{Output: json.RawMessage(`{"move":"ask","message":"more?","done":false}`), Cost: 0.01, Usage: claude.Usage{Input: 1, Output: 1}}, nil
		}
		return fakeclaude.Runner{}.Run(ctx, req)
	})
	o := opts(t, creator, "unsure")
	o.MaxTurns = 3
	oc := run(t, e, o)
	if oc.Status != StatusFailed || !strings.Contains(oc.Note, "no turns are left") {
		t.Fatalf("%+v", oc)
	}
	if n := len(ofType(sk.events(t), "usage")); n != 4 {
		t.Errorf("%d usage events, want one per subject session (4)", n)
	}
	if oc.SubjectCostUSD < 0.039 {
		t.Errorf("subject cost %v: the repair call was not made or metered", oc.SubjectCostUSD)
	}
}

type runnerFunc func(context.Context, claude.Request) (claude.Response, error)

func (f runnerFunc) Run(ctx context.Context, req claude.Request) (claude.Response, error) {
	return f(ctx, req)
}

func TestModelCallFailureFailsTheAttempt(t *testing.T) {
	t.Setenv("FACTORYBENCH_FAKE_FAIL", "person")
	e, _, _, _ := testEnv(t)
	oc := run(t, e, opts(t, "plain", "unsure"))
	if oc.Status != StatusFailed || !strings.Contains(oc.Note, "person") {
		t.Fatalf("%+v", oc)
	}
}

func TestHumanRunIsTaggedAfterwards(t *testing.T) {
	e, rec, sk, out := testEnv(t)
	script, _ := os.ReadFile(fixtures + "/human-script.txt")
	e.Stdin = bytes.NewReader(script)
	o := opts(t, creator, "human")
	o.MaxTurns = 10
	oc := run(t, e, o)
	if oc.Status != StatusCompleted {
		t.Fatalf("%+v", oc)
	}
	if len(rec.role("person")) != 0 || len(rec.role("tagger")) != 1 {
		t.Errorf("person %d tagger %d", len(rec.role("person")), len(rec.role("tagger")))
	}
	if !strings.Contains(out.String(), "Things you really want") || !strings.Contains(out.String(), "A message from an unticked chat is never sent anywhere.") {
		t.Error("the truth was not shown to the human")
	}
	turns, _ := ReadTranscript(filepath.Join(oc.Dir, "transcript.jsonl"))
	if turns[1].Person.Reply != "Sales reps who use WhatsApp on their own laptops are the only users." || len(turns[1].Person.Revealed) != 1 {
		t.Errorf("%+v", turns[1].Person)
	}
	s := score(t, sk)
	if s.Persona != "human" || s.SimViolations != 0 || s.LatentByMove["show"] != 1 {
		t.Errorf("%+v", s)
	}
	for _, e := range ofType(sk.events(t), "context.turn") {
		if ct := contract.ParseContextTurn(e.Payload); ct.Speaker == "person" && ct.TaggedBy != "tagger" {
			t.Errorf("turn %d tagged_by %q, want tagger", ct.N, ct.TaggedBy)
		}
	}
}

func TestHumanWhoLeavesForcesTheFinish(t *testing.T) {
	e, _, sk, _ := testEnv(t)
	e.Stdin = strings.NewReader("one reply\n.\nand then the input ends")
	o := opts(t, creator, "human")
	o.MaxTurns = 10
	oc := run(t, e, o)
	if oc.Status != StatusCompleted {
		t.Fatalf("%+v", oc)
	}
	if oc.Turns != 3 || len(ofType(sk.events(t), "usage")) != 3 {
		t.Errorf("factory turns %d, want 3", oc.Turns)
	}
}

func TestBuildCommandOutcomes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := write("ok.sh", `while [ $# -gt 0 ]; do case $1 in --out) out=$2; shift 2;; *) shift;; esac; done
mkdir -p "$out"; echo '{"hidden_pass":3,"hidden_total":4,"tasks":2,"merged":2,"escalations":1,"repairs":2,"cost_usd":1.5,"note":"n"}' >"$out/build.json"`)
	fail := write("fail.sh", `echo "chain produced no results" >&2; exit 1`)
	silent := write("silent.sh", `exit 0`)
	for name, tc := range map[string]struct {
		cmd     string
		wantNil bool
		cost    float64
	}{"ok": {ok, false, 1.5}, "fail": {fail, true, 0}, "silent": {silent, true, 0}} {
		e, _, sk, _ := testEnv(t)
		o := opts(t, "ceiling", "")
		o.BuildCmd = tc.cmd
		oc := run(t, e, o)
		s := score(t, sk)
		if (s.Build == nil) != tc.wantNil || s.BenchCostUSD.Build != tc.cost || oc.Status != StatusCompleted {
			t.Errorf("%s: build %+v cost %v status %s", name, s.Build, s.BenchCostUSD.Build, oc.Status)
		}
		switch name {
		case "ok":
			if b := s.Build; *b != (contract.ContextBuild{HiddenPass: 3, HiddenTotal: 4, Tasks: 2, Merged: 2, Escalations: 1, Repairs: 2}) {
				t.Errorf("%+v", b)
			}
		case "fail":
			if !strings.Contains(oc.Note, "build failed: ") || !strings.Contains(oc.Note, "chain produced no results") {
				t.Errorf("a failed build is not zero hidden tests; note %q", oc.Note)
			}
		case "silent":
			if !strings.Contains(oc.Note, "build failed") {
				t.Errorf("note %q", oc.Note)
			}
		}
	}
}

func TestBuildCommandIgnoredWithoutBuildSection(t *testing.T) {
	e, _, sk, out := testEnv(t)
	dir := t.TempDir()
	s := filepath.Join(dir, "s")
	copyTree(t, tinyDir, s)
	_ = os.WriteFile(filepath.Join(s, "scenario.json"), []byte(`{"id":"s","kind":"feature","title":"t","work_item":{"repo":"a/b","issue":3},"first_run":true,"max_turns":4}`), 0o644)
	o := opts(t, "ceiling", "")
	o.Scenario, o.BuildCmd = s, "/nonexistent"
	oc := run(t, e, o)
	if oc.Status != StatusCompleted || score(t, sk).Build != nil || !strings.Contains(out.String(), "no build section") {
		t.Fatalf("%+v %s", oc, out)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplaySummary(t *testing.T) {
	sim := []ReplayTurn{
		{Verdict: "same", DontKnowMatch: true, LengthRatio: 1},
		{Verdict: "partial", DontKnowMatch: true, LengthRatio: 3},
		{Verdict: "different", DontKnowMatch: false, LengthRatio: 2},
		{Verdict: "same", DontKnowMatch: true, LengthRatio: 0.5},
	}
	var c CalibrationResult
	summarize(&c, sim)
	if c.Same != 2 || c.Partial != 1 || c.Different != 1 || c.Agreement != (2+0.5)/4 || c.SameRate != 0.5 || c.DontKnowMatchRate != 0.75 {
		t.Errorf("%+v", c)
	}
	if c.LengthRatioMean != 1.625 || c.LengthRatioMedian != 1.5 {
		t.Errorf("length ratios %v %v", c.LengthRatioMean, c.LengthRatioMedian)
	}
	if LengthRatio("a b c d", "a b") != 2 || LengthRatio("a", "") != 0 {
		t.Error("length ratio")
	}
}

func TestReplayFeedsEveryRealQuestionAsAnAskTurnAndSendsNothing(t *testing.T) {
	e, rec, sk, _ := testEnv(t)
	e.CalibrationDir = fixtures + "/calibration"
	c, dir, err := e.Replay(context.Background(), ReplayOptions{Calibration: "tiny-q", History: "real", Person: DefaultPersonModel, Judge: DefaultJudgeModel, MaxCost: 2, Out: t.TempDir(), Yes: true})
	if err != nil || c.Status != "ok" || c.Questions != 3 {
		t.Fatalf("%v %+v", err, c)
	}
	if len(rec.role("person")) != 3 || len(rec.role("replay-judge")) != 1 {
		t.Fatalf("person %d judge %d", len(rec.role("person")), len(rec.role("replay-judge")))
	}
	for i, q := range rec.role("person") {
		if !strings.Contains(q.Prompt, "The assistant's last move: ask") {
			t.Errorf("question %d was not an ask turn", i+1)
		}
	}
	if !strings.Contains(rec.role("person")[2].Prompt, "Sales reps who use WhatsApp on their own laptops are the only users.") {
		t.Error("real history missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "calibration.json")); err != nil {
		t.Error(err)
	}
	if c.Same != 2 || c.Different != 1 {
		t.Errorf("%+v", c)
	}
	if len(sk.raw) != 0 {
		t.Error("a replay stays local: it sent events")
	}
}

func TestPlanCountsSessions(t *testing.T) {
	e, _, _, _ := testEnv(t)
	p, err := e.Prepare(opts(t, "plain", "unsure"))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range p.Sessions() {
		total += s.Max
	}
	if total != (6+5+1+3+1)*2 {
		t.Errorf("%d sessions", total)
	}
}

func TestARunSendsTheAttemptItsTurnsUsageGatesAndScore(t *testing.T) {
	e, rec, sk, _ := testEnv(t)
	o := opts(t, creator, "decided-technical")
	o.MaxTurns = 10
	oc := run(t, e, o)
	evs := sk.events(t)

	if evs[0].Type != "attempt.started" || evs[len(evs)-1].Type != "attempt.finished" {
		t.Fatalf("events run from %s to %s", evs[0].Type, evs[len(evs)-1].Type)
	}
	var st struct {
		WorkItem             contract.WorkItem `json:"work_item"`
		FactoryConfigVersion string            `json:"factory_config_version"`
		Labels               []string          `json:"labels"`
		Role                 string            `json:"role"`
	}
	_ = json.Unmarshal(evs[0].Payload, &st)
	if st.Role != "context" || st.WorkItem != (contract.WorkItem{Repo: "bench/tiny-context", Issue: 1}) ||
		strings.Join(st.Labels, " ") != "scenario:tiny persona:decided-technical max-turns:10" ||
		!strings.HasPrefix(st.FactoryConfigVersion, "file-fake-creator@") || !strings.HasSuffix(st.FactoryConfigVersion, "/claude-sonnet-5-5") {
		t.Errorf("attempt.started %+v", st)
	}
	for _, e := range evs {
		if e.AttemptID != oc.Attempt || !strings.HasPrefix(e.ID, oc.Attempt+":") {
			t.Errorf("event %s of attempt %s", e.ID, e.AttemptID)
		}
	}

	if n := len(ofType(evs, "usage")); n != len(rec.role("subject")) || n != 8 {
		t.Errorf("%d usage events for %d subject sessions", n, len(rec.role("subject")))
	}
	var u struct {
		Model           string  `json:"model"`
		Input, Output   int64   `json:"-"`
		ReportedCostUSD float64 `json:"reported_cost_usd"`
	}
	_ = json.Unmarshal(ofType(evs, "usage")[0].Payload, &u)
	if u.Model != DefaultSubjectModel || u.ReportedCostUSD != 0.01 {
		t.Errorf("usage %+v", u)
	}

	turns := ofType(evs, "context.turn")
	if len(turns) != 1+8+7 {
		t.Fatalf("%d turn events", len(turns))
	}
	for i, e := range turns {
		ct := contract.ParseContextTurn(e.Payload)
		wantSpeaker := "person"
		if i > 0 && i%2 == 1 {
			wantSpeaker = "factory"
		}
		if ct.N != int64(i+1) || ct.Speaker != wantSpeaker || ct.Text == "" || ct.Words == 0 {
			t.Errorf("turn %d: %+v", i+1, ct)
		}
		if ct.Speaker == "person" && ct.TaggedBy != "simulator" {
			t.Errorf("turn %d tagged_by %q", ct.N, ct.TaggedBy)
		}
		if ct.Speaker == "factory" && ct.Move == "" {
			t.Errorf("factory turn %d has no move", ct.N)
		}
	}

	gates := map[string]bool{}
	for _, g := range ofType(evs, "gate") {
		var gp struct {
			Name   string
			Passed bool
		}
		_ = json.Unmarshal(g.Payload, &gp)
		gates[gp.Name] = gp.Passed
	}
	if len(gates) != 4 || !gates["context-frame"] || !gates["context-change"] || !gates["context-testable"] || !gates["context-agreement"] {
		t.Errorf("gates %v", gates)
	}
	s := score(t, sk)
	if s.Scenario != "tiny" || s.Subject != "file-fake-creator" || s.Persona != "decided-technical" || s.Truth.Items != 8 || s.Truth.G4 != 3 || s.Truth.Latent != 2 || s.Truth.Traps != 3 || s.Truth.Noise != 2 {
		t.Errorf("score %+v", s)
	}
	if s.BenchCostUSD.Person != 0.07 || s.BenchCostUSD.Judge != 0.01 || s.BenchCostUSD.Builders != 0.04 {
		t.Errorf("bench cost %+v counts the persons, the judge and the builders and never the subject", s.BenchCostUSD)
	}
	var fin struct{ Status string }
	_ = json.Unmarshal(evs[len(evs)-1].Payload, &fin)
	if fin.Status != "completed" || oc.Status != StatusCompleted || oc.Score == nil {
		t.Errorf("finished %+v", fin)
	}

	b, err := os.ReadFile(filepath.Join(oc.Dir, "events.jsonl"))
	var sent []byte
	for _, raw := range sk.raw {
		sent = append(append(sent, raw...), '\n')
	}
	if err != nil || !bytes.Equal(b, sent) {
		t.Errorf("events.jsonl differs from what was sent (%v)", err)
	}
}

func TestSubjectsWithoutAConversationSendNoTurnsOrUsage(t *testing.T) {
	for _, subject := range []string{"none", "ceiling"} {
		e, _, sk, _ := testEnv(t)
		oc := run(t, e, opts(t, subject, ""))
		evs := sk.events(t)
		if len(ofType(evs, "context.turn")) != 0 || len(ofType(evs, "usage")) != 0 || oc.Status != StatusCompleted {
			t.Errorf("%s: %d turns %d usage events", subject, len(ofType(evs, "context.turn")), len(ofType(evs, "usage")))
		}
		var st struct {
			FactoryConfigVersion string   `json:"factory_config_version"`
			Labels               []string `json:"labels"`
		}
		_ = json.Unmarshal(evs[0].Payload, &st)
		if st.FactoryConfigVersion != subject || strings.Join(st.Labels, " ") != "scenario:tiny" {
			t.Errorf("%s: %+v", subject, st)
		}
		if s := score(t, sk); s.Persona != "" || s.Subject != subject {
			t.Errorf("%s: %+v", subject, s)
		}
	}
}

func TestNoTextLeavesTheTextOutOfTheTurns(t *testing.T) {
	e, _, sk, _ := testEnv(t)
	o := opts(t, "plain", "unsure")
	o.NoText = true
	run(t, e, o)
	turns := ofType(sk.events(t), "context.turn")
	if len(turns) == 0 {
		t.Fatal("no turns")
	}
	for _, e := range turns {
		ct := contract.ParseContextTurn(e.Payload)
		if ct.Text != "" || ct.Words == 0 {
			t.Errorf("turn %d: %+v", ct.N, ct)
		}
	}
}

func TestGateOneIsOnlyReportedOnAFirstRun(t *testing.T) {
	e, _, sk, _ := testEnv(t)
	s := filepath.Join(t.TempDir(), "s")
	copyTree(t, tinyDir, s)
	_ = os.WriteFile(filepath.Join(s, "scenario.json"), []byte(`{"id":"s","kind":"feature","title":"t","work_item":{"repo":"a/b","issue":3},"first_run":false,"max_turns":4}`), 0o644)
	o := opts(t, "ceiling", "")
	o.Scenario = s
	run(t, e, o)
	var names []string
	for _, g := range ofType(sk.events(t), "gate") {
		var gp struct{ Name string }
		_ = json.Unmarshal(g.Payload, &gp)
		names = append(names, gp.Name)
	}
	if strings.Join(names, " ") != "context-change context-testable context-agreement" {
		t.Errorf("gates %v", names)
	}
}

func TestConfigVersionChangesWithPromptAndModel(t *testing.T) {
	a := SubjectSpec{Kind: SubjectFile, Name: "file:/x/creator.md", Prompt: "one"}
	b := a
	b.Prompt = "two"
	va, vb := ConfigVersion(a, "m1"), ConfigVersion(b, "m1")
	if va == vb || va == ConfigVersion(a, "m2") || ConfigVersion(a, "m1") != va {
		t.Errorf("%s %s", va, vb)
	}
	if !strings.HasPrefix(va, "file-creator@") || !strings.HasSuffix(va, "/m1") || len(strings.Split(strings.Split(va, "@")[1], "/")[0]) != 12 {
		t.Errorf("%s", va)
	}
	if ConfigVersion(SubjectSpec{Kind: SubjectNone, Name: "none"}, "m1") != "none" {
		t.Error("none")
	}
}

func TestTurnEventsNumberTheConversationInOrder(t *testing.T) {
	t0 := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	turns := []protocol.Turn{
		{Turn: 0, Person: &protocol.PersonReply{Reply: "I want a thing", PersonTags: protocol.PersonTags{Revealed: []string{}, Suggestion: "none"}, At: t0}},
		{Turn: 1, Subject: &protocol.SubjectSaid{Move: "show", Message: "here is a mock up", At: at(1)},
			Person: &protocol.PersonReply{Reply: "I don't know, maybe", PersonTags: protocol.PersonTags{Revealed: []string{"T1"}, DontKnow: true, Suggestion: "accepted", Frustration: 2}, At: at(2)}},
		{Turn: 2, Subject: &protocol.SubjectSaid{Move: "mirror", Message: "done", Done: true, At: at(3)}},
	}
	evs := TurnEvents(turns, "simulator", false, t0)
	if len(evs) != 4 {
		t.Fatalf("%d events", len(evs))
	}
	want := []struct {
		n       int64
		speaker string
		at      time.Time
	}{{1, "person", t0}, {2, "factory", at(1)}, {3, "person", at(2)}, {4, "factory", at(3)}}
	for i, w := range want {
		if evs[i].Turn.N != w.n || evs[i].Turn.Speaker != w.speaker || !evs[i].At.Equal(w.at) {
			t.Errorf("event %d: %+v at %v", i, evs[i].Turn, evs[i].At)
		}
	}
	brief, ask, answer := evs[0].Turn, evs[1].Turn, evs[2].Turn
	if brief.Frustration != nil || brief.Suggestion != "" || brief.TaggedBy != "simulator" || brief.Words != 4 {
		t.Errorf("the brief carries no tags: %+v", brief)
	}
	if ask.Move != "show" || ask.Words != 5 || ask.Text != "here is a mock up" || ask.TaggedBy != "" {
		t.Errorf("%+v", ask)
	}
	if !answer.DontKnow || answer.Suggestion != "accepted" || answer.Frustration == nil || *answer.Frustration != 2 || len(answer.Revealed) != 1 {
		t.Errorf("%+v", answer)
	}
	if evs := TurnEvents(turns, "tagger", true, t0); evs[2].Turn.Text != "" || evs[2].Turn.TaggedBy != "tagger" {
		t.Errorf("--no-text: %+v", evs[2].Turn)
	}
	for _, e := range evs {
		body, _ := json.Marshal(e.Turn)
		raw, _ := json.Marshal(contract.Event{Spec: contract.Spec, ID: "a:turn", Type: "context.turn", TS: e.At.Format(time.RFC3339), AttemptID: "a", Payload: body})
		if _, _, err := contract.Validate(raw); err != nil {
			t.Errorf("%v: %s", err, raw)
		}
	}
}

func TestSameModel(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"claude-opus-5-5", "claude-opus-5-5", true},
		{"opus", "claude-opus-5-5", true},
		{"claude-sonnet-5-5", "sonnet", true},
		{"claude-opus-5-5", "claude-opus-4-7", false},
		{"opus", "claude-sonnet-5-5", false},
		{"opus", "sonnet", false},
	} {
		if got := sameModel(c.a, c.b); got != c.want {
			t.Errorf("sameModel(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
