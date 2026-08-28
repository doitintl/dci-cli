package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

// transformSuccessBody applies the response-shaping pipeline to successful
// (2xx, JSON) bodies before any output format renders them:
//
//  1. integral float64 values become int64 so no format prints scientific
//     notation for IDs and epoch timestamps
//  2. null list wrappers become [] so consumers never need a null guard
//  3. report result rows are stable-sorted (the API's order is
//     nondeterministic run-to-run)
//  4. all-empty report rows (null group, zero metrics) are dropped unless
//     --include-empty-rows is set
//  5. report rows are capped in agent mode (default 500; --max-rows overrides,
//     0 = unlimited) with explicit omission markers
//  6. --pivot reshapes report rows into a time-as-columns pivot view
//  7. --rows keyed re-keys positional report rows into schema-named objects
func transformSuccessBody(body interface{}) interface{} {
	body = normalizeIntegralNumbers(body)
	body = nullListsToEmpty(body)
	// Before transformInsightsList and applyListView: search matches raw item
	// fields, not presentation-derived text (the insights " (easy win)" title
	// suffix, formatted savings) or the curated view's renamed columns —
	// otherwise the match set would change with the output format.
	body = applyListSearch(body)
	body = transformInsightsList(body)
	// Before applyListView: the view's title rewrite consults the UTC-label
	// registry to keep anomaly window columns labeled and rendered in UTC.
	markAnomalyWindowColumns()
	body = applyListView(body)

	container, rows, schema, ok := reportResultContainer(body)
	if !ok {
		return body
	}

	// Hourly reports must keep the time-of-day everywhere: without this flag
	// the table renderer would collapse the midnight row to a bare date while
	// its siblings show hours.
	viper.Set("report-hourly", hasHourColumn(schema))

	// Currency context travels with the result: agents reading TOON/JSON get
	// an explicit `currency` field, and the table renderer knows which
	// symbol to print. Resolved from the query request config (the response
	// itself carries no currency — an API gap tracked in doiteng/omni#62101);
	// when the request declared none AND the result has money-typed columns,
	// the API's documented default (USD) applies. Usage-only results stay
	// unstamped — a currency on unit metrics would mislabel them (F4).
	currency := requestCurrencyContext()
	moneyColumns := moneyMetricColumns(schema)
	if currency == "" && len(moneyColumns) > 0 {
		currency = "USD"
	}
	if currency != "" {
		if _, present := container["currency"]; !present {
			container["currency"] = currency
		}
		viper.Set("report-currency", currency)
		viper.Set("money-columns", strings.Join(moneyColumns, ","))
	}

	sortReportRows(rows, schema)

	if !viper.GetBool("include-empty-rows") {
		kept, dropped := dropEmptyReportRows(rows, schema)
		if dropped > 0 {
			rows = kept
			container["rows"] = rows
			container["emptyRowsDropped"] = int64(dropped)
		}
	}

	if viper.GetBool("drop-unlabeled-rows") {
		kept, dropped := dropUnlabeledReportRows(rows, schema)
		if dropped > 0 {
			rows = kept
			container["rows"] = rows
			container["unlabeledRowsDropped"] = int64(dropped)
		}
	}

	if rollup := strings.TrimSpace(viper.GetString("report-rollup")); rollup != "" {
		rows, schema = applyReportRollup(container, rows, schema, rollup)
	}

	if shouldPivotReportRows() {
		if pivoted, ok := pivotReportBody(rows, schema); ok {
			return pivoted
		}
	}

	if limit := effectiveMaxRows(); limit > 0 && len(rows) > limit {
		total := len(rows)
		rows = rows[:limit]
		container["rows"] = rows
		container["rowsTotal"] = int64(total)
		container["rowsOmitted"] = int64(total - limit)
		if cli.Stderr != nil {
			_, _ = fmt.Fprintf(cli.Stderr, "note: %d of %d rows omitted; narrow the query (group limit, metricFilter) or pass --max-rows to adjust\n", total-limit, total)
		}
	}

	if strings.EqualFold(viper.GetString("rows-mode"), "keyed") {
		container["rows"] = keyReportRows(rows, schema)
	}

	return body
}

