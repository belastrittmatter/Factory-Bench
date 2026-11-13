# FactoryBench reference

What the code and [spec/](../spec/) don't say. Integration steps are in [INSTALL.md](../INSTALL.md).

## Concepts

- **Factory**: a system that turns GitHub issues into PRs with AI agents. Registered once (`factorybench factory add`), identified by its API token. The server sets `factory_id` from the token, never from the request body.
- **Work item**: one GitHub issue (`repo` + `issue`). Several factories on the same issue share one work item.
- **Attempt**: one factory run on one work item, as the factory defines it (e.g. a `plan` run, then a `build` run). A retry is a new attempt. A follow-up attempt links to its parent through `parent_attempt_id`; if omitted, FactoryBench links it to the latest earlier attempt of the same factory and work item.
- **Stage, handoff, retry, staged run**: some factories split one try at an issue into several attempts, one per stage (e.g. `plan` writes failing tests and opens the PR, then `build` implements). An attempt with a parent (explicit or inferred) is a **handoff** to a new stage when both it and its parent have a `role` and the roles differ, and a **retry** when it has its parent's role and the parent failed or was cancelled. A **run** is one factory on one work item; a run with at least one handoff is a **staged run** and has one stage more than its handoffs. Runs without a handoff (single-session factories, whose reruns keep their role or send none) are reported exactly as before. A spelling change of a free-text role (`build` → `implement`) counts as a handoff.
- **`factory_config_version`**: an opaque string per attempt (e.g. `myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c`). Change it whenever anything that could change results changes; `report factories --by config` splits on it.
- **Unit**: one factory (or factory + config) on one work item. Every metric is built from units. Its PRs are the ones its attempts linked. With `--by config` a staged run stays one unit, under the config of its first attempt; its later configs are listed as escalations.

![One issue through a plan → build factory](diagrams/issue-flow.svg)

Attempt states: `running`, `waiting_for_human`, `completed`, `failed`, `cancelled` ([diagram](diagrams/attempt-states.svg)). A `human.waiting` is closed by the next `human.resumed` on the same attempt or its child (plan waits, build resumes); active time = elapsed − human waits.

## Event contract

Fields and types: [spec/events.schema.json](../spec/events.schema.json), one example per type in [spec/examples/](../spec/examples/). Rules the schema can't express:

- **Ids are stable across retries.** `id` is unique per factory and chosen by the factory (`emit` uses `<attempt>:<key>`). Delivery is at-least-once; a resent id is a duplicate. An accepted event is never overwritten; a resend of a **rejected** id replaces it.
- **Deltas, not totals.** Each `usage` event covers only the calls since the previous one.
- **Token classes don't overlap.** `input` is non-cached input; `cache_read` and `cache_write` are separate; `reasoning` is a subset of `output`, for information.
- **Unknown is missing, not zero.** No usage known, no `usage` event; the attempt then lowers usage coverage. Report usage for failed and cancelled sessions too.
- **`reported_cost_usd` only if the provider reported it.** Otherwise FactoryBench estimates cost from its price table.
- **`phase`** (`implement`, `verify`, `review`) is optional; sessions without it count as unknown, not as implement.
- **Raw facts only.** Factories never compute metrics, costs or outcomes. Events may arrive late or out of order; normalization orders them by `ts`.
- New optional fields are fine within `factorybench/v1`; anything breaking is `v2`.

### Transport

