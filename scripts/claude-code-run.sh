#!/usr/bin/env bash
# Test harness: one real Claude Code run reported as one attempt. Claude Code sends its usage, tool calls and
# subagents itself over OTLP (POST /v1/logs); this script only emits the attempt lifecycle.
# usage: FACTORYBENCH_URL=... FACTORYBENCH_TOKEN=... scripts/claude-code-run.sh "<prompt>" <owner/repo> <issue>
set -uo pipefail
prompt=${1:?prompt}; repo=${2:?repo}; issue=${3:?issue}
fb=${FB:-factorybench}
att="cc-$(date -u +%Y%m%dT%H%M%S)-$$"
out_dir=${OUT_DIR:-$PWD/runs}; mkdir -p "$out_dir"
stream="$out_dir/$att.jsonl"
cfg="claude-code@$(claude --version | awk '{print $1}')+haiku"

"$fb" emit attempt.started --attempt "$att" --key started \
  --json "{\"work_item\":{\"repo\":\"$repo\",\"issue\":$issue},\"factory_config_version\":\"$cfg\",\"role\":\"full\"}"
work=$(mktemp -d)
(cd "$work" && CLAUDE_CODE_ENABLE_TELEMETRY=1 OTEL_LOGS_EXPORTER=otlp OTEL_EXPORTER_OTLP_PROTOCOL=http/json \
  OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="$FACTORYBENCH_URL/v1/logs" \
  OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $FACTORYBENCH_TOKEN" \
  OTEL_RESOURCE_ATTRIBUTES="factorybench.attempt=$att" \
  claude -p "$prompt" --output-format stream-json --verbose --model haiku --permission-mode acceptEdits) | tee "$stream" >/dev/null
echo "claude exit ${PIPESTATUS[0]}; stream in $stream" >&2

# why the run ended is not in the telemetry: take it from the last result line (none = killed or crashed)
finished=$(jq -sc 'map(select(.type == "result")) | last
  | if . == null then {status: "failed", reason: "no result line"}
    elif .is_error then {status: "failed", reason: .subtype}
    else {status: "completed"} end' "$stream")
"$fb" emit attempt.finished --attempt "$att" --key finished --json "$finished"
echo "attempt $att reported; result line:" >&2
jq -c 'select(.type=="result") | {total_cost_usd, duration_ms, is_error, modelUsage: (.modelUsage // {} | map_values({inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens, thinkingTokens, costUSD}))}' "$stream" >&2