// transformInsightsList shapes list-insights responses. Dismissed insights are
// dropped from the results by default — they are noise in a "what should I act
// on" listing — with --include-dismissed restoring them and a dismissedOmitted
// marker recording how many were removed (mirrors emptyRowsDropped on
// reports). The default table/TOON view additionally gets a curated column
// set: title and shortDescription lead, cloudProvider is shown as provider, an
// easyWin marker is derived from easyWinDescription, and long-form or internal
// fields (detailedDescriptionMdx, key, cloudFlowTemplateId, displayStatus)
// stay out of the default columns. Machine formats (json, yaml, csv) and
// explicit -C/--fields selections keep the raw field names.
//
// Keep in sync with the help-center mirror of this behavior: omni
// .github/workflows/actions/generate-cli-docs/command-notes/get-insight-results.mdx
// (rendered into help.doit.com/docs/cli/generated/command-groups/insights/get-insight-results).
func transformInsightsList(body interface{}) interface{} {
	if invokedCommandName != "list-insights" {
		return body
	}
	root, ok := body.(map[string]interface{})
	if !ok {
		return body
	}
	results, ok := root["results"].([]interface{})
	if !ok {
		return body
	}

	if !viper.GetBool("include-dismissed") {
		kept := make([]interface{}, 0, len(results))
		dropped := 0
		for _, item := range results {
			if row, ok := item.(map[string]interface{}); ok {
				if status, _ := row["displayStatus"].(string); strings.EqualFold(strings.TrimSpace(status), "dismissed") {
					dropped++
					continue
				}
			}
			kept = append(kept, item)
		}
		if dropped > 0 {
			results = kept
			root["results"] = kept
			root["dismissedOmitted"] = int64(dropped)
		}
	}

	sortInsightsBySavings(results, !terminalOrderActive())

	if presentationView() {
		applyInsightPresentation(results)
	}
	return body
}

// applyListSearch implements --search: a client-side, case-insensitive
// substring filter over list items, matched against every string leaf (and
// map key) of each item. It exists for discovery flows the API cannot serve —
// list-dimensions holds ~1,000 entries across ~20 pages and its --filter only
// does exact field:value matches — and it implies --all, so the whole
// collection is searched rather than one page. Non-matching items are dropped
// with an explicit searchDropped marker so a small result is distinguishable
// from a small collection; rowCount is corrected when present.
func applyListSearch(body interface{}) interface{} {
	needle := strings.ToLower(strings.TrimSpace(viper.GetString("list-search")))
	if needle == "" {
		return body
	}
	root, ok := body.(map[string]interface{})
	if !ok {
		return body
	}
	key, rows, isList := listWrapperRows(root)
	if !isList {
		return body
	}
	kept := make([]interface{}, 0, len(rows))
	for _, item := range rows {
		if valueContainsFold(item, needle) {
			kept = append(kept, item)
		}
	}
	if len(kept) == len(rows) {
		return body
	}
	root[key] = kept
	root["searchDropped"] = int64(len(rows) - len(kept))
	if _, present := root["rowCount"]; present {
		root["rowCount"] = int64(len(kept))
	}
	return body
}

// valueContainsFold reports whether any string leaf under value contains
// needle (already lowercased). Map keys are matched too: label maps like
// {"team": "core"} should hit on either side.
func valueContainsFold(value interface{}, needle string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(strings.ToLower(typed), needle)
	case map[string]interface{}:
		for mapKey, nested := range typed {
			if strings.Contains(strings.ToLower(mapKey), needle) || valueContainsFold(nested, needle) {
				return true
			}
		}
	case []interface{}:
		for _, nested := range typed {
			if valueContainsFold(nested, needle) {
				return true
			}
		}
	}
	return false
}

