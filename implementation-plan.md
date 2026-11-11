# FactoryBench — Implementation plan

Companion to [mvp-overview.md](mvp-overview.md), which owns the design (concepts, architecture, metrics, reports, acceptance criteria). This file covers how to build it.

## 1. Language and stack

Go. The MVP is one service, SQLite, HTTP intake, a GitHub polling reconciler, and CLI reports. Go covers this with the standard library plus one SQLite driver and ships a single static binary.

- One module, one binary `factorybench`, subcommands `serve`, `emit`, `report` (`live`, `item`, `items`, `factories`), `replay`, `reconcile` (stdlib `flag`, `net/http`).
- SQLite via `modernc.org/sqlite` (pure Go, no cgo). Plain SQL through `database/sql`, embedded `.sql` migrations, no ORM. If many factories write at once, Postgres replaces SQLite with the same schema.
- Contract validation in Go (`internal/contract`). `spec/events.schema.json` is the published contract; a test checks every file in `spec/examples/` passes the Go validator and that both list the same event types. No JSON-schema library.
- GitHub: the REST API through `net/http` (no go-github). The MVP polls the API (reconciler) instead of receiving webhooks, so nothing is exposed publicly. Webhooks with HMAC verification are optional later.
- Reports: `text/template` to terminal or Markdown. No web UI in the MVP.

## 2. Layout

```text
cmd/factorybench/main.go     # subcommand dispatch only
internal/store/              # sqlite, embedded migration, idempotent event insert, factories
internal/contract/           # event types + Go validator (shared by intake and normalize)
internal/intake/             # HTTP: /v1/events (the contract)
internal/emit/               # `factorybench emit`: send one event, spool on failure, flush the spool
internal/normalize/          # pure funcs: raw events -> work item / attempt / usage / gate / PR / check / review / commit rows
internal/github/             # polling reconciler (stores raw github events)
internal/pricing/            # prices/<YYYY-MM-DD>.json (embedded, never edited) + cost estimation
internal/report/             # live, work-item timeline, work-item list, factory scorecard (all metrics are computed here)
internal/claudecode/         # test-harness helper: Claude Code stream-json -> contract payloads
spec/                        # events.schema.json + examples/: the one factory contract
scripts/                     # test harnesses: fake-factory.sh, claude-code-run.sh
testdata/                    # golden cases, captured real factory output
docs/diagrams/               # generate.py + architecture, issue-flow, db-schema, code-map, event-path, attempt-states SVGs
```

**Rule that keeps it maintainable:** intake only writes raw events (payload plus idempotency key). `normalize` is a pure function `(raw events) -> derived rows`. `factorybench replay` truncates the derived tables and re-runs normalize, which satisfies the "replay gives the same result" acceptance criterion. Test with golden files in `testdata/`.

### Reports

Reports only read derived tables (plus human events). `report items [--factory f] [--outcome o]` lists one row per (work item, factory); `report factories [--by config]` is the scorecard, one column per factory (or factory + config version). A unit is one factory on one work item; its linked PRs are the `prs` rows whose URL one of its attempts linked. A review or commit is human when its login/author is not a registered factory. Unknown stays unknown: unpriced cost shows `+?`/`?`, never 0; every rate shows its counts, empty denominators show `n/a`.

| Metric | Definition |
|---|---|
| tokens / issue | input + output + cache read + cache write over all usage ÷ work items; `+?` when an attempt has no usage |
| time / issue | per unit, elapsed = first attempt start → last attempt end (now if open), active = elapsed − human wait; averaged over work items |
| cost total / per issue / per merged issue | sum of priced sessions (provenance shown, `+?` if any unpriced) ÷ work items, ÷ units whose own PR merged |
| outcome (items report) | from the unit's own PRs: merged if any merged, rejected if all closed, else open |
| verification tokens / PR | tokens of usage with phase `verify` or `review` in units with a linked PR ÷ those PRs; `n/a` without phase data |
| issues closed by agent | work items with issue state closed and `closed_by` = the factory's GitHub login ÷ work items |
| first-pass plan approval | units whose plan waited for a human and was resumed: first plan resumed with no `human.steering` before the resume and no second plan ÷ those units (pending reviews are excluded) |
| first-pass PR approval | linked PRs whose first human APPROVED/CHANGES_REQUESTED review is APPROVED ÷ PRs with such a review |
| review rounds / PR | human CHANGES_REQUESTED reviews ÷ linked PRs with any human review |
| human interventions / issue | `human.waiting` + `human.steering` + human commits on linked PRs + human CHANGES_REQUESTED reviews, ÷ work items |
| escaped defects | human CHANGES_REQUESTED reviews and human commits dated after the unit's last attempt ended, shown as `k on m/n PRs` |
| telemetry coverage | attempts with ≥ 1 usage ÷ attempts; unpriced sessions; rejected events of the factory |
| gates | per gate name: failed ÷ runs from `gates`; per CI check on linked PRs: failure or timed_out ÷ executed runs (conclusion success, failure or timed_out; skipped, neutral, cancelled etc. are not runs) |
| CI suite | per check, executed runs only (as in gates): median completed − started (negative durations dropped); flake rate = head SHAs with both a failure and a success ÷ head SHAs with an executed run |

