# Design spec: CLI changelog on help.doit.com/docs/cli

Status: **maintainer-reviewed — open questions decided 2026-08-23 (§12)**.
Audited at commit `94268b6`; file/line citations are approximate at that commit.

Scope: a customer-facing changelog page in the Help Center CLI section —
`https://help.doit.com/docs/cli/changelog` — updated on every release. The
canonical text lives in this repo, is written in CLI-user terms (not commit
subjects), links to Help Center CLI pages when they exist, and flows to the
help site through a deterministic, human-reviewed pipeline. A release cannot
ship without its entry.

---

## 1. Summary

| # | Feature | One line | Phase |
|---|---------|----------|-------|
| C1 | Canonical `CHANGELOG.md` | Curated per-release changelog lives in this repo, versioned with the code | P1 |
| C2 | Entry format & writing rules | User-terms prose, New/Improved/Fixed sections, help-center link policy | P1 |
| C3 | Release gate | `release.yml` refuses to release a tag whose entry is missing or whose links don't resolve | P1 |
| C4 | Help Center delivery via Scribe | Release fires a dispatch to doiteng/scribe, which renders the changelog to omni MDX and opens/updates a draft PR for tech-docs review | P1 |
| C5 | Backfill | Seed the page with rewritten history from v2.0.0 to current | P2 |
| C6 | CLI touchpoints | `dci docs` entry + update-notice link to the changelog page | P2 |
| C7 | Curated GitHub Release header | Prepend the entry to the GitHub Release body above the raw commit list | P3 |

## 2. Current state (what already exists)