// markAnomalyWindowColumns flags the anomaly usage-window boundaries as UTC
// label columns for the table renderer. Per the API contract, startTime is
// the anomaly's *usage start time* at Daily/Hourly bucket grain — a
// data-bucket label, not a detection moment — so unlike other epoch-ms
// metadata it must never render in the viewer's zone: an hourly anomaly
// starting 01:00 UTC would relabel onto the previous local day. The other
// anomaly time fields (acknowledgedAt, notification timestamps) are genuine
// instants and stay on the localizing path.
func markAnomalyWindowColumns() {
	switch invokedCommandName {
	case "list-anomalies", "get-anomaly":
		// Raw field names cover detail views and explicit -C selections;
		// "started (UTC)" covers the curated list view's renamed column.
		viper.Set("utc-label-columns", "startTime,endTime,started (UTC)")
	}
}

// sortInsightsBySavings orders insights by potential daily savings — highest
// first so the most valuable insights lead machine formats and classic
// ordering, lowest first under terminal ordering so the top insight lands
// nearest the prompt (OUTPUT-ORDER-SPEC §4). The sort is stable: rows without
// savings keep the API's order.
func sortInsightsBySavings(rows []interface{}, highestFirst bool) {
	if !highestFirst {
		sort.SliceStable(rows, func(i, j int) bool {
			return insightDailySavings(rows[i]) < insightDailySavings(rows[j])
		})
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return insightDailySavings(rows[i]) > insightDailySavings(rows[j])
	})
}

// formatUSD renders a USD amount with cents and digit grouping ("$1,234.56").
// Unlike formatMoney, which rounds to whole units for cloud-bill-scale report
// cells, daily savings are small enough that cents carry signal.
func formatUSD(amount float64) string {
	cents := int64(math.Round(math.Abs(amount) * 100))
	sign := ""
	if amount < 0 && cents != 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s$%s.%02d", sign, groupDigits(strconv.FormatInt(cents/100, 10)), cents%100)
}

// insightDailySavings reads summary.potentialDailySavings (documented as USD)
// from an insight row; 0 when absent.
func insightDailySavings(item interface{}) float64 {
	row, ok := item.(map[string]interface{})
	if !ok {
		return 0
	}
	summary, ok := row["summary"].(map[string]interface{})
	if !ok {
		return 0
	}
	savings, _ := numericCell(summary["potentialDailySavings"])
	return savings
}

// presentationView reports whether a curated default column view applies
// (the list-insights and list-reports views): table-like formats only
// (table, auto, toon — same set shouldPivotReportRows treats as
// presentational), and only when the user has not made an explicit column
// selection via -C or --fields.
func presentationView() bool {
	switch strings.TrimSpace(viper.GetString("rsh-output-format")) {
	case "table", "auto", "toon":
	default:
		return false
	}
	return strings.TrimSpace(viper.GetString("table-columns")) == ""
}

// insightHiddenColumns are list-insights fields kept out of the default
// column set: prose (detailedDescriptionMdx, shortDescription;
// easyWinDescription is folded into the title highlight), internal
// identifiers (key, cloudFlowTemplateId), displayStatus (dismissed rows are
// filtered instead), low-signal extras (reportUrl, tags), and cloudProvider
// (shown as provider).
var insightHiddenColumns = map[string]bool{
	"detailedDescriptionMdx": true,
	"shortDescription":       true,
	"cloudFlowTemplateId":    true,
	"easyWinDescription":     true,
	"key":                    true,
	"displayStatus":          true,
	"cloudProvider":          true,
	"reportUrl":              true,
	"tags":                   true,
}

