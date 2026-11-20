#!/usr/bin/env bash
# Stand-in for a scenario's build command in the dry run (--build-cmd points at it): no product, no model.
# It checks it was called the way docs/context-bench.md "Scoring" says, then writes a build.json.
#   FACTORYBENCH_FAKE_BUILD_FAIL  set: print an error and exit 1 without writing build.json
#   FACTORYBENCH_FAKE_BUILD_LOG   file the argument line is appended to
scenario= change= out= yes= model= maxcost=
while [ $# -gt 0 ]; do
  case $1 in
    --scenario) scenario=$2; shift 2 ;;
    --change) change=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --yes) yes=1; shift ;;
    --model) model=$2; shift 2 ;;
    --max-cost) maxcost=$2; shift 2 ;;
    *) echo "fake-build: unknown argument $1" >&2; exit 2 ;;
  esac
done
[ -n "${FACTORYBENCH_FAKE_BUILD_LOG:-}" ] && echo "scenario=$scenario change=$change out=$out yes=$yes model=$model maxcost=$maxcost attempt=${FACTORYBENCH_CONTEXT_ATTEMPT:-} labels=${FACTORYBENCH_CONTEXT_LABELS:-}" >>"$FACTORYBENCH_FAKE_BUILD_LOG"
[ -f "$scenario/scenario.json" ] || { echo "fake-build: no scenario.json in $scenario" >&2; exit 2; }
{ [ -f "$change/proposal.md" ] || [ -f "$change/brief.md" ]; } || { echo "fake-build: $change is not a change folder" >&2; exit 2; }
if [ -n "${FACTORYBENCH_FAKE_BUILD_FAIL:-}" ]; then echo "fake-build: the product could not be built" >&2; exit 1; fi
mkdir -p "$out"
echo '{"hidden_pass": 1, "hidden_total": 2, "tasks": 1, "merged": 1, "escalations": 0, "repairs": 1, "cost_usd": 0.05, "note": ""}' >"$out/build.json"
