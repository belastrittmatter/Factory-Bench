# Context creator events and benchmark

A factory's **Context creator** is the step that talks with a person before any code is written: it helps them work out what they want, draws out details, and hands the build step a written change. Any factory can send `context.turn` events for a Context creator attempt; FactoryBench stores them and reports conversation metrics next to everything else it already measures (cost, time, gates, the build attempt that follows). A benchmark that scores a Context creator against a hidden truth sends one `context.score` per run on top. This page covers recording (events and metrics) and benchmarking (`factorybench context run`, which sends those events itself).

FactoryBench stays a logger: the factory or benchmark sends raw facts, and every rate is computed by `report`.

## Events

Both types are new within `factorybench/v1` and optional; a factory that never sends them reports exactly as before.

### `context.turn`

One turn of a Context creator conversation, on the Context creator's attempt.

```json
{
  "spec": "factorybench/v1",
  "id": "ctx-7a1c:turn:3",
  "type": "context.turn",
  "ts": "2026-11-21T10:15:12Z",
  "attempt_id": "ctx-7a1c",
  "payload": {
    "n": 3,
    "speaker": "person",
    "words": 41,
    "dont_know": true,
    "could_not_answer": false,
    "already_said": false,
    "in_attachments": false,
    "suggestion": "none",
    "frustration": 1,
    "confirmed": false,
    "revealed": [],
    "tagged_by": "simulator"
  }
}
```

