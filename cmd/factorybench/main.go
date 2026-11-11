// Command factorybench is the FactoryBench binary; this file only dispatches subcommands.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"factorybench/internal/claudecode"
	"factorybench/internal/emit"
	"factorybench/internal/github"
	"factorybench/internal/intake"
	"factorybench/internal/normalize"
	"factorybench/internal/report"
	"factorybench/internal/store"
)

const usage = `usage:
  factorybench factory add <id> --commit-email <email> [--github-login <login>] [--db fb.db]
  factorybench serve [--db fb.db] [--addr :8080] [--poll 3m [--repo owner/name ...]]
  factorybench emit <type> --attempt <id> --key <k> --json '<payload>' [--ts <rfc3339>]
  factorybench emit --flush
  factorybench reconcile [--repo owner/name ...] [--db fb.db]
  factorybench replay [--db fb.db]
  factorybench report live [--db fb.db]
  factorybench report item <owner/repo#issue> [--db fb.db]
  factorybench report items [--factory f] [--outcome merged|rejected|open] [--db fb.db]
  factorybench report factories [--by config] [--db fb.db]
  factorybench claude-translate <stream.jsonl>   (test harness: Claude Code stream -> contract payloads)`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal(usage)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch {
	case cmd == "factory" && len(args) >= 2 && args[0] == "add":
		fs, db := dbFlags("factory add")
		email := fs.String("commit-email", "", "commit author email of the factory")
		login := fs.String("github-login", "", "GitHub login the factory acts as (closes issues, reviews, commits)")
		fs.Parse(args[2:])
		token, err := store.AddFactory(open(*db), args[1], *email, *login)
		check(err)
		fmt.Println(token)
		fmt.Fprintln(os.Stderr, "token shown once; only its hash is stored")
	case cmd == "serve":
		fs, dbp := dbFlags("serve")
		addr := fs.String("addr", ":8080", "listen address")
		poll := fs.Duration("poll", 0, "reconcile from GitHub at this interval (0 = never): every repo a factory reported work on, plus --repo")
		var repos repoList
		fs.Var(&repos, "repo", "extra owner/name to poll (repeatable)")
		fs.Parse(args)
		db := open(*dbp)
		rebuild := rebuilder(db)
		if *poll > 0 {
			go pollGitHub(db, repos, *poll, githubToken(), rebuild)
		}
		http.Handle("/v1/events", intake.Handler(db, rebuild))
		log.Printf("listening on %s", *addr)
		check((&http.Server{Addr: *addr, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
	case cmd == "emit":
		emit.Run(args, usage)
	case cmd == "reconcile":
		fs, dbp := dbFlags("reconcile")
		var extra repoList
		fs.Var(&extra, "repo", "owner/name to reconcile (repeatable); default every repo a factory reported work on")
		fs.Parse(args)
		db := open(*dbp)
		repos := []string(extra)
		if len(repos) == 0 {
			var err error
			repos, err = github.Repos(db, nil)
			check(err)
		}
		token := githubToken()
		for _, r := range repos {
			st, err := github.Reconcile(db, r, token)
			if errors.Is(err, github.ErrNotFound) {
				fmt.Printf("reconcile %s: not found or no access with this GitHub token, skipped\n", r)
				continue
			}
			check(err)
			fmt.Printf("reconcile %s: %d new events, %d duplicates\n", r, st.Added, st.Duplicates)
		}
		check(normalize.Run(db))
	case cmd == "replay":
		fs, dbp := dbFlags("replay")
		fs.Parse(args)
		check(normalize.Run(open(*dbp)))
		fmt.Println("derived tables rebuilt from events")
	case cmd == "report" && len(args) >= 1 && args[0] == "live":
		fs, dbp := dbFlags("report live")
		fs.Parse(args[1:])
		check(report.Live(open(*dbp), os.Stdout, time.Now()))
	case cmd == "report" && len(args) >= 1 && args[0] == "items":
		fs, dbp := dbFlags("report items")
		factory := fs.String("factory", "", "only this factory")
		outcome := fs.String("outcome", "", "only this outcome (merged, rejected, open)")
		fs.Parse(args[1:])
		check(report.Items(open(*dbp), os.Stdout, *factory, *outcome, time.Now()))
	case cmd == "report" && len(args) >= 1 && args[0] == "factories":
		fs, dbp := dbFlags("report factories")
		by := fs.String("by", "", `"config" for one column per factory + config version`)
		fs.Parse(args[1:])
		if *by != "" && *by != "config" {
			log.Fatal("--by only supports config")
		}
		check(report.Factories(open(*dbp), os.Stdout, *by == "config", time.Now()))
	case cmd == "report" && len(args) >= 2 && args[0] == "item":
		fs, dbp := dbFlags("report item")
		fs.Parse(args[2:])
		m := regexp.MustCompile(`^([\w.-]+/[\w.-]+)#(\d+)$`).FindStringSubmatch(args[1])
		if m == nil {
			log.Fatal("work item must look like owner/repo#123")
		}
		n, err := strconv.ParseInt(m[2], 10, 64)
		check(err)
		check(report.Item(open(*dbp), os.Stdout, m[1], n, time.Now()))
	case cmd == "claude-translate" && len(args) == 1:
		f, err := os.Open(args[0])
		check(err)
		res, err := claudecode.Translate(f)
		check(err)
		check(json.NewEncoder(os.Stdout).Encode(res))
	default:
		log.Fatal(usage)
	}
}

// rebuilder runs normalize.Run in one background goroutine and returns a non-blocking trigger for it.
// Triggers that arrive during a rebuild collapse into one more rebuild, so intake never waits for normalize
// and a burst of batches costs at most two rebuilds.
// shortcut: each rebuild is a full one; switch to incremental normalize when events grow large.
func rebuilder(db *sql.DB) func() {
	dirty := make(chan struct{}, 1)
	go func() {
		for range dirty {
			if err := normalize.Run(db); err != nil {
				log.Print("normalize: ", err)
			}
		}
	}()
	return func() {
		select {
		case dirty <- struct{}{}:
		default: // a rebuild is already queued and will see this batch
		}
	}
}

// pollGitHub reconciles every known repo (see github.Repos) every interval, forever, and triggers a rebuild
// when something new arrived. A repo the token can't see is logged once, then skipped quietly.
func pollGitHub(db *sql.DB, extra []string, every time.Duration, token string, rebuild func()) {
	missing := map[string]bool{}
	for {
		repos, err := github.Repos(db, extra)
		if err != nil {
			log.Print("poll: ", err)
		}
		added := 0
		for _, r := range repos {
			st, err := github.Reconcile(db, r, token)
			switch {
			case errors.Is(err, github.ErrNotFound):
				if !missing[r] {
					log.Printf("poll %s: not found or no access with this GitHub token; skipping it until it appears", r)
					missing[r] = true
				}
			case err != nil:
				log.Printf("poll %s: %v", r, err)
			default:
				delete(missing, r)
				log.Printf("poll %s: %d new events, %d duplicates", r, st.Added, st.Duplicates)
			}
			added += st.Added
		}
		if added > 0 {
			rebuild()
		}
		time.Sleep(every)
	}
}

// repoList is a repeatable --repo flag.
type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(v string) error { *r = append(*r, v); return nil }

func dbFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs, fs.String("db", "fb.db", "SQLite database path")
}

func open(path string) *sql.DB {
	db, err := store.Open(path)
	check(err)
	return db
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func githubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		log.Print("no GITHUB_TOKEN and `gh auth token` failed; calling the API unauthenticated")
	}
	return strings.TrimSpace(string(out))
}
