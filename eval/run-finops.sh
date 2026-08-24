#!/bin/sh
# FinOps regression harness: run each prompt through one-shot `dci ai` under
# a pty (so the tool trace is captured), log the transcript and wall clock.
# Compare against the baseline tables in AI-FINOPS-SPEC.md §1.
#
# Usage: [RUNS=N] [JUDGE=1] eval/run-finops.sh [path-to-dci] [output-dir]
#   eval/run-finops.sh /tmp/dci-dev/dci               # quick: 1 run per prompt
#   RUNS=3 JUDGE=1 eval/run-finops.sh /tmp/dci-dev/dci
#
# RUNS=N repeats every prompt N times (transcripts $id.run1.txt …) and prints
# a per-prompt summary: median and min-max for wall clock, tool calls, and
# output tokens, parsed from the [ai-stats] telemetry lines (DCI_AI_STATS=1).
# Single runs sit near the noise floor — P06 measured 69s/9 tools and
# 90s/11 tools on back-to-back runs of the same binary — so regression calls
# need RUNS>=3. Repeats run sequentially on purpose: the runs share the
# Anthropic key's rate limits, and the server-side query cache warms after
# run 1, so run 1 is the cold measurement and the summary folds that in.
#
# JUDGE=1 additionally writes $OUT/judge.md: per prompt, the question plus the
# ANSI-stripped final answer of the last run, formatted so a human or an
# external LLM can score answer substance in one pass. The script never calls
# any API itself — `dci ai` cannot judge its own runs (it is deny-listed
# inside its own sessions, and same-model/same-tenant judging is circular).
#
# Requires an authenticated dci and an Anthropic API key (env or settings).
set -eu
DCI="${1:-dci}"
OUT="${2:-eval/runs/$(date +%Y%m%d-%H%M%S)}"
DIR="$(dirname "$0")"
RUNS="${RUNS:-1}"
JUDGE="${JUDGE:-0}"
case "$RUNS" in
  ''|*[!0-9]*|0) echo "RUNS must be a positive integer, got '$RUNS'" >&2; exit 2;;
esac
mkdir -p "$OUT"

# Per-turn telemetry: dci ai prints one [ai-stats] line per turn to stderr,
# which the pty capture merges into the transcript; the summary greps it back
# out so runs are compared on token cost, not just wall clock.
export DCI_AI_STATS=1

# script(1) dialects differ: GNU (util-linux, Linux) wants -c "command" and
# supports --version; BSD/macOS takes the command argv directly. stdin is
# redirected from /dev/null in both — an unredirected script(1) forwards its
# stdin to the child, which would drain the prompt file mid-loop.
if script --version >/dev/null 2>&1; then
  run_prompt() { script -q -c "$DCI ai \"$2\"" "$1" </dev/null >/dev/null 2>&1; }
else
  run_prompt() { script -q "$1" "$DCI" ai "$2" </dev/null >/dev/null 2>&1; }
fi

ESC=$(printf '\033')
BEL=$(printf '\007')
BS=$(printf '\010')

# Drop pty artifacts: backspace erasures (the pty echoes "^D" then rubs it
# out with BS), CR/EOT, CSI sequences, OSC sequences (BEL- or ST-terminated),
# and lone two-byte escapes (e.g. the ESC \ string terminator).
strip_ansi() {
  sed -e ':a' -e "s/[^${BS}]${BS}//g" -e 'ta' | tr -d '\r\004\010' | sed \
    -e "s/${ESC}\\[[0-9;?]*[@-~]//g" \
    -e "s/${ESC}\\][^${ESC}${BEL}]*${ESC}\\\\//g" \
    -e "s/${ESC}\\][^${BEL}]*${BEL}//g" \
    -e "s/${ESC}[@-Z\\\\^_]//g"
}

