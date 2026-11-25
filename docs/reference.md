# FactoryBench reference

What the code and [spec/](../spec/) don't say. Integration steps are in [INSTALL.md](../INSTALL.md).

## Concepts

- **Factory**: a system that turns GitHub issues into PRs with AI agents. Registered once (`factorybench factory add`), identified by its API token. The server sets `factory_id` from the token, never from the request body.
- **Work item**: one GitHub issue (`repo` + `issue`). Several factories on the same issue share one work item.
- **Attempt**: one factory run on one work item, as the factory defines it (e.g. a `plan` run, then a `build` run). A retry is a new attempt. A follow-up attempt links to its parent through `parent_attempt_id`; if omitted, FactoryBench links it to the latest earlier attempt of the same factory and work item.
- **Stage, handoff, retry, staged run**: some factories split one try at an issue into several attempts, one per stage (e.g. `plan` writes failing tests and opens the PR, then `build` implements). An attempt with a parent (explicit or inferred) is a **handoff** to a new stage when both it and its parent have a `role` and the roles differ, and a **retry** when it has its parent's role and the parent failed or was cancelled. A **run** is one factory on one work item; a run with at least one handoff is a **staged run** and has one stage more than its handoffs. Runs without a handoff (single-session factories, whose reruns keep their role or send none) are reported exactly as before. A spelling change of a free-text role (`build` → `implement`) counts as a handoff.
- **Step**: the worker step a Claude Code session ran, as the factory names it (free text, e.g. `analyzer`, `red`, `green`, `reviewer`), sent as `factorybench.step` on `/v1/logs` or as `payload.step` of a `usage` event. Optional; one session has one step. Different from **phase** (fixed: `plan` / `implement` / `verify` / `review`, kept for comparing factories) and from **lane** (board lanes from run manifests). A factory should send both step and phase. A run is **sent back** to a step when it goes back to a step that first ran before the current one (review → green); the steps that then run again (green → review) are not send-backs. A factory that knows its send-backs sends them as `send_back` events (from, to, kind, reason): an attempt with any counts exactly those, and only an attempt without any has them inferred from its step order. It **re-runs** a step when the same step runs twice in a row. Both are counted per factory and attempt, never across factories on one issue; the same rule as for lanes, whose dispatches count for the factory and attempt they ran in. A session's time is its event span or, when longer, the sum of its model calls' `duration_ms`; null when neither is known.
- **Session**: one agent session: an OTLP `session.id`, or the `payload.session` of `usage` events. Session counts are distinct sessions per attempt; a usage row without a session counts as one. A step's sessions and time are unknown (null), not 0, when its usage carries no session. A session from `usage` events runs from the earliest `ts − duration_ms` of its rows to their latest `ts` (the event is sent when the session ends); an OTLP session from its first to its last event.
- **`factory_config_version`**: an opaque string per attempt (e.g. `myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c`). Change it whenever anything that could change results changes; `report factories --by config` splits on it.
- **Unit**: one factory's run on one work item, in its column (factory, config, or one config part). Every metric is built from units. Its PRs are the ones its attempts linked. With `--by config` a staged run stays one unit, under the config of its first attempt; its later configs are listed as escalations.

![One issue through a plan → build factory](diagrams/issue-flow.svg)

Attempt states: `running`, `waiting_for_human`, `completed`, `failed`, `cancelled` ([diagram](diagrams/attempt-states.svg)). Several `human.waiting` may be open at once (a question and command approvals): each `human.resumed` closes the oldest open one on the same attempt, or on its parent when it has none (plan waits, build resumes); with a planning one it closes every open planning one (questions asked together are answered together). Human wait is the wall time any of them was open, counted once; active time = elapsed − human waits. Its optional `kind` is `planning` (a question before the work is confirmed) or `blocking` (any later question or approval). A resume with `by` `observer` (an agent answering for the person) closes an observer wait: its time is not human wait (so it counts as active) and it is not a human intervention.

## Event contract

Fields and types: [spec/events.schema.json](../spec/events.schema.json), one example per type in [spec/examples/](../spec/examples/). Rules the schema can't express:

