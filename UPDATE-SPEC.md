# Design spec: `dci update` — channel-aware self-update

Status: **draft for maintainer review**.
Audited at commit `87a26bb`; every claim cites the function and file it is based on, line numbers approximate at that commit.

Scope: one command — `dci update` — that brings the installed CLI to the latest release on every OS and install channel, plus a hardening pass on the existing background update check (detached refresh, advisory lock). Agent mode, non-TTY, and CI behavior stay deterministic and chatter-free per the CLI's decoration contract.

---

## 1. Summary

| # | Feature | One line | Phase |
|---|---------|----------|-------|
| U1 | Channel detection | Classify the running binary's install channel: brew, Scoop, WinGet, deb/rpm, self-managed | P1 |
| U2 | Delegated update | Package-manager installs run the manager's own upgrade command (confirm first) instead of overwriting a managed binary | P1 |
| U3 | Direct self-update | Self-managed installs download the release asset, verify it against `checksums.txt`, and atomically replace the binary | P1 |
| U4 | Detached background check | The passive release check survives fast commands and never runs concurrently (debounce + advisory lock) | P2 |

The user experience on every platform is Antigravity-style: `dci update` → new version, done. The channel awareness is invisible; it exists because `dci` ships through five package channels (Homebrew, Scoop, WinGet, `.deb`, `.rpm` — DISTRIBUTION.md) whose bookkeeping breaks if the binary silently replaces itself. This is the pattern `uv`, `bun`, and `flyctl` use (flyctl goes furthest and executes `brew upgrade flyctl` for the user; `gcloud` disables its own updater under apt/yum installs). Tools that always self-replace (`rustup`, `deno`, Antigravity's `agy`) own their install path — agy's installer drops the binary in `~/.local/bin`, which no package manager touches — a luxury `dci` does not have.

## 2. Current state (what already exists)

The update chapter (update.go) already provides more than half the machinery:

- **Passive check**: `startUpdateCheck` (update.go:27) refreshes a release cache in a goroutine parallel to the command's own API call; `maybeNotifyUpdate` (update.go:46) prints the styled notice (`styleUpdateNotice`, TUI-SPEC F8.1) after output, waiting ≤1s for the in-flight refresh.
- **Cache**: `update_check.json` in the config dir, 5-hour TTL (`updateCheckTTL`, update.go:105), clock-rollback safe (`stale`, update.go:150), temp-file+rename writes (update.go:202).
- **Suppression**: agent mode, non-TTY stderr, `DCI_NO_UPDATE_CHECK`, and unparseable dev versions all suppress the passive check (`updateCheckSuppressed`, update.go:160).
- **Channel detection (v0)**: `upgradeInstruction` (update.go:230) string-matches the resolved executable path (`executablePath` resolves the brew symlink, update.go:69) → `brew upgrade dci`, `scoop update dci`, `winget upgrade DoiT.dci`, or the releases-page fallback.
- **Explicit command**: `dci upgrade` (update.go:80) fetches fresh, compares, and **prints** the instruction — it deliberately installs nothing.
- **Version math**: `parseVersion`/`isNewerVersion` (update.go:259, :279) — prerelease tags never trigger notices.

What is missing: actually performing the update (U2/U3), deb/rpm and self-managed classification (U1), and a check that survives commands shorter than the GitHub round trip (U4 — today the refresh goroutine dies with the process if the command finishes in under ~1s past the notice wait, wasting the fetch).

## 3. U1 — Channel detection

Extend `upgradeInstruction`'s path matching into a classifier returning a channel enum. Detection order (first match wins):

| Channel | Signal |
|---------|--------|
| `brew` | resolved path contains `/cellar/`, `/homebrew/`, or `/linuxbrew/` (covers macOS + Linuxbrew; symlinks already resolved by `executablePath`) |
| `scoop` | GOOS=windows and path contains `\scoop\` |
| `winget` | GOOS=windows and path contains `\winget\` or `\windowsapps\` |
| `deb` | GOOS=linux and `dpkg -S <exe>` exits 0 (binary owned by a package) |
| `rpm` | GOOS=linux and `rpm -qf <exe>` exits 0 |
| `self` | anything else — tarball drops, `~/.local/bin`, `/usr/local/bin`, CI runners, `go build` outputs |

The `dpkg`/`rpm` probes run only on Linux, only when the path heuristics miss, and tolerate the tools being absent (a `dpkg`-less system cannot have a dpkg-owned binary). Dev builds (unparseable `version`) classify as `self` but refuse to update (nothing to compare against — same rule as the passive check).

## 4. U2 — Delegated update (managed installs)

For `brew`/`scoop`/`winget`, `dci update`:

1. Fetches the latest tag fresh (reusing `fetchLatestVersion`, update.go:112) and exits 0 with "already up to date" when current.
2. Prints what it is about to do and **runs the manager's command** (`brew update && brew upgrade dci`, etc.) after an interactive confirm — the F3-style default-Cancel prompt behind `tuiActive()`, plain `[y/N]` otherwise. `--yes` skips the prompt. Homebrew and Scoop resolve a third-party tap/bucket from a local clone that the upgrade does not reliably fetch (Homebrew's auto-update is time-throttled), so the index refresh (`brew update`, `scoop update`) runs first, as part of the same confirmed plan; WinGet queries its sources live and needs none. A refresh that fails warns and does not abort the upgrade — step 3 is what decides whether anything moved.
3. Streams the manager's output through, propagates its exit code, and re-checks that the binary now reports the new version. **The exit code alone is not evidence**: every manager exits 0 when it decides the installed version is already current, so a no-op upgrade (stale index, formula not yet published, pinned package) is otherwise indistinguishable from a real one and would be reported as an update that did not happen. The check runs the launch path (`os.Executable()`, deliberately *not* symlink-resolved — a Homebrew upgrade repoints `bin/dci` at the new keg while the old keg is still on disk) with `--version`; a version still behind the target is a `UPDATE_FAILED` error naming the gap, and a probe that cannot answer is treated as success rather than an invented failure.

For `deb`/`rpm` there is no repository to delegate to (packages are downloaded from GitHub Releases — README "Linux"), and replacing a dpkg/rpm-owned file behind the database's back is the one thing this spec refuses to do. Instead, `dci update` treats the download+install pipeline as the delegated command: it shows the exact steps (`curl -fsSLO …/dci_<ver>_linux_<arch>.deb && sudo dpkg -i …` / `sudo rpm -U …`), and after the same default-Cancel confirm executes them through the user's shell — sudo prompts for the password interactively, exactly as if the user had pasted the line themselves. The CLI never caches or handles the password; declining the confirm (or a non-TTY context) falls back to printing the line. The downloaded package is checksum-verified against `checksums.txt` before the install step runs.

Non-TTY / agent mode: never execute a package manager. Print the instruction (human non-TTY) or emit the structured result (§7).

## 5. U3 — Direct self-update (self-managed installs)

Library: **`github.com/creativeprojects/go-selfupdate`** — the maintained successor of rhysd/go-github-selfupdate. It resolves the right GitHub release asset for GOOS/GOARCH, unpacks tar.gz/zip, and replaces the binary atomically with rollback, including the Windows rename dance (a running .exe cannot be overwritten; the old binary is moved aside first). Evaluate at implementation time that its asset resolution matches GoReleaser's `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}` naming (.goreleaser.yaml:58); if not, pin an explicit asset-name filter.

Requirements on top of the library defaults:

- **Checksum verification is mandatory**, against the `checksums.txt` GoReleaser already publishes (.goreleaser.yaml:69–70). No verified checksum → no update, loud error. (Cosign signatures can layer on later; not in scope.)
- **No downgrades** by default; `--version vX.Y.Z` pins an explicit version in either direction for rollbacks.
- **Permission failure is a clean error**: if the binary's directory is not writable (e.g. a root-owned `/usr/local/bin`), report the path and suggest sudo'ing the printed command — never escalate privileges itself.
- The GitHub API call reuses the existing 5s-timeout client and User-Agent conventions (update.go:113–119).

Flags: `--check` (report only — today's `dci upgrade` behavior), `--yes`, `--version <tag>`.

## 6. Command naming

`dci update` performs; the existing `dci upgrade` becomes an alias of it (decided 2026-08-21). `dci upgrade`'s current check-only behavior moves to `dci update --check`. This is a behavior change for `upgrade` (from "prints instruction" to "asks, then does it") — acceptable because the interactive default-Cancel confirm keeps a bare invocation harmless, and the old output is one flag away. Machine contexts see no change in what runs unprompted (§7).

## 7. Agent mode & exit codes

`dci update` invoked in agent mode (or with non-TTY stdout) never mutates anything without `--yes`:

- Without `--yes`: behaves as `--check`, emitting a JSON result `{ "current": "2.5.1", "latest": "2.5.2", "updateAvailable": true, "channel": "brew", "instruction": "brew update && brew upgrade dci" }` on stdout.
- With `--yes`: performs the update (self channel) or executes the manager (managed channels), then emits the same shape plus `"updated": true`.
- Failures follow the structured error contract (error_contract.go): `UPDATE_FAILED` with a hint naming the manual instruction, network failures as `NETWORK_ERROR` exit 41.

The passive notice keeps its existing suppression rules unchanged (update.go:160).

## 8. U4 — Detached background check (debounce + lock)

Antigravity's updater runs detached with a TTL debounce marker and an advisory lock; the equivalent here fixes a real gap: today's refresh goroutine dies with the process, so on fast commands the fetch is wasted and the cache stays stale (update.go:46–55 abandons after 1s).

- **Detached refresh**: when the cache is stale, re-exec the binary as a released background process — the exact `spawnDetachedNameRefresh` pattern the name cache already uses (name_completion.go:136) — running a hidden `__refresh-update-check` command that fetches and rewrites `update_check.json`, then exits. The foreground command no longer waits at all; the notice renders from whatever the cache holds (at most one run behind, same as today's steady state).
- **Debounce**: the 5-hour TTL already is the debounce marker; keep it. A failed fetch additionally stamps a short (15-minute) retry-backoff field so a GitHub outage doesn't spawn a refresh process per invocation.
- **Advisory lock**: `update_check.lock` (O_CREATE|O_EXCL with a staleness ceiling) so concurrent dci invocations spawn at most one refresher. The temp-file+rename cache write (update.go:202) already makes racing writers safe; the lock just avoids redundant processes.
- **No background auto-install.** The background path only refreshes the notice cache. Installing is always an explicit foreground `dci update` — silent binary swaps and package managers do not mix, and a CLI that changes version mid-session breaks agents' assumptions.

## 9. Testing

- Channel classifier: table-driven paths (Cellar symlink, linuxbrew, scoop shim, WindowsApps, dpkg-owned via a stubbed prober, `~/.local/bin`) — pure function, no exec.
- Delegated update: fake runner capturing the argv; confirm/decline paths reuse the F3 test seams (`confirmDestructiveInteractively`-style var).
- Self-update: `go-selfupdate` against an `httptest` server serving a fake release + checksums.txt (happy path, checksum mismatch, non-writable dir).
- Detached check: lock contention, backoff stamping, and the `__refresh-update-check` command against a fake API — mirroring the existing update_test.go table style.
- Integration: the existing `buildBinary` harness (destructive_contract_test.go) can exercise `dci update --check` end to end against a stubbed API URL (make `latestReleaseAPIURL` overridable via env for tests only, or keep it a var).

## 10. Out of scope

- apt/yum repositories (would make deb/rpm delegation possible; separate distribution decision).
- Signature verification (cosign) — checksums only for now.
- Background auto-install (§8, deliberately excluded).
- WinGet manifest lag: `winget upgrade` can trail a release by hours-to-days while microsoft/winget-pkgs merges the auto-submitted PR (sync-manifests.yml); the delegated command inherits that reality and this spec does not paper over it.

## 11. Decisions (maintainer, 2026-08-21)

1. **§6 naming**: `upgrade` becomes an installing alias of `update`; check-only behavior lives on as `--check`.
2. **§4 deb/rpm**: offer to run the download+install pipeline after the confirm (interactive sudo), with print-only as the decline/non-TTY fallback.
3. **§5 pinning**: `--version <tag>` is in — checksum-verified pin/rollback for self-managed installs.
