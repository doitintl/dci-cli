# Distribution

This repository ships with a release pipeline for macOS, Windows, and Linux.
The goal is:

- publish a tagged GitHub release with prebuilt binaries
- generate Linux `.deb` and `.rpm` packages
- render package-manager manifests for Homebrew, Scoop, and WinGet
- commit Homebrew and Scoop manifests directly to this repo
- keep WinGet manifests in-repo as generated artifacts ready for submission

## Release channels

- GitHub Releases: canonical source of binaries, checksums, and Linux packages
- Homebrew: `brew tap doitintl/dci-cli https://github.com/doitintl/dci-cli && brew install doitintl/dci-cli/dci`
- Scoop: `scoop bucket add doitintl https://github.com/doitintl/dci-cli && scoop install dci`
- WinGet: `winget install DoiT.dci`
- Linux packages: `.deb` and `.rpm` release assets
- `go install`: `go install github.com/doitintl/dci-cli@latest` (requires Go toolchain; installs from source)

## Files added for distribution

- `.goreleaser.yaml`: builds archives, checksums, GitHub release assets, `.deb`, `.rpm`
- `.github/workflows/release.yml`: runs GoReleaser on `v*` tags
- `.github/workflows/sync-manifests.yml`: renders package-manager manifests and commits Homebrew/Scoop to this repo
- `.github/workflows/post-release-verify.yml`: smoke-tests Homebrew, `.deb`, `.rpm`, and Windows zip install paths after release
- `packaging/render.sh`: shared template-rendering script used by both `sync-manifests.yml` and `post-release-verify.yml`
- `packaging/homebrew/dci.rb.tmpl`: Homebrew formula template
- `packaging/scoop/dci.json.tmpl`: Scoop manifest template
- `packaging/winget/*.tmpl`: WinGet manifest templates
- `Formula/dci.rb`: rendered Homebrew formula (committed by CI; makes this repo a Homebrew tap)
- `bucket/dci.json`: rendered Scoop manifest (committed by CI; makes this repo a Scoop bucket)

## Release flow

