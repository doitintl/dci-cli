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

## v2.7.5 — September 4, 2026

### New

- `dci export-datahub-dataset-records` exports the records of a DataHub
  dataset, and the CLI prints that export as the file it is — CSV, or
  newline-delimited JSON with `--format jsonl` — instead of an escaped
  string. See
  [export-datahub-dataset-records](https://help.doit.com/docs/cli/generated/command-groups/datahub/export-datahub-dataset-records).
- `--all` exports a whole dataset in one command — `dci
  export-datahub-dataset-records <dataset> --all --output-file records.csv`,
  no dates needed. The API returns one page per request, accepts at most 366
  days per request, and requires both time bounds; `--all` finds the range the
  dataset actually covers, follows the pages, walks the successive time
  windows, and merges everything into one export with a single header row
  (pages can carry different label and metric columns, so the columns are
  unioned rather than concatenated). Without
  `--all`, a partial export now says so on stderr and prints the
  `--page-token` to continue from, and a window wider than 366 days is
  rejected before the request with a pointer to `--all`.
- `--output-file` (`-O`) writes any command's output to a file instead of the
  terminal. Pass a path, or a directory (`--output-file .`) to save under the
  filename the API suggests.
- `--for-reimport` rewrites an exported CSV into the format
  `dci ingest-datahub-events-csv` accepts — dropping the export-only `batch`,
  `source`, `export_time` and `updated_by` columns and renaming `event_id` to
  `id` — so records can be copied from one dataset to another. See
  [ingest-datahub-events-csv](https://help.doit.com/docs/cli/generated/command-groups/datahub/datahub-events-csvfile).

### Improved

- `dci list-datahub-datasets` shows a **display name** column when at least
  one dataset has one, so the name shown in the console and in report results
  — set with
  [update-datahub-dataset](https://help.doit.com/docs/cli/generated/command-groups/datahub/update-datahub-dataset)
  — is visible in the CLI too. Datasets displayed by their name keep the
  previous three columns.
- `--page-token` and `--max-results` now fail with a clear message on commands
  that return their whole response in one request, instead of accepting the
  value and ignoring it. `--all` still works everywhere and simply notes when
  there was nothing more to fetch.
- Leaving the dataset name off a DataHub command now opens the same
  filter-as-you-type picker the other commands have, instead of a usage
  error: `dci export-datahub-dataset-records`,
  [get-datahub-dataset](https://help.doit.com/docs/cli/generated/command-groups/datahub/get-datahub-dataset),
  [delete-datahub-dataset](https://help.doit.com/docs/cli/generated/command-groups/datahub/delete-datahub-dataset),
  and
  [update-datahub-dataset](https://help.doit.com/docs/cli/generated/command-groups/datahub/update-datahub-dataset)
  (there, write the fields and let the picker supply the name:
  `dci update-datahub-dataset description: "New description"`). Dataset names
  also complete on Tab and resolve from a partial name now — previously none
  of this worked for datasets, because they have no separate ID.
- The picker and Tab completion reach commands that act on a sub-resource, so
  `dci get-report-config`, `dci export-cloudflow-flow` and their siblings take
  a name where they previously took only an ID. Commands whose identifier is
  a number, such as `dci get-ticket`, no longer offer names they cannot
  accept.

## v2.7.4 — September 4, 2026

### New

- `dci ingest-datahub-events-csv` now uploads a CSV file (or a GZ/ZIP archive
  of one) to a DataHub dataset from the command line:
  `dci ingest-datahub-events-csv provider: <dataset>, file: @events.csv`.
  The `@` attaches the file by name; a file over the API's 30 MB limit, a
  missing file, or a forgotten `@` is reported with a fix before anything is
  sent. Previously the command failed with an internal marshaling error. See
  [ingest-datahub-events-csv](https://help.doit.com/docs/cli/generated/command-groups/datahub/datahub-events-csvfile).

## v2.7.3 — September 4, 2026

### New

- `dci <command> --help` now shows curated, tested usage examples and a
  description of each positional argument instead of an example synthesized
  from the API schema. The first commands covered are the anomaly commands —
  [patch-anomaly](https://help.doit.com/docs/cli/generated/command-groups/anomalies/patch-anomaly),
  [get-anomaly](https://help.doit.com/docs/cli/generated/command-groups/anomalies/get-anomaly),
  [list-anomalies](https://help.doit.com/docs/cli/generated/command-groups/anomalies/list-anomalies),
  and `dci anomalies-recent` — and the local commands (`dci status`, `dci
  query`, `dci open`, `dci update`, `dci skill`, and others). Every example
  is checked against the live API before a release ships, so a command line
  shown in help is one the API accepts. The remaining commands keep their
  previous help until their examples land.
- `dci commands --json` carries the same examples, page notes, and related
  commands for each command, so agents and scripts can read them without
  parsing help text.

## v2.7.2 — September 4, 2026

### New

- DoiT employees no longer need an Anthropic API key for the AI session:
  once signed in with `dci login`, `dci ai` runs on DoiT-provided access —
  short-lived tokens fetched automatically with your existing DoiT sign-in,
  refreshed behind the scenes, never written to disk. An explicit key (the
  `ANTHROPIC_API_KEY` environment variable, or a key saved with `/key set`)
  still takes precedence, and `DCI_AI_PROVIDED=off` opts out.
- `dci budgets-at-risk` and `dci anomalies-recent` answer two common
  questions directly — which budgets are currently at risk, and what
  anomalies appeared recently (bounded with `--window`) — without
  hand-building the filter and sort on `dci list-budgets` or
  `dci list-anomalies` each time.
- `--chart=treemap` draws each group's share of a report's total as
  proportional rectangles in the report's colors, with exact shares in the
  legend. It needs no time axis, so it also charts single-period reports
  the other chart modes cannot.
- The AI session's input now shows a command's remaining arguments as faint
  ghost text — path parameters first, then required body fields — consumed
  as you type real values. Tab accepts what the ghost offers next: an empty
  pickable slot opens the name picker (the ghost says so: "enter to pick
  from a list"), a value slot shows a hint from the command's documented
  example, and the next body field inserts its `name:` prefix for you.
- Array body fields guide and guard: the ghost shows the literal syntax to
  type (`tags: [a, b]`), Tab carries the opening bracket, and the line is
  checked before submission — a plain value where the command expects a
  list is rejected with the corrected line to copy
  ("did you mean: tags: [prod, billing]").
- The bundled agent skill now teaches
  [CloudFlow](https://help.doit.com/docs/operate/cloudflow/intro) flow
  authoring: agents can build or refine flows from a natural-language request
  with `dci build-cloud-flow` and `dci refine-cloud-flow`, or author a flow
  bundle by hand through the export → edit → dry-run → import loop, with
  guardrails that keep generated flows honest (clone real flows instead of
  guessing node parameters, validate with a dry run, land as drafts). Run
  `dci skill update` to refresh installed copies.
- The skill also teaches verifying a flow's *behavior*, not just its
  structure: deep-validate a draft with `dci test-run-cloudflow-flow
  --dry-run`, run it once as a test (excluded from run history and
  statistics, approvals never bypassed), and read what every node consumed
  and produced with `dci get-cloudflow-flow-run` — so an agent can fix a
  flow from evidence instead of asking you to check the Console.

### Improved

- `dci build-cloud-flow` and `dci refine-cloud-flow` now answer with a
  readable result — the created flow's ID, the conversation ID for follow-up
  refinements, the builder's reply, and the build steps that ran — instead of
  the raw event stream. Both commands also accept the family-consistent
  spellings `dci build-cloudflow` and `dci refine-cloudflow`.
- `dci list-cloudflows` shows each flow's `id` first — the value
  `dci export-cloudflow-flow` and `dci refine-cloud-flow` need.
- Table output for humans now lands the rows you came for nearest the
  prompt: lists sort newest-last, insights rank top-savings-last, and
  report groups put the largest spenders just above the TOTAL row.
  Configurable with `--output-order`, the `DCI_OUTPUT_ORDER` environment
  variable, or `dci config`; JSON/YAML/CSV, agent mode, and piped output
  keep the classic ordering.
- When a command's output is taller than the terminal, a dim hint next to
  the prompt tells you there's more ("↑ N more lines above — scroll up").
- `--help` shows more of what the API documents: command categories carry
  a one-line description, and flags with a documented example show it.
- `dci beta run-report` resolves report names like the rest of the CLI: a
  typed name resolves to its ID, multi-word names work unquoted, and zero
  arguments open the interactive report picker — in the shell and in the
  AI session alike.
- When a command run from the AI session fails on wrong arguments, the
  result now ends with the command's one-line usage, so you can fix the
  call without leaving the session.

### Fixed

- `dci get-cloudflow-flow-run` prints a failed run in full — status,
  per-node results, and the failure message — instead of collapsing it
  into an error envelope that hid exactly the detail needed to diagnose
  the run.
- In the AI session: Enter with the completion popup open submits the
  fully typed command instead of a similarly named suggestion, and
  completion continues past a group name (`/beta run` completes to
  `beta run-report`). A logged-out session now points at `/login` instead
  of telling you to leave the session and run `dci login`.
- The sign-in success page's click-to-run suggestion no longer fights the
  terminal for keystrokes, and Esc cancels pickers and confirmation
  prompts in the shell, matching the AI session.
- `dci get-report --chart` renders the chart next to the prompt instead of
  above a long table, where it scrolled out of view.

## v2.7.1 — August 26, 2026

### New

- `/key` in the AI session manages your Anthropic API key: on its own it
  shows which key is in use (masked) and where it comes from — including
  when the `ANTHROPIC_API_KEY` environment variable overrides a saved key —
  `/key set` replaces it through the guided entry, and `/key clear` removes
  it. No more hand-editing the settings file.
- Your first-ever session opens with the full `/help` text right after the
  banner, so you don't have to discover the slash-command grammar by
  accident. Later sessions keep the compact banner.

### Improved

- `dci beta` commands complete in the session's popup and run like every
  other `/` command, and a bare `/beta` lists the early-access commands the
  way you'd type them in the session.
- `/exit` now appears in the command popup and in `/help` alongside `/quit`
  (it has always worked as an alias).
- When the session has no API commands to offer, the banner says so
  ("API commands appear after /login") instead of just showing a small
  command count.

### Fixed

- The interactive session shows the full command set again. In v2.7.0 every
  session opened with only the built-in commands — no API commands,
  whatever your sign-in state — and restarting or signing in again couldn't
  bring them back.
- `/login` works inside the session: it hands the browser sign-in your real
  terminal, then picks up your commands and identity live once you're back.
  Previously it failed with "cannot open a browser to log in", which left
  no way back in after a `/logout`. Signing in or out now also refreshes
  the banner's role and tenant immediately, and signing in as a different
  account drops the previous account's customer context and cached names.
- Changing or clearing the API key while an answer is streaming no longer
  risks a status line stuck on a spinner.

## v2.7.0 — August 26, 2026

### New

- Running `dci` with no arguments at a terminal now opens the interactive AI
  session — the same one `dci ai` opens: ask about your cloud costs in plain
  English, or run any command with a `/` prefix. In pipes, scripts, and CI,
  bare `dci` prints the help screen exactly as before, so nothing changes
  for automation. Prefer the help screen at your terminal? Run
  `/default help` once inside the session; `dci --help` always prints help.
- `dci beta` opens the early-access command surface, starting with
  asynchronous report execution: `dci beta run-report`,
  `run-report-config`, `get-report-operation`, `get-report-results`, and
  `cancel-report-operation`. Beta commands use your existing sign-in,
  customer context, and output formats; idempotency keys are generated for
  you unless you pass your own. `dci commands --beta` lists the surface,
  and accounts not yet enrolled in the early-access program get a clear
  hint instead of a bare error.
- Two new chart styles: `--chart sparkline` draws period totals as a
  one-line sparkline for the quickest look at the shape, and
  `--chart heatmap` draws one row per group and one cell per period,
  colored by each value's share of the maximum — spot the hot service and
  the hot month in one glance.
- When a long AI answer finishes while you're in another window, the
  session now sends a desktop notification in addition to the terminal
  bell (in terminals that support notifications and focus reporting), so
  you can safely switch away during an investigation.

### Improved

- Budget utilization bars are color-graded: green through amber (past 70%)
  into red (past 90%), so `dci list-budgets` shows at-risk budgets at a
  glance.
- The AI session got a visual polish: the banner draws the DoiT mark in
  high resolution (and as a full logo on Kitty, Ghostty, and WezTerm),
  identity lines are labeled by role and tenant, the spinner is the DoiT
  mark with a status shimmer, and long waits rotate through the CLI's
  waiting quips and shift color as they age.
- `Ctrl+L` in the session now also jumps back to the latest content along
  with repainting the frame — one press returns a known-good live view.
- Mistyping a command at a terminal now also suggests the plain-English
  alternative (`dci ai "…"`) alongside the usual did-you-mean suggestion.
- CI systems that allocate a terminal are now detected via the standard `CI`
  environment variable and get the script behavior — a pipeline can never
  hang on an interactive session.

### Fixed

- The AI session's command popup scrolls: with more matches than fit (say
  `/list`), pressing `↓` now moves through all of them — a counter on the
  highlighted row shows where you are in the list. Previously matches past
  the sixth row were unreachable.

## v2.6.2 — August 25, 2026

### Fixed

- `dci ai` failed to start in v2.6.1 with a flag error ("unable to redefine
  'q' shorthand"). The one-shot quiet mode is spelled `--quiet` — the `-q`
  shorthand belongs to another flag and is not used.

## v2.6.1 — August 25, 2026

### New

- The AI session rings the terminal bell when an answer that ran commands
  finishes, so you can switch away during a long investigation and still
  catch the result. `/bell` turns it off and remembers your choice.
- One-shot mode can control how much of the investigation you see:
  `dci ai --quiet "question"` prints just the answer, and `--verbose` keeps the
  full narration even when output is piped to a file.

### Improved

- In the AI session's command popup, `Enter` now selects the highlighted
  command, matching other CLIs (`Tab` still works too).
- AI answers label their numbers: cost tables state the currency and usage
  tables name the metric, with one consistent scale per column. Query
  results always carry a currency on cost columns — US dollars when the
  query doesn't choose one.
- The `/mouse` choice is remembered across sessions.
- Starting `dci ai` without an API key offers the guided key setup right
  away — type a `/` command to skip past it, or press `Esc` to dismiss it.

### Fixed

- AI session rendering on wide terminals: answers and tables use the full
  terminal width, and `Ctrl+L` reliably repaints a frame disturbed by
  terminal scrolling.
- `dci export-cloudflow-flow` writes the complete flow bundle as JSON, so
  the exported file can actually be imported; import results report
  validation errors instead of hiding them.

## v2.6.0 — August 24, 2026

### New

- `dci ai` opens an interactive AI session in your terminal: ask about your
  cloud costs in plain English and the AI runs `dci` commands for you and
  explains the results — or run any command yourself with a `/` prefix, with
  completion and history. One-shot mode answers a single question and exits:
  `dci ai "top 3 cost anomalies this month"`. AI features use your own
  Anthropic API key (the session walks you through saving one securely);
  your questions and the command results the AI reads are sent to Anthropic
  under your key, and conversations are never stored anywhere but your
  terminal.
- Inside the session: `/customer` shows or switches the customer context
  (switches the AI makes itself apply to the session only — your saved
  context is never changed), `/model` picks the AI model, `/export` saves
  the transcript to a file, and `Esc` cancels a running command or answer.
  Define your own shortcuts as saved commands.
- Tune AI answer speed versus depth with the `DCI_AI_EFFORT` environment
  variable or the `effort` setting (`low`, `medium`, `high`; `default`
  restores uncapped reasoning). The default is `medium`, measured to keep
  answer quality while responding noticeably faster.
- `--rollup <columns>` totals report and query rows in the CLI: rows group
  by the listed result columns, metric columns are summed, and per-period
  rows collapse into one total per group — no more spreadsheet step for
  "total per service over the quarter".
- `--search <substring>` finds items in any list command by keyword,
  case-insensitively, across every text field and every page — e.g.
  `dci list-dimensions --search genai`.
- `DCI_AI_STATS=1` prints one telemetry line per AI answer (rounds, tool
  calls, tokens, timing) for scripting and benchmarking.

### Improved

- The AI session answers multi-step questions faster: independent commands
  it batches now run concurrently, and long results are presented as a
  top-10 table plus an aggregated total — ask for the full breakdown when
  you want every row.

### Fixed

- Unauthenticated headless runs (CI, scripts) of `--help` on API commands
  fail fast with a structured error instead of hanging on a browser login
  that can never complete.
- A mistyped flag value no longer surfaces as a destructive-command
  confirmation prompt.

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

## v2.2.0 — August 18, 2026

### Improved

- `list-insights` opens with a curated, savings-ranked view: title-led
  columns, potential daily savings in USD, easy-win markers, and links to
  each insight's underlying report.
  ([Insights on the cheat sheet](https://help.doit.com/docs/cli/cheatsheet#insights))

## v2.1.1 — August 17, 2026

### Fixed

- Completing a partial first word offers the matching root commands.
- A Tab press never triggers a network call or an OAuth login — completion
  on a cold cache stays local.
  ([Shell completion](https://help.doit.com/docs/cli#shell-completion))

## v2.1.0 — August 17, 2026

### New

- Use resource names instead of ids: commands resolve names like
  `dci get-report "Monthly costs"`, and shell completion offers your
  resource names — `dci get-report Mon<TAB>` — served from a local cache
  that refreshes in the background.
  ([Shell completion](https://help.doit.com/docs/cli#shell-completion))
- Shell completion scripts ship inside every install package.

### Improved

- The embedded agent guidance covers name resolution, so AI agents address
  resources by name too.

### Fixed

- Multi-word resource names work unquoted in `dci open`, in name
  resolution, and in completion — and shells that strip quotes still
  complete quoted names correctly.
- Sourcing the zsh completion script registers it without extra setup.

## v2.0.0 — August 16, 2026

The CLI's human experience overhaul — see
[CLI v2.0: before and after](https://help.doit.com/docs/cli/before-after-v2)
for the full gallery of what changed.

### New

- Report results render for humans: pivoted tables with heatmap shading
  and trend columns, matching how the console presents a report; machine
  formats get the same results as structured rows.
- `dci docs` prints every documentation entry point, for people and for
  AI agents.
  ([Documentation for agents](https://help.doit.com/docs/cli#documentation-for-agents))
- Built-in FinOps workflows and command discovery guidance for AI agents
  driving the CLI.
  ([Agent mode](https://help.doit.com/docs/cli#agent-mode))

### Improved

- Requests are validated before they are sent: unknown body fields,
  malformed path parameters, and invalid non-interactive invocations fail
  fast with hints instead of opaque server errors.
- Errors classify by the API's real HTTP status and map to distinct exit
  codes, and authentication failures come with tailored hints.
  ([Exit codes](https://help.doit.com/docs/cli#exit-codes))

### Fixed

- The CLI repairs an unusable saved API configuration, recovers from an
  invalid saved API base, and honors the API base you configure.
- Customer context resolves from the canonical customer id, and console
  customers resolve even before any report data exists.
- Generated pivot totals rows are labeled as such, and successful empty
  API responses are accepted instead of reported as errors.

---

For releases before v2.0.0, see the
[GitHub releases page](https://github.com/doitintl/dci-cli/releases).