- `POST /v1/events`, bearer token, a JSON **array** of events (max 10 MB). Response: `{"accepted": 2, "duplicates": 0, "rejected": [{"id": "...", "error": "payload.input missing"}]}`. Rejected events are stored too, with their error, so they count in reports.
- `POST /v1/logs`, same token, OTLP/HTTP JSON logs, uncompressed (today only Claude Code's). Every record needs the resource or record attribute `factorybench.attempt` (the attempt id); records without it are rejected via `partialSuccess`. An optional `factorybench.phase` (`implement`, `verify`, `review`) sets the usage phase of `api_request` records; other values are ignored. Each record is stored raw as type `otlp.<event.name>` with id `session.id:event.sequence:event.timestamp`, so exporter retries are stored once and a resumed session (whose sequence restarts) is not merged. Personal data (`user.email`, account and organization ids) and content attributes (prompts, tool input, raw API bodies) are dropped before storage. Normalization maps `api_request` to usage, `tool_result` to tool calls, `subagent_completed` to subagent runs, `api_error` / `api_retries_exhausted` to API errors, and ignores the rest. The attempt lifecycle still comes over `/v1/events`.

## Metrics

Shown by `report items` (one row per unit) and `report factories [--by config]` (one column per factory or factory + config). A review or commit is human when its login or author isn't a registered factory. Unpriced cost shows `+?`, never 0; every rate shows its counts; an empty denominator shows `n/a`.

| Metric | Definition |
|---|---|
| attempts | attempts (failed: state `failed`). On staged runs also stages and retries, and failed counts only failed attempts that were retried or ended the run, e.g. `3 attempts · 2 stages · 1 retry (2 failed)`; `report item` lists the others as `failed, then handed off` |
| escalations | `--by config`: configs a staged run switched to after its first one, with the number of runs |
| tokens / issue | input + output + cache read + cache write over all usage ÷ work items; `+?` when an attempt has no usage |
| time / issue | per unit, elapsed = first attempt start → last attempt end (now if open), active = elapsed − human wait; averaged over work items |
| cost total / per issue / per merged issue | sum of priced sessions (provenance shown, `+?` if any unpriced) ÷ work items, ÷ units whose own PR merged |
| outcome (items report) | from the unit's own PRs: merged if any merged, rejected if all closed, else open |
| verification tokens / PR | tokens of usage with phase `verify` or `review` in units with a linked PR ÷ those PRs; `n/a` without phase data |
| issues closed by agent | work items with issue state closed and `closed_by` = the factory's GitHub login ÷ work items |
| first-pass plan approval | units whose plan waited for a human and was resumed: first plan resumed with no `human.steering` before the resume and no second plan ÷ those units (pending reviews excluded) |
| first-pass PR approval | linked PRs whose first human APPROVED/CHANGES_REQUESTED review is APPROVED ÷ PRs with such a review |
| review rounds / PR | human CHANGES_REQUESTED reviews ÷ linked PRs with any human review |
| human interventions / issue | `human.waiting` + `human.steering` + human commits on linked PRs + human CHANGES_REQUESTED reviews, ÷ work items |
| escaped defects | human CHANGES_REQUESTED reviews and human commits dated after the unit's last attempt ended, shown as `k on m/n PRs` |
| time to first PR / to merge | per unit, first attempt start → first `pr.linked` / first merge of its PRs; averaged over units that have one. Marked as not comparable on staged runs, where an early stage may open the PR: compare time to merge |
| CI passing on first push | linked PRs whose executed check runs on the first pushed head SHA (that of the earliest-started run) all succeeded ÷ linked PRs with such runs. On staged runs the earliest run started at or after the first handoff, on a SHA that had no run before it: an earlier stage's push may fail CI on purpose, even when its check starts late |
| PR size / cost per changed line | additions, deletions, changed files of sized linked PRs, averaged per PR; cost of units with a sized PR ÷ their additions + deletions |
| tool calls / issue, tool mix, tool failure rate | tool calls ÷ work items; the 3 most used tools; failed ÷ calls that report success or failure |
| time in model / tool calls | sum of usage `duration_ms` / sum of tool call `duration_ms` (parallel subagents can exceed elapsed time) |
| subagents / issue, API errors / attempt | subagent runs ÷ work items, API errors ÷ attempts |
| telemetry coverage | usage: attempts with ≥ 1 usage ÷ attempts; activity: attempts with ≥ 1 tool call ÷ attempts; unpriced sessions; rejected events |
| gates | per gate name: failed ÷ runs; per CI check on linked PRs: failure or timed_out ÷ executed runs (success, failure, timed_out; skipped, neutral, cancelled etc. don't count) |
| CI suite | per check, executed runs only: median completed − started (negative durations dropped); flake rate = head SHAs with both a failure and a success ÷ head SHAs with an executed run |

Tool, subagent, API error and PR size rows are left out of the scorecard when no factory has data for them.

## Storage

SQLite, one schema for all factories ([internal/store/schema.sql](../internal/store/schema.sql), [diagram](diagrams/db-schema.svg)). Intake and the GitHub poller only append to `events`, the raw record of everything received; every other table is derived and rebuilt by `factorybench replay`, so a normalization bug never loses data. Factory-specific extras stay in the raw payload. Existing databases are upgraded automatically on open (v1 → v2 → v3); after an upgrade, run `factorybench replay` to fill new derived tables and columns.

Prices live in `internal/pricing/prices/<YYYY-MM-DD>.json` (USD per million tokens). A price change is a new file, never an edit: a session is priced with the newest table dated on or before it, so past costs never change.

GitHub: `serve --poll <interval>` polls every repo a factory sent an accepted `attempt.started` for (plus `--repo`), with `GITHUB_TOKEN` or `gh auth token`. Commits, reviews and check runs are fetched only for factory PRs (linked via `pr.linked` or authored by a factory's `github_login`).

## Verified tool facts (2026-11-11)

- **Claude Code 2.1.322, OTLP logs**: one `resourceLogs` per body, event name in `event.name`, some numbers and booleans as `stringValue`. `OTEL_RESOURCE_ATTRIBUTES` lands on every record. Summed `api_request` tokens and cost equal the final `result.modelUsage` exactly, subagents included. `parent_agent_id` only on nested subagents; a killed subagent gets no `subagent_completed`. No spool: a failed batch is retried 3 times (~8 s), then dropped; SIGTERM flushes, SIGKILL loses the last batch. Captures: [testdata/captured/claude-code-2.1.322-otlp/FINDINGS.md](../testdata/captured/claude-code-2.1.322-otlp/FINDINGS.md).
- **Claude Code 2.1.322, `-p --output-format stream-json`**: the `result` line has `total_cost_usd`, per-model `modelUsage` (with `costUSD`), `duration_ms`, `subagent_stats`. Hooks carry no usage.
- **agy 1.5.4, `-p --output-format stream-json`**: `step_update` (per-call usage on `agent_response` steps) and `result` (`usage`, `status`, `duration_seconds`); no cost field; usage is cumulative across turns. A failed run exits 3 with zero usage. A successful run is not captured yet.
