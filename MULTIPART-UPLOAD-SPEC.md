# Design spec: `ingest-datahub-events-csv` — file uploads (multipart/form-data) from the CLI

Status: **P1 implemented** (`multipart_upload.go`, the pre-run hook in
`main.go`, the curated YAML); M5 conveniences pending. Resolves
[issue #151](https://github.com/doitintl/dci-cli/issues/151) with its option 1
(add multipart encoding to the CLI); options 2 (hide the command) and 3 (keep
the warning page) are rejected in §9.

Audited at commit `24c5429` (dci-cli), restish v0.21.2, prod spec of
2026-09-04; every claim cites the file it is based on.

Scope: make `dci ingest-datahub-events-csv` upload a CSV (or a ZIP/GZ of one)
to DataHub, with the same syntax, validation, auth, customer context, output
formats, and exit codes as every other write command. The mechanism is
generic — any operation whose request body is `multipart/form-data` gets it —
but today's spec has exactly one such operation, so the DataHub CSV upload is
the acceptance case. Curated docs for the command (`command-docs/`, `--help`,
Help Center page) drop the "not available from the CLI" warning and gain
runnable examples.

---

## 1. Summary

| # | Feature | One line | Phase |
|---|---------|----------|-------|
| M1 | Multipart body encoder | Shorthand fields become form parts; `@path` on a `format: binary` field attaches the file with its name and a content type | P1 |
| M2 | Multipart operation runner | For a multipart operation the CLI replaces restish's `Run` with its own, builds the request, and hands it to restish's request pipeline (auth, headers, TLS, formatting, exit codes unchanged) | P1 |
| M3 | Preflight and errors | Missing `@`, unreadable file, stdin without a filename, and oversize files fail before any request, with a fix-it hint in human and agent mode | P1 |
| M4 | Curated docs | `command-docs/ingest-datahub-events-csv.yaml` rewritten: three runnable examples, DataHub notes, related commands; the warning goes away on `--help`, the catalog, and the Help Center page | P1 |
| M5 | Conveniences | Path completion after `@`, `provider:` value completion from `list-datahub-datasets`, auto-gzip above the size limit | P2 |

The user-visible outcome:

```
$ dci ingest-datahub-events-csv provider: litellm-usage, file: @events.csv
batch                       ingestedRows
events.csv_1756987200000    15
```

instead of today's `not sure how to marshal multipart/form-data`.

## 2. Current state (what already exists)

**The operation.** `POST /datahub/v1/csv/upload`, operationId
`datahubEventsCSVFile`, `x-cli-name: ingest-datahub-events-csv`, alias
`datahub-events-csv-file` (prod spec `:3210–3258`). Request body
`multipart/form-data` with two fields, neither marked required in the schema:
`provider` (string, the dataset identifier) and `file` (string,
`format: binary`, "CSV file, uncompressed or compressed in ZIP or GZ format,
maximum 30 MB"). Success is `201` with `{batch, ingestedRows}`; the batch id
example `your_file.csv.gz_1730972725212` embeds the uploaded filename. It is
the only multipart operation in the spec (`grep -c multipart/form-data` = 1).

**What the CLI does today.** restish records the operation's first request
media type as `Operation.BodyMediaType` (`openapi/openapi.go:383–387`) and
builds the cobra command with `Args: MinimumNArgs(0)` and a `Run` that calls
`GetBody(o.BodyMediaType, args)` (`cli/operation.go:44, 132–133`). `GetBody`
(`cli/input.go`) parses the shorthand and marshals to JSON or YAML; any other
media type returns `not sure how to marshal %s`, which `Run` panics on. The
CLI's recover in `main.go:712` turns that into an error exit, but no request
is ever sent. restish never sets a `Content-Type` request header
(`cli/request.go` has none), so even a hand-built multipart body would need
the CLI to add one.

**What the CLI already has that the fix reuses.**

- `validateRequestBody` (`body_validation.go:54`) parses the request schema
  sketch out of the command's long help (`## Request Schema (multipart/form-data)`
  … `file: (string format:binary)`, `provider: (string)`) and rejects unknown
  top-level fields — it already accepts `provider: X, file: @events.csv`.
- The dci `PersistentPreRunE` (`main.go:2197–2443`) is the single hook every
  API command passes through before `Run`: path resolution, body validation,
  `--customer-context` header injection, destructive gate.
- `enforceDestructiveConfirmation` (`destructive_contract.go:262–263`) already
  replaces a command's `Run`/`RunE` from that hook (dry-run), so swapping the
  runner for one command is an established pattern.
- `loadDCIOperationAPI` (`destructive_contract.go:198`) returns restish's
  `cli.API` with every `cli.Operation` (`Method`, `URITemplate`, `PathParams`,
  `QueryParams`, `HeaderParams`, `BodyMediaType`) from the cached spec.
- `cli.MakeRequestAndFormat` (`cli/request.go:631`) applies profile headers,
  `rsh-header` (user agent, tenant header), `rsh-query` (customer context),
  TLS, auth (`request.go:238–242`), then formats through the CLI's own
  content-type handlers and response transforms (`main.go:2520–2760`).
- `pagingCaps` (`pagination.go:32`, keyed by command name) is the precedent for a
  small per-command table of API limits that the spec does not carry
  machine-readably.
- The curated doc file `command-docs/ingest-datahub-events-csv.yaml` is on
  `main` (PR #156, merged 2026-09-04) with a `:::warning Not available from
  the CLI yet` note and one non-functional example. This worktree's branch
  (`cli-docs-examples-v1`) predates the eight authoring PRs; implementation
  starts from `main`.

**Why not the alternatives.** A transport that rewrites a JSON body into
multipart cannot work: `GetBody` fails before any transport sees a request.
Pre-building the body and feeding it through `cli.Stdin` (as
`wrapBareCloudflowBundle` does, `body_validation.go:649`) cannot work either:
`GetBody` only passes stdin through raw when there are *no* body arguments,
and cobra hands `Run` the same argument slice the pre-run saw.

## 3. M1 — Multipart body encoder

New chapter file `multipart_upload.go` (+ `_test.go`), per the
one-file-per-chapter rule (AGENTS.md).

Input: the body arguments (everything after the path parameters) and the
request schema sketch already parsed by `requestSchemaTopLevelFieldSketches`
(`body_validation.go`), which carries each top-level field's type line.

Rules:

1. Body arguments are parsed with restish's shorthand options **except**
   `EnableFileInput`, which is off — the encoder handles `@` itself so the
   file is streamed once, with its name, instead of being inlined into a
   string by the shorthand parser (`shorthand/v2 parse.go:554–561`).
2. A field whose sketch says `format:binary` is a **file part**. Its value
   must be `@<path>`. The part is written with `Content-Disposition:
   form-data; name="<field>"; filename="<basename>"` and a `Content-Type`
   from the extension: `.csv` → `text/csv`, `.gz` → `application/gzip`,
   `.zip` → `application/zip`, otherwise `application/octet-stream`. The
   filename is preserved because the API's batch id embeds it and the
   server needs the extension to tell a GZ or ZIP from a plain CSV.
3. Every other field is a **plain part** named after the field. Scalars are
   written as text (`provider` → `litellm-usage`). Objects and arrays are
   JSON-encoded — the spec has none today; this is the documented fallback,
   not a designed feature.
4. The body is assembled in memory with `mime/multipart` so the request has
   a `Content-Length`; the size preflight in §5 keeps that bounded.
5. The runner sets `Content-Type: multipart/form-data; boundary=<b>` on the
   request directly. It does not go through `rsh-header`, so it cannot
   collide with the tenant header or the user agent set there
   (`main.go:773, 2428`).

`< file` stdin is **not** a file part in P1 (§5, rule 3): a stream has no
filename, and the server needs one.

## 4. M2 — Multipart operation runner

In the dci `PersistentPreRunE`, after `validateRequestBody` and the
`--customer-context` handling, before `enforceDestructiveConfirmation`:

1. Look the command up in operation metadata (`loadDCIOperationAPI`, matched
   by `cmd.Name()` — restish already uses `x-cli-name` as `Operation.Name`).
   If `BodyMediaType` does not contain `multipart/form-data`, return; every
   other command is untouched. Loading the metadata is the same cached-spec
   read the destructive gate performs (`ensureDestructiveOperations`) — no
   network.
2. Set `cmd.Run = nil` and `cmd.RunE` to the multipart runner (the dry-run
   precedent, `destructive_contract.go:262`). Returning an error from `RunE`
   flows through the CLI's error contract (`error_contract.go`) instead of
   restish's panic.
3. The runner rebuilds what restish's `Run` builds (`cli/operation.go:60–140`):
   substitute path parameters into `URITemplate`, add changed query flags,
   apply `rsh-server`, add changed header flags. Today's single operation has
   none of these; the general path is specified so a future multipart
   operation with an `{id}` or a query flag works without a second design
   round, and is covered by a unit test with a synthetic operation.
4. Encode the body (§3), set `Content-Type`, and call
   `cli.MakeRequestAndFormat(req)`. Everything downstream — auth, tenant
   header, `--customer-context` query, TLS, the spinner transport
   (`installSpinnerTransport`, already installed by the pre-run), response
   transforms, `--output`, agent-mode TOON, exit-code mapping — is the
   existing pipeline. A `201` renders `{batch, ingestedRows}` as a one-row
   table (or JSON/TOON), a `400` maps to exit code 30 with the API's `error`
   message, exactly as for `ingest-datahub-events`.

The destructive gate still runs after this step; the operation is not in the
destructive set, so nothing changes for it, but the ordering means a future
destructive multipart operation gets `--dry-run`/`--yes` for free.

## 5. M3 — Preflight and errors

All checks run before any request. Each error implements
`agentErrorDescriptor` (`error_contract.go:61`) so agents get a hint and
`retryable: false`; the human message carries the same hint inline.

| # | Condition | Message (human) | Hint |
|---|-----------|-----------------|------|
| 1 | Binary field given without `@` (`file: events.csv`) | `file must name a file to upload: use file: @events.csv` | same |
| 2 | `@path` missing or unreadable | `cannot read events.csv: <os error>` | `Check the path; relative paths resolve from the current directory` |
| 3 | Body on stdin (`< events.csv`) for a multipart operation | `this upload needs a filename, which stdin does not carry` | `Pass the file as an argument: file: @events.csv` |
| 4 | Binary field absent entirely | `file is required for this upload` | `Add file: @<path>` — the schema does not mark it required, but a multipart upload without its file is always a 400 |
| 5 | File larger than the command's limit | `events.csv is 41.2 MB; the API accepts up to 30 MB` | `Compress it: gzip -k events.csv, then file: @events.csv.gz (one CSV per archive)` |
| 6 | Empty file | `events.csv is empty` | none |

Rule 5 uses a per-command table `uploadSizeLimits = {"ingest-datahub-events-csv": 30 << 20}`
(the `pagingCaps` precedent) because the limit lives in the field description,
not in the schema. A multipart operation without an entry has no local size
check. The table has a test that fails if the command disappears from the
spec, so it cannot go stale silently (same shape as the `pagingCaps` checks).

**Ordering with the existing body validation.** `validateRequestBody` keeps
its field-name check for multipart operations (unknown fields are still
rejected), but skips `validateBodyValueShapes` for them: that check parses
with `EnableFileInput: true` (`body_validation.go:160`), so shorthand itself
would read `@events.csv` and fail a missing file with its own
`Unable to read file` wording (`shorthand/v2 parse.go:554–563`) before rule 2
above could. The encoder's preflight is the single owner of file errors for
multipart bodies.

Nothing here changes `DCI_SKIP_BODY_VALIDATION`: that variable skips the
field-name check (`body_validation.go:72`), and the preflight above is not a
schema check but the encoder refusing input it cannot send. It is not
bypassable, by design.

## 6. M4 — Curated docs

`command-docs/ingest-datahub-events-csv.yaml` replaces the version on `main`:

```yaml
command: ingest-datahub-events-csv

examples:
  - description: Upload a CSV into the dataset `litellm-usage`; `@` attaches the file and keeps its name.
    command: "dci ingest-datahub-events-csv provider: litellm-usage, file: @events.csv"
    output: |
      batch                       ingestedRows
      events.csv_1756987200000    15
  - description: Upload a compressed export (files over 30 MB must be gzipped or zipped, one CSV per archive).
    command: "dci ingest-datahub-events-csv provider: litellm-usage, file: @events.csv.gz"
  - description: Script-friendly result for another customer's tenant.
    command: "dci ingest-datahub-events-csv provider: litellm-usage, file: @events.csv --output json -D acme.com"
    output: '{"batch": "events.csv_1756987200000", "ingestedRows": 15}'

notes: |
  :::info Before you upload
  `provider` is the dataset name (`dci list-datahub-datasets`); create it
  first with `dci create-datahub-dataset name: <dataset>` if it does not
  exist. The CSV must follow the dataset's schema template — for the
  Default template the header is `usage_date[,id],<dimension>...,<metric>...`
  with RFC 3339 UTC timestamps; see
  [CSV ingestion](/docs/integrations/datahub/import-data/upload-csv).
  Requires the DataHub Admin permission. Rows appear in Cloud Analytics
  within about 15 minutes.
  :::
  :::tip Undo a bad upload
  There is no delete-by-batch API. Remove the rows with
  `dci delete-datahub-events-by-filter` using the time range the file
  covered, then upload the corrected file.
  :::

related: [ingest-datahub-events, create-datahub-dataset, list-datahub-datasets, delete-datahub-events-by-filter]
```

What the three surfaces show:

- **`--help`**: the three examples replace restish's synthesized
  `file: string, provider: Datadog` (COMMAND-DOCS-SPEC D2). The `Arguments:`
  block is not used — it is keyed by path parameters and this command has
  none; the `@` convention is taught by the example descriptions.
- **`dci commands --json`**: the body example becomes
  `{"provider": "litellm-usage", "file": "@events.csv"}`, which agents can
  copy verbatim.
- **Help Center page**: the warning admonition is gone; the request fields
  table (D6) already renders `file` as `string (binary)`; the `notes` above
  land after the description. No omni change is needed.

The COMMAND-DOCS-SPEC validator must accept this file unchanged: it checks
the command, flags (`--output`, `-D` are persistent flags), arity (0), body
fields (`provider`, `file` are in the schema), and shorthand parse. The
validator parses with `EnableFileInput = false` (`command_docs.go:353`), so
`@events.csv` stays a string and no file is read; no validator change is
required, and `command_docs_test.go` gains one case asserting that.

Skill and cheatsheet: `skills/dci-cli/references/capabilities.md:49` lists
`datahub-events-csv-file` by alias; it is updated to the `x-cli-name` with a
one-line pointer to the `@` convention. The README's request-body section
gets one sentence: "For file uploads, `field: @path` attaches the file."

## 7. M5 — Conveniences (P2)

Not required for the command to work; listed so they are decided, not
rediscovered.

- **Path completion after `@`.** API commands complete with
  `ShellCompDirectiveNoFileComp` (`main.go:1618`); for a multipart operation,
  a token that starts with `<binary-field>: @` should return
  `ShellCompDirectiveDefault` so the shell completes paths.
- **`provider:` value completion** from `list-datahub-datasets`, through the
  argument Tab picker (`ai_placeholder.go`), following the same
  `resolvesNames` catalog flag other name-bearing arguments use.
- **Auto-gzip.** When the file exceeds the size limit and is not already
  `.gz`/`.zip`, compress it in memory and upload it as `<name>.gz` instead of
  failing rule 5. Opt-in via a local `--compress` flag or on by default with a
  stderr note — decision deferred until someone hits the limit.
- **Stdin with a name.** `dci ingest-datahub-events-csv provider: X --file-name events.csv < events.csv`
  for pipelines that generate the CSV. Adds a local flag to an API command;
  the validator's `flagKnown` check already handles local flags on the dci
  command.

## 8. Tests and CI

- `multipart_upload_test.go`: an `httptest.Server` receives the request and
  the test asserts the multipart boundary parses, the `file` part carries
  `filename="events.csv"` and `text/csv`, the `provider` part is
  `litellm-usage`, the tenant header and `customerContext` query from
  `--customer-context` are present, and the `201` body renders as a table
  and as JSON. Variants for `.gz` and `.zip` content types, and for a
  synthetic operation with a path parameter and a query flag (rule §4.3).
- Each preflight row in §5 has a test asserting the message, the hint, and
  that no request reached the server.
- A regression test that a JSON-bodied command (`ingest-datahub-events`)
  still reaches restish's own `Run` — the runner swap must be scoped to
  multipart operations only.
- `command_docs_test.go` (live-spec mode, already in CI) validates the
  rewritten YAML; one new offline case asserts `@path` tokens survive the
  validator's shorthand parse as strings.
- A `pagingCaps`-style test asserting every `uploadSizeLimits` key names an
  operation that exists in the spec.
- No end-to-end TUI test: the command has no interactive surface.
- Manual acceptance on a dev tenant: create a dataset, upload the Help
  Center's sample CSV, confirm `ingestedRows` matches the row count, confirm
  rows appear in `dci query` within 15 minutes.

## 9. Decisions

Taken here, open for the maintainer's confirmation:

1. **Syntax: shorthand `field: @path`, not flags or positionals.** It is what
   the schema-derived `--help` and the validator already accept, it matches
   how every other body command reads, and `@` already means "from a file"
   in restish shorthand. `--provider`/`--file` flags would make this the only
   API command whose body is flags; positionals would contradict the spec's
   zero path parameters.
2. **Issue #151 option 1 over 2 and 3.** Hiding the command (`x-cli-hidden`)
   or keeping the warning page leaves the API's headline DataHub ingestion
   path unusable from the CLI, and the MCP server already exposes the same
   upload (`datahub_events_csv_file`), so the CLI would be the only DoiT
   surface without it.
3. **Filename required; stdin rejected in P1.** The server keys compression
   detection on the filename and echoes it in the batch id. A `--file-name`
   flag (§7) is the path to stdin support if a pipeline needs it.
4. **Size limit checked locally from a per-command table.** The 30 MB limit
   is prose in the spec, so the CLI carries it the way it carries paging
   caps, and fails in under a millisecond with the gzip hint rather than
   after a 41 MB upload.
5. **Generic by media type, not by command name.** The runner keys on
   `BodyMediaType`, so the next multipart operation the API adds works the
   day it ships. Only the size limit and the curated docs are per-command.

## 9a. As landed (deviations found during implementation)

- **Body validator scan.** `validateRequestBody` scanned unknown field names
  per shell argument, so `file: events.csv` (split by the shell into
  `file:` and `events.csv`) reported an unknown field `events` — a value
  with a dot read as a dotted field. The scan now joins the arguments the
  way restish does and splits on top-level shorthand commas
  (`splitTopLevelShorthandSegments`), which fixes the same false positive
  for every write command (`config.currency: usd.legacy, …`).
- **Catalog body example.** `exampleBody` (`command_docs.go`) treated any
  `@` token as whole-body file input, so the curated `file: @events.csv`
  example never replaced the schema-synthesized body example in
  `dci commands --json`. Only a *leading* `@file` is whole-body input now;
  the catalog shows `{"file": "@events.csv", "provider": "litellm-usage"}`.
- **Live response shape.** Verified 2026-09-04 against the production API
  (tenant `omni.engineer`, throwaway dataset `dci-cli-upload-test`): both a
  plain `.csv` and a `.csv.gz` upload returned `201` in about five seconds
  with `ingestedRows` matching the file, and the batch id
  `csv_<filename>_<millis>` confirms the filename is preserved. The response
  carries more than the spec's `{batch, ingestedRows}`: `batchId`,
  `generatedEvents`, `sourceRecords`, `provider`, `schemaTemplate`. The
  curated `output:` examples show the real shape.
- **Directory check.** A directory passed as `@path` is its own preflight
  error (rule 7), since `os.Stat` accepts it and `os.ReadFile` would not.

## 10. Out of scope, noted for later

- Upload progress (a byte counter under the spinner) — the 30 MB cap makes
  uploads short; revisit if a larger-limit operation appears.
- Client-side CSV validation against the dataset's schema template (header
  row, RFC 3339 timestamps, two-year window). The API validates and its
  `400` message is specific; duplicating the rules in the CLI would drift.
- Deleting by batch id — not in the API; noted in the command's `:::tip`.
- A rejected CSV comes back as `400` with `{"errors": [{field, message}]}`,
  not the spec's `{"error": string}`. The CLI prints that body verbatim and
  then the generic `VALIDATION_ERROR` envelope (exit 30), so the field
  errors are visible but not folded into the envelope's message. Same
  behavior as every other command with a non-standard error body; a
  follow-up in the error contract, not here.
- Multipart operations with nested object fields: JSON-encoded per §3.3
  without further design.

## 11. Phases

| Phase | Deliverables | Where |
|---|---|---|
| P1 | M1 encoder, M2 runner, M3 preflight (`multipart_upload.go` + tests); M4 YAML rewrite, capabilities.md and README lines; `CHANGELOG.md` entry (release gate, CHANGELOG-SPEC C3); AGENTS.md key-files list gains the new chapter; close #151 | dci-cli |
| P2 | M5 conveniences, in the order listed in §7, each behind its own PR | dci-cli |

The P1 release re-delivers `command-docs/` to omni automatically
(COMMAND-DOCS-SPEC D7), so the Help Center page updates with the same release
that makes the command work.
