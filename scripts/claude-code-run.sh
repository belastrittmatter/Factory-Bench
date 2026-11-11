#!/usr/bin/env bash
# Test harness: one real Claude Code run reported as one attempt, with one usage event per model it used.
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
(cd "$work" && claude -p "$prompt" --output-format stream-json --verbose --model haiku --permission-mode acceptEdits) | tee "$stream" >/dev/null
echo "claude exit ${PIPESTATUS[0]}; stream in $stream" >&2

tr=$("$fb" claude-translate "$stream")
n=$(jq '.usage | length' <<<"$tr")
for ((i = 0; i < n; i++)); do
  "$fb" emit usage --attempt "$att" --key "usage:$((i + 1))" --json "$(jq -c ".usage[$i] + {phase: \"implement\"}" <<<"$tr")"
done
"$fb" emit attempt.finished --attempt "$att" --key finished --json "$(jq -c '.finished' <<<"$tr")"
echo "attempt $att reported; result line:" >&2
jq -c 'select(.type=="result") | {total_cost_usd, duration_ms, is_error, modelUsage: (.modelUsage // {} | map_values({inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens, thinkingTokens, costUSD}))}' "$stream" >&2