# Final answer = everything after the last tool-result footer (a bare Go
# duration like "600ms" / "1.2s", optionally "· truncated for the model", or
# "tool failed · …"), minus [ai-stats] lines. With no tool calls the whole
# transcript is the answer.
extract_answer() {
  strip_ansi <"$1" | awk '
    /^[0-9][0-9a-zµ.]*s( · truncated for the model)?$/ { last = NR }
    /^tool failed · /                                  { last = NR }
    { line[NR] = $0 }
    END { for (i = last + 1; i <= NR; i++) if (line[i] !~ /\[ai-stats\]/) print line[i] }
  '
}

# Pull one key=value field out of an [ai-stats] line; trailing unit "s" (wall,
# ttft) is stripped so every field comes back as a bare number.
stat_field() {
  printf '%s\n' "$1" | tr ' ' '\n' | sed -n "s/^$2=//p" | sed 's/s$//'
}

# Numbers on stdin (blank lines ignored) -> "median<unit> (min<unit>-max<unit>)".
# Even counts average the two middle values.
fmt_stat() {
  sort -n | awk -v u="$1" '
    NF { a[++n] = $1 }
    END {
      if (!n) { print "n/a"; exit }
      m = a[int((n + 1) / 2)]
      if (n % 2 == 0) m = (a[n / 2] + a[n / 2 + 1]) / 2
      printf "%s%s (%s%s-%s%s)\n", m, u, a[1], u, a[n], u
    }'
}

JUDGE_MD="$OUT/judge.md"
if [ "$JUDGE" = "1" ]; then
  {
    echo "# FinOps eval — judging bundle"
    echo
    echo "One section per prompt: the question as asked, then the final answer from"
    echo "the last of $RUNS run(s), ANSI-stripped, tool traces removed. Score each"
    echo "answer for substance: does it answer the question, with plausible numbers"
    echo "and honest caveats?"
    echo
    echo "---"
    echo
  } > "$JUDGE_MD"
fi

# The prompt file is read on fd 3, never fd 0, so nothing in the loop body
# can consume the remaining prompts.
while IFS='|' read -r id cap prompt <&3; do
  case "$id" in ""|"#"*) continue;; esac
  echo "=== $id ($cap)"
  walls= toolvals= outvals= last_txt=
  k=1
  while [ "$k" -le "$RUNS" ]; do
    if [ "$RUNS" -eq 1 ]; then
      txt="$OUT/$id.txt"; label="$id"
    else
      txt="$OUT/$id.run$k.txt"; label="$id run$k"
    fi
    start=$(date +%s)
    run_prompt "$txt" "$prompt" || echo "$label exited non-zero"
    end=$(date +%s)
    secs=$((end-start))
    tools=$(grep -c "⚙" "$txt" 2>/dev/null || true)
    stats=$(grep -a '\[ai-stats\]' "$txt" 2>/dev/null | tail -1 | tr -d '\r' | sed 's/.*\[ai-stats\] //' || true)
    echo "$label: ${secs}s, ${tools:-0} tool calls${stats:+, $stats}"
    w=$(stat_field "$stats" wall); [ -n "$w" ] || w=$secs
    walls="$walls $w"
    toolvals="$toolvals $(stat_field "$stats" tools)"
    outvals="$outvals $(stat_field "$stats" out)"
    last_txt=$txt
    k=$((k+1))
  done
  if [ "$RUNS" -gt 1 ]; then
    # shellcheck disable=SC2086 # word splitting feeds one number per line
    echo "$id summary ($RUNS runs): wall $(printf '%s\n' $walls | fmt_stat s), tools $(printf '%s\n' $toolvals | fmt_stat ''), out $(printf '%s\n' $outvals | fmt_stat '')"
  fi
  if [ "$JUDGE" = "1" ]; then
    {
      echo "## $id — $cap"
      echo
      echo "**Question:** $prompt"
      echo
      echo "**Final answer** (run $RUNS of $RUNS):"
      echo
      extract_answer "$last_txt"
      echo
      echo "---"
      echo
    } >> "$JUDGE_MD"
  fi
done 3< "$DIR/finops-prompts.txt"
echo "transcripts in $OUT (contain tenant data — do not commit)"
[ "$JUDGE" = "1" ] && echo "judging bundle: $JUDGE_MD"
exit 0
