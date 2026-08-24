#!/usr/bin/env bash
# Release gate for CHANGELOG.md (CHANGELOG-SPEC.md §6).
#
# Usage:
#   check-changelog.sh --lint                 # offline format lint (CI on PRs)
#   check-changelog.sh --gate vX.Y.Z          # full gate for a tag (release.yml)
#
# The gate fails (exit 1) when the tag has no changelog entry, the entry is
# malformed, or a help.doit.com link in the entry definitively does not
# resolve — a 404 page, or a live page without the linked anchor. An
# unreachable help site is a warning, never a release blocker (exit 0).
set -euo pipefail

CHANGELOG="${CHANGELOG_FILE:-CHANGELOG.md}"
HELP_HOST="help.doit.com"
CURL="curl -sS --location --max-time 15 --retry 2 --retry-delay 2"

fail=0
err() { echo "::error file=${CHANGELOG}::$1" >&2; fail=1; }
warn() { echo "::warning file=${CHANGELOG}::$1" >&2; }

[ -f "$CHANGELOG" ] || { err "$CHANGELOG not found"; exit 1; }

# --- Format lint (both modes) -----------------------------------------------
# Release headings: "## vX.Y.Z — Month D, YYYY"
bad_headings=$(grep -nE '^## ' "$CHANGELOG" \
  | grep -vE '^[0-9]+:## v[0-9]+\.[0-9]+\.[0-9]+ — (January|February|March|April|May|June|July|August|September|October|November|December) [0-9]{1,2}, [0-9]{4}$' || true)
if [ -n "$bad_headings" ]; then
  err "malformed release heading(s) — expected '## vX.Y.Z — Month D, YYYY':"$'\n'"$bad_headings"
fi

# Subsections: only New / Improved / Fixed
bad_subs=$(grep -nE '^### ' "$CHANGELOG" | grep -vE '^[0-9]+:### (New|Improved|Fixed)$' || true)
if [ -n "$bad_subs" ]; then
  err "unknown subsection(s) — allowed: '### New', '### Improved', '### Fixed':"$'\n'"$bad_subs"
fi

mode="${1:-}"
case "$mode" in
  --lint)
    [ "$fail" -eq 0 ] && echo "lint OK: $CHANGELOG"
    exit "$fail"
    ;;
  --gate)
    tag="${2:-}"
    [ -n "$tag" ] || { err "--gate requires a tag argument (vX.Y.Z)"; exit 1; }
    ;;
  *)
    echo "usage: $0 --lint | --gate vX.Y.Z" >&2
    exit 2
    ;;
esac

# --- Entry exists for the tag ------------------------------------------------
if ! grep -qE "^## ${tag} — " "$CHANGELOG"; then
  err "no changelog entry for ${tag} — add '## ${tag} — <Month D, YYYY>' to $CHANGELOG before tagging (CHANGELOG-SPEC.md §6)"
  exit 1
fi

# Extract the tag's entry (from its heading to the next "## " or EOF).
entry=$(awk -v tag="$tag" '
  $0 ~ "^## " tag " — " {found=1; next}
  found && /^## / {exit}
  found {print}
' "$CHANGELOG")

# Entry must have content: at least one bullet, or the maintenance line.
if ! printf '%s\n' "$entry" | grep -qE '^- |Maintenance release; no user-facing changes\.'; then
  err "entry for ${tag} is empty — add bullets or the maintenance-release line"
fi

# --- Link validation (help.doit.com only, anchors included) -------------------
# Fragment checks use the rendered HTML's id="..." attributes: the Help Center
# assigns custom heading ids (e.g. #update, #timestamps) that markdown-side
# slugification would miss, so the page HTML is the ground truth.
urls=$(printf '%s\n' "$entry" | grep -oE "https://${HELP_HOST}[A-Za-z0-9/._#-]*" | sort -u || true)

# Fetched pages land in temp files: grepping a pipe from a shell variable
# trips pipefail when grep -q exits on an early match (printf gets SIGPIPE).
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

network_ok=1
llms_file="$workdir/llms.txt"
if ! $CURL "https://${HELP_HOST}/llms.txt" -o "$llms_file" 2>/dev/null; then
  warn "could not fetch https://${HELP_HOST}/llms.txt — link validation degraded"
  network_ok=0
  : > "$llms_file"
fi

for url in $urls; do
  page="${url%%#*}"
  frag=""
  case "$url" in *#*) frag="${url#*#}";; esac

  # Page resolves: listed in llms.txt, or answers 200.
  page_ok=0
  if [ -s "$llms_file" ] && grep -qF "$page" "$llms_file"; then
    page_ok=1
  else
    code=$($CURL -o /dev/null -w '%{http_code}' -I "$page" 2>/dev/null || echo 000)
    if [ "$code" = "200" ]; then
      page_ok=1
    elif [ "$code" = "404" ]; then
      err "dead link in ${tag} entry: $page returns 404"
      continue
    elif [ "$network_ok" -eq 0 ] || [ "$code" = "000" ]; then
      warn "could not verify $page (help site unreachable) — not blocking the release"
      continue
    else
      err "link in ${tag} entry: $page returned HTTP $code"
      continue
    fi
  fi

  # Anchor resolves: id="frag" must exist in the rendered page HTML. The
  # Help Center assigns custom heading ids, so the HTML is the ground truth.
  if [ "$page_ok" -eq 1 ] && [ -n "$frag" ]; then
    page_file="$workdir/$(printf '%s' "$page" | tr -c 'A-Za-z0-9' '_').html"
    if [ ! -s "$page_file" ]; then
      if ! $CURL "$page" -o "$page_file" 2>/dev/null; then
        warn "could not fetch $page to verify #$frag — not blocking the release"
        continue
      fi
    fi
    if ! grep -qF "id=\"$frag\"" "$page_file"; then
      err "dead anchor in ${tag} entry: $page has no id=\"$frag\""
    fi
  fi
done

if [ "$fail" -eq 0 ]; then
  echo "gate OK: ${tag} entry present, well-formed, all links resolve"
fi
exit "$fail"
