# Context creator events

A factory's **Context creator** is the step that talks with a person before any code is written: it helps them work out what they want, draws out details, and hands the build step a written change. Any factory can send `context.turn` events for a Context creator attempt; FactoryBench stores them and reports conversation metrics next to everything else it already measures (cost, time, gates, the build attempt that follows). A benchmark that scores a Context creator against a hidden truth sends one `context.score` per run on top.

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

