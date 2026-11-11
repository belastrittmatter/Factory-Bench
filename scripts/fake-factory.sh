#!/usr/bin/env bash
# Test harness: emits one full work item through `factorybench emit`.
# usage: FACTORYBENCH_URL=... FACTORYBENCH_TOKEN=<token of this factory> scripts/fake-factory.sh <factory-id> [repo] [issue] [pr_url]
# START_MIN (default 0) shifts all timestamps, so two factories can interleave on the same issue; MODEL picks the main model.
# Fixed attempt ids and timestamps: re-running it only produces duplicates.
set -euo pipefail
f=${1:?factory-id}; repo=${2:-acme/chat-bridge}; issue=${3:-412}
pr=${4:-https://github.com/$repo/pull/57}
fb=${FB:-factorybench}
model=${MODEL:-claude-sonnet-5-5}
start=$((9 * 60 + ${START_MIN:-0}))
t() { local m=$((start + $1)); printf '2026-11-10T%02d:%02d:00Z' $((m / 60)) $((m % 60)); }
e() { local typ=$1 att=$2 key=$3 min=$4 json=$5; "$fb" emit "$typ" --attempt "$f-$att" --key "$key" --ts "$(t "$min")" --json "$json"; }
wi="\"work_item\":{\"repo\":\"$repo\",\"issue\":$issue},\"factory_config_version\":\"$f@0.1.0+fake\",\"labels\":[\"agent:ready\",\"bug\"]"

e attempt.started  plan    started  0  "{$wi,\"role\":\"plan\"}"
e usage            plan    usage:1  2  '{"model":"'"$model"'","input":12000,"output":2500,"cache_read":40000,"cache_write":8000,"duration_ms":95000,"phase":"implement"}'
e pr.linked        plan    pr       3  "{\"pr_url\":\"$pr\",\"branch\":\"agent/issue-$issue\"}"
e human.waiting    plan    waiting  3  '{"reason":"plan_review"}'
e attempt.finished plan    finished 4  '{"status":"completed"}'
# build: no parent_attempt_id sent; FactoryBench infers the plan as parent
e attempt.started  build   started  12 "{$wi,\"role\":\"build\"}"
e human.resumed    build   resumed  12 '{"reason":"plan_approved"}'
e usage            build   usage:1  16 '{"model":"'"$model"'","input":30000,"output":6000,"cache_read":90000,"reported_cost_usd":0.4213,"duration_ms":240000,"phase":"implement"}'
e gate             build   gate:1   17 '{"name":"build","passed":true}'
e gate             build   gate:2   18 '{"name":"test","passed":false,"reason":"2 failing tests"}'
e usage            build   usage:2  20 '{"model":"gemini-3-pro","input":15000,"output":3000,"cache_read":20000,"reasoning":700,"duration_ms":180000,"phase":"verify"}'
e attempt.finished build   finished 21 '{"status":"failed","reason":"gate test failed"}'
# crashed retry: no usage event at all -> cost unknown, lowers coverage
e attempt.started  retry1  started  22 "{$wi,\"role\":\"build\"}"
e attempt.finished retry1  finished 23 '{"status":"failed","reason":"agent crashed"}'
e attempt.started  retry2  started  25 "{$wi,\"role\":\"build\"}"
e usage            retry2  usage:1  30 '{"model":"'"$model"'","input":20000,"output":4000,"cache_read":60000,"phase":"implement"}'
e gate             retry2  gate:1   30 '{"name":"build","passed":true}'
e gate             retry2  gate:2   30 '{"name":"test","passed":true}'
e attempt.finished retry2  finished 31 '{"status":"completed"}'