// applyInsightPresentation derives the display columns on each row and pins
// the default column order: the headline fields (title, dailySavings from
// summary.potentialDailySavings, provider, categories) first, then every
// remaining scalar field alphabetically. The order is marked auto-set (like
// the pivot's) so the terminal-width fit still trims overflow columns. The
// title column is marked width-priority so it renders untruncated whenever
// the other columns can spare the space. Easy wins (non-empty
// easyWinDescription) get an " (easy win)" title suffix in every
// presentation format plus a green title in interactive tables (via the
// hidden per-row easyWin flag), and a row's reportUrl becomes an OSC 8
// terminal hyperlink on its title.
func applyInsightPresentation(rows []interface{}) {
	if len(rows) == 0 {
		return
	}
	present := map[string]bool{}
	for _, item := range rows {
		row, ok := item.(map[string]interface{})
		if !ok {
			return // not an insight list shape; leave the response raw
		}
		for k := range row {
			present[k] = true
		}
	}

	for _, item := range rows {
		row := item.(map[string]interface{})
		if provider, ok := row["cloudProvider"]; ok {
			row["provider"] = provider
		}
		// USD per the API schema; rows without savings leave the cell blank
		// rather than printing a column of zeros.
		if savings := insightDailySavings(row); savings > 0 {
			row["dailySavings"] = formatUSD(savings)
		}
		if present["easyWinDescription"] {
			marker := ""
			if desc, _ := row["easyWinDescription"].(string); strings.TrimSpace(desc) != "" {
				marker = "✓"
				if title, ok := row["title"].(string); ok && title != "" {
					row["title"] = title + " (easy win)"
				}
			}
			row["easyWin"] = marker
		}
	}

	headline := make([]string, 0, 4)
	for _, column := range []string{"title", "dailySavings", "provider", "categories"} {
		source := column
		switch column {
		case "provider":
			source = "cloudProvider"
		case "dailySavings":
			source = "summary"
		}
		if present[source] {
			headline = append(headline, column)
		}
	}
	rest := make([]string, 0, len(present))
	for column := range present {
		if insightHiddenColumns[column] || column == "title" || column == "categories" {
			continue
		}
		if insightColumnContainsObject(rows, column) {
			continue
		}
		rest = append(rest, column)
	}
	sort.Strings(rest)

	linkColumn, linkURLKey := "", ""
	if present["reportUrl"] {
		linkColumn, linkURLKey = "title", "reportUrl"
	}
	accentColumn, accentFlagKey := "", ""
	if present["easyWinDescription"] {
		accentColumn, accentFlagKey = "title", "easyWin"
	}
	setListViewConfig(append(headline, rest...), "title", linkColumn, linkURLKey, accentColumn, accentFlagKey)
}

func insightColumnContainsObject(rows []interface{}, key string) bool {
	for _, item := range rows {
		if row, ok := item.(map[string]interface{}); ok && containsObject(row[key]) {
			return true
		}
	}
	return false
}

// requestReportCurrency is the currency resolved from the request body of the
// current invocation (set by preflight for query-style commands; "" when the
// command carries no report config).
var requestReportCurrency string

// invokedCommandName is the leaf cobra command of the current invocation (set
// by the dci PersistentPreRunE; "" outside a normal command run), letting the
// response pipeline key command-specific shaping without guessing from the
// body shape.
var invokedCommandName string

func requestCurrencyContext() string {
	return requestReportCurrency
}

// moneyMetricColumns returns the schema columns that carry monetary values —
// float metrics whose name denotes money (cost, amortized_cost, amount, …);
// "usage" and other unit metrics stay plain numbers.
func moneyMetricColumns(schema []reportColumn) []string {
	money := []string{}
	for _, col := range schema {
		if col.Type != "float" && col.Type != "number" {
			continue
		}
		if moneyNamedColumn(col.Name) {
			money = append(money, col.Name)
		}
	}
	return money
}

func hasHourColumn(schema []reportColumn) bool {
	for _, col := range schema {
		if strings.EqualFold(col.Name, "hour") {
			return true
		}
	}
	return false
}

func moneyNamedColumn(name string) bool {
	lower := strings.ToLower(name)
	// "total" and "balance" are exact matches for the invoice view's columns;
	// the pivot's totals column never money-formats through this path because
	// pivot rows carry no currency field.
	return strings.Contains(lower, "cost") || lower == "amount" || strings.Contains(lower, "spend") ||
		strings.Contains(lower, "savings") || lower == "total" || lower == "balance"
}

