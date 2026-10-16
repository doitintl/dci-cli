#!/bin/sh
#
# install.sh -- Install the DoiT Cloud Intelligence CLI (dci) on Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/doitintl/dci-cli/main/install.sh | sh
#
# Downloads the release archive for this machine from GitHub Releases,
# verifies it against the release's checksums.txt, and installs the binary to
# ~/.local/bin (no root needed). That location survives Cloud Shell restarts,
# where only $HOME persists, and `dci update` upgrades it in place.
#
# Options (pass after `sh -s --`, e.g. `... | sh -s -- --version 2.8.0`):
#   --version <v>   install a specific version instead of the latest (env DCI_VERSION)
#   --dir <path>    install directory, default ~/.local/bin (env DCI_INSTALL_DIR)
#   --system        install the .deb/.rpm package system-wide with sudo instead
#   --help          show this help
#
# Everything lives in functions and main runs on the last line, so a download
# cut short can never execute half a script.

set -eu

REPO="doitintl/dci-cli"
RELEASES="https://github.com/$REPO/releases"

say() { printf '%s\n' "$*" >&2; }
fail() {
	say "error: $*"
	exit 1
}

# usage is spelled out rather than read back from "$0": when piped into sh
# there is no script file to read.
usage() {
	cat >&2 <<EOF
Install the DoiT Cloud Intelligence CLI (dci) on Linux.

Usage: install.sh [--version <v>] [--dir <path>] [--system]

  --version <v>   install a specific version instead of the latest (env DCI_VERSION)
  --dir <path>    install directory, default ~/.local/bin (env DCI_INSTALL_DIR)
  --system        install the .deb/.rpm package system-wide with sudo instead
  --help          show this help

Piped from curl, pass options after "sh -s --":
  curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | sh -s -- --version 2.8.0
EOF
}

has() { command -v "$1" >/dev/null 2>&1; }

# download URL FILE -- curl or wget, failing on HTTP errors.
download() {
	if has curl; then
		curl -fsSL --retry 3 --proto '=https' --tlsv1.2 -o "$2" "$1"
	elif has wget; then
		wget -q --https-only -O "$2" "$1"
	else
		fail "curl or wget is required"
	fi
}

sha256_of() {
	if has sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif has shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	elif has openssl; then
		openssl dgst -sha256 -r "$1" | cut -d ' ' -f 1
	else
		fail "sha256sum, shasum, or openssl is required to verify the download"
	fi
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) fail "unsupported CPU architecture '$(uname -m)' -- dci ships for x86_64 and arm64; see $RELEASES" ;;
	esac
}

check_os() {
	case "$(uname -s)" in
	Linux) ;;
	Darwin)
		say "On macOS, install dci with Homebrew:"
		say "  brew install doitintl/dci-cli/dci"
		exit 1
		;;
	*) fail "this installer supports Linux only -- for other systems see $RELEASES/latest" ;;
	esac
	# The binary links against glibc; on musl (Alpine) it would fail with a
	# baffling "not found" at first run, so stop here with the real reason.
	if ls /lib/ld-musl-* >/dev/null 2>&1; then
		fail "dci needs glibc and this system uses musl (e.g. Alpine) -- use a glibc-based image such as Debian, Ubuntu, or Fedora"
	fi
}

# fetch_checksums VERSION FILE -- the release's checksums.txt. With no version
# it comes from releases/latest, which also tells us the latest version
# without the rate-limited GitHub API (shared Cloud Shell and CI IPs hit it).
fetch_checksums() {
	if [ -n "$1" ]; then
		download "$RELEASES/download/v$1/checksums.txt" "$2" ||
			fail "version $1 not found -- see $RELEASES"
	else
		download "$RELEASES/latest/download/checksums.txt" "$2" ||
			fail "could not reach GitHub Releases -- check your network or proxy settings"
	fi
}

# asset_line CHECKSUMS PATTERN -- the "<sha256>  <name>" line for one asset.
asset_line() {
	grep -E "  $2\$" "$1" | head -n 1
}

# verify FILE EXPECTED_SHA256
verify() {
	actual=$(sha256_of "$1")
	[ "$actual" = "$2" ] || fail "checksum mismatch for $(basename "$1") (expected $2, got $actual) -- not installing"
}

path_contains() {
	case ":$PATH:" in
	*":$1:"*) return 0 ;;
	*) return 1 ;;
	esac
}

install_completions() {
	src="$1/completions"
	[ -d "$src" ] || return 0
	data="${XDG_DATA_HOME:-$HOME/.local/share}"
	config="${XDG_CONFIG_HOME:-$HOME/.config}"
	# bash-completion 2.x and fish load these user directories on their own.
	mkdir -p "$data/bash-completion/completions" "$config/fish/completions" "$data/zsh/site-functions"
	cp "$src/dci.bash" "$data/bash-completion/completions/dci"
	cp "$src/dci.fish" "$config/fish/completions/dci.fish"
	cp "$src/dci.zsh" "$data/zsh/site-functions/_dci"
	zsh_completions="$data/zsh/site-functions"
}

