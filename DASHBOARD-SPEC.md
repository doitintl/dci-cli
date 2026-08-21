# Design spec: dashboards in `dci` — CLI commands and TUI view

Status: **draft for maintainer review**.
Audited at commit `94268b6`; every claim about existing code cites the function and file it is based on, line numbers approximate at that commit. API and console claims cite their source (live `openapi.yaml`, help.doit.com, or internal tickets) inline.

Scope: replicate the DoiT Console's dashboard functionality in the CLI — list dashboards, view a dashboard (CLI and full-screen TUI), create and update dashboards, and manage widgets. Everything human-interactive rides on the TUI-SPEC infrastructure already shipped (`tuiActive`, tui.go:28); agent mode, non-TTY, and CI keep the CLI's existing byte-level contracts.

---

## 1. Summary

| # | Feature | One line | Phase |
|---|---------|----------|-------|
| D0 | Dashboards public API | The blocking dependency: `/analytics/v1/dashboards` does not exist yet — contract proposed in §3, to be driven as an API ask | P0 (not this repo) |
| D1 | `list-dashboards` | Spec-derived command + curated table view (name, visibility, owner, widgets, updated) | P1 |
| D2 | `get-dashboard` | Spec-derived config fetch; name resolution, zero-arg picker, and completion arrive free | P1 |
| D3 | `view-dashboard` | The centerpiece: fetch every report widget's results and render the dashboard — stacked/line charts per widget in human mode, full-screen TUI browser | P2 (static) / P3 (TUI) |
| D4 | `create-dashboard` / `update-dashboard` / `delete-dashboard` | Spec-derived CRUD; body validation and the destructive gate arrive free | P1 |
| D5 | Widget management | `add-dashboard-widget` / `remove-dashboard-widget` + a resolver extension so `add-dashboard-widget`'s dashboard argument accepts names | P2 |
| D6 | Plumbing | `open dashboard`, paging cap, skill/docs updates, help epilog | P1 |

The striking property of this feature: **most of the CLI surface ships with zero CLI changes** the day the API ships, because the command tree is generated from the OpenAPI document at load time (`cli.AddLoader(openapi.New())`, main.go:322; `cli.Load`, main.go:338) and every cross-cutting contract — lockdown, output shaping, destructive gate, body validation, `--all` pagination — is derived from the spec rather than hand-registered. The real design work is (a) the API contract itself and (b) the dashboard *viewer*, which is a new composite renderer.

---

## 2. The hard dependency: there is no dashboard API today

Audit of the live spec (`https://api.doit.com/openapi.yaml`, fetched 2026-08-21): **zero occurrences of "dashboard"** across 589 KB / ~90 paths. The Cloud Analytics surface stops at reports, budgets, alerts, allocations, annotations, labels, folders, dimensions, themes, and commitments.

Independent confirmations:

- **CMP-47531** (Calendly reports-API escalation, July 2026): a customer writes *"creating report and adding report to a dashboard is so much manual process which we want to avoid"*; product replies *"There's currently no dashboard API available, but it should be on our road map."* That is both the demand signal and the gap in one thread.
- **CMP-7544** ("Dashboards Public API", epic, **Backlog since 2022-11**) already sketches the contract: `GET/POST /analytics/v1/dashboards`, `GET/PATCH/DELETE /analytics/v1/dashboards/{id}`, `POST /analytics/v1/dashboards/{id}/widgets`, `DELETE /analytics/v1/dashboards/{id}/widgets/{widgetId}`, a dashboard resource `{id, name, type: private|public, public: null|viewer|editor, owner, widgets[]}`, and report-only widgets `{id, type: "report", width: normal|wide|double|full}` for the first iteration.
- Internally, a dashboard document holds a widget name list (`cloudReports::<customerId>_<reportId>`); widget *data* is a separate cached collection refreshed on a schedule, with a ~1 MB per-document ceiling (eng runbook "Add a report-based widget to a public dashboard"; "24Q4 – Use widget data when opening reports" design doc). The public help docs say report widgets *"refresh automatically every two days"* with a manual per-widget refresh (help.doit.com, add-reports-to-dashboards).

