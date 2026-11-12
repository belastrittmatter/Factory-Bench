# Claude Code 2.1.322 OTLP log export: Phase 0 findings

Captured 2026-11-12 with `claude -p ... --model haiku --permission-mode acceptEdits --output-format stream-json --verbose`
against a stub OTLP/HTTP receiver, in an empty temp dir. Env: `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_LOGS_EXPORTER=otlp`,
`OTEL_METRICS_EXPORTER=otlp`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/json`, `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:<port>/v1/logs`,
`OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer test-token"`, `OTEL_RESOURCE_ATTRIBUTES=factorybench.attempt=cc-phase0-<run>`,
`OTEL_LOGS_EXPORT_INTERVAL=2000`. `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS` and `OTEL_LOG_RAW_API_BODIES` were not set.

## Files

| File | Content |
|---|---|
| `subagent.json` | Run a: Write ×2, Bash `ls -la`, Bash `false` (failed), one subagent (Haiku launched it in the background, `is_async=true`) |
| `killed-sigterm.json` | Run b1: a foreground subagent that spawns a nested subagent (depth 2). SIGTERM after 20 s |
| `killed-sigkill.json` | Run b2: sequential Writes. SIGKILL after 15 s |
| `endpoint-down.json` | Run c: the stub answered every POST with 503. Byte-identical retries are collapsed into `attempts[]` |

Each file is `{run, note, requests:[{request, path, contentType, body}]}`, one request per line. `body` is the raw OTLP JSON as received, with personal data scrubbed.
Events caused only by the capture machine's setup (`managed_settings_resolved`, `hook_*`, `plugin_loaded`, `mcp_server_connection`) were removed from the files after analysis; the findings below were made on the full captures. Metrics bodies and the stream-json output are not kept.

## Transport

- `Content-Type: application/json` on every POST, with no `Content-Encoding`. **http/json is honored.**
- The path is exactly what `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` says (`/v1/logs`). Metrics go to `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`.
- `Authorization: Bearer test-token` arrived on every request.
- A `200 {}` (`application/json`) response was accepted.
- Batches arrive about every 2 s while the agent is active, plus a final flush on normal exit.

## JSON shape

```
{ "resourceLogs": [ {                        // always exactly 1 per body
    "resource": { "attributes": [ {key, value:{stringValue}} ... ], "droppedAttributesCount": 0 },
    "scopeLogs": [ {                         // always exactly 1
      "scope": { "name": "com.anthropic.claude_code.events", "version": "2.1.322" },
      "logRecords": [ { "timeUnixNano": "1794464607738000000",   // string, ms precision
                        "observedTimeUnixNano": "...same...",
                        "body": { "stringValue": "claude_code.api_request" },
                        "attributes": [ ... ], "droppedAttributesCount": 0 } ] } ] } ] }
```

- **Resource attributes:** `factorybench.attempt`, `host.arch`, `os.type`, `os.version`, `service.name=claude-code`, `service.version=2.1.322`.
- **Records:** no `severityNumber`, `severityText`, `traceId`, `spanId` or `flags`.
- **Event name, twice:** `body.stringValue` = `claude_code.<name>`, and the `event.name` attribute = `<name>` (no prefix).
- **Value types:** `stringValue`, `intValue` (a JSON number, e.g. `"intValue": 36`), `doubleValue`, `boolValue`, and `arrayValue` (`managed_settings.sources`).
  A parser must accept int64 as both a number and a string.
- **Typing is inconsistent.** These fields are sent as `stringValue`:
  - `tool_result`: `success` (`"true"`/`"false"`), `duration_ms`, `tool_input_size_bytes`, `tool_result_size_bytes`
  - `user_prompt`: `prompt_length`
  - hook events: all counts
  - `mcp_server_connection`: `duration_ms`

  `api_request` and `subagent_completed` send real int, double and bool values.
- **`event.timestamp`:** an ISO-8601 string attribute.

### Common attributes (on every record)

`factorybench.attempt` (also on the resource), `user.id`, `session.id`, `organization.id`, `user.email`, `user.account_uuid`,
`user.account_id`, `anthropic.provider`, `anthropic.custom_base_url`, `anthropic.plan`, `anthropic.seat_type`, `terminal.type`,
`event.name`, `event.timestamp`, and `event.sequence` (int, starts at 0 per session, contiguous). Most events also carry `prompt.id`.

## factorybench.attempt

Present on every record in all 4 runs (160 records, none missing), with the correct value per run. It appears in two places:
- `resourceLogs[].resource.attributes[key="factorybench.attempt"]`
- each record's attributes, under `resourceLogs[].scopeLogs[].logRecords[].attributes[key="factorybench.attempt"]`

## Event names seen (common attributes omitted)

| event.name | Specific attributes |
|---|---|
| `api_request` | model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens, cost_usd (double), cost_usd_micros, duration_ms, ttft_ms, request_id, client_request_id, speed, query_source, effort, [agent.name, agent_id, parent_agent_id] |
| `assistant_response` | response_length, response (`<REDACTED>`), request_id, message.uuid, model, query_source, [agent_id] |
| `tool_decision` | decision, source, tool_name, tool_use_id, tool_source, permission_mode, request_id, [agent_id, parent_agent_id] |
| `tool_result` | tool_name, tool_use_id, success, duration_ms, tool_input_size_bytes, tool_result_size_bytes (absent on failure), error_type (on failure, e.g. `ShellError`), request_id, [agent_id, parent_agent_id] |
| `subagent_completed` | agent_type, agent.source, is_built_in, is_async, total_tokens, total_tool_uses, duration_ms, model, final_model, model_swapped, agent_id, [parent_agent_id] |
| `user_prompt` | prompt_length, prompt (`<REDACTED>`), prompt_text (`<REDACTED>`), message.uuid |
| `hook_registered` / `hook_execution_start` / `hook_execution_complete` | hook_event, hook_name, hook_source, num_* (strings), total_duration_ms, … |
| `plugin_loaded` | plugin.name, plugin.scope, plugin_id_hash, marketplace.name, *_path_count, has_hooks, has_mcp, … |
| `mcp_server_connection` | status, transport_type, server_scope, is_plugin, duration_ms |
| `managed_settings_resolved` | managed_settings.* |

