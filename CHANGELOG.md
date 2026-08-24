<!--
This file is the canonical, customer-facing changelog for the dci CLI. It is
rendered onto https://help.doit.com/docs/cli/changelog on each release
(delivery pipeline: CHANGELOG-SPEC.md §7). Writing rules (CHANGELOG-SPEC.md §5):

- One `## v<X.Y.Z> — <Month D, YYYY>` section per released tag, newest first.
  Subsections: `### New`, `### Improved`, `### Fixed` (omit empty ones). A
  release with nothing user-facing gets the single line
  "Maintenance release; no user-facing changes."
- Describe what the user can do or will notice — not what the code does.
- Name commands and flags exactly as typed (`dci update`, `--chart`).
- No commit hashes, PR numbers, spec codenames, library names, or file names.
- One bullet per user-visible change; merge a feature and its same-release
  follow-up fixes into one bullet.
- Link bullets to the Help Center page documenting the feature, as absolute
  https://help.doit.com/... URLs. Only link pages and anchors that resolve —
  the release gate (.github/changelog/check-changelog.sh) verifies both.
- Changelog edits are `docs:` commits, so they stay out of the raw
  commit changelog GoReleaser puts on the GitHub Release.
-->

# dci CLI changelog

What changed in each release of the DoiT Cloud Intelligence CLI. To get the
latest version, run `dci update`, or see
[install and update](https://help.doit.com/docs/cli#download-and-install) in
the CLI guide.

## v2.5.2 — August 21, 2026

### New

- `dci update` updates the CLI in place on every platform, aware of how you
  installed it: Homebrew, Scoop, and WinGet installs run the package
  manager's own upgrade after a confirmation; Linux `.deb`/`.rpm` installs
  download, checksum-verify, and install the new package; standalone
  binaries replace themselves atomically. `dci upgrade` is now an alias.
  ([Keep it up to date](https://help.doit.com/docs/cli#update))
- `--chart` renders stacked column charts per group by default (largest
  segments on top), `--chart=line` keeps the line of period totals, and
  charts follow your report's color theme.
  ([Table output options](https://help.doit.com/docs/cli#table-output-options))
- `dci login` opens DoiT Cloud Intelligence-branded browser pages, and the
  success page's suggested command is clickable — click it to copy and run
  your first command.
  ([Authentication](https://help.doit.com/docs/cli#authentication))

### Fixed

- Pivot tables hide rows whose values are all zero.
- A rejected token (401) now names the API base that rejected it, so
  pointing at the wrong environment is obvious.
- `DCI_API_BASE_URL` applies only to the invocation it is set on — it is
  never silently persisted into your saved configuration.
  ([Environment variables](https://help.doit.com/docs/cli#environment-variables))
- The CloudFlow import command correctly requires its idempotency key
  up front instead of failing on the server.

## v2.5.1 — August 20, 2026

### New

- Interactive prompts in human mode: a fuzzy picker when a command needs a
  resource you didn't name, a selection menu when a name matches more than
  one resource, and a default-Cancel confirmation before destructive
  commands. Pipes, scripts, and agents never see a prompt.
  ([Resource names and pickers](https://help.doit.com/docs/cli/cheatsheet#names))
- An interactive table viewer and query builder: explore results
  full-screen, sort and filter columns, and compose report queries
  step by step.
- A spinner shows while an API request is in flight, and a compact one-line
  hint lists columns hidden from the current table.

### Improved

- The destructive-command confirmation shows the target's name, owner, and
  description — you see exactly what you're about to delete.
  ([Destructive commands](https://help.doit.com/docs/cli#destructive-commands))
- `dci login` got visual polish, budgets render utilization bars, and the
  update notice is styled instead of plain text.

### Fixed

- Report tables always pivot for humans — the previous 14-period cutoff
  that switched wide results to flat rows is gone (`--flat` still opts
  out). ([Report results](https://help.doit.com/docs/cli#report-results))

## v2.5.0 — August 19, 2026

### New

- `--all` fetches and merges every page of a paged list response, so you
  get the complete list without chasing page tokens.
- `--drop-unlabeled-rows` excludes report rows whose group labels are all
  null — only rows with no label at all are dropped, never partially
  labeled ones.

### Improved

- `dci customer-context set` accepts the customer's URL display name as it
  appears in DoiT Console URLs, alongside domains and customer IDs.
  ([Customer context](https://help.doit.com/docs/cli#customer-context))
- Flat report rows omit datetime dimension columns that duplicate the
  timestamp column.
- Paged lists tell you when a page token was dropped, and `--max-results`
  values beyond the server's cap fail up front with the real limit.

## v2.4.0 — August 18, 2026

### New

- Event timestamps in table output display in your local timezone; report
  period columns stay UTC because they label billing buckets, not moments.
  ([Timestamps and timezones](https://help.doit.com/docs/cli#timestamps))

### Fixed

- When a resource name can't be resolved, the command falls back to your
  argument as typed instead of failing the lookup.
- Table rows come from the command's primary collection, not from
  sideloaded arrays that happened to be in the response.

## v2.3.1 — August 18, 2026

### Fixed

- `list-tickets` renders its curated view for both response shapes the API
  returns.
  ([list-tickets](https://help.doit.com/docs/cli/generated/command-groups/support-requests/list-tickets))

## v2.3.0 — August 18, 2026

### New

- Curated default table views for eighteen more list commands (reports,
  budgets, alerts, and others): the most useful columns lead, and ids
  resolve to names — a report's owner, a role's name — instead of raw
  identifiers. Machine formats and explicit `-C`/`--fields` selections
  keep the raw fields.
  ([Output formats](https://help.doit.com/docs/cli#output-formats))

### Fixed

- Terminal hyperlinks in cells no longer shear table column alignment.

---

For releases before v2.3.0, see the
[GitHub releases page](https://github.com/doitintl/dci-cli/releases).