Not collected yet: spec items covered by E2E tests (needs a product-repo CI convention that reports them).

## 3. Database schema

![Database schema](docs/diagrams/db-schema.svg)

One schema for all factories. No table per factory. The migration is `internal/store/schema.sql`, applied once when `PRAGMA user_version` is 0. There is no production data yet, so schema changes edit that file in place (delete the DB and re-create it); a second migration step comes with the first real data.

```sql
-- registered factories; the API token identifies the factory
CREATE TABLE factories (
  factory_id   TEXT PRIMARY KEY,
  token_hash   TEXT NOT NULL,
  commit_email TEXT,                       -- commit author identity, for factory vs human commits
  github_login TEXT,                       -- GitHub account the factory acts as (closes issues, reviews, commits)
  created_at   TEXT NOT NULL
);

-- source of truth: everything that arrived, untouched, append-only
CREATE TABLE events (
  source       TEXT NOT NULL CHECK (source IN ('contract', 'github')),
  factory_id   TEXT NOT NULL DEFAULT '',   -- '' for GitHub events
  id           TEXT NOT NULL,              -- idempotency key (contract id or GitHub delivery id)
  type         TEXT NOT NULL,
  ts           TEXT NOT NULL,              -- when it happened
  received_at  TEXT NOT NULL,              -- when FactoryBench got it
  payload      TEXT NOT NULL,              -- raw JSON as received
  rejected     TEXT,                       -- validation error, if any
  PRIMARY KEY (source, factory_id, id)
);

-- everything below is derived and rebuilt by `factorybench replay`
CREATE TABLE work_items (
  repo         TEXT NOT NULL,
  issue        INTEGER NOT NULL,
  task_type    TEXT,                       -- from the issue's labels (else the factory's), e.g. bug, perf, chore
  title        TEXT,                       -- from the GitHub issue, NULL until reconciled
  issue_state  TEXT,                       -- open | closed
  closed_at    TEXT,
  closed_by    TEXT,                       -- GitHub login; a factory's github_login = closed by the agent
  first_seen   TEXT NOT NULL,              -- cohort date
  outcome      TEXT,                       -- open | merged | rejected | reworked | reverted
  survival     TEXT,                       -- pending | survived | not_survived | NULL
  corrects     TEXT,                       -- 'repo#issue' this item corrects, if any
  PRIMARY KEY (repo, issue)
);

CREATE TABLE attempts (
  factory_id        TEXT NOT NULL,
  attempt_id        TEXT NOT NULL,
  repo              TEXT NOT NULL,
  issue             INTEGER NOT NULL,
  parent_attempt_id TEXT,
  role              TEXT,                  -- plan | build | review | ...
  config_version    TEXT NOT NULL,
  state             TEXT NOT NULL CHECK (state IN
                      ('running', 'waiting_for_human', 'completed', 'failed', 'cancelled')),
  started_at        TEXT NOT NULL,
  ended_at          TEXT,
  human_wait_ms     INTEGER NOT NULL DEFAULT 0,
  pr_url            TEXT,
  PRIMARY KEY (factory_id, attempt_id)
);

CREATE TABLE usage (                        -- one row per usage event
  factory_id        TEXT NOT NULL,
  attempt_id        TEXT NOT NULL,
  model             TEXT NOT NULL,
  ts                TEXT NOT NULL,
  input             INTEGER NOT NULL,
  output            INTEGER NOT NULL,
  cache_read        INTEGER,                -- NULL = not reported, never 0 by default
  cache_write       INTEGER,
  reasoning         INTEGER,
  duration_ms       INTEGER,
  cost_usd          REAL,                   -- NULL when it can't be priced
  cost_provenance   TEXT CHECK (cost_provenance IN ('reported', 'estimated')),
  price_version     TEXT,
  phase             TEXT                    -- implement | verify | review, NULL if not sent
);

CREATE TABLE gates (                        -- factory-internal gate results (gate events)
  factory_id   TEXT NOT NULL,
  attempt_id   TEXT NOT NULL,
  ts           TEXT NOT NULL,
  name         TEXT NOT NULL,               -- e.g. build, guard, test
  passed       INTEGER NOT NULL,            -- 1 | 0
  reason       TEXT
);

CREATE TABLE prs (
  repo             TEXT NOT NULL,
  number           INTEGER NOT NULL,
  url              TEXT NOT NULL UNIQUE,
  branch           TEXT,
  state            TEXT,                    -- open | closed | merged
  merged_at        TEXT,
  factory_commits  INTEGER NOT NULL DEFAULT 0,
  human_commits    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (repo, number)
);

CREATE TABLE checks (
  repo          TEXT NOT NULL,
  pr_number     INTEGER NOT NULL,
  name          TEXT NOT NULL,              -- e.g. build, test, lint
  head_sha      TEXT NOT NULL,
  conclusion    TEXT,                       -- success | failure | ...
  started_at    TEXT,
  completed_at  TEXT
);

CREATE TABLE reviews (                      -- from the latest pr.reviews event of each PR
  repo          TEXT NOT NULL,
  pr_number     INTEGER NOT NULL,
  reviewer      TEXT NOT NULL,              -- GitHub login
  state         TEXT NOT NULL,              -- APPROVED | CHANGES_REQUESTED | COMMENTED | ...
  submitted_at  TEXT
);

CREATE TABLE pr_commits (                   -- from the latest pr.commits event of each PR
  repo          TEXT NOT NULL,
  pr_number     INTEGER NOT NULL,
  sha           TEXT NOT NULL,
  author_email  TEXT,
  factory_id    TEXT,                       -- NULL = human commit
  committed_at  TEXT                        -- commit author date
);
```