// shouldPivotReportRows decides whether report rows render as a pivot.
// Explicit flags always win (--pivot forces it anywhere, --flat disables).
// Otherwise the pivot is the default *human* report view: table output in
// human mode with no explicit column selection (a -C selection addresses the
// flat columns, so it keeps the flat layout). Machine formats (json, yaml,
// csv, toon) and agent mode stay flat.
func shouldPivotReportRows() bool {
	if viper.GetBool("pivot-rows") {
		return true
	}
	if viper.GetBool("flat-rows") {
		return false
	}
	if agentMode {
		return false
	}
	// PreRun always resolves the output format; an empty value means the
	// pipeline is running outside a normal command (tests, internal calls)
	// where surprising a consumer with a pivot is worse than staying flat.
	output := strings.TrimSpace(viper.GetString("rsh-output-format"))
	if output != "table" && output != "auto" {
		return false
	}
	return strings.TrimSpace(viper.GetString("table-columns")) == ""
}

// effectiveMaxRows resolves the report-row cap: an explicit --max-rows wins
// (0 disables), otherwise agent mode defaults to 500 and human mode to
// unlimited.
const defaultAgentMaxRows = 500

func effectiveMaxRows() int {
	// -1 (or unset) means auto: capped in agent mode, unlimited otherwise.
	if viper.IsSet("max-rows") {
		if v := viper.GetInt("max-rows"); v >= 0 {
			return v
		}
	}
	if agentMode {
		return defaultAgentMaxRows
	}
	return 0
}

// reportResultContainer locates the Cloud Analytics result container
// (result/results object holding a rows array), returning the container, its
// positional rows, and the parsed schema columns.
func reportResultContainer(body interface{}) (map[string]interface{}, []interface{}, []reportColumn, bool) {
	root, ok := body.(map[string]interface{})
	if !ok {
		return nil, nil, nil, false
	}
	for _, key := range []string{"result", "results"} {
		container, ok := root[key].(map[string]interface{})
		if !ok {
			continue
		}
		rows, ok := container["rows"].([]interface{})
		if !ok {
			continue
		}
		return container, rows, readReportSchema(container["schema"]), true
	}
	return nil, nil, nil, false
}

// reportColumn is a parsed result-schema column.
type reportColumn struct {
	Name string
	Type string
}

func readReportSchema(rawSchema interface{}) []reportColumn {
	schema, ok := rawSchema.([]interface{})
	if !ok {
		return nil
	}
	columns := make([]reportColumn, 0, len(schema))
	for _, col := range schema {
		m, ok := col.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		colType, _ := m["type"].(string)
		if strings.TrimSpace(name) != "" {
			columns = append(columns, reportColumn{Name: name, Type: colType})
		}
	}
	return columns
}

// normalizeIntegralNumbers converts whole float64 values (the product of JSON
// decoding int64 fields) back to int64 in place, so JSON/YAML/table output
// prints 1786353374392 instead of 1.786353374392e+12.
func normalizeIntegralNumbers(value interface{}) interface{} {
	const maxSafeInteger = 1 << 53
	switch v := value.(type) {
	case map[string]interface{}:
		for k, child := range v {
			v[k] = normalizeIntegralNumbers(child)
		}
		return v
	case []interface{}:
		for i, child := range v {
			v[i] = normalizeIntegralNumbers(child)
		}
		return v
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < maxSafeInteger {
			return int64(v)
		}
		return v
	default:
		return value
	}
}

// nullListsToEmpty replaces null top-level fields with [] on list-shaped
// responses (identified by their pagination/count metadata), so empty
// collections are always arrays.
func nullListsToEmpty(body interface{}) interface{} {
	root, ok := body.(map[string]interface{})
	if !ok || !hasListMetadata(root) {
		return body
	}
	collectionKey := ""
	for k, v := range root {
		if v != nil || isListMetadataKey(k) {
			continue
		}
		if collectionKey != "" {
			return body
		}
		collectionKey = k
	}
	if collectionKey != "" {
		root[collectionKey] = []interface{}{}
	}
	return body
}

