# Design spec: local-timezone display in `dci`

Status: **draft for maintainer review** — untracked, not for commit.
Audited at commit `fba1fa6` (branch `main` equivalent). Every claim below cites the function and file it is based on; line numbers are approximate at that commit.

---

## 1. Summary

Today every timestamp `dci` renders is UTC, deliberately: epoch-millisecond
values convert via `time.Unix(...).UTC()` in `formatValue` (main.go:2840),
and RFC3339 strings normalize through `.UTC()` in `prettifyTimestamp`
(main.go:2926). This spec proposes rendering **instants** (metadata moments:
`createTime`, `updateTime`, `lastAlerted`, insight `lastUpdated`, and their
kin across the command surface — §3.1) in the viewer's local zone
(`time.Local`, overridable) — but **only** in the human table view.
**Data-bucket labels** (report result period columns, pivot period columns,
anomaly usage windows — §2.1) and **calendar-date business fields**
(contract/commitment terms, invoice dates, budget periods — §3.1) stay UTC,
and **all machine formats** (json, yaml, csv, toon, agent mode) stay
UTC / timezone-less forever.

Scope: the classification covers the **entire locked-down command surface**
— all 207 operation definitions (~180 unique operations) in the OpenAPI
spec were swept for time-typed fields across every 2xx JSON response schema
(§3.1). Command families not listed there have no time-typed response
fields.

The load-bearing design problem is that the current pipeline **conflates the
two categories inside `prettifyTimestamp`**: schema-declared report timestamp
cells (labels) are converted to RFC3339 UTC strings by `displayTimestampCell`
(main.go:2864) and then re-enter the generic string path in
`formatTableValue` (main.go:2906), where they are indistinguishable from
instant strings like insight `lastUpdated`. Localizing instants safely
therefore requires first **splitting the label path from the instant path**
(§6.2) so the label formatter never routes through the function that will
become zone-aware.

---

## 2. The semantic model: instants vs. bucket labels

| Category | Meaning | Examples | Local conversion |
|---|---|---|---|
| **Instant** | A real moment in time; the wall-clock reading is viewer-relative | `updateTime`/`createTime` on reports, budgets, allocations, tickets (epoch ms — fixtures at main_test.go:1138–1171); budget `lastAlerted`; insight `lastUpdated` (RFC3339 string — fixture at response_transform_test.go:143); anomaly `acknowledgedAt` and notification dispatch `timestamp`s (RFC3339 — API contract, §2.1) | **Correct.** `2026-08-18T02:30:59Z` genuinely *is* `05:30` in Jerusalem. |
| **Data-bucket label** | The name of a UTC-grain aggregation bucket or window boundary; the "timestamp" is an identifier, not a moment | Report result `timestamp`/`datetime` schema columns (epoch seconds, converted in `displayTimestampCell`, main.go:2864); pivot period columns built from `year/quarter/month/week/day/hour` dimension columns (`pivotPeriod`, pivot.go:213); anomaly `startTime`/`endTime` — usage-window boundaries at Daily/Hourly bucket grain (§2.1); calendar-date business fields — contract/commitment `startDate`/`endDate`, invoice `invoiceDate`/`dueDate`, budget `startPeriod`/`endPeriod` (§3.1) | **Wrong.** The bucket `2026-08-01` contains costs aggregated over UTC Aug 1. Rendering it as `2026-07-31 21:00` (UTC−3) relabels that money onto the wrong day. Buckets shift only if the *query* is re-aggregated in another zone, which the API does not do. |

### 2.1 Anomaly `startTime`: resolved against the API contract

Checked against the live OpenAPI spec the CLI actually loads —
`https://api.doit.com/openapi.yaml`, advertised by the API base via a
`Link: </openapi.yaml>; rel="service-desc"` header and consumed by restish's
OpenAPI loader (main.go:357); fetched 2026-08-18. Verdict: **label, not
instant**.

- `AnomalyItem.startTime` is `integer`/`int64`, described as "Usage start
  time of the anomaly". The `listAnomalies` filter parameters on the same
  field are explicit: "Inclusive lower bound on the anomaly's **usage start
  time**, in milliseconds since the POSIX epoch. Despite the name, this
  filters the anomaly's usage start time, **not the time the anomaly
  document was created**."
