#!/bin/sh
# FinOps-10 regression harness: run each prompt through one-shot `dci ai`,
# capture the transcript (tool trace included when stderr is a TTY) and the
# wall clock. Compare against the baseline table in AI-FINOPS-SPEC.md §1.
#
# Usage: eval/run-finops.sh [path-to-dci] [output-dir]
# Requires an authenticated dci and an Anthropic API key (env or settings).
set -eu
DCI="${1:-dci}"
OUT="${2:-eval/runs/$(date +%Y%m%d-%H%M%S)}"
DIR="$(dirname "$0")"
mkdir -p "$OUT"
while IFS='|' read -r id cap prompt; do
  [ -z "$id" ] && continue
  echo "=== $id ($cap)"
  start=$(date +%s)
  script -q "$OUT/$id.txt" "$DCI" ai "$prompt" >/dev/null 2>&1 || echo "$id exited non-zero"
  end=$(date +%s)
  tools=$(grep -c "⚙" "$OUT/$id.txt" 2>/dev/null || echo "?")
  echo "$id: $((end-start))s, $tools tool calls"
done < "$DIR/finops-prompts.txt"
echo "transcripts in $OUT (contain tenant data — do not commit)"