install_user() {
	version="$1"
	dir="$2"
	arch=$(detect_arch)
	tmp="$3"

	fetch_checksums "$version" "$tmp/checksums.txt"
	line=$(asset_line "$tmp/checksums.txt" "dci_[0-9][^ ]*_linux_${arch}\\.tar\\.gz")
	[ -n "$line" ] || fail "no linux/$arch archive in this release -- see $RELEASES"
	expected=${line%% *}
	archive=${line##* }

	say "Downloading $archive..."
	download "$RELEASES/download/v$(echo "$archive" | sed -E 's/^dci_(.*)_linux_.*$/\1/')/$archive" "$tmp/$archive"
	verify "$tmp/$archive" "$expected"

	mkdir -p "$tmp/x"
	tar -xzf "$tmp/$archive" -C "$tmp/x"
	[ -f "$tmp/x/dci" ] || fail "archive $archive has no dci binary"

	mkdir -p "$dir" || fail "cannot create $dir"
	# Copy next to the target, then rename: atomic, and safe while an older
	# dci is running.
	cp "$tmp/x/dci" "$dir/.dci.new.$$"
	chmod 0755 "$dir/.dci.new.$$"
	mv -f "$dir/.dci.new.$$" "$dir/dci"

	zsh_completions=""
	install_completions "$tmp/x"

	installed=$("$dir/dci" --version 2>/dev/null) || fail "installed $dir/dci but it does not run on this system"
	say ""
	say "Installed $installed to $dir/dci"

	other=$(command -v dci 2>/dev/null || true)
	if [ -n "$other" ] && [ "$other" != "$dir/dci" ]; then
		say ""
		say "Note: another dci at $other comes first on your PATH. Remove it, or put $dir earlier in PATH."
	fi
	if ! path_contains "$dir"; then
		say ""
		say "$dir is not on your PATH yet. Add it with:"
		case "${SHELL:-}" in
		*/zsh) say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc && export PATH=\"$dir:\$PATH\"" ;;
		*/fish) say "  fish_add_path $dir" ;;
		*) say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.bashrc && export PATH=\"$dir:\$PATH\"" ;;
		esac
	fi
	case "${SHELL:-}" in
	*/zsh)
		if [ -n "$zsh_completions" ]; then
			say ""
			say "For zsh tab completion, add before compinit in ~/.zshrc:"
			say "  fpath=($zsh_completions \$fpath)"
		fi
		;;
	esac
}

install_system() {
	version="$1"
	tmp="$2"
	arch=$(detect_arch)
	if has dpkg; then
		format=deb
	elif has rpm; then
		format=rpm
	else
		fail "--system needs dpkg or rpm; run without --system to install to ~/.local/bin"
	fi
	if [ "${CLOUD_SHELL:-}" = "true" ]; then
		say "Warning: Cloud Shell keeps only your home directory -- a system-wide install disappears when the VM restarts."
	fi
	sudo=""
	if [ "$(id -u)" -ne 0 ]; then
		has sudo || fail "--system needs root or sudo"
		sudo="sudo"
	fi

	fetch_checksums "$version" "$tmp/checksums.txt"
	line=$(asset_line "$tmp/checksums.txt" "dci_[0-9][^ ]*_linux_${arch}\\.${format}")
	[ -n "$line" ] || fail "no linux/$arch .$format package in this release -- see $RELEASES"
	expected=${line%% *}
	package=${line##* }

	say "Downloading $package..."
	download "$RELEASES/download/v$(echo "$package" | sed -E 's/^dci_(.*)_linux_.*$/\1/')/$package" "$tmp/$package"
	verify "$tmp/$package" "$expected"

	if [ "$format" = deb ]; then
		$sudo dpkg -i "$tmp/$package"
	else
		$sudo rpm -U --replacepkgs "$tmp/$package"
	fi
	say ""
	say "Installed $(dci --version 2>/dev/null || echo dci) system-wide."
}

main() {
	version="${DCI_VERSION:-}"
	dir="${DCI_INSTALL_DIR:-$HOME/.local/bin}"
	system=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--version)
			[ $# -ge 2 ] || fail "--version needs a value"
			version="$2"
			shift 2
			;;
		--version=*)
			version="${1#*=}"
			shift
			;;
		--dir)
			[ $# -ge 2 ] || fail "--dir needs a value"
			dir="$2"
			shift 2
			;;
		--dir=*)
			dir="${1#*=}"
			shift
			;;
		--system)
			system=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) fail "unknown option '$1' (see --help)" ;;
		esac
	done
	version="${version#v}"

	check_os
	has tar || fail "tar is required"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t dci-install)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	if [ -n "$system" ]; then
		install_system "$version" "$tmp"
	else
		install_user "$version" "$dir" "$tmp"
	fi
	say ""
	say "Get started:  dci login"
}

main "$@"