**Release notes today are commit subjects.** GoReleaser builds the GitHub
Release changelog from commits between tags (`.goreleaser.yaml:75–84`),
excluding `docs:`/`test:`/`chore:`/merge/manifest commits (AGENTS.md "Commit
Message Conventions"). The result is developer-facing by construction: raw
subjects with hashes, spec codenames ("TUI-SPEC P3", v2.5.1), and library
names ("via ntcharts", v2.5.2). Nothing published today explains a release in
CLI-user terms, and nothing on help.doit.com lists CLI releases at all — the
Help Center has a site-wide "Documentation changelog" and a "What's new"
section, but no CLI page.

**The Help Center is omni's Docusaurus site.** help.doit.com is built from
`services/help-website/docs` in `doiteng/omni` (Cloudflare Pages); the CLI
section is `docs/cli/` — the hand-written guide (`index.mdx`, → `/docs/cli`),
the cheatsheet, and the generated command reference under
`generated/command-groups/` produced from the OpenAPI spec. Every page is also
served as Markdown via the `.md` URL suffix and indexed in `llms.txt` /
`llms-cli.txt`, so a changelog page becomes agent-readable for free. Changing
any page means a PR to omni reviewed by the tech-docs team.

**A dci-cli → omni bridge already has a design and a validated prototype.**
Scribe Scenario A ("Scribe: Multi-Repo Support for Help Center Documentation",
Confluence, 2026-06-19) built a `workflow_dispatch` updater in dci-cli that
edits `docs/cli/index.mdx` and opens a draft PR to omni. Its auth model is the
one to reuse: the built-in `GITHUB_TOKEN` reads the source repo, and the DoiT
Docs Bot App token (secrets `SCRIBE_APP_ID`/`SCRIBE_APP_PRIVATE_KEY`, scoped
to omni) pushes the branch and opens the docs PR — necessary because dci-cli
is in `doitintl` and omni is in `doiteng`, and one App installation token
cannot span orgs. That prototype is agent-driven (Claude rewrites prose); the
changelog delivery below reuses Scribe's omni plumbing but none of the agent
machinery, because the prose is authored here first — and unlike the
Scenario-A shortcut, dci-cli itself holds no omni credentials at all (§7).

**Release plumbing.** `release.yml` runs GoReleaser on `v*` tags, then
dispatches `sync-manifests.yml` and `post-release-verify.yml`
(`.github/workflows/release.yml:36–42`). Both downstream workflows support
manual `workflow_dispatch` re-runs with a tag input (DISTRIBUTION.md
"Manual Re-runs") — the changelog delivery (§7) follows the same
pattern. The CLI itself already ships documentation entry points
(`docs_command.go:10–19`) and an update notice (`maybeNotifyUpdate`,
update.go:63) — both natural homes for a changelog pointer (C6).

## 3. The page (what a CLI user sees)

- **URL**: `https://help.doit.com/docs/cli/changelog` — omni file
  `services/help-website/docs/cli/changelog.mdx`, sidebar-positioned directly
  after the CLI guide and before the generated reference. Markdown mirror at
  `…/changelog.md` and `llms-cli.txt` inclusion come free from the platform.
- **Header**: one-line description ("What changed in each release of the
  DoiT Cloud Intelligence CLI") plus a standing line on how to get the
  latest version: run `dci update`, or see the install methods in the
  [CLI guide](https://help.doit.com/docs/cli#installation).
- **Body**: release sections, newest first:

```markdown
## v2.5.2 — August 21, 2026

### New
- `dci update` updates the CLI in place on every platform, aware of how you
  installed it (Homebrew, Scoop, WinGet, deb/rpm, or a downloaded binary).
- Charts: `--chart` now renders stacked column charts per group and follows
  your report color theme.
  ([Charts in the CLI guide](https://help.doit.com/docs/cli#charts))

### Fixed
- Pivot tables hide all-zero rows by default.
- A clearer error names the API base when a token is rejected (401).
```

- Section headings within a release: `### New` (new commands, flags,
  capabilities), `### Improved` (behavior/UX changes to existing features),
  `### Fixed` (bugs users could hit). Empty headings are omitted. A release
  with nothing user-facing (possible but rare — tags are cut to ship user
  value) gets the single line *"Maintenance release; no user-facing
  changes."* rather than a gap in the version sequence.

## 4. C1 — Canonical source: `CHANGELOG.md` in this repo

The changelog is **authored here, rendered there**. Reasons:

1. **Context.** The person (or agent session) shipping the release is the
   only one who knows which commits collapse into one user-visible change and
   which are invisible. Rewriting from commit subjects after the fact — by
   tech-docs or by an agent in omni — re-derives that knowledge lossily.
2. **Versioned with the code.** The entry lands in the same history as the
   change it describes; `git log CHANGELOG.md` is the release narrative.
3. **The omni PR becomes a review, not an authoring task.** Tech-docs reviews
   rendered output for tone and placement; they never start from raw commits.

Format: standard Markdown at the repo root. Top-of-file comment block states
the writing rules (§5) so they travel with the file. One `## v<X.Y.Z> —
<date>` heading per release, subsections as in §3. The file is the complete
page body — the delivery renderer (§7) only adds front matter.

Commit convention: changelog edits use the `docs:` prefix, so they are
excluded from GoReleaser's commit changelog (`.goreleaser.yaml:80`) — the
curated changelog never recursively appears inside the raw one.

## 5. C2 — Writing rules (CLI-user terms) and link policy

Rules, enforced by review and stated in the file header:

- **Describe what the user can do or will notice**, not what the code does.
  "`dci update` updates the CLI in place" — not "channel-aware self-update
  with detached background check".
- **Name commands and flags exactly as typed** (`dci update`, `--chart`,
  `--all`). Backticks, no angle-bracket grammar unless showing syntax.
- **No internal vocabulary.** No commit hashes, PR numbers, spec codenames
  (TUI-SPEC, P1/P2), library names (ntcharts, restish), or file names. Same
  rule README already follows ("no internal jargon, no restish references",
  AGENTS.md "Project Conventions").
- **One bullet per user-visible change.** Merge the feature commit and its
  follow-up fixes into one bullet when they shipped in the same release
  (v2.5.2's stacked-charts feat + two fix commits = one bullet); split one
  commit into two bullets when it shipped two visible things.
- **Commit prefixes are the inventory, not the text.** `feat:` and `fix:`
  commits since the previous tag are the checklist of candidate bullets;
  filtered prefixes (`chore:`/`test:`/`docs:`) are presumed invisible unless
  they changed behavior.

**Link policy** — "links to Help Center CLI pages when possible/available":

- Link a bullet to the page that documents the feature: guide anchors
  (`/docs/cli#authentication`), the cheatsheet, or a generated command page
  (`/docs/cli/generated/command-groups/<group>/<operation>`).
- Links are absolute `https://help.doit.com/...` URLs — the page is read from
  the site, from `.md` mirrors, and from agent corpora, so relative links are
  fragile.
- **Only link pages — and anchors — that resolve** at release time
  (validated by C3, including fragment ids). A
  feature whose documentation ships later gets its bullet without a link;
  the link is added when the docs land (each delivery re-renders the whole
  page, so retroactive edits flow through, §7). Never hold a bullet hostage to a
  missing page, and never link speculatively.
- New-command releases usually need a same-release omni docs update anyway
  (the generated reference regenerates from the OpenAPI spec; hand-written
  guide changes go through Scribe Scenario A or a manual omni PR). The
  changelog entry links whatever exists when the tag is cut.

## 6. C3 — Release gate in `release.yml`

A step before the GoReleaser step:

1. **Entry exists**: `CHANGELOG.md` contains a `## v<version> ` heading
   matching the pushed tag exactly. Missing → fail with a message naming the
   file and the expected heading. The re-release flow already documented in
   DISTRIBUTION.md applies: fix, delete the tag, re-tag.
2. **Entry is dated** with a plausible date (matches `— <Month D, YYYY>`).
3. **Links resolve — anchors included**: every `help.doit.com` URL in the
   new entry must resolve. A fragment-less URL passes if it appears in
   `https://help.doit.com/llms.txt` or answers an HTTP 200. A URL with a
   `#fragment` needs more — fragments are client-side, so a HEAD on the
   stripped URL only proves the page exists, and anchors are the dominant
   link shape here (§5): the gate fetches the page's `.md` mirror (every
   Help Center page serves one, §2) and requires the fragment's anchor id
   to appear in the body, so a renamed heading fails loudly instead of
   shipping a link that lands at the top of the page. Network flake
   tolerance: retry twice, and only a definitive miss (404, or a live page
   without the anchor) fails the gate — an unreachable help site must not
   block a release (the gate then warns instead).

Failing *before* GoReleaser means a missing entry costs a re-tag, never a
broken half-release. Additionally, a cheap format lint (heading grammar,
known subsection names) runs in `ci.yml` on PRs that touch `CHANGELOG.md`,
so mistakes surface before tagging.

## 7. C4 — Delivery via Scribe (deterministic, human-merged)

Decided 2026-08-23: **Scribe delivers, this repo authors.** The scribe
pipeline already owns everything about getting a change into omni — the DoiT
Docs Bot App token, draft-PR conventions, tech-docs reviewers, Slack
notification, the dashboard. Rebuilding that as a bespoke workflow here would
duplicate plumbing that exists and is operated elsewhere. Scribe's *agent*
stays out of it: authoring remains C1–C3 in this repo.

1. **Trigger**: after the release is published, `release.yml` fires a
   `repository_dispatch` (event type `dci-cli-changelog`) to `doiteng/scribe`
   carrying the tag. This slots in next to the existing downstream triggers
   (`sync-manifests.yml`, `post-release-verify.yml`).
2. **Render (in scribe)**: a new deterministic, non-agent job fetches
   `CHANGELOG.md` from this repo at the dispatched tag (the repo is publicly
   readable; the file travels by ref, not in the 64KB dispatch payload) and
   transforms it to `changelog.mdx` — Docusaurus front matter (title,
   description, sidebar position copied from a neighboring `docs/cli/` page)
   plus the body verbatim. The renderer lives in scribe because omni's page
   conventions live there; this repo owns only the prose.
3. **Deliver (in scribe)**: push the rendered file to a **fixed branch** in
   omni (`dci-cli/changelog-sync`) and open a **draft PR** to omni's `dev`
   with `doiteng/tech-docs` as reviewers — the established Scribe posture
   (always a draft, always human-merged, never auto-merge), adopted as-is.
   The PR is also **assigned to the dci-cli maintainers** — spark2ignite,
   apgiorgi, taltultc, chaim0m (CODEOWNERS' individuals; teams can't be
   assignees) — so each delivery lands on their radar (decided 2026-08-24);
   assignment is idempotent on both the create and update paths and
   best-effort per user. If the branch/PR already exists (previous
   release's delivery not yet merged), force-push the re-rendered page to
   the same branch: the render is the *whole page* from the canonical
   file, so the open PR always shows the complete current state, and
   back-to-back releases (three shipped Aug 19–21) accumulate into **one**
   PR instead of spamming tech-docs.
4. **Auth**: scribe already holds the Docs Bot App credentials for omni
   writes. The only new credential anywhere is in this repo: a minimal
   dispatch-only token able to fire `repository_dispatch` on
   `doiteng/scribe` (a fine-grained PAT with actions scope on that one repo,
   or an App-minted token — settled at implementation). No Docs Bot private
   key and no Anthropic key ever land in dci-cli; the job is deterministic —
   no agent, no prose generation, no cost, no review variance.
5. **Failure isolation**: delivery runs after the release is published;
   binaries and manifests never wait on omni or scribe. A failed delivery is
   loud (red workflow in scribe, Slack notify) and re-runnable by re-firing
   the dispatch — `release.yml`'s dispatch step, or scribe's own
   `workflow_dispatch` with a tag input, mirroring the DISTRIBUTION.md
   "Manual Re-runs" pattern.

Retroactive fixes (typo, adding a link once docs land) are ordinary `docs:`
commits to `CHANGELOG.md` + a manual re-fire of the dispatch. The scribe-side
job is a small addition to that repo, specced here but implemented there.

## 8. C6 — CLI touchpoints

Small, after the page exists:

- **`dci docs`**: add `{"Changelog", "https://help.doit.com/docs/cli/changelog"}`
  to `docsEntryPoints` (docs_command.go:10) with the matching assertion in
  docs_command_test.go:20.
- **Update notice**: the passive notice (`maybeNotifyUpdate`, update.go:63)
  gains the changelog URL so "new version available" answers the obvious next
  question — what's in it. One line total, still suppressed in agent
  mode/non-TTY per the existing decoration contract.
- The `--help` documentation footer (main.go:928) already points at
  `/docs/cli`; it stays as is — the guide links the changelog from its
  sidebar.

These are `feat:`/`fix:`-class changes shipping in a normal release — which
gives the changelog its first self-referential entry.

## 9. C5 — Backfill

Seed `CHANGELOG.md` before the first delivery so the page never publishes
near-empty:

- **v2.0.0 → current** (decided 2026-08-23 as v2.3.0; extended to the
  whole v2 major 2026-08-24): one section per release, rewritten in user
  terms from the GitHub Release bodies. v2.0.0 is the epoch the page tells
  the story of — the human-experience overhaul the Help Center's
  "before and after" page documents.
- **Before v2.0.0**: a single closing line — *"For earlier releases, see the
  [GitHub releases page](https://github.com/doitintl/dci-cli/releases)."*
- The seed is a PR the maintainer reviews line-by-line; it is the one place
  where entries are written without release-time context.

## 10. C7 — Curated GitHub Release header (optional)

`release.yml` extracts the tag's entry from `CHANGELOG.md` into a temp file
and passes it via GoReleaser's release-header mechanism, so the GitHub
Release shows the human prose first and the raw commit list below it. Nice
for the release page's direct visitors; entirely independent of the Help
Center page. P3 — do it only if the duplication doesn't annoy.

## 11. What never changes (the "never" list)

1. **Never auto-merge into omni.** Always a draft PR reviewed by tech-docs —
   the standing Scribe ground rule for Help Center writes.
2. **Never generate the page from commit subjects.** Commits are the
   inventory (§5); the published text is always authored by a person or a
   reviewed session in this repo.
3. **Never link a page or anchor that doesn't resolve** — drop the link,
   keep the bullet, add the link later.
4. **Never block or delay binaries on the Help Center.** Delivery is
   downstream of the published release, like manifests; only the C3 gate
   (this repo's own file) can stop a tag.
5. **Never leak internal vocabulary onto the page** — no restish, no spec
   codenames, no hashes (the README rule, extended).
6. **Never skip a version on the page.** Every released tag has a section,
   even if it says "no user-facing changes".

## 12. Decisions (maintainer, 2026-08-23)

1. **Slug & placement**: `/docs/cli/changelog` inside the CLI section, and
   nothing else. The site-wide "What's new" section stays a manual,
   occasional call (via the product-announcement flow) for milestone
   releases — no standing per-release obligation.
2. **Backfill depth**: seed from v2.0.0 (§9; originally v2.3.0, extended
   2026-08-24); earlier history collapses to
   the GitHub-releases link.
3. **Delivery mechanism**: Scribe delivers, this repo authors (§7). The
   originally-drafted self-contained `sync-changelog.yml` (with Docs Bot
   secrets copied into dci-cli) was considered and rejected — it duplicates
   omni plumbing scribe already operates. Full Scribe *authoring* (agent
   writes the entry in omni from release commits) was also rejected: it
   kills the no-release-without-entry gate and loses release-time authoring
   context. Consequently the old sequencing question against the pending
   Scenario-A branch is moot — the changelog work shares no secrets with it
   and neither blocks the other.
4. **Heading cosmetics**: `## v2.5.2 — August 21, 2026` as drafted — the
   `v` prefix matches tags, the releases page, and CLI output; spelled-out
   dates fit a customer-facing docs site.
5. **omni-side sign-off**: not a precondition. The draft-PR +
   `doiteng/tech-docs`-reviewers flow is established Scribe practice,
   adopted as-is; if tech-docs objects to the fixed-branch force-push
   cadence once it's running, adjust then.
