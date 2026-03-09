#!/usr/bin/env bash
#
# render.sh -- Render a package-manager manifest template.
#
# Usage:
#   packaging/render.sh <checksums-file> <version> <base-url> <template> <output>
#
# Exits non-zero if any expected checksum is missing from the checksums file.
set -euo pipefail

checksum_file="$1"
version="$2"
base_url="$3"
template="$4"
output="$5"

sha_for() {
  local file="$1"
  local hash
  hash="$(grep "  ${file}$" "$checksum_file" | awk '{print $1}')"
  if [ -z "$hash" ]; then
    echo "ERROR: checksum not found for ${file} in ${checksum_file}" >&2
    exit 1
  fi
  echo "$hash"
}

darwin_amd64="dci_${version}_darwin_amd64.tar.gz"
darwin_arm64="dci_${version}_darwin_arm64.tar.gz"
linux_amd64="dci_${version}_linux_amd64.tar.gz"
linux_arm64="dci_${version}_linux_arm64.tar.gz"
windows_amd64="dci_${version}_windows_amd64.zip"
windows_arm64="dci_${version}_windows_arm64.zip"

# Resolve all checksums upfront so missing hashes fail before any rendering.
sha_darwin_amd64="$(sha_for "$darwin_amd64")"
sha_darwin_arm64="$(sha_for "$darwin_arm64")"
sha_linux_amd64="$(sha_for "$linux_amd64")"
sha_linux_arm64="$(sha_for "$linux_arm64")"
sha_windows_amd64="$(sha_for "$windows_amd64")"
sha_windows_arm64="$(sha_for "$windows_arm64")"

mkdir -p "$(dirname "$output")"

sed \
  -e "s|__VERSION__|${version}|g" \
  -e "s|__DARWIN_AMD64_URL__|${base_url}/${darwin_amd64}|g" \
  -e "s|__DARWIN_AMD64_SHA256__|${sha_darwin_amd64}|g" \
  -e "s|__DARWIN_ARM64_URL__|${base_url}/${darwin_arm64}|g" \
  -e "s|__DARWIN_ARM64_SHA256__|${sha_darwin_arm64}|g" \
  -e "s|__LINUX_AMD64_URL__|${base_url}/${linux_amd64}|g" \
  -e "s|__LINUX_AMD64_SHA256__|${sha_linux_amd64}|g" \
  -e "s|__LINUX_ARM64_URL__|${base_url}/${linux_arm64}|g" \
  -e "s|__LINUX_ARM64_SHA256__|${sha_linux_arm64}|g" \
  -e "s|__WINDOWS_AMD64_URL__|${base_url}/${windows_amd64}|g" \
  -e "s|__WINDOWS_AMD64_SHA256__|${sha_windows_amd64}|g" \
  -e "s|__WINDOWS_ARM64_URL__|${base_url}/${windows_arm64}|g" \
  -e "s|__WINDOWS_ARM64_SHA256__|${sha_windows_arm64}|g" \
  "$template" > "$output"
