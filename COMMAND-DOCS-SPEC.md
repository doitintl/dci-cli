# Design spec: curated command docs — examples, notes, and a usable reference page

Status: **draft for maintainer review**. Scope decisions 1–4 and 6 were taken
2026-09-03 (§11); decision 5 (delivery) is recommended here and open for
confirmation.

Audited at commit `a842a1f` (dci-cli), omni `dev`, scribe `main`; every claim
cites the file it is based on.

Scope: every `dci` command — the 193 API operations rendered on
help.doit.com today, the local commands (`status`, `query`, `open`, …), and
every operation the API adds in the future — gets curated, validated usage
examples and CLI-behavior notes that render identically in `dci <cmd> --help`,
in `dci commands --json`, and on the Help Center command page. The Help
Center page template is reworked so a write command reads as a command, not
as a JSON Schema dump.

---

## 1. Summary

| # | Feature | One line | Phase |
|---|---------|----------|-------|
| D1 | Command doc files | One `command-docs/<command>.yaml` per command: examples, argument notes, CLI-behavior notes, related commands. Embedded in the binary | P1 |
| D2 | `--help` examples | Curated examples replace restish's synthesized (and sometimes invalid) shorthand example in `dci <cmd> --help` | P1 |
| D3 | Catalog export | `dci commands --json` carries the curated examples and notes per command (additive, schema version unchanged) | P1 |
| D4 | Validator | Every example is parsed and checked against the live OpenAPI spec in CI: command exists, flags exist, body fields and enum values are valid, positional arity matches | P1 |
| D5 | Coverage gates | PR CI reports uncovered commands; a scheduled drift job opens a PR with scaffolded stubs when the API adds operations; the release gate fails when a GA operation ships without a curated example | P3 |
| D6 | Help Center page rework | omni's generator renders six sections — synopsis, examples, request fields table, flags table, output, related — from the spec plus the delivered doc files | P2 |
| D7 | Delivery | Scribe's existing dci-cli delivery job ships `command-docs/` into omni alongside the changelog, same fixed branch and draft PR | P2 |
| D8 | Authoring | All 193 API commands and the local commands get curated files; the 19 omni `command-notes` fold into them | P1–P2 |

