# Agent Guide

Guidance for AI agents working with this repository. The etiquette section applies to external agents — agents not acting on the maintainer's direct instructions. The project context applies to everyone.

## Etiquette for External Agents

This section is for agents acting on their own initiative, or on behalf of someone other than the maintainer. Agents working on the maintainer's direct instructions follow those instructions instead.

This repo is one piece of a much larger system. `dci` wraps restish, sits in front of the DCI API, and ships through multiple distribution channels. A change that looks correct from the code alone may conflict with things in flight — the API roadmap, planned CLI changes, or packaging constraints — that live nowhere in this repo.

**Maintainer first.** The maintainer has context that isn't in this repo: the DCI API roadmap, internal usage patterns, and work already in progress. Before taking any action that affects the repo's public surface, check with the maintainer first.

**Contribution flow: open an issue, do not open a PR.** Search open **and closed** issues first (closed issues carry decisions, not just resolved bugs), describe the problem rather than a solution, and stop there — do not implement, wait for maintainer input. The full policy and the good-issue checklist are in [CONTRIBUTING.md](CONTRIBUTING.md); read it before acting. It applies to agents and humans alike.

## Project Context

### What Is This

`dci` is the CLI for the Cloud Intelligence™ (DCI) API. It wraps [restish](https://github.com/rest-sh/restish) with DCI-specific configuration — auto-configured API base, OAuth2 via the DoiT Console, table-first output, and a locked-down command surface that exposes only DCI API operations. The entire CLI is a single `package main` — one file per chapter of functionality, no sub-packages. It ships as a Go binary distributed via Homebrew, Scoop, WinGet, and `.deb`/`.rpm` packages.

### Restish Version (don't upgrade to v2)

Pinned to restish **v0.21.2** (already includes the CVE-2025-22868 patch). Restish **v2** is a ground-up redesign that **deletes the in-process library model `dci` is built on** — it makes restish the binary and pushes extensions to out-of-process plugins, so moving to v2 is a rewrite, not a dependency bump, with no security pressure and real UX regressions. **Don't upgrade without revisiting the full evaluation in [issue #20](https://github.com/doitintl/dci-cli/issues/20).**

### Charm Stack (v2)

The TUI layer is on the Charm v2 stack: `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, `charm.land/huh/v2`, and `github.com/NimbleMarkets/ntcharts/v2`. Do not "fix" imports back to the `github.com/charmbracelet/*` v1 paths — lipgloss v1 and termenv remain in the module graph only as restish/glamour transitive dependencies, and glamour stays at v1 on purpose (it rides restish's requirement; see BUBBLETEA-V2-SPEC.md §5). The migration record, per-symbol API map, and as-landed deviations live in [BUBBLETEA-V2-SPEC.md](BUBBLETEA-V2-SPEC.md).

### Commit Message Conventions

GoReleaser changelog auto-generates from commits between tags. Filtered prefixes (excluded from changelog):
- `docs:` — documentation-only changes (README, DISTRIBUTION.md, etc.)
- `test:` — test-only changes
- `chore:` — maintenance (manifest updates, CI config, dependency bumps, PR review fixups, secrets/CI fixes)

GoReleaser also filters merge commits (`Merge ...`) and auto-generated manifest commits (`Update Homebrew formula ...`) automatically.

**Use `chore:` for anything not user-facing** — CI/CD fixes, gitleaks config, PR review nits, and internal tooling. Use `fix:` only for bugs that affect CLI users. Commits without a filtered prefix appear in the GitHub Release changelog.

### Release Pipeline

- GoReleaser v2 via `goreleaser-cross` Docker image
- Tag `v*` triggers `release.yml` → `sync-manifests.yml` + `post-release-verify.yml`
- Manifests (`Formula/dci.rb`, `bucket/dci.json`) are committed to main by CI
- WinGet manifests submitted automatically via PR to `microsoft/winget-pkgs`

### Versioning

Routine releases bump the **patch** digit (v2.3.0 → v2.3.1), even when they carry `feat:` commits — the CLI releases often, and minor-per-release would race the version to 3.0. Reserve **minor** bumps (v2.4.0) for notable milestones (new command groups, major UX overhauls) and **major** bumps for breaking changes. Policy set 2026-08-18, starting after v2.3.0.

### Key Files

- CLI source: single `package main`, one file per chapter — `main.go` (core: config & onboarding, arg/completion normalization, usage branding & lockdown, auth & doer context, customer context, table rendering) plus sibling files for extracted chapters (`name_resolution.go`, `name_completion.go`, `list_views.go`, `pagination.go`, `response_transform.go`, `output_contract.go`, `error_contract.go`, `destructive_contract.go`, `skill_management.go`, `update.go`, `command_docs.go`, `multipart_upload.go`, `export_download.go`, `export_pagination.go`, and others), each with a matching `_test.go`
- File-shaped responses (CSV/NDJSON exports): `export_download.go` (verbatim body passthrough, `--output-file`, `--for-reimport`) and `export_pagination.go` (`X-Next-Page-Token` paging, the API's time-window cap, CSV header union). Design: [EXPORT-SPEC.md](EXPORT-SPEC.md)
- Curated command docs: `command-docs/<command>.yaml` (examples, argument notes, Help Center notes, related commands) embedded by `command_docs.go` and rendered in `--help`, `dci commands --json`, and the Help Center reference. Validated against the live spec by `command_docs_test.go` (CI fetches the prod spec); scaffold and coverage via `go run ./tools/commanddocs`. Design: [COMMAND-DOCS-SPEC.md](COMMAND-DOCS-SPEC.md)
- File uploads: `multipart_upload.go` encodes `field: @path` shorthand as `multipart/form-data` for operations whose request body is multipart (today `ingest-datahub-events-csv`) and swaps restish's `Run` for its own from the dci pre-run hook; per-command upload size ceilings live in `uploadSizeLimits`. Design: [MULTIPART-UPLOAD-SPEC.md](MULTIPART-UPLOAD-SPEC.md)
- TUI end-to-end tests: `ai_tui_e2e_test.go` drives the real binary on a real pty (keystrokes in, rendered frames out), offline and Unix-only. When a session bug is fixed from user feedback, add its keystroke replay there — the file's header documents the harness and its two assertion rules. Run just these with `go test -run TestE2E .`
- Build config: `.goreleaser.yaml`
- Release workflows: `.github/workflows/release.yml`, `sync-manifests.yml`, `post-release-verify.yml`
- Package templates: `packaging/`

#### Why a single `package main`

This is intentional. The CLI has no external package consumers, so sub-packages would buy nothing but churn — there is no API surface to justify them. Instead the code is one flat `package main`, split into chapter-per-file siblings: each extracted concern (name resolution, shell completion, list views, pagination, response transforms, output/error/destructive contracts, skill management, update checks, …) lives in its own file with a matching `_test.go`, and each file opens with a comment stating its chapter's purpose.

`main.go` remains the core: config & onboarding → arg/completion normalization → agent mode → usage branding & lockdown → auth & doer context → customer context → table rendering.

**When to extract:** when a chapter in `main.go` grows past ~700 lines or is coherent enough to stand alone, move it to a new sibling file in the same `package main`. Do not introduce sub-packages.

### Project Conventions

- README and DISTRIBUTION.md are user-facing — no internal jargon, no restish references
- README targets end users; DISTRIBUTION.md targets developers/contributors
- Homebrew tap works via GitHub redirect (`doitintl/homebrew-dci-cli` → `doitintl/dci-cli`)
- Windows ARM64 excluded (upstream goreleaser-cross issue #117)
