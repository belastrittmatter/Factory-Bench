# Integrating a factory with FactoryBench

FactoryBench compares AI coding factories on the same GitHub issues. A factory only **reports raw facts**: an attempt started, which PR it opened, how it ended, and (for agents other than Claude Code) the tokens it used. FactoryBench prices tokens, reads PRs, CI and merges from GitHub, and computes every metric.

Reporting must never block, slow down or fail the factory. Never put prompts, code, diffs or secrets in events. The full contract is in [spec/events.schema.json](spec/events.schema.json), with one example per type in [spec/examples/](spec/examples/).

## 1. Setup

1. **Get a token from the FactoryBench operator.** They run this once, and the token is shown only once:
   ```bash
   factorybench factory add <factory-id> --commit-email <email the factory commits as> --github-login <login the factory acts as>
   ```
   The commit email and login are how FactoryBench tells factory work from human work, so get them right.
2. **Set two environment variables** wherever the factory runs (local shell, CI secrets):
   ```bash
   FACTORYBENCH_URL=https://factorybench.internal   # no trailing /v1/events
   FACTORYBENCH_TOKEN=<token>
   ```
   If `FACTORYBENCH_URL` is unset, `factorybench emit` does nothing.
3. **Install the binary** (Go 1.27+), from the FactoryBench repo: `go install ./cmd/factorybench`.

The operator runs `factorybench serve --poll 3m` with a GitHub token that can read the factory's repos. Without polling, PR outcome, reviews and CI stay unknown.

## 2. Claude Code factories

An **attempt** is one run of the factory on one issue. A retry is a new attempt. Wrap each attempt like this:

```bash
A=cc-$(uuidgen)   # attempt id: unique, generated once per attempt

factorybench emit attempt.started --attempt "$A" --key started \
  --json '{"work_item":{"repo":"acme/app","issue":412},"factory_config_version":"myfactory@1.2.0+claude-code@2.1.322"}'

CLAUDE_CODE_ENABLE_TELEMETRY=1 \
OTEL_LOGS_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json \
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="$FACTORYBENCH_URL/v1/logs" \
OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $FACTORYBENCH_TOKEN" \
OTEL_RESOURCE_ATTRIBUTES="factorybench.attempt=$A" \
  claude -p "$prompt"

factorybench emit pr.linked        --attempt "$A" --key pr       --json '{"pr_url":"https://github.com/acme/app/pull/57"}'
factorybench emit attempt.finished --attempt "$A" --key finished --json '{"status":"completed"}'   # or failed / cancelled, with a "reason"
```

- **What Claude Code sends:** with these variables, Claude Code (≥ 2.1.310) sends its own telemetry while it runs. That covers tokens and cost per model call, every tool call, subagents, and API errors. The factory sends **no `usage` or `activity`** for Claude.
- **Where the variables go:** they must be set in the `claude` process environment. Claude Code ignores them in a repo's `.claude/settings.json`.
- **Content flags:** leave `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS` and `OTEL_LOG_RAW_API_BODIES` unset.
- **`factory_config_version`:** change it whenever anything that could change results changes (factory version, Claude Code version, model, config).
- **Send `attempt.finished` from a `finally`/`trap`,** so crashed and cancelled attempts end too.
- **Personal data:** Claude Code includes the user's email and account ids. FactoryBench drops them on arrival.
- **Reliability:** telemetry is not spooled.
  - **FactoryBench down:** each batch is retried for ~8 s and then dropped. Claude keeps running, and the gap shows as missing coverage.
  - **SIGKILL:** loses the last ~5 s of telemetry. SIGTERM flushes the last batch; only a model call still in flight is lost.

A working example: [scripts/claude-code-run.sh](scripts/claude-code-run.sh).

## 3. Other factories (agy, custom agents)

Only Claude Code reports through telemetry so far. Other agents send the same lifecycle events as above, plus one `usage` event per model after every agent session, including failed ones:

```bash
factorybench emit usage --attempt "$A" --key usage:1 \
  --json '{"model":"gemini-3-pro","input":20000,"output":3000,"cache_read":40000,"duration_ms":151000}'
```

**Usage rules:**
- **Deltas, not totals.** Each event covers only the calls since the last one. `agy` totals are cumulative across turns, so subtract what you already sent.
- **Token classes don't overlap.** `input` is non-cached input only, and `cache_read` / `cache_write` are separate. If your tool counts cached tokens inside `input`, subtract them.
- **Unknown is missing, not zero.** If usage is unknown, send no `usage` event.
- **`model`** exactly as the provider names it, because FactoryBench prices by exact name.
- **`reported_cost_usd`** only if the provider reported a cost.

**Other events:**
- `gate`: a factory's own build/test checks.
- `human.waiting` / `human.resumed` / `human.steering`: human involvement.
- `activity`: one tool call each, with `tool` and optional `failed`, `duration_ms` and `agent_id`.

Every event's id is `<attempt>:<key>`. Keys must be **fixed per call site** (`started`, `usage:1`, `pr`, `finished`), never random, so retries are harmless duplicates.

**Retries:** `factorybench emit` always exits 0. A failed send goes to a spool file (`~/.factorybench/spool.jsonl`). Run `factorybench emit --flush` at factory start to resend it.

**Without the binary:** `POST $FACTORYBENCH_URL/v1/events` with `Authorization: Bearer $FACTORYBENCH_TOKEN` and a JSON **array** of events (see [spec/examples/](spec/examples/)). A full example sequence is in [scripts/fake-factory.sh](scripts/fake-factory.sh).

## 4. Verify

Run one real attempt, or a throwaway one on a repo that doesn't exist so it doesn't skew real metrics:

```bash
A=fbtest-$(date -u +%Y%m%d%H%M%S)
factorybench emit attempt.started  --attempt $A --key started  --json '{"work_item":{"repo":"example/fb-smoke-test","issue":1},"factory_config_version":"smoke"}'
factorybench emit attempt.finished --attempt $A --key finished --json '{"status":"completed"}'
```

No stderr output means the events were accepted. The operator can then check on the FactoryBench host:

```bash
factorybench report live                          # attempts running now, with their last tool call
factorybench report item example/fb-smoke-test#1  # one issue: attempts, sessions, subagents, cost
factorybench report factories                     # scorecard
```

## 5. Troubleshooting

| Symptom | Fix |
|---|---|
| `401 invalid token` | Wrong or missing `FACTORYBENCH_TOKEN`. Ask the operator; there is no way to show it again. |
| `rejected: ...` on stderr | The payload breaks the contract; the message says which field. Fix it and re-send with **the same key**. |
| Spool keeps growing | Sends are failing (stderr says why). Fix that, then `factorybench emit --flush`. |
| Nothing arrives, no errors | `FACTORYBENCH_URL` is unset in the factory's environment. |
| Claude attempt has no sessions or tool calls | The OTel variables aren't in the `claude` process environment, Claude Code is older than 2.1.310, `factorybench.attempt` doesn't match the attempt id, or FactoryBench was unreachable during the run. |
| `+?` next to tokens or cost | A lower bound: some session sent no usage, or its model has no price yet (ask the operator). |
| Outcome stays `open`, no PR or CI data | FactoryBench isn't polling GitHub, can't read the repo, or `pr.linked` was never sent. |
