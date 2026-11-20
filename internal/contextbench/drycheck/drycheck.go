// Package drycheck is `factorybench context dry-run`: the whole bench end to end with the fake claude against an
// in-process FactoryBench (a temporary SQLite database behind the real intake handler on a loopback server), then
// the invariants the bench depends on. It calls no model and uses no network beyond loopback.
package drycheck

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"factorybench/internal/contextbench/change"
	"factorybench/internal/contextbench/fakeclaude"
	"factorybench/internal/contextbench/fixtures"
	"factorybench/internal/intake"
	"factorybench/internal/store"
)

// factoryID is the factory the dry run registers and sends as.
const factoryID = "dry-run"

// Config says how to run the dry run.
type Config struct {
	// Exe is the factorybench binary: the fake claude is a script that runs `<Exe> context fake-claude`.
	Exe string
	// Fake is a ready fake claude program, used instead of that script (the tests start their own test binary).
	Fake   string
	Stdout io.Writer
	Keep   bool // keep the temporary directory and print its path
	// Run is the `factorybench context` command line (contextbench.Run); it is passed in because that package
	// imports this one.
	Run func(args []string, stdin io.Reader, stdout, stderr io.Writer) int
}

type dry struct {
	cfg      Config
	tmp      string
	fx       string // the extracted fixtures
	scen     string // fx/scenarios
	runs     string // where the runs' local evidence goes
	logs     string // the fake claude's call logs
	spool    string
	db       *sql.DB
	srv      *httptest.Server
	token    string
	failures []string
	checks   int
	seq      int

	mu        sync.Mutex
	posted    int // events POSTed to the server
	rejected  []string
	badStatus []string
}

func (d *dry) check(ok bool, format string, a ...any) {
	d.checks++
	if !ok {
		d.failures = append(d.failures, fmt.Sprintf(format, a...))
	}
}

func (d *dry) say(format string, a ...any) { fmt.Fprintf(d.cfg.Stdout, format+"\n", a...) }

// Run does the dry run. It returns an error naming every invariant that failed.
func Run(cfg Config) error {
	tmp, err := os.MkdirTemp("", "factorybench-context-dry-")
	if err != nil {
		return err
	}
	if cfg.Keep {
		fmt.Fprintln(cfg.Stdout, "dry-run files kept in", tmp)
	} else {
		defer os.RemoveAll(tmp)
	}
	d := &dry{cfg: cfg, tmp: tmp, fx: filepath.Join(tmp, "fixtures"), runs: filepath.Join(tmp, "runs"), logs: filepath.Join(tmp, "logs"),
		spool: filepath.Join(tmp, "spool.jsonl")}
	d.scen = filepath.Join(d.fx, "scenarios")
	for _, dir := range []string{d.logs, d.runs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := fixtures.Extract(d.fx); err != nil {
		return err
	}
	fake := cfg.Fake
	if fake == "" {
		fake = filepath.Join(tmp, fakeclaude.Name)
		script := "#!/bin/sh\nexec '" + strings.ReplaceAll(cfg.Exe, "'", `'\''`) + "' context fake-claude \"$@\"\n"
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			return err
		}
	}
	if err := d.startFactoryBench(); err != nil {
		return err
	}
	defer d.srv.Close()
	defer d.db.Close()

	restore := setenv(map[string]string{
		"FACTORYBENCH_CLAUDE": fake, "FACTORYBENCH_URL": d.srv.URL, "FACTORYBENCH_TOKEN": d.token, "FACTORYBENCH_SPOOL": d.spool,
		"FACTORYBENCH_FAKE_LOG": "", "FACTORYBENCH_FAKE_COST": "", "FACTORYBENCH_FAKE_FAIL": "", "FACTORYBENCH_FAKE_SUBJECT_TURNS": "",
		"FACTORYBENCH_FAKE_VIOLATE": "", "FACTORYBENCH_FAKE_UNKNOWN_ID": "", "FACTORYBENCH_FAKE_INVENT": "", "FACTORYBENCH_FAKE_CONTRADICT": "",
		"FACTORYBENCH_FAKE_DISAGREE": "", "FACTORYBENCH_FAKE_BUILD_FAIL": "", "FACTORYBENCH_FAKE_BUILD_LOG": filepath.Join(tmp, "build.log"),
	})
	defer restore()

	d.gates()
	d.lint()
	d.cost()
	got := d.scenarioRuns()
	d.matrix()
	d.offline(got)
	d.replayAndRetag(got)
	d.database(got)

	d.mu.Lock()
	d.check(len(d.rejected) == 0 && len(d.badStatus) == 0, "FactoryBench rejected events or answered badly: %v %v", d.rejected, d.badStatus)
	d.mu.Unlock()
	if len(d.failures) > 0 {
		for _, f := range d.failures {
			fmt.Fprintln(cfg.Stdout, "FAIL:", f)
		}
		return fmt.Errorf("%d of %d checks failed", len(d.failures), d.checks)
	}
	d.say("dry-run ok: %d checks passed", d.checks)
	return nil
}