- **Not seen:** `compaction` and `api_error`. Neither was triggered, so their shapes are **not verified**.
- **Not in our list from the docs:** `assistant_response`, and the hook, plugin, MCP and managed-settings events. They are emitted by default; tolerate or ignore them.

## Subagents

- **`agent_id`:** on every event emitted inside a subagent. Main-thread events have none. Subagent `api_request`s also carry `agent.name` and `query_source=agent:builtin:general-purpose`.
- **`parent_agent_id`:** **only appears when the parent is itself a subagent** (depth 2). Verified: nested agent `a3bea183083d509c1` has parent `a659980bca3fb01e4`. A depth-1 subagent has `agent_id` and no `parent_agent_id`.
- **`subagent_completed`:** emitted for both background (`is_async=true`) and foreground (`is_async=false`) subagents. A subagent killed by SIGTERM gets none.
- **`subagent_completed.total_tokens` is not usage.** It is the last request's input + output + cache_read + cache_creation (18966 = 2 + 413 + 16220 + 2331), not the sum over the subagent's requests (35388). **Use `api_request` for usage.**
- **IDs line up:** `agent_id` matches stream-json `task_id` / `agent_id`.

## Stable ids for dedup

- **`api_request.request_id`** (`req_...`): present on all 25 api_requests, no duplicates. `client_request_id` (a UUID) is also present.
- **`tool_result.tool_use_id`** (`toolu_...`): always present. `tool_decision` has it too, and both carry the `request_id` of the API response that asked for the tool.
- **`session.id` + `event.sequence`:** on every event, unique and contiguous per session. It is the universal key.
- **Retries:** they resend byte-identical bodies, so **the endpoint must dedup**.

## app.version

Not present on logs or metrics, which contradicts the docs. The version is only in the resource `service.version` and in `scope.version`.
`OTEL_METRICS_INCLUDE_VERSION` was not tested.

## Tokens: OTLP vs stream-json `result.modelUsage` (run a)

| | input | output | cache_read | cache_creation | cost_usd |
|---|---|---|---|---|---|
| sum of 9 `api_request` | 1338 | 2629 | 166488 | 36410 | 0.009003855 |
| `result.modelUsage["claude-haiku-5-5"]` | 1338 | 2629 | 166488 | 36410 | 0.009003855 |

- **Exact match.** Subagent tokens (3 of the 9 requests) and the session-title request are counted in both.
- **`result.usage` covers only the last turn.**
- **Two `result` lines:** run a produced two because the background subagent re-entered the main loop. `modelUsage` is cumulative, so the last line holds the total.

## Killed runs

- **SIGTERM:** exit 143 about 1 s after the signal, with no `result` line.
  - **The final log batch was flushed**, 35 ms after the signal; `event.sequence` 0..46 is gap-free.
  - Metrics were not flushed.
  - An API call still in flight produces no `api_request` event.
- **SIGKILL:** exit 137 and no flush. The last logs POST was 3.4 s before the kill, so the last completed Write and its `api_request` were lost. Expect to lose up to about one export interval plus any in-flight work.

## Endpoint down (503)

- **Claude was not blocked:** exit 0, a normal result, nothing on stderr, and nothing about telemetry in the stream.
  - Wall time was 10.9 s against `duration_ms` 6255. The final batch's retries may have delayed exit by about 1.5 s.
  - No baseline run was done, so the slowdown is **not verified**.
- **Retries:** each batch was retried with exponential backoff (+1.0, +2.5, +4.8 s) using byte-identical bodies, then dropped.
- While one batch was retrying, no other logs batch went out. The rest was sent in one batch at shutdown (2 attempts, then exit).
- **All log data was lost.** There is no disk buffer.
- **Not tested:** an endpoint with no receiver listening at all (connection refused).

## Personal data sent by default

- **On every record:** `user.email` (the real email), `user.account_uuid`, `user.account_id`, `user.id` (hash) and `organization.id`. **Intake must drop these.** They are scrubbed in the saved files.
- **Prompt and response text:** sent only as `<REDACTED>`; only their lengths are real.
- **Logs:** no file paths or commands. Tool inputs appear only as sizes.
- **stream-json:** contains paths (cwd, plugin paths, output_file). These are scrubbed to `/home/user`, `/tmp/scratch` and `acme`.

## Other notes

- **Metrics seen:** `claude_code.session.count`, `token.usage`, `cost.usage`, `lines_of_code.count`, `code_edit_tool.decision`, `active_time.total`.
- **Background subagent:** Haiku put the subagent in the background on its own; the Agent tool_result came back in 13 ms, before the subagent ran.
- **Local noise:** a user-level SessionStart hook and the plugins on the capture machine produce `hook_*` and `plugin_loaded` events. Ignore them.