Consequence for this spec: §3 is an **API contract proposal** (the ask to file against CMP-7544), and §§4–8 are the CLI/TUI design, written so that P1 is almost entirely "the API shipped" plus one small PR here.

A deliberate simplification falls out of the internal model: the CLI should **not** ask the API to expose the console's cached widget data. A report widget maps 1:1 to a saved report, and `GET /analytics/v1/reports/{id}` already returns that report's live results — the exact mechanism the Grafana tutorial uses to put console widgets on external dashboards (help.doit.com, cloud-analytics/tutorials/grafana). The CLI renders widgets by running their reports live: fresher than the console's two-day cache, at the cost of query time (§6.4).

---

## 3. Proposed API contract (D0 — the ask, not this repo's code)

Follows CMP-7544's shape, updated to the conventions the live spec settled on since 2022. Everything below is chosen so the CLI's spec-derived machinery lights up automatically; each constraint cites the rule it satisfies.

### 3.1 Endpoints

```
GET    /analytics/v1/dashboards                          listDashboards
POST   /analytics/v1/dashboards                          createDashboard
GET    /analytics/v1/dashboards/{id}                     getDashboard
PATCH  /analytics/v1/dashboards/{id}                     updateDashboard
DELETE /analytics/v1/dashboards/{id}                     deleteDashboard
POST   /analytics/v1/dashboards/{id}/widgets             addDashboardWidget
DELETE /analytics/v1/dashboards/{id}/widgets/{widgetId}  removeDashboardWidget
```

