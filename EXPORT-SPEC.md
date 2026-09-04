# Design spec: file-shaped responses — exports, downloads, and paging that isn't in the body

Status: **implemented** (this repo: `export_download.go`, `export_pagination.go`,
the paging-flag guard in `pagination.go`). Written and landed 2026-09-04
against `GET /datahub/v1/datasets/{name}/records`
([exportDatahubDatasetRecords](https://developer.doit.com/reference/exportdatahubdatasetrecords)),
the first DCI operation whose response body is a file rather than a document.

Scope: how the CLI renders, pages, windows and saves an operation whose
response body is already a file, and how paging flags behave on operations
that cannot use them. The DataHub record export is the only such operation
today; every rule below is expressed so the next one needs a map entry, not a
new chapter.

---

## 1. Summary

| # | Feature | One line |
|---|---------|----------|
| E1 | Verbatim body | A `text/csv` or `application/x-ndjson` success body reaches stdout exactly as the API sent it, instead of being JSON-marshalled into a quoted string or base64 |
| E2 | Header paging | `--all` follows `X-Next-Page-Token` (the token is not in the body, so the JSON merge cannot see it) |
| E3 | Window walking | `--all` also walks the API's 366-day window cap across the range the user asked for, and a wider window without `--all` is rejected up front |
| E4 | CSV header union | Merged CSV pages are re-emitted against the union of their headers, because the API computes business columns per page |
| E5 | `--output-file` | Any command's output can be written to a file; a directory target uses the API's `Content-Disposition` filename |
| E6 | `--for-reimport` | Rewrites an exported CSV into the ingest vocabulary the operation's own description documents |
| E7 | Paging-flag guard | `--page-token`/`--max-results` are rejected on an operation that declares no such parameter; `--all` is noted, not rejected |

## 2. E1 — a file-shaped body is not a document

**Problem.** Restish parses a response by content type. `text/csv` matches its
`Text` handler and becomes a Go string, which the JSON/TOON/table renderers
then print as one quoted scalar with literal `\n` escapes.
`application/x-ndjson` matches nothing, stays `[]byte`, and is emitted as
base64. Both are unusable; the only escape was `-r`/`--rsh-raw`, an
undocumented restish flag that dci's `defaultToBodyOutput()` otherwise steers
every command away from.

**Rule.** `rawBodyOutputGuard` is installed as the outermost
`cli.ResponseFormatter`. A 2xx response whose content type is on an exact
allowlist (`text/csv`, `application/csv`, `application/x-ndjson`,
`application/jsonl`, `application/x-ldjson`) is written to stdout byte for
byte, bypassing every renderer and body transform below.

`--output` is ignored for such a body: there is no document to re-render, and
`--output json` on a CSV export would have to either lie or re-parse. The
curated command doc says so.

**Deliberately not on the allowlist:** `text/plain` and `text/html`. Gateway
and edge error pages use them, and those must stay on the error contract
(`error_contract.go`, `isHTMLErrorPage`) rather than being printed as data.
The guard also claims only 2xx responses, for the same reason.

## 3. E2/E3 — paging and windowing a file

Two server mechanics leak into the user's shell for this endpoint:

1. The continuation token comes back in the `X-Next-Page-Token` **header** — a
   CSV or NDJSON stream has nowhere to carry a wrapper field.
2. `endTime` must be at most **366 days** after `startTime`.

Before this spec, `--all` silently returned page 1 (its transport bailed on
any non-JSON body), and a wider window returned the API's bare 400. Exporting
a two-year dataset therefore meant: compute the window boundaries by hand, run
one command per window, run one command per page inside each window while
reading tokens out of `-v` output, and concatenate the results.

**Rule.** With `--all`, `paginatingTransport` (pagination.go) hands a
file-shaped response to `mergeFileExportPages`, which:

- follows `X-Next-Page-Token` to the end of the current window;
- then advances to the next window (`exportWindowWalk`), whose bounds abut
  exactly — `startTime` inclusive, `endTime` exclusive, so no row is fetched
  twice or skipped — clearing `pageToken`;
- merges every page into one body, and reports pages/windows/rows on stderr;
- stops at `maxFileExportRequests` (200) and keeps the resume token in the
  header, with a note naming the command that continues from there.

The loop checks the request cap **before** consuming a window from the walk:
`walk.next()` advances `currentEnd` permanently, so consuming a window for a
request the cap then refuses would drop that window's rows and point the
resume note past them. Truncation therefore always resumes from data actually
fetched — mid-window with `--page-token` (the token encodes its own window),
at a boundary with `--start-time`/`--end-time` for the range still missing,
and never with an empty `--page-token`.

The walk gates on the user having **typed** `--all` (`allPagesExplicit`), not
on the `all-pages` key: `--search` also sets that key (searching one page
would miss the collection), and `--search` can neither filter nor even reach
a file-shaped body — `rawBodyOutputGuard` returns the bytes before
`applyListSearch` runs. Gating on the overloaded key would have let a bare
`--search` launch a 200-request page-and-window walk that changed nothing.
`--search` on a file export is now a preflight usage error
(`validateSearchOnFileExport`) rather than a silent no-op.

The first request is clamped to the cap by `clampExportWindow` **only** under
`--all`. Without `--all` there is no loop to continue, and quietly exporting a
narrower range than the one requested is the worst available answer — so
`validateExportWindow` rejects that combination in preflight instead, pointing
at `--all`.

Window metadata lives in `fileExportOperations`, keyed by command name, with
the evidence for the cap recorded (`windowSource: "spec"` — the OpenAPI
document states it). The same map records which exports `--for-reimport`
applies to. A new file-shaped export adds one entry.

`--all` sizes its pages from `pagingCaps`, so the export carries an entry
there too (50,000, declared in the spec); a test asserts every
`fileExportOperations` key has one.

## 4. E4 — merged CSV pages need one header

The operation's own description: *"The business columns are computed from the
rows of each page, so consecutive pages can have different label and metric
columns; union the headers when concatenating pages."*

**Rule.** `mergeCSVExportPages` keeps first-seen column order, maps each page's
header onto that union, and pads rows that predate a later page's new column
with empty cells. A single page is returned verbatim, so `--all` on a
one-page export is byte-identical to the same command without it — including
the API's own quoting.

This buffers the whole export in memory, which the union guarantee makes
unavoidable: a column discovered on the last page adds a cell to every row
already collected. A 92,325-row, 17.7 MB export merges in ~34 s dominated by
the API.

NDJSON pages are concatenated with newline normalization (a page that does not
end in a newline must not glue its last record to the next page's first).

## 5. E5 — `--output-file`

`-O`/`--output-file PATH` writes the response to a file. For a file-shaped
body that is the bytes; for everything else, the guard points `cli.Stdout` at
the file for the duration of the inner `Format` call, so table, JSON, YAML,
CSV and TOON output all land there without any renderer knowing about it. The
byte count and path are reported on stderr.

A path naming an existing directory (or written with a trailing separator, or
`.`) uses the filename from `Content-Disposition`, falling back to
`<command>.<ext>`. That name is server-controlled, so only its base name is
used — a header carrying `../../.ssh/authorized_keys` cannot escape the
directory — and a derived name never overwrites an existing file (an explicit
path does, like a shell redirect).

A write that fails partway (disk full, permissions revoked, a renderer
erroring mid-render) removes the file it created, best-effort, before
reporting: a half-written file at the path the user named looks exactly like a
complete export.

`-O` deliberately takes a value rather than using pflag's `NoOptDefVal`:
with a default value set, `-O out.csv` would silently treat `out.csv` as a
positional argument.

## 6. E6 — `--for-reimport`

The operation documents its own round trip: *"To re-import an export into
another dataset, drop the `batch`, `source`, `export_time` and `updated_by`
columns, and either drop `event_id` or rename it to `id`."* The flag does
exactly that, so `export … --for-reimport --output-file records.csv` feeds
`dci ingest-datahub-events-csv` unchanged.

It is rejected in preflight on a command with no CSV export, and alongside
`--format jsonl` (that stream is already in the ingest event shape). At
runtime, a non-CSV or unparsable body is passed through **unchanged with a
stderr note** rather than erroring: the export is the valuable thing, and
losing it to a rewrite surprise would be worse than skipping the rewrite.

## 7. E7 — paging flags that cannot apply

`--page-token` and `--max-results` reach 187 operations that declare no such
query parameter, where the value was silently dropped: `--max-results 10`
looked accepted and changed nothing, which reads as "this collection has 200
items", not "your page size was ignored". Those are now preflight usage
errors, checked against the operation's declared query parameters.

`--all` is **not** rejected on those operations, only noted on stderr: it asks
for the complete collection, and a command that returns everything in one
response has already answered that. Passing it defensively across a script's
list commands stays valid. `--search` is likewise ungated — it filters rows
client-side and works on any list response, paged or not.

## 8. Related surfaces touched

- `list_views.go` gained `hideWhenEmpty` on view columns, and
  `list-datahub-datasets` a **display name** column: `update-datahub-dataset`
  can now set `displayName`, which the console and report results show
  instead of `name`, and which appeared nowhere in the CLI. The column is
  dropped when no dataset has one, so the common case keeps its width.
- Curated docs for `export-datahub-dataset-records` and
  `update-datahub-dataset` (`command-docs/`, COMMAND-DOCS-SPEC.md).

## 9. Open items (API side, not this repo)

- **`firstEventTime`/`lastEventTime` on the dataset resource.**
  `get-datahub-dataset` reports `records` and `lastUpdated` but no event-time
  range, so "export this dataset" cannot be expressed without the user
  supplying bounds. With that pair, `--all` and no window would mean exactly
  the dataset's own range.
- **`x-cli-name` on `updateDatahubDataset`.** Every other DCI operation
  declares one; without it restish derives the command name from the
  operationId and also generates an `updatedatahubdataset` alias that shows
  up in help and completion.