The user-visible outcome for
[patch-anomaly](https://help.doit.com/docs/cli/generated/command-groups/anomalies/patch-anomaly):
the page opens with three runnable commands (move to under review, resolve
with a reason, resolve from a JSON file), says where the anomaly ID comes
from, shows the request as a fields table with a required column, and links
to `get-anomaly`, `list-anomalies`, and the API reference. `dci patch-anomaly
--help` shows the same three commands.

## 2. Current state (what already exists)

**The reference page is a straight render of the OpenAPI operation.** omni's
help-website build (`.github/workflows/help-website.yaml:93–97`) runs
`.github/workflows/actions/generate-cli-docs/scripts/generate-cli-docs.mts`
over `services/external-api/openapi.yaml` on every `dev` push that touches
the spec or the docs, writing `docs/cli/generated/command-groups/<tag>/<op>.mdx`
through `templates/command-page.mdx.liquid`. The template renders: usage line,
operation description, an optional `cliNotes` overlay, the request as
Content-Type + optional JSON example + **raw JSON Schema**, flags as bare
`--name: (type)` lines, the output as a fields table plus collapsed schema,
and an error table.

**The only hand-written hook is `command-notes/<route>.mdx`** — injected
verbatim after the description (`loadCommandNotes`, generate-cli-docs.mts).
19 exist, all `:::info CLI default view` notes for list commands, each
carrying a "keep in sync with list_views.go" comment. dci-cli points back
at them from `list_views.go:22–24` and `response_transform.go:141–143`.

**Coverage today** (prod spec, 2026-09-03):

| | Count |
|---|---|
| Operations | 193 |
| Operations taking a request body | 72 |
| Body operations with a spec `example` | 10 |
| Operations with query flags | 49 |
| Operations with a success-response example | 15 |
| Hand-written command notes | 19 |
| `x-cli-name` / `x-cli-aliases` overrides | 24 |

So 62 write commands ship with no example anywhere on the page, and the 10
that have one show a JSON blob, not a CLI invocation.

**The CLI's own example is synthesized and can be wrong.** restish builds
`cobra.Command.Example` from the operation's request examples
(`cli/operation.go:51`), and when the spec has none it generates one from the
schema (`openapi/openapi.go:204`, `GenExample` — first enum value per field,
`"string"` for free text). For `patch-anomaly` that yields
`customerFeedback{resolution: ANOMALY_CONFIRMED, resolutionNote{comment: string,
reason: FAULTY_ANOMALY_DETECTION_MODEL}, reviewStatus: NEEDS_REVIEW}` — a body
the API rejects (`resolution` is forbidden unless `reviewStatus` is
`RESOLVED`) with a reason from the wrong family. Generation cannot satisfy
conditional rules; only curation can.

**Plumbing that already exists and is reused here:**

- Help rendering hook: `cli.Root.SetHelpFunc` in main.go (`:1620–1660`)
  already mutates the command before the default help runs
  (`sanitizeFlagPlaceholders`, `augmentVerifiedFlagHelp`,
  `appendFlagExamples` from `help_context.go`). Curated examples slot in
  there.
- Machine catalog: `dci commands --json` (`command_catalog.go`) already emits
  a `body` argument with an `example` (the synthesized shorthand) and was
  extended additively before (`stage`, `early_access`).
- Body validation: `body_validation.go` parses shorthand with restish's exact
  parse options (`bodyShorthandParseOptions`, `:160`) and checks top-level
  fields and array shapes against the request schema. The validator (D4)
  reuses it.
- Embedded generated inputs: `beta/openapi.beta.yaml` is `go:embed`-ded and
  refreshed by a scheduled drift PR (`refresh-beta-spec.yml`, BETA-SPEC.md
  §2.3). D5's drift job is the same pattern.
- Release gate precedent: `release.yml` fails when `CHANGELOG.md` lacks the
  tag's entry (CHANGELOG-SPEC.md §6). D5's release gate is the same shape.
- Delivery to omni: scribe's `dci-cli-changelog.yml` fires on
  `repository_dispatch` from `release.yml:55–69`, fetches a file from this
  public repo by ref, renders, force-pushes the fixed omni branch
  `dci-cli/changelog-sync`, and opens or updates one draft PR to omni `dev`
  reviewed by tech-docs and assigned to the maintainers (CHANGELOG-SPEC.md
  §7). D7 extends that job rather than adding a second one.

## 3. D1 — Command doc files

One YAML file per command in `command-docs/`, named by the CLI command name
(the `x-cli-name` when present, else the kebab-cased operationId — the same
`routeName` omni's generator uses, so the two sides key identically).
Embedded with `//go:embed command-docs/*.yaml` in a new chapter file
`command_docs.go` (+ `_test.go`), per the one-file-per-chapter rule.

```yaml
# command-docs/patch-anomaly.yaml
command: patch-anomaly

# Optional. Rendered under the synopsis on the web and as an "Arguments:"
# block in --help. Keyed by the positional argument name from the spec.
arguments:
  id: >-
    Anomaly ID as shown by `dci list-anomalies` or `dci anomalies-recent`.
    Anomalies have no names, so name resolution does not apply.

# Required. Two to four, ordered from the common case to the edge case.
# `command` is the exact line a user types. `description` is one sentence.
# `output` is optional: what the user sees, or how to verify.
examples:
  - description: Take an anomaly into review.
    command: dci patch-anomaly <anomaly-id> customerFeedback.reviewStatus: UNDER_REVIEW
  - description: Resolve it as a confirmed anomaly with a reason and a comment.
    command: >-
      dci patch-anomaly <anomaly-id>
      customerFeedback{reviewStatus: RESOLVED, resolution: ANOMALY_CONFIRMED,
      resolutionNote{reason: MISCONFIGURATION, comment: "Autoscaler max raised by mistake"}}
    output: Exit code 0 and no output; `dci get-anomaly <anomaly-id> --output json` shows the new status.
  - description: Dismiss it as not an anomaly, with the body read from a file.
    command: dci patch-anomaly <anomaly-id> < feedback.json
    output: |
      # feedback.json
      {"customerFeedback": {"reviewStatus": "RESOLVED", "resolution": "NOT_ANOMALY",
        "resolutionNote": {"reason": "EXPECTED_COST_SPIKE"}}}

# Optional. MDX, rendered on the web page right after the description —
# this is where the 19 omni command-notes move. Not rendered in --help.
notes: |
  :::tip Review workflow
  `resolution` is required when `reviewStatus` is `RESOLVED` and rejected
  otherwise; `resolutionNote.reason` must belong to the chosen resolution.
  The CLI validates field names locally; the API validates these rules.
  :::

# Optional. Defaults to the other commands in the same group.
related: [get-anomaly, list-anomalies, anomalies-recent]
```

Rules:

- **`<placeholder>` tokens** (`<anomaly-id>`, `<report-id>`) are the
  placeholder convention, matching the cheatsheet and skill files today. The
  validator treats a token matching `^<[a-z][a-z0-9-]*>$` as a positional
  placeholder; `<` followed by a space and a filename is stdin, exactly as the
  shell reads it. The two forms cannot be confused.
- **Shorthand forms**: dotted (`a.b: v`) for one or two fields, braced
  (`a{b: v, c: w}`) for nested bodies, `< file.json` for anything long enough
  that a reader would rather see JSON. Both shorthand forms parse under
  restish's options (verified with `shorthand.Unmarshal`,
  `EnableObjectDetection: true`).
- **Flag examples** for GET commands use real-looking values in the flag's
  declared type; where the spec carries a parameter `example`, prefer it
  (`help_context.go` already surfaces those in `--help`).
- **No output pasting** of live customer data. `output` is either a
  described outcome or a sanitized shape.
- **Local commands** (`status`, `login`, `query`, `open`, `customer-context`,
  `skill`, `update`, `anomalies-recent`, `budgets-at-risk`, `beta …`) get
  files too — they feed `--help` and the catalog. They are not on the
  generated reference (that surface is API operations only, documented
  hand-written in the CLI guide), so their `notes` and `related` are
  ignored by omni.
- **Deprecated operations** keep a file (one example plus a `notes`
  pointing at the replacement) — the page still exists.

## 4. D2 — `--help` rendering

In the `SetHelpFunc` hook (main.go `:1620`), after `appendFlagExamples`:

1. Look up the command's doc file by `cmd.Name()`. If found, set
   `cmd.Example` to the curated examples in cobra's two-space indented
   format, one `# description` comment line above each command, and
   restore the original after `defaultHelp` returns (the same
   save-and-defer pattern `terseHelpText` uses).
2. If `arguments` is present, insert an `Arguments:` block into the usage
   template between `Aliases:` and `Examples:` (template at main.go `:1405`).
3. If not found, leave restish's synthesized example in place — it is the
   transitional fallback until D5 closes the gap.

`--help-full` is unchanged: its `## Input Example` JSON and request schema
come from restish's long description and stay as the exhaustive view.

Agent mode: help output is already plain text; no change.

## 5. D3 — Catalog export

`commandCatalogEntry` gains:

```go
Examples []commandCatalogExample `json:"examples,omitempty"`
Notes    string                  `json:"notes,omitempty"`    // MDX, verbatim
Related  []string                `json:"related,omitempty"`
```

with `commandCatalogExample{Description, Command, Output string}`. The
`body` argument's synthesized `example` is replaced by the first curated
example's parsed body (JSON) when a file exists. Additive: `Version` stays
`"1"`, matching the precedent set by `stage`/`early_access`.

Skill files (`skills/dci-cli/references/examples.md`) are not changed by
this spec; generating them from the same files is a natural follow-up and
is listed in §12.

## 6. D4 — Validator

`command_docs_test.go` plus `tools/commanddocs` (a `go run` tool in the
`tools/betaspec` mold: `check`, `scaffold`, `coverage`). Checks per file:

| Check | Source of truth |
|---|---|
| File name equals `command:` and names a real command | live spec (API ops) or the cobra tree (local) |
| Every `related` entry is a real command | same |
| Every `arguments` key is a positional parameter of the command | spec path params |
| Example's first token is `dci`, second is the command or an alias | spec `x-cli-aliases` + restish alias rule |
| Flags exist: operation flags (`--kebab-name` of query params) or CLI-wide flags | spec + the cobra flag set of the built command |
| Positional arity: placeholders + literals equal the path-param count | spec |
| Body shorthand parses under `bodyShorthandParseOptions` | `body_validation.go` |
| Body top-level fields are known; array-typed fields hold arrays | `body_validation.go` (existing) |
| Enum-typed leaf values are members of the enum; required top-level fields present | new walk over the dereferenced request schema |
| `< file` appears only on body-taking commands | spec |
| `notes` has balanced admonition fences and no unescaped `{`/`<` outside code (scribe's `findMdxUnsafeLines` rule) | local |

The validator needs the prod spec. `go test ./...` stays offline: the test
skips unless `DCI_COMMAND_DOCS_SPEC` points at a spec file. CI (`ci.yml`)
adds a job that downloads `https://api.doit.com/openapi.yaml` (public, no
auth) and runs the test with it, mirroring how `refresh-beta-spec.yml`
already reaches the public dev spec.

What the validator cannot check: conditional rules expressed only in prose
(`resolution` required iff `RESOLVED`). Those are the author's job, which is
the whole reason curation replaces generation.

## 7. D5 — Coverage gates

Three gates, escalating:

1. **PR CI** (`ci.yml`): `commanddocs coverage` prints uncovered commands as
   a `::warning::` annotation and the count in the job summary. Never fails
   a PR — unrelated PRs must not go red because the API grew.
2. **Scheduled drift** (`refresh-command-docs.yml`, weekly plus
   `workflow_dispatch`, clone of `refresh-beta-spec.yml`): fetch the prod
   spec, run `commanddocs scaffold` for operations without a file, and open
   one `chore:` PR with the stubs. A stub carries the spec-derived
   `arguments`, `related`, and **one** example generated from the schema and
   marked `draft: true`; drafts render on the web page with no examples
   section and are excluded from `--help` (restish's fallback stays). The
   PR body lists what to fill in. This is how `patch-anomaly` would have
   surfaced within a week of the backend release, with a checklist attached.
3. **Release gate** (`release.yml`, before GoReleaser, next to the changelog
   gate): fail when any GA operation in the prod spec lacks a file or has
   only a `draft` example. Beta operations (`beta/manifest.yaml`) are
   exempt until promotion. The failure names the commands; the fix is
   authoring, not tagging around it. Trade-off: a backend team can block
   the next CLI release by shipping an endpoint. That is intended — the CLI
   "picking it up" now includes its docs — and the drift PR (2) gives a
   week's warning in the normal case.

## 8. D6 — Help Center page rework (omni)

Changes to `generate-cli-docs.mts`, `command-page.mdx.liquid`, and
`generate-cli-docs.test.mjs`; a PR to omni by the dci-cli maintainers, in
the cli-spec-lint path filter so the generator tests run.

**Inputs**: the spec (as today) plus `--docs <dir>` pointing at the
delivered `command-docs/` YAML (replaces `--notes`; `js-yaml` is already a
dependency). `command-notes/` is deleted once the 19 notes are folded (D8).
A doc file with no matching operation is a `::warning::` annotation, as
orphaned notes are today.

**Page**, top to bottom:

1. **Title + summary** (unchanged) and a **synopsis** block:
   `dci patch-anomaly <id> [body]` for body commands, `… [flags]` for
   flag commands, both when both. Under it, one line per positional
   argument from `arguments` (falling back to the spec parameter
   description), and for body commands a fixed sentence: "Pass the body as
   `name: value` arguments or pipe JSON on stdin — see
   [Command structure](/docs/cli#command-structure)." (the guide section
   that documents both forms today; no new anchor needed).
2. **Examples**: one fenced `bash` block, `# description` above each
   command, `output` rendered as a following plain block when present.
   Drafts render nothing here.
3. **Request** (body commands): a **fields table** — Field, Type,
   Required, Description — produced by the same `addFieldRows` walk the
   output section uses, with a Required column from the schema's `required`
   arrays and `readOnly` fields skipped. Enum members and defaults stay in
   the description cell as today. Raw schema moves into a collapsed
   `<details>`, exactly like the output section. The spec `example`, when
   present, stays as a JSON block titled "Example body (JSON)".
4. **Flags** (flag commands): a table — Flag, Type, Default, Example,
   Description — replacing the bare lines. Example comes from the parameter
   `example`. One trailing sentence links to the guide's
   [Output formats](/docs/cli#output-formats) and
   [Table output options](/docs/cli#table-output-options) sections for the
   CLI-wide flags instead of listing them on every page.
5. **Output** and **Errors**: unchanged, errors table wrapped in
   `<details>` since it is identical on nearly every page.
6. **Related**: `related` from the file, else the other commands in the
   same tag, each linked; plus "API reference:
   https://developer.doit.com/reference/<operationId lowercased>" — the URL
   pattern the developer portal uses (e.g. `/reference/patchanomaly`).

The `notes` MDX renders after the description, where `cliNotes` renders
today. The group index (`tag-index.mdx.liquid`) gains each command's first
example under its summary so a group page is scannable.

`llms-cli.txt` and the `.md` mirrors pick all of this up automatically
(CHANGELOG-SPEC.md §2).

## 9. D7 — Delivery (decision 5: what exists, and the recommendation)

Three routes were checked against what runs today:

| Route | Exists today | Fit |
|---|---|---|
| **Scribe delivery job** (`dci-cli-changelog.yml`): dispatch from `release.yml`, fetch from this public repo by ref, render, fixed branch, draft PR to omni `dev`, tech-docs review, maintainer assignment, Slack on failure | Yes, live for the changelog | Reviewed by tech-docs before publish; omni's build stays hermetic; already fires per release |
| omni generator fetches `command-docs/` from GitHub at build time | No. The help-website runner (`gke-runner-help-website`) does reach GCS, so egress is plausible | Unreviewed content would publish directly; the build is triggered by omni commits, not dci-cli releases, so there is no trigger; adds an external network dependency to every docs build |
| GoReleaser publishes a `command-docs.json` release asset | Not configured (`.goreleaser.yaml` has no `extra_files`) | Useful as a versioned machine artifact for third parties; does not by itself get anything into omni |

**Recommendation: extend the scribe job.** Rename it to "dci CLI docs
delivery" (event type unchanged, `dci-cli-changelog`, so `release.yml`
needs no edit), and add one step: download the repo tarball at the
**released tag** (`https://api.github.com/repos/doitintl/dci-cli/tarball/<tag>`,
public), copy `command-docs/` into
`omni/.github/workflows/actions/generate-cli-docs/command-docs/`, and stage
it in the same commit as the changelog. Reading at the tag, not `main`,
keeps the web page identical to what the released binary's `--help` shows.
The changelog keeps reading at `main` for the reason documented in that
workflow. One fixed branch, one draft PR per release cycle, the same
reviewers — nothing new for tech-docs to learn.

Optionally, also publish `command-docs.json` (the catalog's `examples`
and `notes` for every command) as a GoReleaser release asset for
programmatic consumers. Cheap, independent, and not required for the page.

## 10. D8 — Authoring plan

193 API commands plus roughly 20 local commands. Split by effort:

| Bucket | Count | Approach |
|---|---|---|
| GET with flags (`list-*`, searches) | ~49 | Scaffold from the spec: default call, one filtered call using flag examples, one `-C`/`--output json` call. Curate the filter semantics |
| GET by id (`get-*`) | ~70 | Scaffold: `dci get-x <x-id>`, `--output json`, and "find the id with list-x". Mostly accept as generated |
| Body commands (`create/update/patch/…`) | 72 | Hand-curated, two to four examples each; the 10 with spec examples convert directly. Pair with the owning API team's PR description where one exists (the Slack release note for `patch-anomaly` is a good source) |
| Local commands | ~20 | Lift from the cheatsheet and `skills/dci-cli/references/examples.md`, which already carry them |
| Existing 19 notes | 19 | Move verbatim into `notes:`; drop the "keep in sync" comments in favor of the pointer in `list_views.go` |

Drafting is agent-assisted from the spec descriptions and the skill
references; every file is human-reviewed before its `draft: true` comes
off, and every file passes D4. Land in PRs per command group so review
stays tractable (about 40 groups; several per PR).

## 11. Decisions (maintainer, 2026-09-03)

1. **Source of truth**: option B — curated files in this repo, embedded in
   the binary. The spec (`x-cli-examples`) was rejected because API teams
   own it and it cannot cover local commands; omni command-notes were
   rejected because they give no `--help` parity and no validation.
2. **Coverage**: all existing commands and every future one — hence the
   release gate (§7.3), not just a warning.
3. **Surfaces**: both the Help Center page and `dci --help`.
4. **Page rework**: all six sections in §8.
5. **Delivery**: recommended — extend scribe's existing job (§9). Open for
   confirmation.
6. **Command notes**: fold the 19 omni `command-notes` into the per-command
   files; delete the omni directory once delivered.

## 12. Out of scope, noted for later

- Generating `skills/dci-cli/references/examples.md` and the cheatsheet's
  per-command lines from `command-docs/` (single source for agents too).
- Rendering `notes` in `--help` (needs an MDX-to-plain-text pass).
- Live smoke-running GET examples against a dev tenant in CI.
- Curating the spec's own success-response examples (omni's
  `lint-spec-cli-friendliness.mjs` already reports that coverage: 15 of
  the eligible operations).

## 13. Phases

| Phase | Deliverables | Where |
|---|---|---|
| P1 | D1 loader + embed, D2 help rendering, D3 catalog, D4 validator + CI job, scaffold tool, first files (Anomalies group, all local commands) | dci-cli |
| P2 | D6 generator/template rework reading `command-docs/`; D7 scribe step; D8 authoring in group-sized PRs; delete `command-notes/` | omni, scribe, dci-cli |
| P3 | D5 drift workflow and release gate, switched on once P2 authoring reaches 100% of GA operations | dci-cli |