func isListMetadataKey(key string) bool {
	switch key {
	case "count", "rowCount", "total", "totalCount", "pageToken", "nextPageToken", "cursor", "nextCursor", "hasMore":
		return true
	default:
		return false
	}
}

// sortReportRows stable-sorts positional report rows cell-by-cell (numbers
// numerically, everything else as strings, nulls first) so identical queries
// render identically across runs.
func sortReportRows(rows []interface{}, schema []reportColumn) {
	sort.SliceStable(rows, func(i, j int) bool {
		return compareReportRows(rows[i], rows[j]) < 0
	})
}

func compareReportRows(a, b interface{}) int {
	cellsA, okA := a.([]interface{})
	cellsB, okB := b.([]interface{})
	if !okA || !okB {
		return strings.Compare(fmt.Sprintf("%v", a), fmt.Sprintf("%v", b))
	}
	for i := 0; i < len(cellsA) && i < len(cellsB); i++ {
		if c := compareCells(cellsA[i], cellsB[i]); c != 0 {
			return c
		}
	}
	return len(cellsA) - len(cellsB)
}

func compareCells(a, b interface{}) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	na, aNum := numericCell(a)
	nb, bNum := numericCell(b)
	if aNum && bNum {
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(fmt.Sprintf("%v", a), fmt.Sprintf("%v", b))
}