| Field | Required | Meaning |
|---|---|---|
| `n` | yes | turn number, from 1, in conversation order (the person's opening message is 1) |
| `speaker` | yes | `factory` (the Context creator) or `person` |
| `move` | when `speaker` is `factory` | `capture`, `mirror`, `show`, `suggest`, `expand`, `sharpen`, `challenge`, `narrow`, `ask`, `confirm` |
| `words` | yes | words in the turn's text |
| `dont_know`, `could_not_answer`, `already_said`, `in_attachments`, `confirmed` | no, person turns | the person said they don't know or the factory should decide; could not answer because the question needed knowledge they don't have; was asked for something already said; was asked for something in the files they handed over; confirmed the start |
| `suggestion` | no, person turns | `accepted`, `rejected` or `none`: the response to a `show` or `suggest` turn |
| `frustration` | no, person turns | 0 to 5 |
| `revealed` | no | ids of hidden-truth entries this turn stated for the first time (benchmarks only) |
| `tagged_by` | no | who set the person tags: `person`, `tagger`, `simulator` |
| `text` | no | the turn's text, for evidence; leave it out where privacy matters |

### `context.score`

One per Context creator attempt, sent by a benchmark after scoring against the hidden truth. Counts only; `report` computes the rates. A resend with the same id is a duplicate, so send it once.

```json
{
  "spec": "factorybench/v1",
  "id": "ctx-7a1c:score",
  "type": "context.score",
  "ts": "2026-11-21T10:31:40Z",
  "attempt_id": "ctx-7a1c",
  "payload": {
    "scenario": "chat-picker-filters",
    "subject": "plain",
    "persona": "unsure",
    "truth": {"items": 39, "g4": 14, "latent": 10, "traps": 9, "noise": 5},
    "captured": {"full": 17, "partial": 6, "g4_full": 7, "g4_partial": 2, "latent": 3},
    "latent_by_move": {"show": 2, "suggest": 1},
    "traps": {"raised": 4, "followed": 3},
    "invented": 2, "invented_unmarked": 1,
    "map_tag_errors": 3, "assumed_marked": 5,
    "requirements": 22, "untraced": 2,
    "noise_left": 1, "missing_slots": 2, "contradictions": 0,
    "builders": {"agree": 18, "total": 22},
    "vague_warnings": 4,
    "sim_violations": 0,
    "bench_cost_usd": {"person": 0.91, "judge": 0.33, "builders": 0.52, "build": 0.0},
    "build": {"hidden_pass": 0, "hidden_total": 0, "tasks": 0, "merged": 0, "escalations": 0, "repairs": 0}
  }
}
```

- `traps.raised`: traps the subject put in front of the person in the conversation. `traps.followed`: traps whose resolution the change folder follows (for a resolution of "I don't know", the change marks it open or assumed).
- `build` is absent when the run was not built.
- `bench_cost_usd` is what the benchmark itself spent (the simulated person, the judge, the builders, the build). It is never added to the factory's cost: only the Context creator's own model sessions are `usage` events.

### Gates

The four gates of a change are ordinary `gate` events on the Context creator's attempt: `context-frame` (gate 1, only on a first run), `context-change` (gate 2), `context-testable` (gate 3), `context-agreement` (gate 4, once builders have compared the change). A failed gate carries its first failure as `reason`.

### The attempt

A Context creator run is an attempt with `role: "context"`. FactoryBench never infers its parent, so each Context creator attempt without one starts its own run, even when benchmark runs of one scenario share a work item. When the same factory builds from its output, the build attempt names it as `parent_attempt_id` and is a handoff, so the existing staged-run metrics (stages, escalations, gates, time, cost) cover what happened after the conversation. A benchmark labels its runs `scenario:<id>`, `persona:<id>` and `max-turns:<n>`; `report context` groups by them.

## Metrics

`report items`, `report factories [--by config]` and the dashboard show a **Context** section when any unit has `context.turn` or `context.score` data. All rates show their counts; an empty denominator shows `n/a`.

| Metric | Definition |
|---|---|
| context turns / conversation | factory turns ÷ attempts with turns (a bench run of a scenario always uses the same work item, so per work item would count runs) |
| person words / conversation | words of person turns ÷ those attempts |
| moves | the three most used moves, with counts |
| could not answer | person turns with `could_not_answer` ÷ person turns |
| asked again / asked what was given | person turns with `already_said` / with `in_attachments`, per conversation |
| suggestions accepted | person turns with `suggestion: accepted` ÷ person turns with `accepted` or `rejected` |
| don't-know recovered | person turns with `dont_know` whose next factory turn is `show` or `suggest` and whose next person turn reveals at least one id ÷ person turns with `dont_know` |
| frustration | mean and max over person turns that have it |
| recall | (`captured.full` + 0.5 × `captured.partial`) ÷ `truth.items` |
| weighted recall | the same with G4 entries counted 3 times: (full + 2 × g4_full + 0.5 × (partial + 2 × g4_partial)) ÷ (items + 2 × g4) |
| G4 recall | (g4_full + 0.5 × g4_partial) ÷ `truth.g4` |
| items per turn | (full + 0.5 × partial) ÷ factory turns |
| latent drawn | `captured.latent` ÷ `truth.latent`, and which moves drew them |
| traps raised / followed | `traps.raised` ÷ `truth.traps`, `traps.followed` ÷ `truth.traps` |
| invented | invented requirements per run, and how many were not marked assumed |
| map tag errors, noise left, missing slots, contradictions | per run |
| traceability | (requirements − untraced) ÷ requirements |
| builder agreement | `builders.agree` ÷ `builders.total` |
| hidden tests | `build.hidden_pass` ÷ `build.hidden_total`, with escalations and repairs per built run |
| simulator violations | per run; a run with any is marked |
| bench cost | sum of `bench_cost_usd`, shown apart from the factory's cost |

`factorybench report context [--db fb.db] [--factory f] [--label L ...]` prints what the scorecard can't:

- **effort curve**: runs grouped by `max-turns` label, with person words, recall, builder agreement and hidden tests;
- **moves**: per move, how often it was used, the ids revealed by the person turn that followed, latent entries drawn, and suggestion acceptance;
- **simulated against real**: per scenario, runs whose person turns are `tagged_by: simulator` beside runs tagged by `person` or `tagger`.

## Benchmarking

`factorybench context run` benchmarks a Context creator against a scenario with a hidden truth. A simulated person plays the truth, the Context creator under test talks to it and hands over a change folder, and the bench scores that folder and reports the run to FactoryBench as an attempt with the events above.

```text
factorybench context run --scenarios DIR --scenario ID --subject none|plain|ceiling|file:PATH --person PERSONA [flags]
```

Flags: `--max-turns N`, `--builders K` (default 3), `--subject-model`, `--person-model`, `--judge-model`, `--builder-model`, `--max-cost USD` (per model session), `--out DIR` (local evidence, default `context-runs`), `--no-text` (leave the turns' text out of the events) and `--yes`. Events go to `FACTORYBENCH_URL` with `FACTORYBENCH_TOKEN` through the same path as `factorybench emit` (spooled when the server is down, never blocking). Without `FACTORYBENCH_URL` the run still writes its local evidence.

Model calls go through the `claude` CLI (`--output-format json`, `--json-schema`, `--system-prompt`, `--tools ""`, `--max-budget-usd`); `FACTORYBENCH_CLAUDE` names the program. Every session starts in an empty temporary directory, so no project settings, hooks or `CLAUDE.md` reach it. Without `--yes` the command prints its plan (which sessions it would start, on which models, and the most they could cost at the cap) and exits 0: nothing is called, written or sent. The person and the subject must run on different models, and the run is refused up front when they may be the same one. A `file:` subject whose prompt is too long for a command-line argument is refused up front too.

Local evidence per run, under `--out`: `run.json` (how it started and ended), `transcript.jsonl`, `change/` (the change folder), `gates.json`, `judge.json` and `builders.json` (the judge's and the builders' answers) and `events.jsonl` (what was sent). FactoryBench's database holds the facts; the files hold the detail behind them.

### Scenarios

Scenarios live with the product they describe, not in FactoryBench; `--scenarios` points at them and `--scenario` is an id under it (or a path to one scenario directory). One directory per scenario:

| File | Required | Who sees it |
|---|---|---|
| `scenario.json` | yes | the bench |
| `brief.md` | yes | subject and person: the person's first message |
| `truth.json` | yes | person and judge only |
| `attachments/` | no | subject and person |
| `project.md` | no | subject: the settled project frame when it is not a first run |
| `living-specs/<capability>/spec.md` | no | subject and builders |
| `ceiling/` | no | the ceiling baseline's change folder |

`scenario.json`:

```json
{
  "id": "chat-picker-filters",
  "kind": "feature",
  "title": "Chat picker and filters",
  "work_item": {"repo": "bench/chatbridge-context", "issue": 2},
  "first_run": true,
  "max_turns": 30
}
```

`work_item` is how FactoryBench groups runs of the same scenario; it never has to be a real GitHub issue (run `serve` without `--poll` for these repos). `first_run` says whether the subject has to write `project.md` and whether gate 1 applies. `max_turns` is the cap on the Context creator's turns unless `--max-turns` says otherwise.

`truth.json` has `items` (`id`, `area`, `text`, `g4`, `latent`, `latent_hint`, `technical`, `source`), `traps` (`id`, `kind`: `contradiction`, `person-only` or `vague`, `items`, `text`, `resolution`) and `noise` (`id`, `text`). An item's `area` is one of `actors`, `journeys`, `data`, `rules`, `screens`, `failures`, `limits`. Ids are unique across all three lists, and a latent item needs a `latent_hint`.

A scenario the run cannot use is refused with the reason before any model is called: unknown fields in `scenario.json` (a `build` section, used by a build command outside FactoryBench, is accepted and ignored) or `truth.json`, an `id` that is empty or not made of letters, digits, `.`, `_` and `-`, a missing or invalid `work_item`, an empty brief, no items, an empty or repeated id, an unknown area, a latent item without a hint.

### Subjects

| Subject | What it is |
|---|---|
| `none` | no conversation; the change folder is the brief and attachments |
| `plain` | a plain question-asking agent |
| `ceiling` | no conversation; the change folder is `ceiling/`, or one rendered from the truth |
| `file:<path>` | a Context creator whose system prompt is the file |

`--person` is needed for `plain` and `file:` subjects, which talk. The subject gets one session per turn; on its last allowed turn it must finish with the change folder. A reply that breaks its protocol gets one repair (the same session again with the problem named) before the run fails.

### Personas and person rules

Four personas: `decided-technical`, `decided-nontechnical`, `unsure`, `talkative`. The persona sets only the person's manner. Every persona follows six rules:

1. answers only from the truth, otherwise "I don't know" or "you decide";
2. states a latent entry only after a `show` or `suggest` turn that touches its `latent_hint`;
3. states a vague trap's entry vaguely until the subject sharpens it;
4. if non-technical, can't answer technical questions;
5. answers a trap with its `resolution` once it is raised;
6. never reveals that it has a truth file.

The bench checks the person's tags after every reply: an id that is not in the truth makes the run invalid, ids already revealed are dropped, and a latent entry revealed after any other move counts as a simulator violation (`sim_violations` in `context.score`; the reveal still counts).

### Change folder

`proposal.md` (`## Why`, `## What Changes`, `## Capabilities`, `## Impact`), `specs/<capability>/spec.md` (spec deltas: `## ADDED|MODIFIED|REMOVED Requirements`, `### Requirement:` with SHALL or MUST, `**Scope fence:**`, `**Failure:**` and `**Visual:**` where needed, `#### Scenario:` with `**WHEN**` and `**THEN**`; REMOVED adds `**Reason:**`, `**Migration:**`, `**Effect on existing data:**`), `map.md` (sections Actors, Journeys, Data, Rules, Screens, Failures, Limits; bullets tagged `[said]`, `[accepted]`, `[assumed]`, `[open]`), `decisions.md`, and `project.md` on a first run (Goal, Users, Platforms, Integrations, Data and privacy, Acceptance, Hard limits).

### Scoring

1. Gates 1 to 3 by code. Gate 4 from K builder sessions that see only the change folder and the living specs, and a comparison session that marks each requirement `agree` or `disagree`; the gate passes when none disagree.
2. A judge session with the truth, the change folder and the transcript returns the counts in `context.score`.

### Events the run sends

- `attempt.started` with `role: "context"`, the scenario's `work_item`, a `factory_config_version` (the subject's name; for `plain` and `file:` subjects also a hash of its prompt and the subject model, so a change to either shows as a new configuration) and the labels `scenario:<id>`, `persona:<id>` and `max-turns:<n>` (the last two only for subjects that talk);
- `context.turn` for every turn of the conversation, with the person's tags marked `tagged_by: simulator`;
- `usage` for each of the subject's own sessions. The person, judge and builder sessions are the bench's spending: they are counted in `bench_cost_usd` and never sent as `usage`;
- `gate` for `context-frame` (first runs only), `context-change`, `context-testable` and `context-agreement`;
- `context.score`;
- `attempt.finished`, `completed`, `failed` or `cancelled`. A run fails when a model call fails or a protocol is broken after its one repair, and is cancelled when interrupted (Ctrl-C); either way its turns and usage are still sent, but it has no gates and no score.

Then `factorybench report context` shows the runs.