`listDashboards` takes the standard `maxResults` (default 50) / `pageToken` / `nameContains` / `filter` parameters and returns the standard wrapper — `{pageToken, rowCount, dashboards: [...]}` — mirroring `ReportList`. `updateDashboard` may replace the whole `widgets` array (CMP-7544's intent), making the two widget endpoints conveniences rather than the only path.

### 3.2 Dashboard resource

```json
{
  "id": "EE8CtpzYiKp0dVAESVrB",
  "name": "FinOps Weekly",
  "type": "custom",                  // custom | preset — same enum split as Report.type
  "visibility": "public",            // private | public  ("Public" = whole org, per console)
  "allowEdit": true,                 // the console's "Allow other users to edit widgets and layout"
  "owner": "vadim@doit.com",
  "createTime": 1755500000000,       // epoch ms, matching Report.createTime
  "updateTime": 1755700000000,
  "urlUI": "https://console.doit.com/customers/<id>/dashboards/...",
  "widgets": [ ... ]                 // §3.3; list items may carry widgetCount instead
}
```

Deliberate choices:

- **`name`, not `dashboardName`.** The resolver discovers display names at runtime from a fixed priority list — `name, reportName, budgetName, displayName, title, content` (`resourceNameFieldPriority`, name_resolution.go:676) — and `dashboardName` is not on it. `name` works today with zero CLI changes; `dashboardName` would cost a one-token CLI patch and a release. (CMP-7544 also said `name`.)
- **`visibility` + `allowEdit` instead of CMP-7544's `public: null|viewer|editor`.** The console's own creation dialog exposes exactly these two controls — Private/Public plus the edit checkbox that explicitly excludes name and visibility (help.doit.com, create-dashboard). The API should speak the product's language; `viewer|editor` encodes the same two bits less legibly.
- **`type: custom|preset`** mirrors `Report.type` (live spec). Preset dashboards (Pulse, AWS/GCP/Azure/BigQuery/GenAI Intelligence, …) should appear in listings read-only — they are half the reason a user lists dashboards — with create/update/delete rejected server-side. Whether preset *viewing* is in the API's v1 is an open question (§10 Q2).
- **Epoch-ms times** (`createTime`/`updateTime`) match every sibling resource, so the curated view's `updated (UTC)` column, local-time rendering, and `sortRowsByEpochDesc` (list_views.go:356) apply unchanged.
- **`urlUI`** matches reports and enables the OSC-8 hyperlink column (`table-link-url-key`, list_views.go:326) and `open` fallbacks.

### 3.3 Widget resource

```json
{
  "id": "b9dP...q2",              // widget id, stable within the dashboard
  "type": "report",               // v1 manages only report widgets, per CMP-7544
  "reportId": "2vPmkQ...",
  "reportName": "Monthly AWS Spend",   // denormalized for display; server-maintained
  "width": "normal"               // normal | wide | double | full — console tile widths
}
```

The console's widget library has three categories — widgets (Budget Utilization, Active Cloud Incidents, Latest Invoices, …), reports, and assets — plus carousel widgets (help.doit.com, custom-dashboards/widgets). v1 of the API **lists** non-report widgets (`type` names them, other fields omitted) so `get-dashboard` is truthful about what a dashboard contains, but only report widgets are creatable via `addDashboardWidget`: `{reportId, width?}`. This matches CMP-7544's first-iteration scope and the actual ask ("attach report to dashboard", CMP-47531).

### 3.4 Path-shape rationale (why these exact URIs)

`buildResolutionIndex` derives which commands get name→ID resolution from URI shapes alone (name_resolution.go:88–134): a resolvable operation has exactly one path parameter (:102) which is the **last** segment (:110–113), a parent path free of parameters (:114–117), and a zero-param GET list operation on that parent (:122–125). `/analytics/v1/dashboards` + `/analytics/v1/dashboards/{id}` satisfies all four, so `get-dashboard`, `update-dashboard`, and `delete-dashboard` accept fuzzy names, get the zero-arg picker (`zeroArgPickerApplies`, tui_picker.go:28), and Tab-complete cached names (name_completion.go:190) **with zero CLI changes**.

The widget endpoints do not qualify — `POST .../{id}/widgets` has its parameter mid-path, `DELETE .../{id}/widgets/{widgetId}` has two — so D5 specs a small resolver extension (§7.2) rather than contorting the API into non-REST shapes to please the CLI.

---

## 4. What ships with zero CLI changes (audit)

When the API of §3 appears in `openapi.yaml`, the following all engage on the next spec-cache refresh, with no commit to this repo:

- **Commands exist.** Restish generates one command per operation at load (main.go:322, :338); lockdown keeps every spec-derived command and removes nothing API-grouped (`lockToDCI`, main.go:991–1032). `dci commands` catalogs them automatically (`buildCommandCatalog`, command_catalog.go:119).
- **Resolution/picker/completion** for the `{id}` operations, per §3.4. `list-dashboards` feeds the name cache (`fetchResourceNames`, name_resolution.go:600; cache TTLs at name_completion.go:33–34).
- **Destructive gate.** `delete-dashboard` and `remove-dashboard-widget` are DELETE, hence gated by method (`isDestructiveOperation`, destructive_contract.go:152–154): `--yes`/`--dry-run`/exit-30 vocabulary and the interactive confirm (destructive_contract.go:241) all apply. `update-dashboard` stays ungated, consistent with `update-report` (§10 Q4).
- **Body validation** for `create-dashboard`/`update-dashboard`/`add-dashboard-widget` — top-level field checking against the generated `## Request Schema` help (body_validation.go:52, :105), stdin and shorthand alike.
- **`--all` pagination.** Token-driven and transport-level (`paginatingTransport.RoundTrip`, pagination.go:154); the standard `pageToken` wrapper is discovered generically (`collectionPageToken`, pagination.go:296).
- **Output contracts.** Table/JSON/YAML/TOON shaping, `--fields`, timestamps, agent-mode defaults — all response-guard machinery is command-agnostic (main.go:1890–1991).

The one-PR P1 delta in this repo is §5 + §8.

---

## 5. D1 — `list-dashboards` curated view

One entry in the `listViews` registry (list_views.go:67), shaped like `list-reports` (list_views.go:68):

```go
"list-dashboards": {
    itemsKey: "dashboards",
    columns: []viewColumn{
        {title: "dashboard name", source: "name", derive: starredNameCell},
        {title: "visibility"},          // private | public
        {title: "type"},                // custom | preset
        {title: "widgets", source: "widgetCount"},
        {title: "owner"},
        {title: "updated (UTC)", source: "updateTime"},
    },
    linkURLKey: "urlUI",
    sortField:  "updateTime",
},
```

Everything downstream is existing machinery: epoch sort (list_views.go:356), local-time titles (list_views.go:343), the OSC-8 link on the lead column (main.go:3407), TOON/agent parity via `presentationView` (response_transform.go:217). If the list item carries a `widgets` array instead of a `widgetCount`, the column becomes a `derive` counting it — same pattern as existing derived cells (list_views.go:375–550).

Plus a `pagingCaps` entry once the real server cap is known (pagination.go:32), and a help epilog note in `augmentVerifiedFlagHelp` (main.go:1148) if dashboards need one.

---

## 6. D3 — `view-dashboard`: the composite renderer

The console experience being replicated: open a dashboard, see its widgets rendered. In API terms that is one `getDashboard` + N `getReport` calls; no single spec-derived command can do it, so this is a **local command** (like `open`, `docs`, `query`'s builder) — the first one that composes multiple API calls into one rendering.

### 6.1 Command shape

`dci view-dashboard [name-or-id]` — name resolution via the same static-path mechanism `open` uses (`openResourceListPaths`, name_resolution.go:733; `resolveOpenResourceID`, :771), zero-arg picker via `pickOpenResourceID` (tui_picker.go:132). Flags: `--widgets <n,m,…>` (subset), `--chart/--no-chart` passthrough, standard `--output`.

**Agent mode / non-TTY (recommendation):** `view-dashboard` is a human rendering; agents get a `USAGE_ERROR` with the recipe hint — *"Run: dci get-dashboard <id> then dci get-report <reportId> per widget"* — consistent with the CLI's "charts are decoration" contract and with the skill teaching composable calls. The alternative (a composite JSON envelope on stdout) is §10 Q3.

### 6.2 Static human view (P2)

For each report widget, sequentially: fetch `GET /analytics/v1/reports/{reportId}` (the resolver's programmatic-call pattern: bearer token + tenant context, `fetchSettingsJSON`, charts.go:250), feed the `result.{schema, rows}` payload through the *same* classification and pivot the single-response path uses — the pivot is a pure function over schema+rows (`pivotReportBody`, pivot.go:20; `classifyPivotColumns`, pivot.go:238) — and render one titled section per widget:

```
━━ FinOps Weekly ── public · 4 widgets · updated 2h ago ━━━━━━━━━━━━━

▌ Monthly AWS Spend                                      line_chart
    2.1k ┤                                          ╭──
    1.8k ┤                    ╭─────╮      ╭────────╯
    1.5k ┼────────────────────╯     ╰──────╯
         cost by period — 2026-02 → 2026-08

▌ Cost by Top Services                          stacked_column_chart
    ██ ▂▂ ██ ██ …  █ BigQuery 41%  █ GCE 27%  █ other (12 groups) 9%

▌ Budget Utilization                                          budgets
    (widget type not renderable in the terminal — 2 budgets; run
     dci list-budgets)
```

Chart per widget follows the report's own saved renderer — `ExternalConfig.layout`, enum `ExternalRenderer` (live spec), fetched per widget via `GET /analytics/v1/reports/{id}/config` or (better) denormalized into the widget resource (§10 Q5):

| Report `layout` | Terminal rendering |
|---|---|
| `line_chart`, `spline_chart`, `area_*` | asciigraph line of period totals (charts.go:104–110) |
| `column_chart`, `bar_chart`, `stacked_*` | ntcharts stacked columns + share legend (`renderStackedChart`, charts.go:295), same color-capability fallback to the line (`stackedChartRenderable`, charts.go:139) |
| `table`, `table_*_heatmap` | compact table (existing renderer; heatmap shading per `heatmapEnabled`, main.go:755) capped at ~10 rows with an elision note |
| `treemap_chart` | group share legend only (the stacked chart's legend, charts.go:339–345, without bars) |
| `csv_export`, `sheets_export` | table + note |

Chart colors already follow the user's console report theme (`chartThemePalette`, charts.go:188; live theme fetch, charts.go:218) — dashboards inherit that for free. Non-report widgets render the one-line placeholder naming their type and the sibling command that shows the data (`list-budgets` already grows a Budget Utilization bar column, `augmentTableViewColumns`, charts.go:362).

Output discipline: the whole rendering goes to **stdout** (it *is* the data of this command, like the static table today); progress spinner to stderr (`startTUISpinner`, tui.go:215).

### 6.3 Full-screen TUI view (P3)

`view-dashboard` on a TTY when `tuiActive()` (tui.go:28) upgrades to an alt-screen bubbletea program — the second full-screen program after the table viewer, same construction (`tea.NewProgram(..., tea.WithAltScreen(), tea.WithOutput(os.Stderr))`, tui_viewer.go:53):

- **Tiles.** One tile per widget in a vertical flow (single column; two columns when the terminal is ≥120 cells wide — `detectTerminalWidth`, main.go:2737). `width: full/double` widgets always span the row. Charts re-render at tile width.
- **Navigation.** `↑↓`/`tab` move focus between tiles; `enter` drills the focused widget into the existing interactive table viewer over that widget's pivot rows (`runTableViewer` seam, tui_viewer.go:49 — it takes plain `rows, keys`); `esc` returns to the dashboard.
- **`o`** opens the focused widget's full report in the console (`consoleResourceURL`, open_command.go:106); **`O`** opens the dashboard itself.
- **`r`** re-fetches the focused widget; **`R`** all widgets (the console's per-widget refresh and "Refresh all widgets", help.doit.com).
- **`q`/ctrl-C** quit. Exit prints nothing to stdout (unlike the table viewer's selection contract — a dashboard has no single selection).
- **Loading.** Tiles render immediately with a per-tile spinner and fill in as results arrive (bubbletea messages per completed fetch), so the first chart is visible in seconds even on a slow dashboard.

Not in v1: editing from inside the viewer (add/remove/resize widgets interactively), dashboard-level time-range/filter overrides (the console treats those as viewer-local "local changes" anyway), and rendering preset-dashboard system widgets beyond the placeholder.

### 6.4 Fetch behavior and limits

Report execution is the expensive part. Known constraints: the report endpoint effectively serializes per report (`ReportRequestQueueLimit = 1`, CMP-47531) and long queries can hit the ~100 s gateway ceiling; the Grafana plugin deliberately runs one query at a time per instance to avoid throttling. The viewer therefore fetches with **bounded concurrency (2–3 distinct reports in flight)**, a per-widget timeout with a readable per-tile error state (widget errors never fail the dashboard), and no caching in v1 — `r`/`R` is the refresh story. If the planned async report API (CMP-47531 thread) ships, it slots in behind the same tile-fill mechanism.

---

## 7. D4/D5 — create, update, and widget management

### 7.1 CRUD (P1, free)

`create-dashboard` and `update-dashboard` work like every body operation: stdin JSON or shorthand args, validated against the request schema (body_validation.go:52). The README/skill get the canonical recipes:

```
dci create-dashboard name: "FinOps Weekly", visibility: public, allowEdit: true
dci update-dashboard "FinOps Weekly" visibility: private
dci delete-dashboard "FinOps Weekly"            # destructive gate, exit 30 without --yes
```

`update-dashboard`'s body may replace `widgets` wholesale (§3.1) — that is the scriptable bulk path (reorder, re-width, prune), and `get-dashboard --output json | jq … | dci update-dashboard <id>` is the round-trip the output contract already supports.

### 7.2 Widget commands and the resolver extension (P2)

`add-dashboard-widget <dashboard> reportId: <id>` and `remove-dashboard-widget <dashboard> <widgetId>` arrive as spec commands, but without name resolution for `<dashboard>` (§3.4). The extension: `buildResolutionIndex` learns a second accepted shape — **exactly one path parameter, not in last position, with only static segments after it** (relaxing name_resolution.go:110–113, leaving the one-parameter gate at :102 in place); the resource segment is the one *preceding* the parameter, and the existing parent-list requirement (:122–125) applies to the path up to the parameter. That covers `add-dashboard-widget` only. `remove-dashboard-widget` has **two** path parameters, so it is rejected at the :102 gate before any shape check and takes literal IDs for both arguments — dashboard and widget ids are read off `get-dashboard`, which is the natural flow anyway; per-parameter resolution (resolving one segment of a two-parameter path) would need a different index shape than the current one-target-per-operation map (name_resolution.go:126–131) and is deliberately not proposed. Needs the same wrapper-injection care F1 documented (`installPickerArgInjection`, tui_picker.go:110) plus tests over the new shape.

The `reportId:` body field gets no completion (body operations short-circuit it, name_completion.go:194–199). The human answer is an interactive report picker when `add-dashboard-widget` is invoked with a dashboard but no body on a TTY — the query-builder trigger pattern exactly (`runQueryBuilderHook`, tui_querybuilder.go:63: TTY stdin, zero body, `tuiActive()`), reusing the report name cache the F1 picker reads. Small, but P2, and severable (§10 Q6).

---

## 8. D6 — plumbing (P1, this repo, small)

- **`open dashboard [name]`**: entries in `consoleResourcePaths` (open_command.go:26) and `openResourceListPaths` (name_resolution.go:733). Console path: `dashboards` (verify the exact console route at implementation). Note: `open_command_test.go:146` uses `"widget"` as its *unknown-resource* fixture — leave `widget` unmapped or update the fixture.
- **Skill updates** (`skills/dci-cli/`): SKILL.md gains a Dashboards section (list/view/create/attach-report recipes); `references/capabilities.md:34–41` gains the Dashboards command family; new or changed reference files need their digests added to `releasedSkillFileDigests` (skill_management.go:22–56) so the updater's local-modification check recognizes released content.
- **Docs**: README command-group blurb; no restish jargon (AGENTS.md convention).
- **Versioning**: P1/P2 are patch releases; the TUI dashboard viewer (P3) plausibly anchors a minor bump ("new command groups, major UX overhauls", AGENTS.md).

---

## 9. Phasing

- **P0 — API** (not this repo): file the contract of §3 as the ask against CMP-7544 / the roadmap item product acknowledged in CMP-47531. Nothing in P1+ can ship without at least `listDashboards`/`getDashboard`.
- **P1 — free surface + small PR**: §4 arrives with the API; this repo adds the list view, `open` mapping, paging cap, skill/docs (§5, §8). One patch release.
- **P2 — static viewer + widget UX**: `view-dashboard` static rendering (§6.2), resolver extension + `add-dashboard-widget` picker (§7.2).
- **P3 — TUI dashboard browser**: §6.3. The largest piece; behind `tuiActive()` end to end.

P2/P3 are severable: if P3 slips, the static view is already the core value ("list dashboards, then view a dashboard").

---

## 10. Open questions for the maintainer

1. **API ask routing.** §3 as a comment/attachment on CMP-7544, or a fresh issue referencing it? The CLI side of this spec assumes the *shape* (§3.4's path rules, `name` field, epoch times, standard wrapper) even if fields get renamed — flag early if API conventions have moved.
2. **Preset dashboards in scope?** Listing them read-only is half the user value (Pulse, cloud Intelligence dashboards), but their system widgets are largely non-renderable in a terminal (§6.2 placeholders). Include in `listDashboards` v1, or custom-only first?
3. **Agent-mode `view-dashboard`.** Recommended: usage error + recipe hint (§6.1). Alternative: a composite JSON envelope `{dashboard, widgets: [{…, result}]}` — convenient for agents but a new output shape to freeze. Which?
4. **`update-dashboard` destructiveness.** PATCH with full `widgets` replacement can wipe a layout in one call. Leave ungated (consistent with `update-report`), or add to `explicitlyDestructiveOperations` (destructive_contract.go:34)?
5. **Renderer source.** Denormalize the report's `layout` into the widget resource (one fetch per dashboard) vs. `GET reports/{id}/config` per widget (N extra calls)? Spec recommends denormalizing.
6. **Widget-add picker (P2)** worth it, or is shorthand-with-report-id enough for v1?
7. **Fetch concurrency** (§6.4): is 2–3 in flight acceptable to the API team, or should the viewer serialize like the Grafana plugin does?
