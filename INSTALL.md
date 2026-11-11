# Integrating a factory with FactoryBench

You are an agent working in a factory's own repo and were asked to "integrate FactoryBench". This file is all you need; you don't have to read FactoryBench's Go code.

FactoryBench compares AI coding factories on the same GitHub issues. The factory **reports raw facts** (an attempt started, an agent session used N tokens, a PR was linked, the attempt ended) as JSON events over HTTP. FactoryBench prices tokens, reads PRs/CI/merges from GitHub itself, and computes every metric.

**Do:** add a small reporting layer (an emitter plus ~5 call sites). Nothing in how the factory works changes.
**Don't:**
- compute metrics, cost estimates or outcomes in the factory;
- let reporting block, slow down or fail the factory (never throw, never exit non-zero, spool on failure);
- put prompts, code, diffs, transcripts or secrets in events (`activity.summary` is a short label like `pnpm test`);
- change gates, budgets, ledgers or the PR flow.

Contract: [spec/events.schema.json](spec/events.schema.json) (validate your payloads against it), one example per type in [spec/examples/](spec/examples/), rules in [implementation-plan.md §4](implementation-plan.md#4-the-event-contract).

## 1. Ask the human for

1. `FACTORYBENCH_URL`, e.g. `https://factorybench.internal` (no trailing `/v1/events`).
2. `FACTORYBENCH_TOKEN`. The FactoryBench operator creates it once and it is shown only once:
   `factorybench factory add <factory-id> --commit-email <email the factory commits as> --github-login <GitHub login the factory acts as>`
   (e.g. `--commit-email factory-bot@users.noreply.github.com`). Commit email and login are how FactoryBench tells factory work from human work, so get them right.
3. That the factory's product repo is polled: the operator runs `factorybench serve --poll 3m --repo <owner/name> ...`. Without it tokens still show up, but PR outcome, reviews and CI stay unknown.

Put both env vars wherever the factory reads its environment (local shell and CI secrets). If `FACTORYBENCH_URL` is unset, the emitter must do nothing.

## 2. Decide three mappings

| Mapping | Rule | Examples |
|---|---|---|
| **Attempt** | One run of the factory on one issue. Agent sessions inside it are *not* attempts; they are `usage` events. Retries/follow-up runs are new attempts. | Loop factory: one loop run (stage `plan`, `build` or `full`). Claude Code factory: one `claude -p` job for an issue. |
| **attempt_id** | Generated once at attempt start, unique within the factory, passed to every later call site. | `af-<uuid>`, `cc-20261111T091400-4242` |
| **factory_config_version** | Changes whenever anything that could change results changes: factory version + agent CLI version + hash of the resolved config. | `myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c`, `claude-code@2.1.322+haiku` |

Event `id` = `<attempt_id>:<key>` where key is fixed per call site (`started`, `usage:1`, `usage:2`, `pr`, `gate:1`, `finished`). **Ids must be stable across retries**: never put a timestamp or random value in the key. Re-sending the same id is a harmless duplicate.

## 3. Send events

### (a) The `factorybench` binary (shell factories, hook scripts)

Build it from the FactoryBench repo (Go 1.27+): `go build -o factorybench ./cmd/factorybench` (or `go install ./cmd/factorybench`).

```
factorybench emit <type> --attempt <attempt_id> --key <key> --json '<payload>' [--ts <rfc3339>]
factorybench emit --flush
```

It reads `FACTORYBENCH_URL` / `FACTORYBENCH_TOKEN`, sends one event with id `<attempt>:<key>` and `ts` = now unless `--ts` is given. It **always exits 0**: if sending fails (network, 401, 5xx) it appends the event to `FACTORYBENCH_SPOOL` (default `~/.factorybench/spool.jsonl`); `emit --flush` resends the spool (run it at factory start or from cron). Rejections are printed to stderr. Every event type:

```bash
A=af-7f3e2c1a   # your attempt_id
factorybench emit attempt.started  --attempt $A --key started  --json '{"work_item":{"repo":"acme/chat-bridge","issue":412},"factory_config_version":"myfactory@0.1.0+agy@1.5.4+cfg:3e1a9c","role":"build","labels":["agent:ready","bug"],"base_commit":"9593822"}'
factorybench emit human.resumed    --attempt $A --key resumed  --json '{"reason":"plan_approved"}'
factorybench emit activity         --attempt $A --key act:1    --json '{"tool":"run_command","summary":"pnpm test"}'
factorybench emit usage            --attempt $A --key usage:1  --json '{"model":"gemini-3-pro","input":20000,"output":3000,"cache_read":40000,"reasoning":900,"duration_ms":151000,"phase":"implement"}'
factorybench emit gate             --attempt $A --key gate:1   --json '{"name":"test","passed":false,"reason":"3 failing tests in pkg/parser"}'
factorybench emit human.steering   --attempt $A --key steer:1  --json '{"source":"comment"}'
factorybench emit pr.linked        --attempt $A --key pr       --json '{"pr_url":"https://github.com/acme/chat-bridge/pull/57","branch":"agent/issue-412"}'
factorybench emit human.waiting    --attempt $A --key waiting  --json '{"reason":"plan_review"}'
factorybench emit attempt.finished --attempt $A --key finished --json '{"status":"failed","reason":"gate test failed after 3 attempts"}'
```

A full plan → build → retry sequence is in [scripts/fake-factory.sh](scripts/fake-factory.sh).

### (b) Plain HTTP (any language)

`POST $FACTORYBENCH_URL/v1/events`, header `Authorization: Bearer $FACTORYBENCH_TOKEN`, body = JSON **array** of events (one or a batch, < 10 MB):

```bash
curl -sS -X POST "$FACTORYBENCH_URL/v1/events" \
  -H "Authorization: Bearer $FACTORYBENCH_TOKEN" -H "Content-Type: application/json" \
  -d '[{"spec":"factorybench/v1","id":"af-7f3e2c1a:usage:2","type":"usage","ts":"2026-11-11T09:20:00Z","attempt_id":"af-7f3e2c1a","payload":{"model":"claude-sonnet-5-5","input":1200,"output":800,"cache_read":45000,"cache_write":6000,"reported_cost_usd":0.0412,"phase":"review"}}]'
# -> {"accepted":1,"duplicates":0,"rejected":[]}
```

Minimal TypeScript emitter (Node 18+, no dependencies). It never throws, spools to the same file format as `factorybench emit` (so `factorybench emit --flush` can also drain it), and keeps ids stable because the id is built from attempt + key:

```ts
// factorybench.ts: reporting layer. Never throws; on failure events go to a spool file and are resent by flushSpool().
import { appendFileSync, existsSync, mkdirSync, readFileSync, renameSync, unlinkSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';

const URL = process.env.FACTORYBENCH_URL, TOKEN = process.env.FACTORYBENCH_TOKEN;
const SPOOL = process.env.FACTORYBENCH_SPOOL || join(homedir(), '.factorybench', 'spool.jsonl');

async function post(events: object[]): Promise<boolean> {
  try {
    const res = await fetch(`${URL!.replace(/\/$/, '')}/v1/events`, {
      method: 'POST', body: JSON.stringify(events), signal: AbortSignal.timeout(10_000),
      headers: { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' },
    });
    const body = await res.text();
    if (!res.ok) { console.error(`factorybench: ${res.status} ${body.trim()}`); return false; }
    if (JSON.parse(body).rejected?.length) console.error(`factorybench: rejected (fix and re-send same id): ${body}`);
    return true; // rejected events are stored server-side; spooling them would not help
  } catch (e) { console.error(`factorybench: ${e}`); return false; }
}

function spool(events: object[]): void {
  try {
    mkdirSync(dirname(SPOOL), { recursive: true });
    appendFileSync(SPOOL, events.map((e) => JSON.stringify(e) + '\n').join(''));
  } catch (e) { console.error(`factorybench: cannot spool, event lost: ${e}`); }
}

/** Await it where the process may exit right after (attempt.finished); otherwise fire and forget. Never rejects. */
export async function emit(type: string, attemptId: string, key: string, payload: object, ts = new Date()): Promise<void> {
  if (!URL) return; // not configured: do nothing
  const ev = { spec: 'factorybench/v1', id: `${attemptId}:${key}`, type, ts: ts.toISOString(), attempt_id: attemptId, payload };
  if (!(await post([ev]))) spool([ev]);
}

/** Call once at factory start. Resends spooled events in batches of 500. */
export async function flushSpool(): Promise<void> {
  if (!URL) return;
  const sending = SPOOL + '.sending';
  try {
    if (!existsSync(sending)) renameSync(SPOOL, sending); // throws when nothing is spooled
    const evs = readFileSync(sending, 'utf8').split('\n').flatMap((l) => { try { return l ? [JSON.parse(l)] : []; } catch { return []; } });
    for (let i = 0; i < evs.length; i += 500)
      if (!(await post(evs.slice(i, i + 500)))) { spool(evs.slice(i)); break; }
    unlinkSync(sending);
  } catch { /* nothing spooled */ }
}
```

## 4. Call sites

| When | Event | Payload |
|---|---|---|
| Attempt starts (issue picked up) | `attempt.started` | `work_item {repo, issue}`, `factory_config_version`; optional `role`, `labels`, `base_commit`, `parent_attempt_id` (omit if unknown: FactoryBench links to the latest earlier attempt on the same issue) |
| After **every** agent session, including failed/crashed/cancelled ones | `usage` (one per model) | `model`, `input`, `output`; optional `cache_read`, `cache_write`, `reasoning`, `duration_ms`, `phase` (`implement`/`verify`/`review`), `reported_cost_usd` |
| Factory-internal gate result (build/lint/test ladder) | `gate` | `name`, `passed`, optional `reason` |
| PR created or reused | `pr.linked` | `pr_url`, optional `branch` |
| Factory stops to wait for a human / continues after | `human.waiting` / `human.resumed` | optional `reason` (e.g. `plan_review`, `plan_approved`) |
| Human redirected the factory (comment, chat, label) | `human.steering` | optional `source`: `comment`, `chat`, `label` |
| Attempt ends, in a `finally` | `attempt.finished` | `status`: `completed`/`failed`/`cancelled`, optional `reason` |
| Optional, live view only | `activity` | `tool`, `summary` |

**Factory with its own orchestrator code**: `attempt.started`/`pr.linked`/`human.*`/`attempt.finished` in the function that runs one issue (`attempt.finished` in a `finally`); `usage` from a wrapper around each agent-CLI call (**not** a running-total callback); one `gate` per build/lint/test result; `factory_config_version` = factory version + agent CLI version + hash of the resolved config.

**Claude Code based factory**: run `claude -p ... --output-format stream-json --verbose`, keep the stream, and turn its final `result` line into usage events. [scripts/claude-code-run.sh](scripts/claude-code-run.sh) is a working example; `factorybench claude-translate <stream.jsonl>` prints `{"usage":[...],"finished":{...}}` ready to emit. Hooks carry no usage, so don't build on them.

## 5. Token rules (check every `usage` payload)

- [ ] **Deltas, not totals.** One event covers only the calls since the previous one. If your tool reports cumulative numbers, subtract the last reported value.
- [ ] **No overlap.** `input` = non-cached input only; `cache_read`, `cache_write` separate; `reasoning` is a subset of `output` (don't add it to `output`).
- [ ] **Unknown is missing, not zero.** No usage available → send no `usage` event (and omit unknown optional fields). The attempt then shows as `+?`.
- [ ] **Every session counts**, also failed/crashed/cancelled ones.
- [ ] **`reported_cost_usd` only if the provider reported it.** Never compute it yourself.
- [ ] **`model`** exactly as the provider names it (FactoryBench prices by exact name).
- [ ] **`phase`** if you know it: `implement`, `verify` (tests, gate fixes), `review`.

**agy.** If your factory's usage type counts cached tokens inside `input`, subtract them. Session reports `{input: 60000, cachedInput: 40000, output: 3000}` → send `{"model":"gemini-3-pro","input":20000,"cache_read":40000,"output":3000}`. `agy` totals are cumulative across turns of one session: if you emit per turn and turn 1 reports input 20000, turn 2 reports 50000, send 20000 then 30000. No cost field → omit `reported_cost_usd`.

**Claude Code.** One `usage` event per entry of `modelUsage` in the `result` line (`inputTokens` is already non-cached):

```
"modelUsage":{"claude-sonnet-5-5":{"inputTokens":1200,"outputTokens":800,"cacheReadInputTokens":45000,"cacheCreationInputTokens":6000,"costUSD":0.0412},
              "claude-haiku-5-5":{"inputTokens":300,"outputTokens":90,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.00075}}, "duration_ms":95000
→ usage:1 {"model":"claude-haiku-5-5","input":300,"output":90,"cache_read":0,"cache_write":0,"reported_cost_usd":0.00075,"duration_ms":95000}
→ usage:2 {"model":"claude-sonnet-5-5","input":1200,"output":800,"cache_read":45000,"cache_write":6000,"reported_cost_usd":0.0412}
```

`thinkingTokens` → `reasoning`, `costUSD` → `reported_cost_usd`; the run's `duration_ms` goes on the first event only. `is_error: true` → `attempt.finished {"status":"failed","reason":<subtype>}`; no `result` line at all → no usage, `failed`.

## 6. Verify

Send one throwaway attempt on a dummy issue (it stays in FactoryBench; use a repo name nobody polls so it doesn't skew real metrics):

```bash
A=fbtest-$(date -u +%Y%m%d%H%M%S)
factorybench emit attempt.started  --attempt $A --key started  --json '{"work_item":{"repo":"example/fb-smoke-test","issue":1},"factory_config_version":"smoke"}'
factorybench emit usage            --attempt $A --key usage:1  --json '{"model":"claude-sonnet-5-5","input":100,"output":50}'
factorybench emit attempt.finished --attempt $A --key finished --json '{"status":"completed"}'
```

No stderr output means delivered and accepted. Then run your own integration once (or its tests) and ask the human to run, on the FactoryBench host (reports read the database, default `fb.db`, `--db` to change):

```bash
factorybench report item example/fb-smoke-test#1   # attempts, sessions, tokens, cost of one issue
factorybench report items --factory <factory-id>   # one row per issue
factorybench report live                           # attempts running now
factorybench report factories                      # scorecard (add --by config to split by factory_config_version)
```

Response to every POST: `{"accepted":n,"duplicates":n,"rejected":[{"id":"...","error":"..."}]}`.
- `accepted`: new events stored.
- `duplicates`: an id that was already stored (a retry); nothing changed. A changed payload under an already-accepted id is ignored, so use a new key for a new fact.
- `rejected`: contract violation; the event is still stored (it counts against telemetry coverage). Fix the payload and re-send **the same id**: it replaces the rejected row.

## 7. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `401 invalid token` | Wrong or missing `FACTORYBENCH_TOKEN`, or a token from another FactoryBench database. Ask the operator; the token is shown only once at `factory add` and there is no rotate command. |
| `400 body must be a JSON array of events` | You posted an object; wrap it in `[...]`. |
| `rejected` `payload.input missing` / `payload.model missing` / `payload.output missing` | Usage needs all three. No usage known → send no event. |
| `rejected` `payload.phase must be implement, verify, or review` | Omit `phase` or use one of those. |
| `rejected` `payload.work_item.repo must be owner/name` / `issue must be a positive integer` | `issue` is a JSON number, repo has no URL prefix. |
| `rejected` `payload.status must be completed, failed, or cancelled`, `ts must be RFC 3339`, `spec must be "factorybench/v1"`, `unknown type`, `payload.<x> must be >= 0` | As stated. Validate against [spec/events.schema.json](spec/events.schema.json). |
| Spool keeps growing | Sends are failing (stderr says why: unreachable URL, 401, 5xx). Fix that, then `factorybench emit --flush` (or `flushSpool()`). A leftover `spool.jsonl.sending` is resent first. `emit --flush` with `FACTORYBENCH_URL` unset does nothing. |
| Nothing arrives, no errors | `FACTORYBENCH_URL` is unset in the factory's environment (the emitter silently does nothing), or the process exited before an un-awaited `emit()` finished. |
| `+?` in a report | Lower bound: some attempt sent no `usage`, or a session's `model` (or a token class it used) has no price. Check that every session emits usage; ask the operator to add a price for the model. `?` = nothing known at all. |
| Outcome stays `open`, no PR/CI data | The repo is not polled (`serve --poll ... --repo`), or `pr.linked` was not sent. |