1. Merge changes to `main`.
2. Create and push a version tag:

   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```

3. `release.yml` runs and publishes:
   - macOS tarballs (`amd64`, `arm64`)
   - Linux tarballs (`amd64`, `arm64`)
   - Windows zip files (`amd64`)
   - Linux `.deb` and `.rpm`
   - `checksums.txt`

4. `sync-manifests.yml` runs on the published release:
   - downloads `checksums.txt`
   - renders Homebrew, Scoop, and WinGet manifests using `packaging/render.sh`
   - uploads the rendered files as a workflow artifact
   - commits `Formula/dci.rb` and `bucket/dci.json` to this repo
   - leaves WinGet manifests ready for submission to `microsoft/winget-pkgs`

5. `post-release-verify.yml` smoke-tests:
   - Homebrew installation on macOS using the rendered formula
   - `.deb` install on Ubuntu
   - `.rpm` install on Fedora (container job)
   - Windows zip extraction and execution

## Artifact naming

The release pipeline expects these archive names:

- `dci_<version>_darwin_amd64.tar.gz`
- `dci_<version>_darwin_arm64.tar.gz`
- `dci_<version>_linux_amd64.tar.gz`
- `dci_<version>_linux_arm64.tar.gz`
- `dci_<version>_windows_amd64.zip`

Linux package names:

- `dci_<version>_linux_amd64.deb`
- `dci_<version>_linux_arm64.deb`
- `dci_<version>_linux_amd64.rpm`
- `dci_<version>_linux_arm64.rpm`

## Operational notes

- `main.version` is injected at build time using `-X main.version={{ .Version }}`
- `dist/` is ignored so local GoReleaser runs do not pollute the working tree
- GoReleaser v2 is required for local and CI builds; the `version` field in `.goreleaser.yaml` is `2` and the workflow pins `~> v2`
- `.goreleaser.yaml` runs `go test ./...` as a `before.hooks` entry — the release will not proceed if tests fail
- the package templates in `packaging/` are the human-reviewed source used by the manifest render workflow
- `packaging/render.sh` exits non-zero if any expected checksum is missing from `checksums.txt`, preventing manifests with blank hashes
- WinGet is intentionally generated but not auto-submitted here; that keeps the repo automation simple and avoids coupling this repository to a fork-and-PR flow for `winget-pkgs`
- Windows ARM64 is currently excluded because the `goreleaser-cross` Docker image is missing the `aarch64-w64-mingw32-gcc` compiler ([goreleaser-cross#117](https://github.com/goreleaser/goreleaser-cross/issues/117)); remove the `ignore` entry in `.goreleaser.yaml` and restore the ARM64 sections in the packaging templates when the upstream image is fixed

## Rollback

If a release is broken:

1. **Delete the GitHub release** via the web UI or `gh release delete vX.Y.Z`.
2. **Delete the tag**: `git push --delete origin vX.Y.Z && git tag -d vX.Y.Z`.
3. **Revert Homebrew and Scoop manifests** — push a commit that restores the previous `Formula/dci.rb` and `bucket/dci.json`, or re-run `sync-manifests.yml` for the last good tag via `workflow_dispatch`.
4. **WinGet** — if the manifest was already submitted to `microsoft/winget-pkgs`, open a PR there to revert.

To re-release after fixing:

1. Fix the issue on `main`.
2. Create a new patch tag (e.g., `v0.1.1`).
3. Push the tag to trigger a fresh release.

## Manual re-runs (workflow_dispatch)

Both distribution workflows support `workflow_dispatch` for manual triggering:

- **sync-manifests.yml** — `Actions > Sync Package Manifests > Run workflow`. Enter the release tag (e.g., `v0.1.0`) in the `tag` input. Useful to re-render manifests or retry a failed sync without creating a new release.
- **post-release-verify.yml** — `Actions > Post-release Verify > Run workflow`. Enter the release tag. Useful to re-run smoke tests after a transient CI failure.

`release.yml` is triggered only by `v*` tag pushes and does not have a `workflow_dispatch` trigger.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `sync-manifests` fails with "checksum not found" | GoReleaser archive naming changed or a platform was removed | Verify `checksums.txt` contents match the filenames expected by `packaging/render.sh` |
| Homebrew/Scoop commit step says "already up to date" | Manifest content is identical to what was committed previously | Normal for re-runs of the same tag |
| Homebrew/Scoop commit step fails with permission error | `GITHUB_TOKEN` lacks write access | Ensure `permissions: contents: write` is set in the workflow |
| Homebrew/Scoop commit step fails with "protected branch" | `main` has branch protection rules that block pushes from `GITHUB_TOKEN` | Add `github-actions[bot]` to the bypass list in branch protection settings, or use a PAT with bypass permissions |
| Post-release verify fails on Homebrew install | Formula rendered with wrong checksums or download URL returns 404 | Confirm release assets were published and re-run `sync-manifests` via `workflow_dispatch` |
| Post-release RPM job fails to install `gh` | Fedora mirror issue | Re-run the workflow; transient mirror failures resolve on retry |
| `release.yml` is queued and not running | Concurrency group is holding it because another release for the same tag is in progress | Wait for the in-progress run to finish, or cancel it manually |

## First-time setup checklist

1. Push a test tag from a non-production version.
2. Confirm:
   - GitHub release assets were published
   - `Formula/dci.rb` and `bucket/dci.json` were committed to this repo
   - WinGet manifests were attached as an artifact
   - post-release smoke tests passed (Homebrew, deb, rpm, Windows)

## Follow-up work

- add a LICENSE file and update SPDX identifiers in `packaging/scoop/dci.json.tmpl` and `packaging/winget/dci.locale.en-US.yaml.tmpl`
- add a hosted apt repository if you want `apt install dci` instead of downloading `.deb` assets
- add a hosted rpm repository if you want `dnf install dci`
- add provenance or SBOM generation if supply-chain attestation becomes a requirement