func numericCell(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// dropEmptyReportRows removes noise rows: a row is empty when at least one
// string-typed (dimension) cell is null and every numeric (metric) cell is
// zero or null. Rows that identify a real group with a genuine zero cost are
// kept — their dimensions are non-null.
func dropEmptyReportRows(rows []interface{}, schema []reportColumn) ([]interface{}, int) {
	if len(schema) == 0 {
		return rows, 0
	}
	kept := make([]interface{}, 0, len(rows))
	dropped := 0
	for _, row := range rows {
		cells, ok := row.([]interface{})
		if !ok || !isEmptyReportRow(cells, schema) {
			kept = append(kept, row)
			continue
		}
		dropped++
	}
	if dropped == 0 {
		return rows, 0
	}
	return kept, dropped
}

// dropUnlabeledReportRows removes rows where every grouped label dimension is
// null, regardless of metric value — the opt-in (--drop-unlabeled-rows)
// complement of dropEmptyReportRows' conservative default. Grouping all
// billing data by a sparse label yields one giant row aggregating every
// unlabeled cost; that bucket is sometimes the question ("how much spend is
// NOT labeled?"), which is why this never applies implicitly. The API's
// explicit "[Value N/A]" marker counts as null: it is the server's own way
// of saying the label does not apply to the row.
//
// "Every", not "any": providers label mutually exclusive subsets (an
// Anthropic row carries genai/cost_type but never genai/billing_category;
// a Cursor row the reverse), so a multi-label grouping has some null label
// on nearly every row. Only the all-null row is the unlabeled bucket.
func dropUnlabeledReportRows(rows []interface{}, schema []reportColumn) ([]interface{}, int) {
	checked := unlabeledCheckColumns(schema)
	if len(checked) == 0 {
		return rows, 0
	}
	kept := make([]interface{}, 0, len(rows))
	dropped := 0
	for _, row := range rows {
		cells, ok := row.([]interface{})
		if !ok || !allLabelCellsNull(cells, checked) {
			kept = append(kept, row)
			continue
		}
		dropped++
	}
	if dropped == 0 {
		return rows, 0
	}
	return kept, dropped
}

// unlabeledCheckColumns picks the schema columns whose joint nullness defines
// an unlabeled row: the label-derived group columns from the request config
// when it is available (query bodies arrive buffered), otherwise every
// string-typed dimension except the datetime parts — year/month/day columns
// are always populated and would mask the null bucket.
func unlabeledCheckColumns(schema []reportColumn) []int {
	labelGroups := requestLabelGroupIDs()
	timeParts := map[string]bool{}
	for _, part := range pivotTimeParts {
		timeParts[part] = true
	}
	indexes := []int{}
	for i, col := range schema {
		if col.Type != "string" || timeParts[strings.ToLower(col.Name)] {
			continue
		}
		if labelGroups != nil && !labelGroups[col.Name] {
			continue
		}
		indexes = append(indexes, i)
	}
	return indexes
}

func allLabelCellsNull(cells []interface{}, checked []int) bool {
	for _, i := range checked {
		if i >= len(cells) {
			continue
		}
		if cells[i] != nil && cells[i] != "[Value N/A]" {
			return false
		}
	}
	return true
}

// requestLabelGroupIDs returns the ids of label-derived group dimensions in
// the buffered request config (nil when no config is available, e.g. saved
// reports fetched by id).
func requestLabelGroupIDs() map[string]bool {
	if len(bufferedRequestBody) == 0 {
		return nil
	}
	var body struct {
		Config struct {
			Group []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"group"`
		} `json:"config"`
	}
	if err := json.Unmarshal(bufferedRequestBody, &body); err != nil || len(body.Config.Group) == 0 {
		return nil
	}
	labelTypes := map[string]bool{
		"label": true, "tag": true, "project_label": true,
		"system_label": true, "gke_label": true, "gke": true,
	}
	ids := map[string]bool{}
	for _, group := range body.Config.Group {
		if labelTypes[group.Type] {
			ids[group.ID] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func isEmptyReportRow(cells []interface{}, schema []reportColumn) bool {
	hasNullDimension := false
	for i, col := range schema {
		if i >= len(cells) {
			break
		}
		switch col.Type {
		case "string":
			if cells[i] == nil {
				hasNullDimension = true
			}
		case "float", "integer", "number":
			if cells[i] == nil {
				continue
			}
			if n, ok := numericCell(cells[i]); !ok || n != 0 {
				return false
			}
		}
	}
	return hasNullDimension
}

// applyReportRollup aggregates report rows client-side (--rollup, decision
// AI-FINOPS-SPEC follow-up): rows are grouped by the requested result
// columns, numeric metric columns are summed, and every other column —
// including per-period timestamps — is dropped. This exists because the
// server always emits one row per group × time bucket, so "total per X over
// the period" otherwise lands on the consumer as row-level arithmetic: for a
// model that means minutes of error-prone reasoning; for a human, a
// spreadsheet step. Unknown columns skip the rollup with a rollupError
// marker naming the valid columns, so an agent can self-correct in one step.
func applyReportRollup(container map[string]interface{}, rows []interface{}, schema []reportColumn, rollup string) ([]interface{}, []reportColumn) {
	keyIndexes := []int{}
	valid := make([]string, 0, len(schema))
	for _, requested := range strings.Split(rollup, ",") {
		requested = strings.TrimSpace(requested)
		if requested == "" {
			continue
		}
		found := -1
		for i, col := range schema {
			if strings.EqualFold(col.Name, requested) {
				found = i
				break
			}
		}
		if found < 0 {
			for _, col := range schema {
				valid = append(valid, col.Name)
			}
			container["rollupError"] = fmt.Sprintf("column %q is not in the result schema (columns: %s)", requested, strings.Join(valid, ", "))
			return rows, schema
		}
		keyIndexes = append(keyIndexes, found)
	}
	if len(keyIndexes) == 0 {
		return rows, schema
	}
	keyed := map[int]bool{}
	for _, i := range keyIndexes {
		keyed[i] = true
	}
	sumIndexes := []int{}
	for i, col := range schema {
		if keyed[i] {
			continue // a numeric rollup key groups; it must not also sum
		}
		switch strings.ToLower(col.Type) {
		case "float", "int", "integer", "number":
			sumIndexes = append(sumIndexes, i)
		}
	}

	type bucket struct {
		keys []interface{}
		sums []float64
	}
	order := []string{}
	buckets := map[string]*bucket{}
	for _, row := range rows {
		cells, ok := row.([]interface{})
		if !ok {
			continue
		}
		keyParts := make([]string, len(keyIndexes))
		keys := make([]interface{}, len(keyIndexes))
		for j, i := range keyIndexes {
			if i < len(cells) {
				keys[j] = cells[i]
			}
			keyParts[j] = fmt.Sprintf("%v", keys[j])
		}
		key := strings.Join(keyParts, "\x1f")
		b, seen := buckets[key]
		if !seen {
			b = &bucket{keys: keys, sums: make([]float64, len(sumIndexes))}
			buckets[key] = b
			order = append(order, key)
		}
		for j, i := range sumIndexes {
			if i < len(cells) {
				if v, ok := numericCell(cells[i]); ok {
					b.sums[j] += v
				}
			}
		}
	}

	rolledSchema := make([]reportColumn, 0, len(keyIndexes)+len(sumIndexes))
	rawSchema := make([]interface{}, 0, cap(rolledSchema))
	for _, i := range keyIndexes {
		rolledSchema = append(rolledSchema, schema[i])
		rawSchema = append(rawSchema, map[string]interface{}{"name": schema[i].Name, "type": schema[i].Type})
	}
	for _, i := range sumIndexes {
		rolledSchema = append(rolledSchema, schema[i])
		rawSchema = append(rawSchema, map[string]interface{}{"name": schema[i].Name, "type": schema[i].Type})
	}
	rolled := make([]interface{}, 0, len(order))
	for _, key := range order {
		b := buckets[key]
		cells := make([]interface{}, 0, len(b.keys)+len(b.sums))
		cells = append(cells, b.keys...)
		for _, sum := range b.sums {
			cells = append(cells, sum)
		}
		rolled = append(rolled, cells)
	}
	sortReportRows(rolled, rolledSchema)
	container["rows"] = rolled
	container["schema"] = rawSchema
	container["rolledUpFrom"] = int64(len(rows))
	return rolled, rolledSchema
}

// liftConstantReportColumns removes report columns that carry the identical
// string (or null) value in every row of a TOON result, recording them once
// in container.constantColumns. A single-period grouped query repeats the
// same timestamp in all rows — hundreds of copies of one value is pure token
// cost and pure reading work for the consuming model. Numeric cells are
// never lifted (metrics are the data even when constant), small results are
// left alone (nothing to save), and explicit field selections (-C, custom -f
// filters) are honored — requested columns are never dropped.
const liftConstantMinRows = 10

func liftConstantReportColumns(container map[string]interface{}, rows []map[string]interface{}) {
	if len(rows) < liftConstantMinRows {
		return
	}
	opts := toonRowOptionsFromConfig()
	if opts.keepAll || len(opts.selected) > 0 {
		return
	}
	constant := map[string]interface{}{}
	for key, first := range rows[0] {
		switch first.(type) {
		case string, nil:
		default:
			continue
		}
		same := true
		for _, row := range rows[1:] {
			value, present := row[key]
			if !present || value != first {
				same = false
				break
			}
		}
		if same {
			constant[key] = first
		}
	}
	if len(constant) == 0 || len(constant) == len(rows[0]) {
		return
	}
	for key := range constant {
		for _, row := range rows {
			delete(row, key)
		}
	}
	container["constantColumns"] = constant
}

// keyReportRows converts positional rows into schema-named objects so JSON
// consumers can address fields by name instead of zipping with the schema.
func keyReportRows(rows []interface{}, schema []reportColumn) []interface{} {
	names := make([]string, len(schema))
	for i, col := range schema {
		names[i] = col.Name
	}
	keyed := make([]interface{}, len(rows))
	for rowIndex, row := range rows {
		cells, ok := row.([]interface{})
		if !ok {
			keyed[rowIndex] = row
			continue
		}
		obj := make(map[string]interface{}, len(cells))
		for i, cell := range cells {
			obj[reportColumnName(names, i)] = cell
		}
		keyed[rowIndex] = obj
	}
	return keyed
}
