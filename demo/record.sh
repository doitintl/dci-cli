#!/usr/bin/env bash
# Record the "what's new" demo: one VHS tape per scene, a title slide before
# each scene, stitched into out/demo.mp4 (4K master) and out/demo-1080p.mp4.
#
#   demo/record.sh              record every section, then stitch
#   demo/record.sh 05 09        record only these sections, then stitch
#   demo/record.sh --stitch     stitch what is already in out/
#   demo/record.sh --cleanup    delete the cli-demo-import dataset
#
# Prerequisites: vhs, ffmpeg, jq, uv (for slides.py); `dci login` done as a
# Doer with customer context doit.com; the report
# "CLI Demo - Top 10 Services (6 months)" and the DataHub dataset
# "Daily Tenant Usage" present on the tenant. Sections run sequentially on
# purpose: parallel queries on the same report hit the API's 429 quota.
#
# Section 10 (dci update) is recorded FIRST when the installed CLI is behind
# the latest release, so every other section then runs on the new version.
# It is skipped, slide only, when the CLI is already current — reinstall the
# previous release to re-record it.
set -euo pipefail
cd "$(dirname "$0")"

SLIDE_SECONDS=${SLIDE_SECONDS:-4}
IMPORT_DATASET=cli-demo-import

log() { printf '\033[1;34m[record]\033[0m %s\n' "$*"; }

ensure_import_dataset() {
  if ! DCI_AGENT_MODE=1 dci list-datahub-datasets --search "$IMPORT_DATASET" --output json \
      | jq -e --arg n "$IMPORT_DATASET" '.datasets[]? | select(.name==$n)' >/dev/null; then
    log "creating DataHub dataset $IMPORT_DATASET"
    DCI_AGENT_MODE=1 dci create-datahub-dataset name: "$IMPORT_DATASET", \
      description: "Target dataset for the CLI demo re-import scene" >/dev/null
  fi
}

cleanup() {
  log "deleting DataHub dataset $IMPORT_DATASET"
  DCI_AGENT_MODE=1 dci delete-datahub-dataset "$IMPORT_DATASET" --yes
}

record_scene() {
  local tape=$1
  log "recording $tape"
  # The launching shell may be an agent harness; the tape's hidden setup
  # forces human mode, but scrub the detection variables here too.
  env -u CLAUDECODE -u CLAUDE_CODE -u CLAUDE_CODE_ENTRYPOINT -u DCI_AGENT_MODE -u CI \
    vhs "tapes/$tape" >"out/${tape%.tape}.log" 2>&1 \
    || { log "FAILED $tape — see out/${tape%.tape}.log"; return 1; }
}

slide_clip() {  # slide_clip <png> <mp4>
  ffmpeg -y -v error -loop 1 -t "$SLIDE_SECONDS" -i "$1" \
    -vf "format=yuv420p" -r 25 -c:v libx264 -preset veryfast -crf 18 "$2"
}

stitch() {
  log "rendering slides"
  ./slides.py out >/dev/null
  : >out/concat.txt
  for png in out/slide-*.png; do
    stem=${png#out/slide-}; stem=${stem%.png}
    slide_clip "$png" "out/slide-$stem.mp4"
    echo "file 'slide-$stem.mp4'" >>out/concat.txt
    if [[ -f "out/$stem.mp4" ]]; then
      echo "file '$stem.mp4'" >>out/concat.txt
    elif [[ $stem != 00-title && $stem != 99-closing ]]; then
      log "no recording for section $stem — slide only"
    fi
  done
  log "stitching out/demo.mp4"
  ffmpeg -y -v error -f concat -safe 0 -i out/concat.txt \
    -c:v libx264 -preset medium -crf 18 -pix_fmt yuv420p -r 25 -movflags +faststart out/demo.mp4
  log "downscaling out/demo-1080p.mp4"
  ffmpeg -y -v error -i out/demo.mp4 -vf scale=1920:1080:flags=lanczos \
    -c:v libx264 -preset medium -crf 20 -pix_fmt yuv420p -movflags +faststart out/demo-1080p.mp4
  ffprobe -v error -show_entries format=duration -of csv=p=0 out/demo.mp4 | sed 's/^/duration: /'
}

mkdir -p out
case "${1:-}" in
  --stitch) stitch; exit 0 ;;
  --cleanup) cleanup; exit 0 ;;
esac

update_available() {
  DCI_AGENT_MODE=1 dci update 2>/dev/null | jq -e '.updateAvailable == true' >/dev/null
}

if [[ $# -gt 0 ]]; then
  tapes=(); for n in "$@"; do tapes+=("$(basename tapes/"$n"-*.tape)"); done
else
  tapes=(10-update.tape $(cd tapes && ls 0[0-9]-*.tape))
fi

for tape in "${tapes[@]}"; do
  if [[ $tape == 10-* ]]; then
    if ! update_available; then
      log "skipping $tape — the installed dci is already the latest release"
      continue
    fi
    log "refreshing Homebrew so the on-camera upgrade is only the upgrade"
    brew update >/dev/null 2>&1 || true
  fi
  [[ $tape == 08-* ]] && ensure_import_dataset
  record_scene "$tape" || true
done
stitch
