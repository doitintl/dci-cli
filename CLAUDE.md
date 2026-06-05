# Claude Code Project Context

## What Is This

`dci` is the CLI for the DoiT Cloud Intelligence (DCI) API. It wraps [restish](https://github.com/rest-sh/restish) with DCI-specific configuration — auto-configured API base, OAuth2 via the DoiT Console, table-first output, and a locked-down command surface that exposes only DCI API operations. The entire CLI is a single `main.go` file. It ships as a Go binary distributed via Homebrew, Scoop, WinGet, and `.deb`/`.rpm` packages.

## Restish Version (don't upgrade to v2)

Pinned to restish **v0.21.2** (already includes the CVE-2025-22868 patch). Restish **v2** is a ground-up redesign that **deletes the in-process library model `dci` is built on** — it makes restish the binary and pushes extensions to out-of-process plugins, so moving to v2 is a rewrite, not a dependency bump, with no security pressure and real UX regressions. **Don't upgrade without revisiting the full evaluation in [issue #20](https://github.com/doitintl/dci-cli/issues/20).**

## Commit Message Conventions

GoReleaser changelog auto-generates from commits between tags. Filtered prefixes (excluded from changelog):
- `docs:` — documentation-only changes (README, DISTRIBUTION.md, etc.)
- `test:` — test-only changes
- `chore:` — maintenance (manifest updates, CI config, dependency bumps, PR review fixups, secrets/CI fixes)

Goreleaser also filters merge commits (`Merge ...`) and auto-generated manifest commits (`Update Homebrew formula ...`) automatically.

**Use `chore:` for anything not user-facing** — CI/CD fixes, gitleaks config, PR review nits, and internal tooling. Use `fix:` only for bugs that affect CLI users. Commits without a filtered prefix appear in the GitHub Release changelog.

## Release Pipeline

- GoReleaser v2 via `goreleaser-cross` Docker image
- Tag `v*` triggers `release.yml` → `sync-manifests.yml` + `post-release-verify.yml`
- Manifests (`Formula/dci.rb`, `bucket/dci.json`) are committed to main by CI
- WinGet manifests submitted automatically via PR to `microsoft/winget-pkgs`

## Key Files

- Single-file CLI: `main.go` (all logic) + `main_test.go`
- Build config: `.goreleaser.yaml`
- Release workflows: `.github/workflows/release.yml`, `sync-manifests.yml`, `post-release-verify.yml`
- Package templates: `packaging/`

## Project Conventions

- README and DISTRIBUTION.md are user-facing — no internal jargon, no restish references
- README targets end users; DISTRIBUTION.md targets developers/contributors
- Homebrew tap works via GitHub redirect (`doitintl/homebrew-dci-cli` → `doitintl/dci-cli`)
- Windows ARM64 excluded (upstream goreleaser-cross issue #117)
