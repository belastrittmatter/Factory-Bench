# Integrating a factory with FactoryBench

A factory reports raw facts: an attempt started, which PR it opened, how it ended, and (for agents other than Claude Code) the tokens it used. FactoryBench does the rest. Reporting must never block, slow down or fail the factory. Never put prompts, code, diffs or secrets in events. Fields are in [spec/events.schema.json](spec/events.schema.json), one example per type in [spec/examples/](spec/examples/).

## 1. Setup

1. **Get a token from the FactoryBench operator.** It is shown only once:
   ```bash
   factorybench factory add <factory-id> --commit-email <email the factory commits as> --github-login <login the factory acts as>
   ```
   The commit email and login tell factory work from human work, so get them right.
2. **Set two environment variables** wherever the factory runs:
   ```bash
   FACTORYBENCH_URL=https://factorybench.internal   # no trailing /v1/events
   FACTORYBENCH_TOKEN=<token>
   ```
   If `FACTORYBENCH_URL` is unset, `factorybench emit` does nothing.
3. **Install the binary** (Go 1.27+) from the FactoryBench repo: `go install ./cmd/factorybench`.

The operator runs `factorybench serve --poll 3m` with a GitHub token (`GITHUB_TOKEN` or `gh auth token`) that can read the factory's repos. Without polling, PR outcome, reviews and CI stay unknown.

## 2. Claude Code factories

An **attempt** is one run of the factory on one issue; a retry is a new attempt. Wrap each attempt like this:

```bash
A=cc-$(uuidgen)   # attempt id, generated once per attempt

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

- Claude Code (≥ 2.1.310) then sends its tokens, cost, tool calls, subagents and API errors itself. Send **no `usage` or `activity`** for Claude.
- Optional: tag each `claude` call with its phase, `OTEL_RESOURCE_ATTRIBUTES="factorybench.attempt=$A,factorybench.phase=verify"` (`implement`, `verify` or `review`). Untagged calls are not counted in "verification tokens / PR" (n/a if none are tagged).
- Set the variables in the `claude` process environment; Claude Code ignores them in `.claude/settings.json`. Keep `OTEL_EXPORTER_OTLP_COMPRESSION` unset (only uncompressed JSON is accepted).
- Leave `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS` and `OTEL_LOG_RAW_API_BODIES` unset. FactoryBench drops the user's email and account ids on arrival.
- Change `factory_config_version` whenever anything that could change results changes (factory, Claude Code version, model, config).
- Send `attempt.finished` from a `finally`/`trap`, so crashed and cancelled attempts end too.
- Telemetry is not spooled. If FactoryBench is down, each batch is retried for ~8 s, then dropped (it shows as missing coverage). SIGKILL loses the last ~5 s; SIGTERM flushes, losing only a model call still in flight.

Working example: [scripts/claude-code-run.sh](scripts/claude-code-run.sh).

## 3. Other factories (agy, custom agents)

Send the same lifecycle events, plus one `usage` event per model after every agent session, failed ones included:

```bash
factorybench emit usage --attempt "$A" --key usage:1 \
  --json '{"model":"gemini-3.1-pro-preview","input":20000,"output":3000,"cache_read":40000,"duration_ms":151000}'
```

- **Deltas, not totals.** `agy` totals are cumulative across turns: subtract what you already sent.
- **Token classes don't overlap.** `input` is non-cached input; `cache_read` / `cache_write` are separate.
- **Unknown is missing, not zero.** No usage known, no `usage` event.
- **`model`** exactly as the provider names it (prices match by exact name). **`reported_cost_usd`** only if the provider reported one.
- Optional: `gate` (the factory's own checks), `human.waiting` / `human.resumed` / `human.steering`, `activity` (one tool call each).

Event ids are `<attempt>:<key>`. Keys must be **fixed per call site** (`started`, `usage:1`, `pr`, `finished`), never random, so retries are harmless duplicates.

`factorybench emit` always exits 0; a failed send goes to `~/.factorybench/spool.jsonl` (or `$FACTORYBENCH_SPOOL`). Run `factorybench emit --flush` at factory start to resend it. Without the binary, `POST $FACTORYBENCH_URL/v1/events` with `Authorization: Bearer $FACTORYBENCH_TOKEN` and a JSON **array** of events; full sequence in [scripts/fake-factory.sh](scripts/fake-factory.sh).

## 4. Verify

Run one real attempt, or a throwaway one on a repo that doesn't exist:

```bash
A=fbtest-$(date -u +%Y%m%d%H%M%S)
factorybench emit attempt.started  --attempt $A --key started  --json '{"work_item":{"repo":"example/fb-smoke-test","issue":1},"factory_config_version":"smoke"}'
factorybench emit attempt.finished --attempt $A --key finished --json '{"status":"completed"}'
```

No stderr output means both were accepted. On the FactoryBench host: `factorybench report live`, `report item example/fb-smoke-test#1`, `report factories`.

## 5. Troubleshooting

| Symptom | Fix |
|---|---|
| `401 Unauthorized: invalid token` | Wrong, missing or revoked `FACTORYBENCH_TOKEN`. Ask the operator; it can't be shown again, but the operator can issue a new one on the dashboard's Factories page, which stops the old one working. |
| `rejected by FactoryBench` on stderr | The payload breaks the contract; the message names the field. Fix it and re-send with **the same key**. |
| Spool keeps growing | Sends fail (stderr says why). Fix that, then `factorybench emit --flush`. |
| Nothing arrives, no errors | `FACTORYBENCH_URL` is unset in the factory's environment. |
| Claude attempt has no sessions or tool calls | OTel variables not in the `claude` environment, Claude Code older than 2.1.310, `factorybench.attempt` ≠ attempt id, or FactoryBench unreachable during the run. |
| `+?` next to tokens or cost | Lower bound: a session sent no usage, or its model has no price yet. |
| Outcome stays `open`, no PR or CI data | FactoryBench isn't polling, can't read the repo, or `pr.linked` was never sent. |