- **Ids are stable across retries.** `id` is unique per factory and chosen by the factory (`emit` uses `<attempt>:<key>`). Delivery is at-least-once; a resent id is a duplicate. An accepted event is never overwritten; a resend of a **rejected** id replaces it.
- **Deltas, not totals.** Each `usage` event covers only the calls since the previous one.
- **Token classes don't overlap.** `input` is non-cached input; `cache_read` and `cache_write` are separate; `reasoning` is a subset of `output`, for information.
- **Unknown is missing, not zero.** No usage known, no `usage` event; the attempt then lowers usage coverage. Report usage for failed and cancelled sessions too.
- **`reported_cost_usd` only if the provider reported it.** Otherwise FactoryBench estimates cost from its price table.
- **`phase`** (`plan`, `implement`, `verify`, `review`) is optional; `plan` is planning before the work starts (e.g. a context creator or strategist); sessions without it count as unknown, not as implement. An `activity` with `agent_id` and `phase` gives that phase to the subagent's usage rows that have none, OTLP ones included; the first phase sent for an agent wins, and arrival order doesn't matter. A phase set on the usage itself (or `factorybench.phase` on OTLP) always wins.
- **Raw facts only.** Factories never compute metrics, costs or outcomes. Events may arrive late or out of order; normalization orders them by `ts`.
- New optional fields are fine within `factorybench/v1`; anything breaking is `v2`.

### Transport

