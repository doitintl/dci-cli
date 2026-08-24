#!/bin/sh
# FinOps-10 regression harness: run each prompt through one-shot `dci ai`
# under a pty (so the tool trace is captured), log the transcript and wall
# clock. Compare against the baseline table in AI-FINOPS-SPEC.md §1.
#
# Usage: eval/run-finops.sh [path-to-dci] [output-dir]
# Requires an authenticated dci and an Anthropic API key (env or settings).
set -eu
DCI="${1:-dci}"
OUT="${2:-eval/runs/$(date +%Y%m%d-%H%M%S)}"
DIR="$(dirname "$0")"
mkdir -p "$OUT"

# script(1) dialects differ: GNU (util-linux, Linux) wants -c "command" and
# supports --version; BSD/macOS takes the command argv directly. stdin is
# redirected from /dev/null in both — an unredirected script(1) forwards its
# stdin to the child, which would drain the prompt file mid-loop.
if script --version >/dev/null 2>&1; then
  run_prompt() { script -q -c "$DCI ai \"$2\"" "$1" </dev/null >/dev/null 2>&1; }
else
  run_prompt() { script -q "$1" "$DCI" ai "$2" </dev/null >/dev/null 2>&1; }
fi

# The prompt file is read on fd 3, never fd 0, so nothing in the loop body
# can consume the remaining prompts.
while IFS='|' read -r id cap prompt <&3; do
  [ -z "$id" ] && continue
  echo "=== $id ($cap)"
  start=$(date +%s)
  run_prompt "$OUT/$id.txt" "$prompt" || echo "$id exited non-zero"
  end=$(date +%s)
  tools=$(grep -c "⚙" "$OUT/$id.txt" 2>/dev/null || true)
  echo "$id: $((end-start))s, ${tools:-0} tool calls"
done 3< "$DIR/finops-prompts.txt"
echo "transcripts in $OUT (contain tenant data — do not commit)"