// startFactoryBench opens a temporary database with one factory behind the real intake handler.
func (d *dry) startFactoryBench() error {
	db, err := store.Open(filepath.Join(d.tmp, "fb.db"))
	if err != nil {
		return err
	}
	if d.token, err = store.AddFactory(db, factoryID, "dry-run@example.com", ""); err != nil {
		return err
	}
	d.db = db
	handler := intake.Handler(db, nil)
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, status: 200}
		handler.ServeHTTP(rec, r)
		d.note(r, rec)
	}))
	return nil
}

type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// note keeps what FactoryBench answered, so a rejected event fails the dry run.
func (d *dry) note(r *http.Request, rec *recorder) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if rec.status != 200 {
		d.badStatus = append(d.badStatus, fmt.Sprintf("%s: %d %s", r.URL.Path, rec.status, strings.TrimSpace(rec.body.String())))
		return
	}
	var resp intake.Response
	if err := json.Unmarshal(rec.body.Bytes(), &resp); err != nil {
		d.badStatus = append(d.badStatus, "unreadable answer: "+rec.body.String())
		return
	}
	d.posted += resp.Accepted + resp.Duplicates + len(resp.Rejected)
	for _, rj := range resp.Rejected {
		d.rejected = append(d.rejected, rj.ID+": "+rj.Error)
	}
}

func (d *dry) postedEvents() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.posted
}