- `POST /v1/events`, bearer token, a JSON **array** of events (max 10 MB). Response: `{"accepted": 2, "duplicates": 0, "rejected": [{"id": "...", "error": "payload.input missing"}]}`. Rejected events are stored too, with their error, so they count in reports.
- `POST /v1/logs`, same token, OTLP/HTTP JSON logs, uncompressed (today only Claude Code's). Every record needs the resource or record attribute `factorybench.attempt` (the attempt id); records without it are rejected via `partialSuccess`. An optional `factorybench.phase` (`plan`, `implement`, `verify`, `review`) sets the usage phase of `api_request` records; other values are ignored. An optional `factorybench.step` (free text, trimmed, cut to 64 characters, not rejected) names the worker step the session ran; it sets the usage step of `api_request` records and the step of the session on the dashboard. `session.id` sets the usage session of `api_request` records. Each record is stored raw as type `otlp.<event.name>` with id `session.id:event.sequence:event.timestamp`, so exporter retries are stored once and a resumed session (whose sequence restarts) is not merged. Personal data (`user.email`, account and organization ids) and content attributes (prompts, tool input, raw API bodies) are dropped before storage. Normalization maps `api_request` to usage, `tool_result` to tool calls, `subagent_completed` to subagent runs, `api_error` / `api_retries_exhausted` to API errors, and ignores the rest. The attempt lifecycle still comes over `/v1/events`.

## Metrics

Shown by `report items` (one row per unit) and `report factories [--by config|harness|model|commit]` (one column per factory, config version, or config part: commit, harness, model without its provider prefix). A grouped column pools the runs of several factories and shows per-run medians with their min–max (cost, active and wall time, send-backs) instead of per-issue sums; lanes merged comes from the finish reason's `lanes=merged/planned`. A review or commit is human when its login or author isn't a registered factory. Unpriced cost shows `+?`, never 0; every rate shows its counts; an empty denominator shows `n/a`.

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
| human interventions / issue | `human.waiting` (except `kind` `planning` and waits an observer answered) + `human.steering` + human commits on linked PRs + human CHANGES_REQUESTED reviews, ÷ work items |
| observer answers / issue | non-planning waits closed by a `human.resumed` with `by` `observer` ÷ work items, with the wall time only an observer was awaited; left out when no factory has one |
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

## Dashboard API: timeline

`/api/item?repo=&issue=` and `/api/attempt?factory=&id=` carry, besides their other fields:

- `sessions[]`: `{factory_id, attempt_id, session, step, phase, start, end, cost_usd, tokens, tools, failed_tools}`, every session (see **Session** for start and end); every session also has `first_ts`, `last_ts` and `events` (for a usage session: start, end and its usage rows). `tools` / `failed_tools` are the attempt's tool calls inside the session's time, null when the attempt reported none or another of its sessions overlaps that time (tool calls carry no session, so they cannot be told apart).
- `checks[]`: `{factory_id, attempt_id, name, passed, at, step, reason}`, the gates; `step` is the gate's name when its attempt has a session of that step, else null. GitHub check runs of the issue's PRs are `ci_checks[]`.
- `send_backs[]`: `{factory_id, attempt_id, at, from, to, reason, kind, inferred}`; inferred ones (from step order) have null reason and kind. Left out when empty.
- `waits[]`: `{factory_id, attempt_id, start, end, kind, reason, by}`, one per `human.waiting`, `end` and `by` (`person`, `observer`, or null when not sent) null while open. Left out when empty.

- `timeline[]` (attempt only): the run's event log, the `report item` lines of the attempt's factory on its issue plus the issue's GitHub lines.

- `attempts[]` (item only) also carry the run's token mix (`input`, `output`, `cache_read`, `cache_write`), `tool_calls` / `failed_tools` and `finish_reason` (the reason of its `attempt.finished`); null when not reported.

Unknown values are null, never 0. `step_stats[].sent_back` counts `send_backs[]`. The item has no `steps[]` or `gates[]`: its step sessions are `sessions[]` with a `step`, its gates are `checks[]`.

## Storage

SQLite, one schema for all factories ([internal/store/schema.sql](../internal/store/schema.sql), [diagram](diagrams/db-schema.svg)). Intake and the GitHub poller only append to `events`, the raw record of everything received; every other table is derived and rebuilt by `factorybench replay`, so a normalization bug never loses data. Factory-specific extras stay in the raw payload. Existing databases are upgraded automatically on open (v1 → v2 → v3 → v4 → v5 → v6) by `serve` or `replay`; after an upgrade, run `factorybench replay` to fill new derived tables and columns (v4 adds `usage.step`, v5 `usage.session` and the `send_backs` table, v6 `usage.harness`, `status`, `turns`, `lane`, `role` and `lane` on `tool_calls`, `gates` and `send_backs`). The dashboard opens the database read-only and refuses any other schema version, so upgrade it with the current binary first.

Prices live in `internal/pricing/prices/<YYYY-MM-DD>.json` (USD per million tokens). A price change is a new file, never an edit: a session is priced with the newest table dated on or before it, so past costs never change.

GitHub: `serve --poll <interval>` polls every repo a factory sent an accepted `attempt.started` for (plus `--repo`), with `GITHUB_TOKEN` or `gh auth token`. Commits, reviews and check runs are fetched only for factory PRs (linked via `pr.linked` or authored by a factory's `github_login`).

## Verified tool facts (2026-11-11)

- **Claude Code 2.1.322, OTLP logs**: one `resourceLogs` per body, event name in `event.name`, some numbers and booleans as `stringValue`. `OTEL_RESOURCE_ATTRIBUTES` lands on every record. Summed `api_request` tokens and cost equal the final `result.modelUsage` exactly, subagents included. `parent_agent_id` only on nested subagents; a killed subagent gets no `subagent_completed`. No spool: a failed batch is retried 3 times (~8 s), then dropped; SIGTERM flushes, SIGKILL loses the last batch. Captures: [testdata/captured/claude-code-2.1.322-otlp/FINDINGS.md](../testdata/captured/claude-code-2.1.322-otlp/FINDINGS.md).
- **Claude Code 2.1.322, `-p --output-format stream-json`**: the `result` line has `total_cost_usd`, per-model `modelUsage` (with `costUSD`), `duration_ms`, `subagent_stats`. Hooks carry no usage.
- **agy 1.5.4, `-p --output-format stream-json`**: `step_update` (per-call usage on `agent_response` steps) and `result` (`usage`, `status`, `duration_seconds`); no cost field; usage is cumulative across turns. A failed run exits 3 with zero usage. A successful run is not captured yet.
