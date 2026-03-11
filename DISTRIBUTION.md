# Distribution

This document covers how `dci` releases are built, published, and distributed across platforms.

## Release Channels

| Channel          | Install command                                                                 |
|------------------|---------------------------------------------------------------------------------|
| GitHub Releases  | Download from [Releases](https://github.com/doitintl/dci-cli/releases/latest)  |
| Homebrew (macOS) | `brew install doitintl/dci-cli/dci`                                             |
| Scoop (Windows)  | `scoop bucket add doitintl https://github.com/doitintl/dci-cli && scoop install dci` |
| WinGet (Windows) | `winget install DoiT.dci`                                                       |
| Linux (.deb)     | `sudo dpkg -i dci_*_linux_amd64.deb`                                           |
| Linux (.rpm)     | `sudo rpm -i dci_*_linux_amd64.rpm`                                            |
| Go install       | `go install github.com/doitintl/dci-cli@latest`                                |

## How to Release

1. Merge your changes to `main`.
2. Tag and push:

   ```bash
   git tag v0.3.0
   git push origin v0.3.0
   ```

3. The CI pipeline handles the rest automatically:
   - **`release.yml`** — builds binaries, archives, `.deb`/`.rpm` packages, and publishes a GitHub Release.
   - **`sync-manifests.yml`** — renders and commits Homebrew and Scoop manifests to this repo, and submits a PR to [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs) for WinGet.
   - **`post-release-verify.yml`** — smoke-tests Homebrew, `.deb`, `.rpm`, and Windows zip installs.

## Release Artifacts

Archives:

- `dci_<version>_darwin_amd64.tar.gz` / `_arm64.tar.gz`
- `dci_<version>_linux_amd64.tar.gz` / `_arm64.tar.gz`
- `dci_<version>_windows_amd64.zip`

Linux packages:

- `dci_<version>_linux_amd64.deb` / `_arm64.deb`
- `dci_<version>_linux_amd64.rpm` / `_arm64.rpm`

## Repository Layout (Distribution Files)

```
.goreleaser.yaml                    # GoReleaser v2 build config
.github/workflows/release.yml      # Tag-triggered release
.github/workflows/sync-manifests.yml
.github/workflows/post-release-verify.yml
packaging/render.sh                 # Template renderer for manifests
packaging/homebrew/dci.rb.tmpl
packaging/scoop/dci.json.tmpl
packaging/winget/*.tmpl
Formula/dci.rb                      # Committed by CI (Homebrew tap)
bucket/dci.json                     # Committed by CI (Scoop bucket)
```

## Manual Re-runs

Both `sync-manifests.yml` and `post-release-verify.yml` support `workflow_dispatch`:

- **Actions > Sync Package Manifests > Run workflow** — enter the tag (e.g. `v0.3.0`) to re-render manifests.
- **Actions > Post-release Verify > Run workflow** — enter the tag to re-run smoke tests.

`release.yml` is triggered only by `v*` tag pushes.

## Rollback

1. Delete the GitHub release: `gh release delete vX.Y.Z`
2. Delete the tag: `git push --delete origin vX.Y.Z && git tag -d vX.Y.Z`
3. Revert manifests — push a commit restoring the previous `Formula/dci.rb` and `bucket/dci.json`, or re-run `sync-manifests.yml` for the last good tag.
To re-release: fix the issue, tag a new patch version, and push.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `sync-manifests` fails with "checksum not found" | Verify `checksums.txt` matches filenames expected by `packaging/render.sh` |
| Homebrew/Scoop commit says "already up to date" | Normal for re-runs of the same tag |
| Homebrew/Scoop commit fails with permission error | Ensure `permissions: contents: write` is set in the workflow |
| Homebrew/Scoop commit blocked by branch protection | Add `github-actions[bot]` to the bypass list |
| Post-release Homebrew install fails | Confirm release assets exist, then re-run `sync-manifests` |
| `release.yml` stuck queued | Another release for the same tag is in progress — wait or cancel it |
| WinGet PR submission skipped | `WINGET_GH_PAT` secret is not configured — add a PAT with `public_repo` scope |
| WinGet PR fails validation | Check [microsoft/winget-pkgs validation policies](https://github.com/microsoft/winget-pkgs/blob/master/doc/README.md) |

## Known Limitations

- **Windows ARM64** is excluded due to a missing cross-compiler in `goreleaser-cross` ([goreleaser-cross#117](https://github.com/goreleaser/goreleaser-cross/issues/117)).
- **WinGet** submissions are automated via `sync-manifests.yml`, which opens a PR to [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs) on each release. This requires a `WINGET_GH_PAT` secret (a GitHub PAT with `public_repo` scope) and a fork of `winget-pkgs` (currently `apgiorgi/winget-pkgs`). Microsoft's automated validation runs on each PR — approval is typically within hours.
- **Homebrew tap** relies on a GitHub redirect from `doitintl/homebrew-dci-cli` to this repo. Do not create a repo named `homebrew-dci-cli` in the org.
