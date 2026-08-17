#!/usr/bin/env bash
#
# completions.sh -- Generate shell completion scripts for release packaging.
#
# Writes completions/dci.{bash,zsh,fish,ps1}. Run from the repo root (GoReleaser
# invokes it as a before hook).
#
# The scripts embed the name of the invoking binary (cobra uses os.Args[0]),
# so this builds a throwaway binary named exactly "dci" -- `go run .` would
# produce scripts wired to the wrong command name.
set -euo pipefail

rm -rf completions
mkdir completions

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

go build -o "$tmp/dci" .

# Keep the generation hermetic: the CLI writes its config on first run and
# checks for updates in the background; neither belongs in a release build.
export HOME="$tmp/home"
export XDG_CONFIG_HOME="$tmp/home/.config"
export DCI_NO_UPDATE_CHECK=1
mkdir -p "$HOME"

"$tmp/dci" completion bash >completions/dci.bash
"$tmp/dci" completion zsh >completions/dci.zsh
"$tmp/dci" completion fish >completions/dci.fish
"$tmp/dci" completion powershell >completions/dci.ps1