func setenv(kv map[string]string) (restore func()) {
	old := map[string]*string{}
	for k, v := range kv {
		if cur, ok := os.LookupEnv(k); ok {
			c := cur
			old[k] = &c
		} else {
			old[k] = nil
		}
		os.Setenv(k, v)
	}
	return func() {
		for k, v := range old {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	}
}

// cli runs one `factorybench context` command in process with the given environment overrides and stdin.
func (d *dry) cli(env map[string]string, stdin string, args ...string) (code int, stdout, stderr string) {
	restore := setenv(env)
	defer restore()
	var out, errb bytes.Buffer
	code = d.cfg.Run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

// ---- gates on known-good and known-bad change folders ----

func (d *dry) gates() {
	vs, err := change.LoadVariants(filepath.Join(d.fx, "changes"))
	if err != nil {
		d.check(false, "load change-folder fixtures: %v", err)
		return
	}
	for _, v := range vs {
		g := change.Check(v.Folder, true)
		got := change.Expect{Gate1: g.Gate1, Gate2: g.Gate2, Gate3: g.Gate3, VagueWarnings: len(g.VagueWarnings)}
		d.check(got == v.Expect, "gates of fixture %q: got %+v want %+v (failures: %v)", v.Name, got, v.Expect, g.Failures)
	}
	d.say("ok  gates 1-3 on %d known-good and known-bad change folders", len(vs))
}

// ---- lint ----

func (d *dry) lint() {
	code, out, errs := d.cli(nil, "", "lint", "--scenarios", d.scen)
	d.check(code == 0, "lint of the test scenario failed: %s%s", out, errs)

	bad := filepath.Join(d.tmp, "bad", "scenarios")
	one := filepath.Join(bad, "bad-one")
	_ = os.MkdirAll(one, 0o755)
	_ = os.WriteFile(filepath.Join(one, "scenario.json"), []byte(`{"id":"bad-one","kind":"feature","title":"t","first_run":true,"max_turns":5,"build":{"chain":"x"}}`), 0o644)
	_ = os.WriteFile(filepath.Join(one, "brief.md"), []byte("brief\n"), 0o644)
	_ = os.WriteFile(filepath.Join(one, "truth.json"), []byte(`{"items":[
	 {"id":"A1","area":"rules","text":"x","g4":false,"latent":true,"technical":false,"source":"s"},
	 {"id":"A1","area":"nowhere","text":"y","g4":false,"latent":false,"technical":false,"source":"s"}],
	 "traps":[{"id":"X1","kind":"vague","items":["ZZ"],"text":"t","resolution":"r"}],"noise":[]}`), 0o644)
	// a second scenario with a bad work item, and a third that shares the tiny scenario's
	for id, wi := range map[string]string{"bad-two": `{"repo":"not a repo","issue":0}`, "bad-three": `{"repo":"bench/tiny-context","issue":1}`} {
		dir := filepath.Join(bad, id)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(`{"id":"`+id+`","kind":"feature","title":"t","work_item":`+wi+`,"first_run":false,"max_turns":5}`), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "brief.md"), []byte("brief\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "truth.json"), []byte(`{"items":[{"id":"A1","area":"rules","text":"x","g4":false,"latent":false,"technical":false,"source":"s"}],"traps":[],"noise":[]}`), 0o644)
	}
	// bad-three shares bad-one's and tiny's work item only when both are in the same directory: copy tiny in
	b, _ := os.ReadFile(filepath.Join(d.scen, "tiny", "scenario.json"))
	tiny := filepath.Join(bad, "tiny")
	_ = os.MkdirAll(tiny, 0o755)
	_ = os.WriteFile(filepath.Join(tiny, "scenario.json"), b, 0o644)
	for _, f := range []string{"brief.md", "truth.json"} {
		b, _ := os.ReadFile(filepath.Join(d.scen, "tiny", f))
		_ = os.WriteFile(filepath.Join(tiny, f), b, 0o644)
	}
	code, out, _ = d.cli(nil, "", "lint", "--scenarios", bad)
	d.check(code != 0, "lint accepted broken scenarios")
	for _, want := range []string{`scenario.json has no "work_item"`, "latent but has no latent_hint", "ids are unique", `area "nowhere"`, `references "ZZ"`,
		"has build but no interface.md", `work_item.repo "not a repo" must be owner/name`, "is also scenario"} {
		d.check(strings.Contains(out, want), "lint of the broken scenarios did not say %q; it said:\n%s", want, out)
	}
	d.say("ok  lint passes the test scenario and fails broken ones with precise messages")
}

// ---- cost guard ----

func (d *dry) cost() {
	log := filepath.Join(d.logs, "noyes.jsonl")
	env := map[string]string{"FACTORYBENCH_FAKE_LOG": log}
	before := d.postedEvents()
	base := []string{"run", "--scenarios", d.scen, "--out", d.runs, "--scenario", "tiny", "--subject", "plain", "--person", "unsure"}
	code, out, _ := d.cli(env, "", base...)
	d.check(code == 0, "run without --yes exited %d", code)
	d.check(strings.Contains(out, "plan") && strings.Contains(out, "add --yes") && strings.Contains(out, "person") && strings.Contains(out, "judge"), "run without --yes did not print the plan: %s", out)
	_, err := os.Stat(log)
	d.check(os.IsNotExist(err), "run without --yes called a model")
	code, out, _ = d.cli(env, "", "matrix", "--scenarios", d.scen, "--scenarios-list", "tiny", "--subjects", "none,plain", "--personas", "all", "--out", d.runs)
	d.check(code == 0 && strings.Contains(out, "plan") && strings.Contains(out, "5 runs"), "matrix without --yes: exit %d, %q", code, out)
	_, err = os.Stat(log)
	d.check(os.IsNotExist(err), "matrix without --yes called a model")

	code, _, errs := d.cli(env, "", append(base, "--yes", "--subject-model", "same-model", "--person-model", "same-model")...)
	d.check(code != 0 && strings.Contains(errs, "different models"), "the same model for person and subject was not refused: exit %d, %s", code, errs)
	_, err = os.Stat(log)
	d.check(os.IsNotExist(err), "a refused run called a model")
	ents, _ := os.ReadDir(d.runs)
	d.check(len(ents) == 0 && d.postedEvents() == before, "a run without --yes or a refused one wrote evidence (%d entries) or sent events (%d)", len(ents), d.postedEvents()-before)
	d.say("ok  without --yes nothing is called, written or sent; the same model for person and subject is refused")
}
