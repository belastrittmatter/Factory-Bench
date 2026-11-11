# FactoryBench — MVP design

FactoryBench observes a software factory while it resolves real work items. It connects factory events with code-host outcomes and shows whether cost, speed, autonomy, and survival improve after factory changes.

The MVP is passive: the factory owns its work, execution environment, retry policy, and safety limits. FactoryBench never injects tasks, edits a repository, decides whether a PR merges, runs app benchmarks, or kills a production run.

FactoryBench is factory-agnostic. Every factory reports through **one event contract** (see [implementation-plan.md §4](implementation-plan.md#4-the-event-contract)). The factory reports raw facts; FactoryBench does all processing.

## 1. Goals and boundaries

### Goals

- Record every factory attempt at a real work item, including failures and retries.
- Measure tokens, cost, active and elapsed time, and human help.
- Connect attempts to PRs, CI, merges, human code pushes, reverts, and corrective work.
- Attach the exact factory configuration to every attempt.
- Show live status and longitudinal trends per factory; allow comparisons between factories when conditions are equivalent.

### Not FactoryBench's job

- **Computing metrics inside the factory.** Factories send raw facts only. They don't calculate cost, rates, or outcomes for FactoryBench.
- **Reading factory-specific data.** FactoryBench never reads a factory's own logs, ledgers, or transcripts.
- **Measuring the app.** App performance (start time, memory, leaks) belongs to the product repo's bench gate (§9). FactoryBench only sees that gate's pass/fail result through GitHub.
- **Gating PRs or stopping runs.**

### Later, not in the MVP

- Factory-owned task fixtures, sandboxing, network proxies, or hidden graders.
- Controlled task packs, repeated trials, and maintenance sequences.
- Mandatory mutation testing, static quality detectors, or an LLM review of every run.
- Factory hardware usage (laptop/runner CPU and memory). See §6.
- A single weighted factory score.

A controlled benchmark is a separate follow-up tool. Production observation answers whether real work is improving; controlled replays help determine whether a particular change caused the improvement.

## 2. Concepts

| Term | Meaning |
|---|---|
| **Factory** | A system that turns work items into PRs with AI agents. It's registered once in FactoryBench and gets a `factory_id` and an API token. |
| **Event contract** | The one JSON format every factory uses to report what happened. |
| **Work item** | A real GitHub issue the factory was asked to resolve, identified by `repo` and `issue` number. If two factories work the same issue, it's one work item with attempts from both. |
| **Attempt** | One factory execution for a work item, as the factory defines it (e.g. one loop run, either `plan` or `build`). Child attempts (e.g. a build attempt after its plan) stay linked through `parent_attempt_id`. |
| **Factory configuration** | An opaque version string the factory supplies, for example `myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c`. |
| **Event** | A timestamped factory or code-host observation. Raw events are kept forever. |
| **Outcome** | What happened to the work: open, merged, rejected, reworked, reverted, or followed by corrective work. |
| **Survival** | A merged change that remains present after 14 days without a revert or identified corrective change. |

An attempt is not successful merely because the factory stopped or its tests passed. Production acceptance and subsequent survival are separate observations.

## 3. Architecture

![Who handles what](docs/diagrams/architecture.svg)

Solid arrows are work and control; dashed arrows are observation only. FactoryBench never sends anything back.

### Who owns what

Factory maintainers each run their own factory; one FactoryBench operator runs FactoryBench and owns the app benchmarks. Hosting is in [implementation-plan.md §8](implementation-plan.md#8-hosting).

| Part | Owner | Does | Never does |
|---|---|---|---|
| **Factories** | Factory maintainers, one factory each | Claim issues, run agent sessions, run their own gates, push PRs, send contract events | Compute metrics or read FactoryBench |
| **Bench gate + nightly soak** (product CI) | FactoryBench operator | Owns `bench/`, budgets, main-vs-PR comparison, 2-hour soak, writes `docs/health/` | Know which factory made a change |
| **FactoryBench** | FactoryBench operator | Receives events, polls GitHub, keeps raw events, prices tokens, builds reports and survival | Run benchmarks, gate PRs, or send anything back |
| **GitHub** | — | Shared record: issues, labels, PRs, check runs, commit authors | — |
| **Issues, plan approvals, reviews, merges** | Humans | The human steps of the loop | — |

FactoryBench stays factory-agnostic: it only sees contract events and GitHub. The only app-specific code is `bench/` in the product repo.

### FactoryBench's four parts

1. **Event intake** (`POST /v1/events`): validates contract events and stores them raw.
2. **GitHub sync**: polls the GitHub API every few minutes (reconciler). Webhooks are optional later; polling needs no public endpoint.
3. **Normalization**: a pure, replayable step that turns raw events into work items, attempts, and usage.
4. **Reports**: live attempts, completed work items, weekly or rolling 28-day trends with configuration changes marked, and 14-day survival.

Start with one service and SQLite. A generated terminal or Markdown report is enough for the first vertical slice; a web dashboard can follow after ingestion is reliable.

### One issue end to end (a plan → build factory)

![One issue through a plan → build factory](docs/diagrams/issue-flow.svg)

Dashed arrows are what FactoryBench sees at each step. A plan and its build are two linked attempts; the time between them is human wait.

## 4. Data collection

### Factory events

Factories send the contract events defined in [implementation-plan.md §4](implementation-plan.md#4-the-event-contract):

- `attempt.started` and `attempt.finished` (completed, failed, or cancelled).
- `usage`: raw token counts per agent session, plus cost only if the provider reported it.
- `human.waiting`, `human.resumed`, `human.steering`.
- `pr.linked`.
- `activity` (optional, for the live view).

Do not ask the model to estimate its own cost. Prefer provider-reported usage. If usage is unavailable, send nothing; FactoryBench stores it as unknown, not zero, and reduces telemetry coverage.

Keep prompt and completion capture out entirely. The MVP needs operational metadata, not private chain-of-thought or source context.

### Code host

FactoryBench reads GitHub itself, identically for every factory:

- PR opened, updated, reviewed, closed, and merged.
- Factory and human commit authors (e.g. a factory committing as `factory-bot@users.noreply.github.com`).
- Check run conclusions, including the bench gate.
- Labels on issues and PRs.
- Reverts and linked corrective PRs or issues.

Human review is not automatically an intervention. Track steering, waiting time, and human code pushes separately.

The MVP polls instead of using webhooks: it needs no public endpoint and can't miss deliveries. If webhooks are added later, polling stays as the reconciler.

### Telemetry standards

OpenTelemetry GenAI conventions are not the intake format. If a factory only emits OTLP, an adapter maps it into the contract later.

## 5. Storage

![Database schema](docs/diagrams/db-schema.svg)

All factories share **one schema** with a `factory_id` column. There is no table per factory: adding a factory needs no migration, and comparing factories is a `GROUP BY`.

- `events` is the only table intake writes. It holds every contract and GitHub event as received, and is append-only.
- Every other table is derived and can be dropped and rebuilt with `factorybench replay`. A normalization bug therefore never loses data.
- The server sets `factory_id` from the API token, never from the request body.
- Factory-specific extras stay in the raw `payload` JSON. A field becomes a column only when it's added to the contract for everyone.
- Every usage or cost value records its provenance: `reported`, `estimated`, or unknown (null). Estimated cost includes the price-table version.

The live state machine is deliberately small:

```text
running | waiting_for_human | completed | failed | cancelled
```

Do not introduce a detailed phase taxonomy until real reports demonstrate a need for one. The full SQL is in [implementation-plan.md §3](implementation-plan.md#3-database-schema).

## 6. Metrics

Always show outcome, cost, duration, and human effort separately. Every metric can be split by factory, configuration version, model, repository, label/task type, and week.

| Group | Metric | Definition | Source |
|---|---|---|---|
| **Volume** | Work items started | Distinct work items with at least one attempt | contract |
| | Attempts per work item | Including failed and child attempts | contract |
| | Sessions per attempt | Number of `usage` events | contract |
| **Outcome** | Merged / rejected / open / needs-human / cancelled | Final state per work item | GitHub + contract |
| | Autonomous merge rate | Merged without a human code push | GitHub commit authors |
| | First-attempt merge rate | Merged without another factory attempt | contract + GitHub |
| | Rework rate | Required a human code push or a corrective attempt | GitHub + contract |
| | Survival rate | Merged and not reverted or correctively changed after 14 days | GitHub reconciler |
| **Cost** | Attempt cost | All sessions and child attempts, with provenance shown | tokens × price table, or reported cost |
| | Cost per work item | Sum across all attempts, including failed and abandoned | derived |
| | Cost per merged / surviving work item | Total cohort spend ÷ merged / surviving items from that cohort | derived |
| | Wasted cost | Spend on attempts that did not lead to a merge | derived |
| **Tokens** | Input, output, cache read/write, reasoning | Per session, summed per attempt | contract |
| | Cache hit rate | cache_read ÷ input | derived |
| **Time** | Elapsed duration | Start to terminal state, including waits | contract |
| | Active duration | Elapsed minus explicit human waits | contract |
| | Session duration | Per agent session, if the factory sends it | contract |
| | Time to first PR, PR open-to-merge | | contract + GitHub |
| **Human effort** | Human wait count and time | Time explicitly marked as waiting for a person | contract |
| | Steering events, manual resumes | Reported separately | contract |
| | Human commits on agent PRs | Commits not authored by the factory | GitHub |
| **Delivery quality** | CI pass rate on agent PRs | Per check, including the bench gate | GitHub check runs |
| | Bench gate failures per PR | Budget or regression failures | GitHub check runs |
| | Perf issues created afterwards | Corrective `perf` issues linked to the PR | GitHub |
| **Activity** | Model calls, tool calls | From optional `activity` events | contract |
| **Data quality** | Telemetry coverage | Share of attempts with complete usage and timing | FactoryBench |
| | Rejected events | Events that failed validation | FactoryBench |

Cost per surviving work item is the best long-term optimization target, but it is immature until the follow-up window closes. Open and pending outcomes are shown separately, never counted as successes or silently removed from cohort spend.

Follow-up and revert attribution can be ambiguous. Record the evidence and treat survival as an operational signal, not proof that the original change caused or avoided a defect.

### Hardware usage

- **The app's hardware usage** (CPU, memory, leaks) is measured by the bench gate and nightly soak (§9), not by FactoryBench. FactoryBench sees pass/fail via the check run. Later, if the gate also writes `history.jsonl`, FactoryBench can chart those numbers per factory configuration as an optional generic metric.
- **The factory's own hardware usage** is out of the MVP. Agent compute happens on the model provider's side and shows up as tokens and cost; local CPU on a shared laptop is noisy. Two cheap additions later: CI minutes per agent PR from GitHub's workflow-run API (no contract change), and an optional `resource` event (CPU seconds, peak memory, duration) for factories that run in CI or containers.

## 7. Reports

### Live attempts

One row per active attempt:

```text
factory          issue  status    elapsed   active   human wait   tokens   cost   last event
example-factory  #412   running   12:04     11:32    00:00        81k      $0.48  8s ago
example-factory  #418   waiting   22:10     08:11    13:59        34k      $0.22  2m ago
```

Warnings such as silence or rapid spend are advisory. The observer does not change the run.

### Work-item result

```text
#412 cache returns stale rows                 factory example-factory
  attempts  2 (plan completed, build completed)  config myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c
  time      18m active / 26m elapsed          human 1 plan wait (8m), 0 code pushes
  usage     142k input / 9k output            cost $1.12 estimated (prices v3)
  delivery  CI passed, bench gate passed, merged autonomously
  survival  pending until 2026-11-24
```

### Work-item timeline

Everything that happened to one issue, across every factory that worked on it, in time order. It's built from the stored events, linked through attempts and PRs:

```text
acme/chat-bridge#412 cache returns stale rows            outcome merged · survival pending until 2026-11-24
  09:14  example-factory  attempt a1 started (plan)       config myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c
  09:16                   session gemini-3-pro            48k in / 31k cached / 3k out   $0.21 est.
  09:19                   PR #57 linked (draft)
  09:19                   waiting for human (plan_review)
  09:19                   attempt a1 completed            3m, $0.21
  09:27  human            label agent:plan-approved       wait 8m
  09:27  example-factory  attempt a2 started (build, parent a1)
  09:31                   session 1  …                    gate test failed
  09:36                   session 2  …                    all gates passed
  09:36                   attempt a2 completed            9m, $0.91
  09:41  github           check bench-gate: success       check test: success
  10:02  github           PR #57 merged (0 human commits)
  totals  2 attempts · 3 sessions · 12m active / 48m elapsed · $1.12 est. · 1 human wait · autonomous merge
```

Lists of work items and attempts can be filtered and sorted by factory, configuration, outcome, cost, duration, human wait, and date. Opening one shows its timeline. If two factories worked the same issue, the timeline shows both side by side.

### Work items

`factorybench report items [--factory f] [--outcome o]`: the entry point, one row per work item and factory; two factories on one issue sit next to each other. `+?` marks partly unknown tokens or cost.

```text
item         title                                     factory  outcome   attempts      tokens   cost      active  elapsed  human  PR          escaped
acme/app#11  Fix typo in docs                          fa       open      2             1.7k     $0.01 +?  7m0s    15m0s    1      #22 open    0
acme/app#10  Add retry with backoff to the upstream …  fa       merged    2             7.9k     $0.05     14m0s   21m0s    4      #20 merged  2
acme/app#10  Add retry with backoff to the upstream …  fb       rejected  2 (1 failed)  3.1k +?  $0.03 +?  13m0s   13m0s    2      #21 closed  2
+? = lower bound: some attempts sent no usage or some sessions couldn't be priced
open one: factorybench report item <owner/repo#n>
```

### Factory scorecard

`factorybench report factories [--by config]`: factories head to head (metrics as rows), then the gates that caught defects and CI suite health. Definitions are in [implementation-plan.md](implementation-plan.md) §2 Reports.

```text
metric                       fa                             fb
work items                   2                              1
attempts                     4 (0 failed)                   2 (1 failed)
sessions                     5                              2
tokens / issue               4.8k                           3.1k +? (1 attempt(s) without usage)
time / issue                 10m30s active / 18m0s elapsed  13m0s active / 13m0s elapsed
cost total                   $0.06 reported +?              $0.03 reported +? (1 attempt(s) without usage)
cost / issue                 $0.03 +?                       $0.03 +?
cost / merged issue          $0.06 +? (1 merged)            n/a (0 merged)
verification tokens / PR     1.1k (2.2k / 2 PRs)            700 (700 / 1 PRs)
issues closed by agent       50% (1/2)                      0% (0/1)
first-pass plan approval     50% (1/2)                      n/a
first-pass PR approval       50% (1/2)                      0% (0/1)
review rounds / PR           0.5 (1/2)                      2.0 (2/1)
human interventions / issue  2.5 (5/2)                      2.0 (2/1)
escaped defects              2 on 1/2 PRs                   2 on 1/1 PRs
usage coverage               100% (4/4) attempts            50% (1/2) attempts
unpriced sessions            1                              0
rejected events              1                              0
+? = lower bound: some attempts sent no usage or some sessions couldn't be priced

Gates (defects caught)
  factory  source   gate   failed / runs
  fa       factory  test   50% (1/2)
  fa       CI       docs   n/a
  fa       CI       lint   n/a
  fa       CI       test   25% (1/4)
  fb       factory  build  0% (0/1)
  fb       CI       test   100% (1/1)

CI suite
  factory  check  median runtime  flake rate (sha pairs)
  fa       docs   n/a             n/a
  fa       lint   n/a             n/a
  fa       test   4m30s           33% (1/3)
  fb       test   6m0s            0% (0/1)
```

### Longitudinal report

Group by week or rolling 28 days, by factory, and by factory configuration. Show:

- Work items started, merged, rejected, open, reworked, and surviving.
- Total cost and cost per merged/surviving work item.
- Median active and elapsed duration.
- Autonomous and first-attempt merge rates.
- Human interventions and wait time.
- CI and bench gate pass rates.
- Attempt counts and telemetry coverage.
- Configuration-change markers.

Cohort reports by **work-item start date**, not merge date. This keeps unresolved work visible and assigns every attempt's cost to the correct cohort.

Break down results by repository and task type when the mix changes. A factory can appear cheaper simply because it received easier work.

## 8. Interpreting change

1. Collect a baseline during normal production work.
2. Change one measurable factory setting at a time.
3. Record the configuration version and adoption time.
4. Continue observation and compare similar repositories and task types.
5. Inspect individual attempts behind important movements.
6. Use controlled replays or randomized configuration rollout when a causal claim matters.

Show counts and distributions alongside percentages. Do not use a universal “ten points is noise” rule. Small samples, changing task mix, external model updates, and incomplete telemetry all limit before/after conclusions.

Comparisons across factories require equivalent work and instrumentation. Telemetry provenance is part of the result, not a reason to make cross-source analysis impossible.

## 9. App health: bench gate, nightly soak, and docs/health

App health lives in the **product repo**, owned by the FactoryBench operator. It is not part of FactoryBench, but FactoryBench observes its results through GitHub.

### Bench gate (every PR)

`bench/` is a small benchmark suite for the app. **One run** starts the built app as a fresh process, measures once, stops it, and prints one JSON line:

| Metric | How one run measures it |
|---|---|
| Start time | Launch until the app reports ready |
| Idle CPU and memory | Leave it idle 30 s, average the samples |
| 500-message import: speed | Feed a fixed set of 500 fake messages; messages/second |
| 500-message import: latency | p50 and p95 per message |
| Peak memory | Highest memory during the import |

```json
{"start_ms": 840, "idle_cpu_pct": 0.4, "idle_rss_mb": 205, "import_msgs_per_s": 310, "import_p95_ms": 42, "peak_rss_mb": 388}
```

On every PR, CI:

1. Builds main and the PR.
2. On the same runner, runs the suite 10 times, alternating main, PR, main, PR (5 runs each). Same runner and alternation cancel most runner noise.
3. Takes the median of each metric per side.
4. Fails if a PR median is over budget (`bench/budget.json`) or more than 10% worse than main.
5. On failure, writes feedback the agent can act on: `idle memory 312 MB > budget 250 MB (main: 205 MB)`, plus the top 5 functions by CPU from a profiled PR run.

The factory's own gate ladder can also run the bench against the budget only. The agent then gets fast feedback before the PR exists; CI stays the authority for the main-vs-PR comparison.

### Nightly soak (every night, on main)

A scheduled 2-hour run that catches slow problems like memory leaks:

1. Start the app from main with fake sinks.
2. Send fake traffic at a steady realistic rate for 2 hours, with some bursts and failing sinks so retry paths run.
3. Sample memory, heap (if available), CPU, and outbox length every 30 s.
4. Ignore the first 15 minutes of warm-up, fit a line through the memory samples, and fail if growth is above budget (e.g. 5 MB/h) or the outbox keeps growing.
5. On failure, open an `agent:ready` + `perf` issue with the memory curve and heap snapshots from start and end.

Rates and thresholds are placeholders for the FactoryBench operator to set.

### docs/health/

Two Markdown files on **main**, written only by CI:

| File | Contents | Updated |
|---|---|---|
| `latest.md` | Current value of every metric next to its budget, the last soak result, what's close to budget, slowest functions | Every merge and every night |
| `history.md` | One row per merge or night, so the trend is visible | Every merge and every night |

They exist for three reasons: anyone can read the app's health without a dashboard; `AGENTS.md` tells the agent to read `latest.md` before touching the engine, outbox, or a sink; and the numbers can be trusted because nobody types them in.

They must be on main because factories typically clone only the default branch. A bot (GitHub App or bot token allowed by branch protection) commits them with `[skip ci]`. `docs/health/**` and `bench/**` are protected: in the factory's protected paths and in CODEOWNERS with the FactoryBench operator as owner. If one commit per merge is too noisy, update only nightly.

### Where the bench code lives

`bench/` measures one specific app, so it lives **in the product repo**, not in a shared repo or a submodule. Submodules can break factories: one that clones without `--recurse-submodules`, or whose sandbox skips submodule directories, means the agent and its gates would run without `bench/`.

```text
product-repo/
├── bench/
│   ├── README.md              # what is measured, how to run it locally
│   ├── budget.json            # budgets, direction, runs, regression %, soak settings
│   ├── run.ts                 # entry: one run of all scenarios, prints one JSON line
│   ├── scenarios/
│   │   ├── startup.ts         # start time
│   │   ├── idle.ts            # idle CPU + memory
│   │   └── import-500.ts      # throughput, p50/p95, peak memory
│   ├── soak/
│   │   └── traffic.ts         # fake traffic generator for the 2-hour run
│   └── fixtures/
│       └── messages-500.jsonl # fixed, seeded fake messages (same input every run)
├── docs/health/               # latest.md, history.md, history.jsonl (CI-written)
├── .github/workflows/
│   ├── bench-gate.yml         # on PR: calls the shared gate workflow
│   └── nightly-soak.yml       # on schedule: calls the shared soak workflow
└── .github/CODEOWNERS         # /bench/ and /docs/health/ → FactoryBench operator
```

`budget.json`:

```json
{
  "runs": 5,
  "maxRegressionPct": 10,
  "metrics": {
    "start_ms":          { "max": 1500 },
    "idle_cpu_pct":      { "max": 2 },
    "idle_rss_mb":       { "max": 250 },
    "import_msgs_per_s": { "min": 200 },
    "import_p95_ms":     { "max": 100, "maxRegressionPct": 20 },
    "peak_rss_mb":       { "max": 500 }
  },
  "soak": { "durationMin": 120, "warmupMin": 15, "msgsPerSec": 5, "maxGrowthMbPerHour": 5 }
}
```

`min`/`max` gives each metric its direction, so "worse than main" is unambiguous. Noisy metrics can override the regression threshold.

The generic part (alternate main/PR runs, medians, budget comparison, failure message, profiling, writing `docs/health/`) is the same for every app. Keep it inside the first product repo until a second product repo needs it. Then extract it into a separate repo (e.g. `bench-gate`) that provides **reusable GitHub workflows** plus a small CLI, pinned by tag:

```yaml
# product-repo/.github/workflows/bench-gate.yml
on: pull_request
jobs:
  gate:
    uses: <org>/bench-gate/.github/workflows/gate.yml@v1
    with:
      bench-command: pnpm tsx bench/run.ts
      budget: bench/budget.json
```

The interface between the two is just the bench command's JSON output line and `budget.json`, the same idea as the FactoryBench event contract.

### What happens when the gate fails

The gate comments the feedback on the PR and opens a new issue labelled `agent:ready` + `perf` that names the PR. The factory picks it up like any other issue, and FactoryBench links it as corrective work for the original work item.

The spec's other path, "the agent fixes it in the same PR", needs a factory change: many factories read only PR reviews and comments, not failed CI checks.

## 10. Delivery order

1. Write the event contract (JSON Schema + golden examples).
2. Trace one real work item from attempt start to PR with timing, tokens, cost, and configuration, using one real factory.
3. Add GitHub merge, CI, bench gate, and human-push attribution.
4. Aggregate retries, failures, and multiple work items.
5. Produce the first longitudinal report with configuration markers.
6. Add the 14-day survival updater.
7. Add a web dashboard only if the generated reports are insufficient.
8. Choose the next investment based on observed gaps: sampled quality scoring, app health numbers per configuration, or controlled replays.

### MVP acceptance criteria

- A real factory work item is observed without FactoryBench controlling its execution.
- A second factory could integrate with no FactoryBench code change.
- Failed attempts and retries contribute to work-item cost.
- Missing usage remains unknown and affects reported coverage.
- Human steering, waits, and code pushes are distinguishable.
- PR, CI, merge, and human authorship are correct after GitHub reconciliation.
- Replaying stored raw events produces the same normalized result.
- A configuration change appears on the trend report.
- Open work is not counted as merged or surviving.
- Survival remains pending until its 14-day observation closes.

## 11. Research basis

- [Warp: Measure and improve a factory](https://docs.warp.dev/factories/measure-and-improve/) — production activity, cost, autonomy, cycle time, sampled scorers, and the baseline/change/monitor loop.
- [Warp: Factory benchmarks](https://docs.warp.dev/factories/benchmarks/) — controlled fixed-task comparison as a separate mechanism.
- [OpenTelemetry GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai) — vendor-neutral agent telemetry.
- [DORA software delivery metrics](https://dora.dev/guides/dora-metrics) — longitudinal throughput and instability outcomes.
- [OpenAI evaluation best practices](https://developers.openai.com/api/docs/guides/evaluation-best-practices) — continuous evaluation, production-derived cases, and human calibration.
- [SWE-bench-Live](https://www.microsoft.com/en-us/research/publication/swe-bench-goes-live) — fresh real tasks reduce static-benchmark contamination.