FactoryBench's own prices are not a table: they live in `internal/pricing/prices/<YYYY-MM-DD>.json` (per model: `input`, `output`, `cache_read`, `cache_write` per million tokens), embedded in the binary. A new version is a new file named by its effective date, never an edit; file names must sort chronologically (so no `v2`/`v10`), a session is estimated with the newest table dated on or before its `ts` (the oldest table for sessions before it), so adding a table never changes the cost of past sessions; its name without `.json` (e.g. `2026-11-11`) is recorded in `usage.price_version`. A session whose model, or a token class it used, has no price stays `cost_usd = NULL`.

Rules:
- Intake writes only `events`. Everything else is derived. An event is never overwritten once accepted; the one exception to append-only is a re-send of a previously **rejected** id, which replaces the rejected row (and counts as accepted if it now validates). `events.payload` holds the whole raw event as received (contract) or the reconciler's wrapper around the raw API object (github).
- GitHub events (written by `reconcile`, or by `serve --poll`): `pr` (`{"repo", "pr": <raw PR>}`, id `pr:<n>:<updated_at>`), `pr.commits` (`{"repo", "number", "head_sha", "commits": [<raw>]}`, id `commits:<n>:<head_sha>`), `pr.reviews` (`{"repo", "number", "reviews": [<raw>]}`, id `reviews:<n>:<count>:<last submitted_at>`), `check_run` (`{"repo", "pr_number", "check_run": <raw>}`, id `check:<id>:<status>:<completed_at>`; fetched with `filter=all`, so a failed run that was re-run is kept next to the rerun), `issue` (`{"repo", "issue": <raw issue>}`, id `issue:<n>:<updated_at>`; every issue of the reconciled repo that a factory sent an `attempt.started` for). A `pr` event is stored for each of the 100 most recently updated PRs (one list request), but commits, reviews, and check runs are fetched only for factory PRs: the PR's `html_url` is the `pr_url` of an accepted `pr.linked` event, or its author is a factory's `github_login`. Re-polling unchanged objects yields only duplicates. A closed PR whose `pr` event is a duplicate (unchanged `updated_at`) and that already has a `pr.commits` event is not re-fetched for commits, reviews, and check runs, to stay under GitHub's 5,000 requests/hour. Issues are re-fetched on every run (one request each).
- `pr_commits` and `reviews` come from the latest `pr.commits` / `pr.reviews` event of each PR. A commit is the factory's when its author email is a factory's `commit_email`, or its GitHub author login is a factory's `github_login`; otherwise `factory_id` is NULL (human). `prs.factory_commits`/`human_commits` count the same rows.
- `work_items.title`, `issue_state`, `closed_at`, `closed_by` come from the latest `issue` event. `task_type` is the first issue label without `:`; without an `issue` event, the first such label the factory sent in `attempt.started`. An issue whose `closed_by` is a factory's `github_login` was closed by the agent.
- Escaped defects at acceptance (human PR review) are read from `reviews` (`CHANGES_REQUESTED`) and `pr_commits` (`factory_id` NULL, `committed_at` after the linking attempt's `ended_at`).
- A work item's PR is the attempt's `pr.linked` URL matched to `prs.url`. Outcome: `merged` if any linked PR merged (survival `pending`), `rejected` if all are closed unmerged, else `open`.
- Human wait: a `human.waiting` is closed by the next `human.resumed` on the same attempt, or on its child (plan waits, build resumes); the wait is added to the resuming attempt's `human_wait_ms`. Active time = elapsed − human waits. An attempt that finishes while its own wait is open (a plan run ends, then waits for plan review) gets `ended_at` but stays `waiting_for_human`; when the wait is closed, it takes the status it finished with.
- `factory_id` comes from the API token, never from the request body, so one factory cannot write into another's data.
- Usage and cost columns are nullable, never defaulted to 0. Unknown stays unknown and lowers coverage.
- Factory-specific extras stay in `events.payload`. A field becomes a column only when it joins the contract for everyone.
- Factory vs. human commits are told apart by commit author. Each factory registers its commit identity and, optionally, the GitHub login it acts as: `factorybench factory add <id> --commit-email <email> --github-login <login>` (e.g. `factory-bot@users.noreply.github.com`).
- Polling: `factorybench serve --poll 3m --repo a/b --repo c/d` reconciles each repo in turn at startup and then every interval, and re-normalizes once if anything new was stored. Errors are logged, never fatal. The token comes from `GITHUB_TOKEN` or `gh auth token`, as for `reconcile`. Without `--poll` nothing is polled.
- Rebuilds: intake and polling only mark the derived tables stale; one background goroutine runs `normalize.Run` (one transaction on the database's single connection), and triggers during a rebuild collapse into one more. A POST therefore never waits for normalization, and reports can lag a batch by one rebuild.

## 4. The event contract

FactoryBench defines **one event contract**, and every factory reports through it. The factory **reports raw facts** when they happen. **FactoryBench processes them**: it prices tokens, computes durations, links PRs, aggregates retries, and builds every metric. A factory never computes metrics, cost estimates, or outcomes for FactoryBench, and FactoryBench never reads a factory's own logs, ledgers, or transcripts.

GitHub data (PRs, CI, merges, commit authors) is the same for every factory, so FactoryBench collects it itself and it is not part of the contract.

### Transport

- `POST /v1/events` with a JSON array of events (one or a batch) and a bearer token.
- `factorybench emit <type> --attempt <id> --key <k> --json '<payload>' [--ts <rfc3339>]` does the same for shell-based factories and hook scripts (id = `<attempt>:<key>`; URL/token from `FACTORYBENCH_URL`/`FACTORYBENCH_TOKEN`). On failure it appends to a spool file (`FACTORYBENCH_SPOOL`, default `~/.factorybench/spool.jsonl`) and exits 0; `factorybench emit --flush` resends it in batches of 500, skipping (and logging) lines that aren't valid JSON. The spool is first renamed to `.sending`; that file is removed only once its events are delivered or re-spooled, and a leftover `.sending` from a crashed flush is sent first.
- Delivery is at-least-once: the factory retries on failure, and the `id` field deduplicates.
- Events may arrive late or out of order. Normalization orders them by `ts`.

### Example request

```bash
curl -X POST https://factorybench.internal/v1/events \
  -H "Authorization: Bearer $FACTORYBENCH_TOKEN" \
  -H "Content-Type: application/json" \
  -d @events.json
```

```json
[
  {
    "spec": "factorybench/v1",
    "id": "run-8f2c:started",
    "type": "attempt.started",
    "ts": "2026-11-11T09:14:00Z",
    "attempt_id": "run-8f2c",
    "payload": {
      "work_item": { "repo": "acme/chat-bridge", "issue": 412 },
      "factory_config_version": "myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c",
      "role": "plan",
      "labels": ["bug"],
      "base_commit": "9593822"
    }
  },
  {
    "spec": "factorybench/v1",
    "id": "run-8f2c:usage:1",
    "type": "usage",
    "ts": "2026-11-11T09:16:41Z",
    "attempt_id": "run-8f2c",
    "payload": {
      "model": "gemini-3-pro",
      "input": 48210,
      "output": 3120,
      "cache_read": 31000,
      "reasoning": 900,
      "duration_ms": 151000
    }
  }
]
```

### Example response

```json
{ "accepted": 2, "duplicates": 0, "rejected": [] }
```

A rejected event is returned with its reason, e.g. `{"id": "run-8f2c:usage:2", "error": "payload.input missing"}`. It is still stored with `rejected` set, so it shows up in coverage instead of disappearing.

### Envelope

| Field | Meaning |
|---|---|
| `spec` | Contract version, `factorybench/v1` |
| `id` | Chosen by the factory, unique per factory, stable across retries |
| `type` | One of the event types below |
| `ts` | When it happened (RFC 3339, UTC) |
| `attempt_id` | Unique within the factory |
| `payload` | Type-specific, see below |

The factory is identified by its token. A `factory_id` in the body is ignored.

### Event types

| Type | Required payload | Optional |
|---|---|---|
| `attempt.started` | `work_item` (`{"repo": "owner/name", "issue": 412}`), `factory_config_version` | `parent_attempt_id`, `base_commit`, `labels`, `role` |
| `usage` | `model`, `input`, `output` | `cache_read`, `cache_write`, `reasoning`, `reported_cost_usd`, `duration_ms`, `phase` (`implement`, `verify`, `review`) |
| `activity` | — | `tool`, `summary` (advisory, live report only) |
| `human.waiting` | — | `reason` (e.g. `plan_review`) |
| `human.resumed` | — | `reason` |
| `human.steering` | — | `source` (`comment`, `chat`, `label`) |
| `pr.linked` | `pr_url` | `branch` |
| `attempt.finished` | `status`: `completed`, `failed`, or `cancelled` | `reason` |
| `gate` | `name` (e.g. `build`, `guard`, `test`), `passed` (bool) | `reason` |

Payload examples:

```jsonc
// activity
{ "tool": "run_command", "summary": "pnpm test" }
// human.waiting / human.resumed
{ "reason": "plan_review" }
// human.steering
{ "source": "comment" }
// pr.linked
{ "pr_url": "https://github.com/acme/chat-bridge/pull/57", "branch": "agent/issue-412" }
// attempt.finished
{ "status": "failed", "reason": "gate test failed after 3 attempts" }
// gate: one factory-internal gate result (e.g. a build/lint/test ladder)
{ "name": "test", "passed": false, "reason": "3 failing tests in pkg/parser" }
// attempt.started for a build that follows its plan
{ "work_item": { "repo": "acme/chat-bridge", "issue": 412 },
  "factory_config_version": "myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c",
  "role": "build", "parent_attempt_id": "run-8f2c" }
```

### Rules

- **Deltas, not totals.** Each `usage` event covers only the calls since the previous one. Tools that report cumulative totals (e.g. multi-turn `agy` sessions) must be diffed before sending.
- **Token classes don't overlap.** `input` is non-cached input only; `cache_read` and `cache_write` are separate; `reasoning` is a subset of `output`, shown for information. Factories whose tool lumps cached tokens into input must subtract them.
- **Missing parent is inferred.** If a follow-up attempt omits `parent_attempt_id`, FactoryBench links it to the latest earlier attempt of the same factory and work item.
- **Raw facts only.** Send `reported_cost_usd` only if the provider reported it. Otherwise FactoryBench prices the tokens from its own versioned price table (`estimated`).
- **Unknown is missing, not zero.** If usage is unavailable, send no `usage` event. That attempt counts against telemetry coverage.
- **Every agent session counts.** Report usage for failed, crashed, and cancelled sessions too.
- **Phase is optional.** `usage.phase` says what the session did: `implement`, `verify` (e.g. tests, gate fixes), or `review`. Verification tokens of a PR = usage with phase `verify` or `review`. Sessions without a phase count as unknown, not as implement.
- **Contract violations are rejected and kept** (see example response).
- **Keep it small.** Never require a field a factory cannot know. New optional fields are fine within `v1`; anything breaking is `v2`.

## 5. Integrating a factory

What a factory's maintainers change. Nothing in how the factory works changes; they add a reporting layer.

### One-time setup

1. Register the factory in FactoryBench: get a `factory_id` and an API token, and register its commit author identity.
2. Add the product repo to FactoryBench's watched repos (its read-only GitHub token needs access).

### Code changes

1. **Add an emitter** (about 20 lines). It must never break the factory: if FactoryBench is unreachable, it writes the event to a local spool file and retries later. It never throws.
2. **Decide three mappings:** what one *attempt* is in this factory, a stable attempt ID, and the `factory_config_version` string (e.g. factory version + agent CLI version + config hash).
3. **Call the emitter at about five points:** attempt start, after each agent session, PR created, human wait/resume, attempt end.

Reference emitter in TypeScript:

```ts
// emit.ts
const URL = process.env.FACTORYBENCH_URL, TOKEN = process.env.FACTORYBENCH_TOKEN;
export function emit(type: string, attemptId: string, key: string, payload: object): void {
  if (!URL) return;                                   // not configured: do nothing
  const ev = { spec: 'factorybench/v1', id: `${attemptId}:${key}`, type,
               ts: new Date().toISOString(), attempt_id: attemptId, payload };
  fetch(`${URL}/v1/events`, { method: 'POST', body: JSON.stringify([ev]),
    headers: { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' } })
    .catch(() => spool(ev));                          // never fail the run; retry later
}
```

Factories in other languages do the same with one HTTP POST, or call `factorybench emit`.

### What maintainers don't do

They don't change their gates, budget, ledger, or PR flow; don't calculate cost or metrics; and don't run FactoryBench code inside the agent's sandbox.

## 6. Verified tool facts (2026-11-11)


- **Claude Code 2.1.322**, `claude -p --output-format stream-json --verbose`: the final `result` line includes `total_cost_usd`, per-model `modelUsage` (with `costUSD`, list-price basis), tokens including cache and thinking, `duration_ms`, `num_turns`, `terminal_reason`, and `subagent_stats`. Hooks (`SessionStart`, `PostToolUse`, `Stop`, `SessionEnd`) carry `session_id` and `transcript_path` but no usage.
- **`agy` 1.5.4**, `-p --output-format stream-json`: `init` (with `conversation_id`), `step_update` (`agent_response` steps carry per-call usage), and `result` with `usage`, `status`, `duration_seconds`, `finished_reason`. No cost field. A failed run exits with code 3 and zero usage. Hook config uses a different format and event names (`PreInvocation`, `PostInvocation`, `PreToolUse`, `PostToolUse`, `Stop`) than Claude Code; not verified. A successful `agy` run is not yet captured (quota exhausted at test time).

## 7. Delivery order

1. **Contract.** Write `spec/events.schema.json` and golden event files in `testdata/`.
2. **Vertical slice.** `serve` with `POST /v1/events`, store, normalizer, `report live`, `factorybench emit`, and a tiny fake factory script that emits one full attempt.
3. **First real factory.** Add the call sites to one real factory; trace one real issue end to end.
4. **GitHub.** Polling reconciler (every 2–5 min) for PR/check/label/merge sync, factory vs. human commit attribution. Webhooks only if minutes of delay become a problem.
5. **Aggregation.** Retries and child attempts into work-item cost; coverage metrics; versioned price table.
6. **Longitudinal report** with configuration markers, cohorted by work-item start date.
7. **Survival updater** (14-day revert and corrective-change check; evidence stored, outcome stays `pending` until the window closes).
8. Dashboard only if reports are insufficient. Optional later: OTLP-to-contract adapter, `resource` events.

## 8. Risks

- Every factory must adopt the contract. Keep it small and stable; version it (`spec`) and never require fields a factory cannot know.
- The first integration needs buy-in from that factory's maintainers.
- Revert and corrective-change attribution is ambiguous. Store evidence with the verdict; keep it out of the core path until step 7.
- Tool output formats are not stable contracts. Drift is the factory's problem, but it shows up in FactoryBench as falling telemetry coverage.
- Cost is estimated for any tool that reports only tokens; price tables must be versioned and never edited in place.