- `AnomalyItem.timeFrame` is "Daily or Hourly": the anomaly is defined over
  usage buckets, so `startTime` (and nullable `endTime`, "End of the
  anomaly") are boundaries of the anomalous usage *window* at bucket grain.
  A daily anomaly's `startTime` is the start of the anomalous day; rendered
  in UTC+3 it would display as `21:00` of the *previous* day — relabeling
  which day was anomalous, the exact hazard the label rule exists for.
  (The spec does not literally say "UTC-aligned", but the API's billing
  grain is UTC throughout, and keeping UTC is the conservative reading
  either way.)
- The anomaly's genuine instants live in other fields, and those localize
  normally: `acknowledgedAt` (`string`/`date-time`, "When the anomaly was
  first acknowledged" — a user action) and `NotificationEvent.timestamp`
  ("Dispatch timestamp in RFC3339 UTC").

Consequence: anomaly `startTime`/`endTime` are epoch-**milliseconds**, so
they travel the field-blind epoch-ms path (`formatValue`, audit #1) that
this spec makes zone-aware — unlike report labels, they are *not* protected
by the schema-typed label path (§6.2). They therefore need an explicit
exclusion (§6.3).

Two existing behaviors only make sense for labels and confirm the split:

- **Midnight collapse**: `prettifyTimestamp` renders midnight-UTC values as a
  bare date because daily/monthly report grain carries no time information
  (main.go:2935–2937, gated by `report-hourly` set in
  `transformSuccessBody`, response_transform.go:41). Collapsing a *local*
  midnight would be meaningless; collapsing a *UTC* midnight after local
  conversion would never fire. This behavior belongs to the label path only.
- **Pivot classification** already treats `timestamp`-typed columns as
  redundant with the year/month/day label columns and skips them
  (`classifyPivotColumns`, pivot.go:202–203) — the schema's own model says
  these columns are bucket identity, not event time.

---

## 3. Audit: every rendering path that touches time

| # | Path | Where | Feeds | Category | Current behavior | Proposed |
|---|---|---|---|---|---|---|
| 1 | `formatValue` | main.go:2840–2850 | table cells (via `formatTableValue`, `tableCellText` array joins), TOON array cells (`joinPrimitives`, main.go:2294), `jsonCell` fallback | **Instant** (epoch-ms metadata: `createTime`, `updateTime`, `lastAlerted`) — **except** anomaly `startTime`/`endTime` (§2.1) and the epoch-ms calendar-date fields (invoice `invoiceDate`/`dueDate`, budget `startPeriod`/`endPeriod` — verified midnight-aligned, §3.1), which are **labels** on this same field-blind path | epoch ms in `[1e12, 4.1e12)` → `time.Unix(...).UTC().Format(RFC3339)` | Format in `displayLocation()` (§6.1). The resolver returns UTC for every non-human context, so the TOON/`jsonCell` callers are unaffected without signature changes. Anomaly `startTime`/`endTime` stay UTC via the label-field exclusion (§6.3). |
| 2 | `formatTableValue` epoch-ms branch | main.go:2907–2910 | table cells | **Instant** | float64 in `[1e12, 4.1e12)` → `prettifyTimestamp(formatValue(v))` | Same instant treatment via #1 + #3. |
| 3 | `prettifyTimestamp` | main.go:2926–2939 | every string table cell (via `formatTableValue`, main.go:2906) | **Mixed today** — instant strings (insight `lastUpdated`), re-parsed label strings emitted by `displayTimestampCell`, *and* calendar-date business fields at midnight UTC (contract/commitment `startDate`/`endDate`, version `effectiveStart`/`effectiveEnd` — §3.1) | parse RFC3339 → `.UTC()`; midnight collapse unless `report-hourly`; `2006-01-02 15:04` | After the label split (§6.2): values at **exactly midnight UTC keep collapsing to a bare UTC date and are never zone-shifted** — the full-surface sweep (§3.1) shows this collapse is what currently renders calendar-date RFC3339 fields correctly, so it is retained as a date-value heuristic, not dropped. Only values with a nonzero UTC time-of-day localize via `displayLocation()`. |
| 4 | `tableCellText` epoch-second branch | main.go:2673–2676 (`timeNamedColumn`, main.go:2681) | table cells whose key contains "time"/"date" holding epoch seconds `[1e9, 4.1e9)` | **Instant** | → UTC RFC3339 → `prettifyTimestamp` | Format in `displayLocation()`. Note `timeNamedColumn("lastUpdated")` is true — "upd**ate**d" contains "date" — so epoch-second variants of that field are covered here. |
| 5 | `displayTimestampCell` / `displayTimestampFields` | main.go:2864–2893, applied in `extractGetReportRows` (main.go:2377, 2381) | report result rows for **table** (`toTableRows`, main.go:2332), **TOON** (`toonPrepare`, main.go:2113) and **CSV** (csv_output.go:23 → `toTableRows`) | **Label** (schema `timestamp`/`datetime` columns, epoch seconds) | epoch s → UTC RFC3339 string; `--raw-numbers` keeps the epoch (main.go:2865) | **Stays UTC forever.** Additionally: in the table path, format terminally to the final label text (`2026-08-01` / `2026-08-09 01:00`, UTC, midnight collapse here) so the string never re-enters #3 (§6.2). TOON and CSV keep full RFC3339 `Z` strings, unchanged. |
| 6 | Pivot period columns | `pivotPeriod` (pivot.go:213–238) composes keys from `year/quarter/month/week/day/hour` dimension columns (`pivotTimeParts`, pivot.go:189) | pivot table column keys, heatmap detection (`newHeatmap` date-shape check, main.go:3114) | **Label** | plain strings (`2026-08`, `2026-08-09 01:00`); never parsed as time — `prettifyTimestamp`'s length-20 pre-check (main.go:2927) passes them through | **No change, ever.** Also unaffected mechanically: these are map keys and column headers, outside every time-formatting function. |
| 7 | list-reports "updated (UTC)" derived column | — | — | — | **Does not exist at this commit.** No derived `updated` column is produced in response_transform.go; `list-reports` `updateTime` flows through path #1/#2 as raw epoch ms. | If such a derived column is added later, it is an instant and follows §4; its header should not bake a zone name into the JSON key (§4.2). |
| 8 | Insight `lastUpdated` | RFC3339 string (fixture response_transform_test.go:143); surfaces through the curated view's alphabetical "rest" columns (`applyInsightPresentation`, response_transform.go:274–284) | table via path #3; TOON as raw string | **Instant** | table: `prettifyTimestamp` → UTC `2026-08-18 02:30`; TOON/json: untouched RFC3339 `Z` | table: local via #3. TOON/json: untouched (still `Z`). |
| 9 | CSV | `dciCSVContentType.Marshal` (csv_output.go:16) → `toTableRows` → `csvCell` (csv_output.go:70, plain `%v`) | csv output | machine | list-field epoch ms stay **raw numbers**; report schema timestamps arrive as **RFC3339 UTC** strings (via #5 — see fixture csv_output_test.go:58,65); no `prettifyTimestamp` | **No change.** CSV stays timezone-less/UTC permanently (§4.3). The raw-epoch vs RFC3339 inconsistency between list fields and report columns is pre-existing; flagged as an open question (§11), not touched by this spec. |
| 10 | TOON | `dciToonContentType.Marshal` (main.go:2073); scalar cells pass through raw (`toonNormalizeRows`, main.go:2259); array cells join via `formatValue` (main.go:2299); report rows via `toonPrepare` → #5 | agent-facing output (default format in agent mode, `defaultOutputFormat`, main.go:879–884) | machine | scalar epoch ms **raw**; schema timestamps RFC3339 UTC; joined array values UTC RFC3339 | **No change.** The `displayLocation()` resolver returns UTC whenever the output format is not table/auto, so the shared `formatValue` cannot leak local times into TOON (§6.1). |
| 11 | json / yaml | restish formatters; body only passes `transformSuccessBody` (response_transform.go:28), which normalizes number types (`normalizeIntegralNumbers`) and never formats time | machine | raw epochs, untouched RFC3339 strings | **No change, ever.** |
| 12 | Sorting | `sortReportRows` (response_transform.go:507) compares raw cells before any display formatting; `sortInsightsBySavings` (response_transform.go:149) sorts by savings | row order | n/a | epoch/string comparison | **Unaffected by design** — display zone can never reorder rows. State this as an invariant. |
| 13 | `time.Now().UTC()` onboarding marker | main.go:923 | internal marker file | internal | UTC | Out of scope (not display). |

### 3.1 Full command-surface sweep

Method: every operation in `https://api.doit.com/openapi.yaml` (207 path
definitions, ~180 unique operations after de-duplicating path variants —
the CLI's entire locked-down command surface, since commands are generated
from these operations via restish) was swept with a ref-resolving walker
over all 2xx `application/json` response schemas, collecting fields whose
name, `format` (`date-time`/`date`), or description (epoch/RFC3339/UTC)
marks them as time-carrying: **217 unique fields** (2026-08-18; name-regex
false positives like `marketplaceSpend`, `recommendedCommitment`,
`trendingPct`, `calendlyLink` manually discarded). Streaming/CSV operations
(`askAvaStreaming`, `datahubEventsCSVFile`) bypass the JSON render pipeline
and are out of scope.

Classifications marked **verified** below were additionally checked against
live API responses on 2026-08-18 (read-only `list-*`/`get-*`/`query` calls
via the released `dci` 2.2.0 against the maintainer's account).

Per command family — wire type, classification, treatment:

| Family (commands) | Instants (localize) | Labels / inert (stay UTC) | Notes |
|---|---|---|---|
| Reports (`list-reports`, `get-report`, `query`, `get-report-config`, create/update/delete) | `createTime`, `updateTime` (epoch ms) | result `timestamp`/`datetime` columns (§6.2); config `customTimeRange`/`forecastSettings` `from`/`to` (RFC3339 query-window boundaries → midnight heuristic, §6.2) | **verified**: at daily *and* hourly grain the result column arrives as epoch-second **numbers** typed `"timestamp"` (hourly: `1787011200`, `+3600` steps); time dims (`year`/`month`/`day`/`hour`) are plain strings (`"08"`, `"00:00"`) — inert; no `"datetime"` column type observed (it is a request-side *dimension* type in the contract; `SchemaField.type` is an unconstrained string, so the CLI's `datetime` branch stays as defense) |
| Anomalies (`list-anomalies`, `get-anomaly`) | `acknowledgedAt`, `notifications[].timestamp` (RFC3339) | `startTime`/`endTime` (epoch ms — §2.1, §6.3) | resolved against contract |
| Insights (`get-insight-results`, `get-insight-result`, resource results, status updates) | `lastUpdated`, `lastStatusChange.lastChangedAt`, `enhancement.lastUpdatedAt` (RFC3339) | — | |
| Budgets (`list-budgets`, `get-budget`, create/update; suggestions) | `createTime`, `updateTime` (epoch ms — **verified**, real ms-of-day); suggestion `generatedTime` (RFC3339) | `startPeriod`/`endPeriod` (epoch ms **at midnight UTC** — **verified live**: `1785542400000` = 2026-08-01T00:00:00Z); `forecastedUtilizationDate` and `alerts[].forecastedDate` (epoch ms — **midnight UTC by construction**: both are produced by the same backend path, `getForecastedDateByValue` → `GetRowDate(…, TimeIntervalDay)` → `time.Date(y, m, d, 0, 0, 0, 0, time.UTC)` in doiteng/omni `budgets/service/forecast.go` + `query/utils/get_row_date.go`; corroborated by live `forecastedUtilizationDate` values at exactly 00:00:00Z — day-grain predictions, heuristic renders bare dates); `eligibleUsage[].usageTime` (usage buckets); `dailyCoverage[].date`, `monthlyStats[].month`, `planningCycle*Date`, `steps[].scheduledDate` (bare `date`/month strings — inert) | |
| Alerts (`list-alerts`, `get-alert`, create/update) | `createTime`, `updateTime` (epoch ms — **verified**) | — | the sweep's `forecastedDate` attribution here was a walker error: the field lives on `ExternalBudgetAlert` ("Budget alert status details") and surfaces under budgets, not analytics alerts — see Budgets row |
| Allocations | `createTime`, `updateTime` (epoch ms) | — | |
| Annotations | `createTime`, `updateTime`, **`timestamp`** (RFC3339 — **verified**: second precision, ≈1 s *before* `createTime`, i.e. the annotated moment, not a chart date) | — | reclassified instant by live data; a user-set chart date at midnight UTC would still render as a bare date via the §6.2 heuristic |
| Assets (`get-asset`, `id-of-asset(s)`, create) | `createTime` (epoch ms — **verified**), `subscription.creationTime` (epoch ms — **verified**, real ms-of-day), `subscription.plan.commitmentInterval.startTime`/`endTime` (epoch ms — **verified instants** against a Workspace-reselling customer, 2026-08-18: an ANNUAL plan shows `2023-12-28T17:07:50.145Z → 2026-12-28T17:07:50.145Z`, millisecond-precision activation-anniversary term like Savings Plans, §11.8 — despite the contract describing "dates of an annual commitment term") | — | `commitmentInterval` is null in practice on some ANNUAL plans and absent on FLEXIBLE — nullable either way; no §6.3 entry needed |
| Support tickets (`list-tickets`, `get-ticket`, comments) | `createTime`, `updateTime`, comment `created` (epoch ms) | — | |
| Invoices (`list-invoices`, `get-invoice`) | — | `invoiceDate`, `dueDate` (epoch ms **calendar dates** — **verified midnight-aligned**: `1767225600000` = 2026-01-01T00:00:00Z); `invoiceMonth` (string, inert) | localizing a due date across a day boundary is the §2 hazard verbatim. Data wart observed: credit memos carry a Go-zero-time sentinel `dueDate` (`-62135596800000`); negative → outside the `[1e12, 4.1e12)` window, renders as a raw number today (pre-existing, unaffected by this spec) |
| Billing Explainer | — | `timeRange.startDate`/`endDate` (`date`, inert); `trend[].bucketStart` (ISO date/week label, inert); `monthlyStats[].month` (inert) | |
| Known issues / cloud incidents | `createTime` (epoch ms) | — | |
| Contracts + contract templates (PartnerOps) | `timeCreated`, `createdAt`, `updatedAt`, `archivedAt` (RFC3339) | `startDate`/`endDate`, `versions[].startDate`/`endDate`/`effectiveStart`/`effectiveEnd` (RFC3339 **calendar dates** — protected by the midnight heuristic, §6.2) | |
| Commitment Manager | `createTime`, `updateTime` (epoch ms — **verified**, real ms-of-day) | `startDate`/`endDate`, `periods[].startDate`/`endDate` (RFC3339 calendar dates — **verified midnight-aligned**: `2026-01-01T00:00:00Z` / `2026-12-31T00:00:00Z`) | |
| Labels / Custom themes / Folders | `createTime`, `updateTime` (RFC3339) | — | |
| DataHub (`list-datahub-datasets`, events) | `lastUpdated` (**verified RFC3339**: `2026-07-30T08:23:00Z`), `import.syncedAt` (RFC3339) | — | |
| CloudFlow + connections + templates | `createdAt`, `updatedAt`, `lastExecutedTime`, `nextRun` (RFC3339 — `nextRun` is a scheduled *moment*, localizing helps) | — | |
| Cloud Diagrams | snapshot `createdAt`, activity `timestamp`s, export `metadata.date`, `observedAt` (RFC3339) | `trend[].bucketStart`, `timeRange.startDate`/`endDate` (bare dates, inert) | |
| PerfectScale for Commitments AWS (recommendations, RIs, savings plans, planned purchases, member accounts/orgs) | `onboardingStartedAt`, `savingsPlansSyncTime` (**verified** RFC3339, real time-of-day), inventory-sync `updatedAt`, `lastRefreshTime`, `effectiveTime` (RFC3339); RI/SP term `startTime`/`endTime` (**verified instants**: e.g. `2025-05-09T07:29:17Z` → `2028-05-08T07:29:16Z` — second-precision purchase-activation moments, `end = start + term − 1s`) | `planningCycleStartDate`/`EndDate`, `steps[].scheduledDate` (bare `date`, inert) | RIs empty in the checked account; classified with Savings Plans (same shape and semantics) |
| Billing Transfer (mappings, handshakes, DPMA) | `createdAt`, `updatedAt`, `effectiveTime`, `lastRefreshTime` (RFC3339) | — | |
| Users / Roles / invites | `timeLinked` (RFC3339) | — | |
| Customers, Organizations, Platforms, Products, Dimensions, AccountTeam, Auth, Ava, Sharing, Service Quotas, Cloud Connect, Statussheet | — | — | **no time-typed response fields** in the sweep |

Structural takeaways the main audit missed:

1. **Calendar-date business fields are a third population**, label-class but
   outside the report-schema path: RFC3339-at-midnight strings (contracts,
   commitments, versions) and epoch-ms-at-midnight integers (budget
   periods, invoice dates, commitment intervals). The midnight-UTC
   date-value heuristic (§6.2) protects the string form wholesale; the
   epoch-ms form gets the same heuristic plus the §6.3 exclusion list for
   any field that may not be midnight-aligned.
2. **Bare `format: date` strings and month/week labels are inert by
   construction** — shorter than `prettifyTimestamp`'s 20-char pre-check
   (main.go:2927), they never enter any time-formatting code. No action
   needed, in any phase.
3. The instant population is dominated by `create*/update*/*edAt/sync/
   refresh` fields — exactly the fields local display is for.

---

## 4. Display convention

### 4.1 Which zone

`time.Local` — Go resolves it from the `TZ` env var, falling back to the
system zone. An explicit `DCI_TZ` override (IANA name, e.g.
`Asia/Jerusalem`) takes precedence over `TZ` (§5), matching the repo's
`os.Getenv("DCI_*")` convention (`DCI_API_KEY` main.go:37, `DCI_API_BASE_URL`
main.go:44, `DCI_AGENT_MODE` main.go:311).

### 4.2 How the output indicates the zone

**Recommendation: a one-line stderr note, not header renaming.**

```
note: times shown in Asia/Jerusalem (UTC+03:00); pass --utc for UTC
```

Emitted at most once per invocation, only when (a) the resolved zone is not
UTC and (b) the table view actually rendered at least one localized instant.
Stderr keeps stdout parseable, consistent with the existing pivot and
row-cap notes (pivot.go:94, response_transform.go:81).

Why not `updated (IDT)`-style headers:

- **Headers are raw JSON keys.** `buildTableString` prints the row-map keys
  verbatim (main.go:2982–2987). Renaming `updateTime` → `updateTime (IDT)`
  breaks `-C` column selection (`getTableOptions`, main.go:2542), the
  hidden-columns hint that echoes a ready-to-paste `-C` list
  (main.go:2518–2519), and table/TOON key parity
  (`toonNormalizeRows` honors the same `-C` selection, main.go:2219).
- **Abbreviations are ambiguous.** IST is Israel, India, *and* Ireland; CST
  is China, Cuba, and US Central. Go frequently has no letter abbreviation
  at all and yields numeric forms like `+0530`.
- **DST makes any single per-listing zone tag partially wrong.** A 30-day
  listing spanning a US transition contains rows whose true local
  designation is EST and rows that are EDT. A header can only name one; a
  per-row suffix costs ~5 columns of width in every time cell. The wall-clock
  *values* are correct either way — each instant formats with its own
  offset — so the honest global descriptor is the IANA zone name, which is
  stable across the listing, plus the *current* offset as a parenthetical
  hint. That is exactly what the stderr note carries.

### 4.3 Machine formats stay timezone-less — explicitly

**json, yaml, csv, toon, and anything rendered in agent mode never show
machine-local times.** Agents and pipelines must produce identical output
regardless of the host's zone — reproducible transcripts, diffable CI
output, cacheable results. Concretely: json/yaml keep raw epochs and
untouched strings (audit #11), CSV keeps raw epochs / RFC3339 `Z` (audit
#9), TOON keeps raw epochs / RFC3339 `Z` (audit #10), and agent mode
(`agentMode`, main.go:81) forces UTC even if someone passes
`--output table --agent`. This is a hard rule in every phase, not a default.

---

## 5. Control surface

| Priority | Control | Effect |
|---|---|---|
| 1 | Machine format (json/yaml/csv/toon) or agent mode | Always UTC/epoch. Not overridable — `--utc` is a no-op here, and `DCI_TZ` is deliberately ignored (a deterministic agent transcript must not depend on host env). |
| 2 | `--utc` (new global flag, registered alongside `--raw-numbers`, main.go:1613) | Force UTC in the human table view — the escape hatch and the "restore today's behavior" switch. |
| 3 | `DCI_TZ=<IANA name>` | Render instants in that zone regardless of system zone (useful for shared terminals, containers where TZ is unset, or "show me what my Tel-Aviv teammate sees"). Invalid value → one stderr warning, fall back to priority 4. |
| 4 | Default | `time.Local` (respects `TZ`/system zone). |

**Default is local, not opt-in.** Local display for instants is the point of
the feature; an opt-in flag would leave the default experience unchanged and
the flag undiscovered. The blast radius is bounded because the label path
and every machine format are structurally excluded (§6), and `--utc`
restores current behavior exactly.

**Interaction with `--raw-numbers`:** unchanged and orthogonal. It already
short-circuits every conversion — `displayTimestampCell` (main.go:2865),
`tableCellText` (main.go:2673), `formatTableValue` (main.go:2901) — so raw
epochs remain available in table/TOON output; the zone question is moot on
that path. Document the pairing: `--raw-numbers` when you want the number,
`--utc` when you want the old string.

---

## 6. Implementation sketch

### 6.1 One resolver, resolved once

```go
// displayLocation returns the zone instants render in. UTC for every
// machine-consumed context; the user's zone only in the human table view.
func displayLocation() *time.Location {
    if agentMode || viper.GetBool("display-utc") {
        return time.UTC
    }
    switch strings.TrimSpace(viper.GetString("rsh-output-format")) {
    case "table", "auto":
        // human view; fall through
    default:
        return time.UTC // toon, json, yaml, csv — and, crucially,
        // "" (pipeline running outside a normal command: tests, internal
        // calls), mirroring shouldPivotReportRows' same fallback
        // (response_transform.go:369–376).
    }
    // DCI_TZ / time.Local resolution, cached per invocation.
    ...
}
```

Resolve once in the `PersistentPreRunE` that already sets
`invokedCommandName` (main.go:1629) into a package var, so tests can inject
a zone without fighting `time.Local` initialization (§10). Because
`formatValue` is shared by table and TOON (`joinPrimitives`, main.go:2299),
the format-gated resolver — not per-call-site plumbing — is what keeps local
times out of TOON.

The `"table", "auto"` human set matches the precedent in
`shouldPivotReportRows` (response_transform.go:372–375) and
`insightPresentationView` (response_transform.go:187–191, minus `toon`).

### 6.2 Split the label path out of `prettifyTimestamp`

The one structural change. Today: `displayTimestampCell` (label, schema-aware)
emits RFC3339 UTC → the string re-enters `formatTableValue` →
`prettifyTimestamp` (main.go:2906), the same function instant strings use.
If `prettifyTimestamp` becomes zone-aware, labels get localized — the exact
bug this spec exists to prevent.

Fix: in the **table** row path (`extractGetReportRows` as called from
`toTableRows`), format schema `timestamp`/`datetime` cells **terminally** —
`2026-08-01` (midnight collapse, honoring `report-hourly`) or
`2026-08-09 01:00`, always UTC. Both forms are shorter than 20 characters,
so `prettifyTimestamp`'s pre-check (main.go:2927) passes them through
untouched, mechanically guaranteeing labels never reach the zone-aware code.
The TOON path (`toonPrepare`, main.go:2113) keeps emitting full RFC3339 `Z`
as today, so agent output is byte-identical.

**Midnight-UTC = date-valued (the retained heuristic).** The full-surface
sweep (§3.1) shows `prettifyTimestamp`'s midnight collapse is not merely a
report-grain nicety: it is the only thing rendering calendar-date business
fields correctly today. A contract `endDate` of `2027-01-01T00:00:00Z`
collapses to `2027-01-01`; naively localized it becomes `2026-12-31 21:00`
in UTC−3 — a corrupted contract term. So the instant path keeps the rule,
reframed: **a value at exactly midnight UTC renders as a bare UTC date and
is never zone-shifted; only values with nonzero UTC time-of-day localize.**
(An earlier draft proposed dropping the collapse from the instant path;
the sweep reversed that.) The trade-off — a genuine event at exactly
00:00:00Z displays date-only — is the status quo today and loses nothing.
This single rule protects every RFC3339 calendar-date field (contracts,
commitments, versions) and every midnight-aligned epoch-ms date (budget
`startPeriod` per the spec's own example) without enumerating fields.

### 6.3 Label-field exclusion for epoch-ms window boundaries

The schema-typed label split (§6.2) protects report columns and the
midnight heuristic protects midnight-aligned values, but some epoch-ms
label fields in ordinary list/detail rows can carry a nonzero UTC
time-of-day and would leak through both guards: anomaly
`startTime`/`endTime` (hourly anomalies start mid-day — §2.1), and any
calendar-date field that turns out not to be midnight-aligned. Live-data
checks (§3.1) cleared most candidates: invoice `invoiceDate`/`dueDate` and
budget `startPeriod`/`endPeriod`/`forecastedUtilizationDate` are
midnight-aligned in practice, so the heuristic already covers them and
they need no entry; asset `commitmentInterval.startTime`/`endTime` turned
out to be millisecond-precision **instants** (§3.1), so it needs no entry
either — localizing it is correct; and budget `alerts[].forecastedDate` is
midnight UTC by construction in the producing backend code (§3.1). Every
candidate is now resolved: **the exclusion list is exactly anomaly
`startTime`/`endTime`**, which needs a per-command exclusion:
keep the named fields UTC when `invokedCommandName` (main.go:1629,
response_transform.go:317) matches the owning command. Precedent for
command-keyed shaping: `transformInsightsList` (response_transform.go:107).
A practical shape: mark the fields in the response transform (e.g. convert
them to their final UTC label strings there, the same terminal-formatting
trick as §6.2) rather than threading field names into `formatValue`. The
list stays short and documented next to the transform; anything not on it
is an instant by default. (`fitPriorityColumns` listing `startTime` /
`createTime` at main.go:3182 is display priority only — no semantic
coupling.)

### 6.4 Windows: embed tzdata

`DCI_TZ` needs `time.LoadLocation`, which on Windows requires the embedded
zone database: add `import _ "time/tzdata"` (~450 KB binary size). Without
it, `LoadLocation("Asia/Jerusalem")` fails on Scoop/WinGet installs even
though `time.Local` itself works. Worth calling out in the release notes for
whichever phase ships `DCI_TZ`.

---

## 7. Edge cases

- **Half-hour / 45-minute zones** (Asia/Kolkata +05:30, Pacific/Chatham
  +12:45): the `2006-01-02 15:04` minute-precision format handles them, and
  the midnight heuristic keys on **UTC** midnight (§6.2), so it behaves
  identically regardless of the viewer's offset — a calendar date stays a
  bare date in every zone.
- **DST transition mid-listing**: rows on either side format with different
  offsets — each is individually correct wall-clock. Covered by using the
  IANA name, not an abbreviation, in the zone note (§4.2). Spring-forward
  gaps can't occur (we convert *from* UTC, never parse local); fall-back
  means two distinct UTC instants can render the same local wall time —
  acceptable for metadata display, and the underlying epoch is one
  `--raw-numbers` or `--output json` away.
- **Sorting is unaffected by construction**: report rows sort on raw cells
  before display (`sortReportRows`, response_transform.go:507); insights
  sort by savings. No display change can reorder output.
- **Pivot/heatmap are unaffected by construction**: period keys are strings
  assembled from dimension columns (pivot.go:213), and heatmap period
  detection matches key shape (main.go:3114); neither touches a
  time-formatting function.
- **Existing tests assert UTC strings** — `TestFormatValue`
  (main_test.go:1209–1210), `TestDisplayTimestampCellRawNumbersPreservesEpoch`
  (main_test.go:1544), the `formatTableValue` midnight/hourly tests
  (main_test.go:1557–1573), `pivot_test.go:189`. They keep passing without
  edits because the resolver's empty-output-format fallback returns UTC in
  test context (§6.1). Tests that *want* a zone inject one explicitly (§10).
- **Docs mirror (omni)**: the help-center CLI reference is generated from
  omni's `generate-cli-docs` action (see memory note; the insights view's
  mirror is explicitly referenced at response_transform.go:102–105 →
  `command-notes/get-insight-results.mdx`). Shipping this feature requires
  an omni-side update for: the new global `--utc` flag (auto-generated flag
  reference), a `DCI_TZ` entry wherever `DCI_API_KEY`/`DCI_AGENT_MODE` env
  vars are documented, a behavior note that table timestamps are now local
  while report period columns remain UTC, and the get-insight-results notes
  page (`lastUpdated` rendering). README's user-facing sections need the
  same sweep if they mention timestamps.
- **Prompt-vs-code discrepancy, recorded for accuracy**: the brief that
  motivated this spec described a list-reports "updated (UTC)" derived
  column in response_transform.go. No such column exists at `fba1fa6`
  (audit #7); the audit covers what the code actually does.

---

## 8. What never changes (the "never" list)

1. Report result `timestamp`/`datetime` schema columns — UTC labels (audit #5).
2. Pivot period columns and the hour column — UTC labels (audit #6; `report-hourly`, response_transform.go:41).
2a. Anomaly `startTime`/`endTime` — usage-window bucket boundaries per the API contract (§2.1, §6.3).
2b. Calendar-date business fields — contract/commitment/version start/end dates, invoice `invoiceDate`/`dueDate`, budget `startPeriod`/`endPeriod` (§3.1; enforced by the midnight heuristic §6.2 and the §6.3 exclusions).
3. json / yaml / csv / toon byte output — timezone-less/UTC (audit #9–11).
4. Agent mode, any format — UTC/epoch, deterministic across hosts (§4.3).
5. Row ordering (§7).
6. `--raw-numbers` semantics (§5).

---

## 9. Recommended phased rollout

**Phase 0 — plumbing, zero visible change.**
Add `displayLocation()` returning UTC unconditionally; perform the
label/instant split (§6.2) with TOON/CSV output byte-identical and table
label output identical; embed `time/tzdata`; add characterization tests that
pin current UTC output *under injected non-UTC zones* (proving the label
path and machine formats are zone-inert). This phase is safe to merge alone
and is where review scrutiny pays off.

**Phase 1 — curated list views' instant columns.**
Flip the default to local for the epoch-ms metadata path (#1, #2, #4) and
the instant-string path (#3) — which, after Phase 0, is precisely "all
instants in the human table view" — plus the stderr zone note, `--utc`, and
`DCI_TZ`. The midnight-UTC date heuristic (§6.2) and the §6.3 exclusion
list (anomaly `startTime`/`endTime`, plus any §3.1 calendar-date epoch
field verified as not midnight-aligned) ship in the same change as the
path flip, never later. If a more cautious cut is wanted, gate by `invokedCommandName`
(precedent: `transformInsightsList`, response_transform.go:107) to
list-reports / list-budgets / list-insights / anomalies first; the
plumbing supports either. Ship the omni docs update in the same release.

**Phase 2 — everything remaining + polish.**
Remove any Phase-1 view gating so detail views (single-row tables,
main.go:2341) match list views; sweep stragglers (array-joined time cells
via `joinPrimitives`); decide the open questions (§11).

**Never** — the §8 list.

---

## 10. Testing strategy

- **Zone injection, not `TZ` mutation.** Go caches `time.Local` at process
  init; `t.Setenv("TZ", ...)` mid-test does not update it. The resolver
  therefore reads an injectable resolved location (package var set in
  PreRun, like `invokedCommandName`); tests set it directly, e.g.
  `withDisplayZone(t, "Asia/Kolkata")` using `time.LoadLocation` (available
  everywhere once `time/tzdata` is embedded).
- **Zone matrix**: UTC (default/fallback), `Asia/Jerusalem` (DST, whole
  hours), `Asia/Kolkata` (+05:30, no DST), `Pacific/Chatham` (+12:45),
  `America/New_York` with instants straddling the 2026-03-08 spring-forward
  and 2026-11-01 fall-back.
- **Invariant tests** (the important ones):
  - Report result fixture with a `timestamp` schema column rendered as
    table/CSV/TOON under `Pacific/Chatham` → labels byte-identical to the
    UTC run (label immunity).
  - Anomalies list fixture (epoch-ms `startTime`/`endTime`, plus
    `acknowledgedAt` and a notification `timestamp`) under a non-UTC zone →
    `startTime`/`endTime` byte-identical to the UTC run while
    `acknowledgedAt`/notification timestamps localize (§6.3 exclusion works
    and doesn't over-reach).
  - Contracts/commitments fixture (`startDate`/`endDate` at midnight UTC)
    and budgets fixture (`startPeriod` = `1704067200000`) under
    `Pacific/Chatham` → render as the same bare UTC dates as the UTC run
    (midnight heuristic holds for both RFC3339 and epoch-ms forms), while
    a non-midnight `updateTime` in the same row localizes.
  - Full TOON and json outputs under a non-UTC zone → byte-identical to UTC
    run (machine-format immunity), including agent mode with
    `--output table`.
  - Row order equality across zones (sorting invariant).
- **Instant tests**: epoch-ms `updateTime` and RFC3339 `lastUpdated` render
  as expected local wall time per zone; `--utc` restores the current
  golden strings; invalid `DCI_TZ` warns once and falls back.
- **Existing UTC assertions** stay untouched (empty-format fallback, §7).
- **CI**: run the test suite once with `TZ=Pacific/Chatham` exported (a
  deliberately hostile zone) to catch any accidental `time.Local`
  dependence outside the resolver.

---

## 11. Open questions for the maintainer

1. ~~**`datetime` schema columns**~~ — **resolved 2026-08-18 against live
   results** (daily `get-report` + hourly `query`): result timestamp
   columns arrive as epoch-second *numbers* typed `"timestamp"` at both
   grains; hour/day/month/year dims are short plain strings (inert); no
   `"datetime"` column type was observed — in the contract, `datetime` is a
   request-side *dimension* type, and `SchemaField.type` is an
   unconstrained string. No schema-aware string branch is needed; keep the
   CLI's existing `datetime` check as defense.
2. **CSV consistency** (audit #9): list-field epoch ms are raw while report
   schema timestamps are RFC3339 `Z` — both timezone-less, but inconsistent
   with each other. Normalize (breaking for CSV consumers), or leave?
3. ~~**Anomaly `startTime` semantics**~~ — **resolved 2026-08-18** against
   `https://api.doit.com/openapi.yaml`: it is the anomaly's *usage start
   time* at Daily/Hourly bucket grain, not a detection moment — a label.
   See §2.1 for the evidence and §6.3 for the required exclusion.
4. **Zone note fatigue**: stderr note on every table render vs. first render
   per invocation only (recommended: once per invocation). Any appetite for
   a config-file switch to silence it?
5. **Midnight alignment of epoch-ms calendar dates** (§3.1, §6.3) —
   **mostly resolved 2026-08-18 against live data**: invoice
   `invoiceDate`/`dueDate` and budget `startPeriod` are midnight-aligned
   (and commitment `startDate`/`endDate` on the RFC3339 side too).
   **Fully resolved 2026-08-18**: asset `commitmentInterval.startTime`/
   `endTime` verified against a Workspace-reselling customer context
   (maintainer-nominated). A populated ANNUAL commitment shows
   `startTime = 2023-12-28T17:07:50.145Z`,
   `endTime = 2026-12-28T17:07:50.145Z` — **millisecond-precision
   activation-anniversary instants** (identical time-of-day three years
   apart), the same family as Savings Plans terms (§11.8), despite the
   contract's "dates of an annual commitment term" wording. Classified
   **instant**; no §6.3 exclusion. Also observed: `commitmentInterval` can
   be null even on ANNUAL plans, and is absent on FLEXIBLE — renderers
   already treat missing values as blank cells.
6. ~~**Forecast-date fields**~~ — **fully resolved 2026-08-18, from the
   producing source code**. First, a sweep correction: `forecastedDate` is
   not an analytics-alert field at all — it lives on `ExternalBudgetAlert`
   ("Budget alert status details") and surfaces under budgets, which is
   why every `list-alerts` row showed null (the field does not exist
   there). Both it and `forecastedUtilizationDate` are computed by the
   same backend path — doiteng/omni
   `budgets/service/forecast.go` (`getForecastedDateByValue`, which sets
   `Config.Alerts[i].ForecastedDate` and `ForecastedTotalAmountDate` from
   the first *daily* forecast row crossing the threshold) →
   `query/utils/get_row_date.go` (`getDateFromRecord` builds
   `time.Date(y, m, d, 0, 0, 0, 0, time.UTC)` for `TimeIntervalDay`) — so
   both are **midnight UTC by construction**, day-grain labels covered by
   the §6.2 heuristic. Live `forecastedUtilizationDate` values at exactly
   00:00:00Z corroborate the code reading. No non-null
   `alerts[].forecastedDate` existed in reachable live data (own account
   and the nominated customer), so the code, not a sample, is the
   authority here — and it is the stronger evidence anyway.
7. ~~**Annotation `timestamp`**~~ — **resolved 2026-08-18 against live
   data**: values carry second precision ≈1 s before `createTime` — the
   annotated *moment*, not a chart date. Reclassified **instant** (§3.1);
   localizing is correct, and midnight-set chart dates still render as
   bare dates via the heuristic.
8. ~~**AWS RI/Savings-Plan term `startTime`/`endTime`**~~ — **resolved
   2026-08-18 against live data**: Savings Plans show second-precision
   activation moments (`2025-05-09T07:29:17Z` → `2028-05-08T07:29:16Z`,
   i.e. `start + 3y − 1s`) — **instants**, localize normally. RIs were
   empty in the checked account but share the shape and semantics.
9. ~~**DataHub `lastUpdated`**~~ — **resolved 2026-08-18 against live
   data**: RFC3339 (`2026-07-30T08:23:00Z`); localizes as an instant via
   `prettifyTimestamp` as designed.
