# Agent Etiquette

This repo is one piece of a much larger system. `dci` wraps restish, sits in
front of the DCI API, and ships through multiple distribution channels. A
change that looks correct from the code alone may conflict with things in
flight — the API roadmap, planned CLI changes, or packaging constraints — that
live nowhere in this repo.

**Read the history before proposing anything.** Closed issues carry decisions,
not just resolved bugs. Rejected approaches, deliberate constraints, and
"why we didn't do X" reasoning are all documented there. Reading only open
issues gives an incomplete picture.

**Maintainer first.** The maintainer has context that isn't in this repo:
the DCI API roadmap, internal usage patterns, and work already in progress.
Before taking any action that affects the repo's public surface, check with
the maintainer first.

## Preferred contribution flow

**Open an issue. Do not open a PR.**

PRs are cheap to create and expensive to decline. An unsolicited PR puts the
maintainer in the position of managing work they didn't ask for. An issue lets
an idea be evaluated, shaped, or discarded before any implementation happens.

The maintainer prefers to open PRs themselves — when and if a change is
wanted, using their own agent, with full context. If you identify something
worth addressing:

1. Search open **and closed** issues first. The idea may have already been
   evaluated and decided.
2. Open an issue describing the **problem**, not the solution. Explain what's
   broken or missing and why it matters. Include your observations, not just
   your conclusions.
3. Stop there. Do not implement. Do not open a PR. Wait for maintainer input.

If a maintainer explicitly asks you to open a PR, proceed. Otherwise, don't.

## What a good issue looks like

- **Problem first.** What is broken or missing, and what's the impact?
- **Observations, not just conclusions.** What did you find? What did you read?
- **Scope flags.** Does this touch the release pipeline, distribution
  manifests, the DCI API contract, or restish internals? Call it out — these
  areas are most likely to have invisible constraints.
- **No solution required.** You may propose one, but it's not the
  deliverable. The maintainer may have a better approach or may decide not
  to act at all.

---

# Project Context

## What Is This

`dci` is the CLI for the DoiT Cloud Intelligence (DCI) API. It wraps
[restish](https://github.com/rest-sh/restish) with DCI-specific configuration
— auto-configured API base, OAuth2 via the DoiT Console, table-first output,
and a locked-down command surface that exposes only DCI API operations. The
entire CLI is a single `main.go` file. It ships as a Go binary distributed via
Homebrew, Scoop, WinGet, and `.deb`/`.rpm` packages.

## Restish Version (don't upgrade to v2)

Pinned to restish **v0.21.2** (already includes the CVE-2025-22868 patch).
Restish **v2** is a ground-up redesign that **deletes the in-process library
model `dci` is built on** — it makes restish the binary and pushes extensions
to out-of-process plugins, so moving to v2 is a rewrite, not a dependency
bump, with no security pressure and real UX regressions. **Don't upgrade
without revisiting the full evaluation in
[issue #20](https://github.com/doitintl/dci-cli/issues/20).**

## Commit Message Conventions

GoReleaser changelog auto-generates from commits between tags. Filtered
prefixes (excluded from changelog):
- `docs:` — documentation-only changes (README, DISTRIBUTION.md, etc.)
- `test:` — test-only changes
- `chore:` — maintenance (manifest updates, CI config, dependency bumps,
  PR review fixups, secrets/CI fixes)

GoReleaser also filters merge commits (`Merge ...`) and auto-generated
manifest commits (`Update Homebrew formula ...`) automatically.

**Use `chore:` for anything not user-facing** — CI/CD fixes, gitleaks config,
PR review nits, and internal tooling. Use `fix:` only for bugs that affect
CLI users. Commits without a filtered prefix appear in the GitHub Release
changelog.

## Release Pipeline

- GoReleaser v2 via `goreleaser-cross` Docker image
- Tag `v*` triggers `release.yml` → `sync-manifests.yml` +
  `post-release-verify.yml`
- Manifests (`Formula/dci.rb`, `bucket/dci.json`) are committed to main by CI
- WinGet manifests submitted automatically via PR to `microsoft/winget-pkgs`

## Key Files

- Single-file CLI: `main.go` (all logic) + `main_test.go`
- Build config: `.goreleaser.yaml`
- Release workflows: `.github/workflows/release.yml`, `sync-manifests.yml`,
  `post-release-verify.yml`
- Package templates: `packaging/`

## Project Conventions

- README and DISTRIBUTION.md are user-facing — no internal jargon, no
  restish references
- README targets end users; DISTRIBUTION.md targets developers/contributors
- Homebrew tap works via GitHub redirect
  (`doitintl/homebrew-dci-cli` → `doitintl/dci-cli`)
- Windows ARM64 excluded (upstream goreleaser-cross issue #117)
