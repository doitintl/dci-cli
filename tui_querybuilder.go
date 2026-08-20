package main

// F4 (TUI-SPEC): the interactive query builder. `dci query` on an interactive
// terminal with nothing piped walks through composing a report config — time
// range, group-by dimensions fed live from the dimensions collection, metric,
// zero-row filter — shows the JSON, and either runs it, saves it and runs it,
// or prints it and exits. The print branch is the point, not a bonus: the
// builder teaches the scriptable `dci query <query.json` interface. Kept in a
// sibling file per the AGENTS.md chapter-split guidance.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

type queryDimension struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

type queryTimePreset struct {
	label    string
	amount   int
	unit     string
	interval string
}

var queryTimePresets = []queryTimePreset{
	{"Last 7 days", 7, "day", "day"},
	{"Last 30 days", 30, "day", "day"},
	{"Last 90 days", 90, "day", "week"},
	{"Last 3 months", 3, "month", "month"},
	{"Last 12 months", 12, "month", "month"},
}

const (
	queryActionRun = iota
	queryActionSaveAndRun
	queryActionPrint
)

// maybeRunQueryBuilder opens the builder for `dci query` when a human is at
// the terminal with no piped stdin and no body-shorthand args. It either
// substitutes the composed config as the request body (so validation,
// --dry-run, and output shaping behave exactly as with a piped file), or
// prints the JSON and neuters the run. A var so tests can fake it.
var maybeRunQueryBuilder = runQueryBuilderHook

func runQueryBuilderHook(cmd *cobra.Command, args []string) error {
	if cmd.Name() != "query" || len(args) > 0 || !tuiActive() {
		return nil
	}
	// tuiActive already requires a TTY stdin, but the body input may have
	// been redirected independently; a non-terminal body keeps today's path.
	if info, err := cli.Stdin.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	configJSON, action, err := buildQueryInteractively(resolutionCustomerContext(cmd))
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nameSelectionCancelledError("query builder")
		}
		return err
	}
	switch action {
	case queryActionPrint:
		fmt.Fprintln(os.Stdout, string(configJSON))
		neuterCommandRun(cmd)
		return nil
	case queryActionSaveAndRun:
		path, err := saveQueryConfig(configJSON)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "saved to %s — rerun any time with: dci query <%s\n", path, path)
	}
	cli.Stdin = &bufferedBodyInput{
		Reader: bytes.NewReader(configJSON),
		info:   builderBodyInfo{size: int64(len(configJSON))},
	}
	return nil
}

// neuterCommandRun turns the command into a no-op after the builder already
// produced the invocation's output (the dry-run guard's pattern).
func neuterCommandRun(cmd *cobra.Command) {
	cmd.Run = nil
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return nil }
}

// builderBodyInfo satisfies the Stat restish performs on the body input; a
// zero mode (not a char device) makes it read the buffered config.
type builderBodyInfo struct{ size int64 }

func (info builderBodyInfo) Name() string       { return "query.json" }
func (info builderBodyInfo) Size() int64        { return info.size }
func (info builderBodyInfo) Mode() fs.FileMode  { return 0 }
func (info builderBodyInfo) ModTime() time.Time { return time.Time{} }
func (info builderBodyInfo) IsDir() bool        { return false }
func (info builderBodyInfo) Sys() interface{}   { return nil }

func buildQueryInteractively(context string) ([]byte, int, error) {
	stopSpinner := startTUISpinner("Fetching dimensions…")
	dimensions, err := fetchQueryDimensions(context)
	stopSpinner()
	if err != nil {
		return nil, 0, err
	}

	presetIndex := 1 // Last 30 days
	presetOptions := make([]huh.Option[int], len(queryTimePresets))
	for i, preset := range queryTimePresets {
		presetOptions[i] = huh.NewOption(preset.label, i)
	}
	includeCurrent := false
	interval := ""
	intervalOptions := []huh.Option[string]{
		huh.NewOption("match the time range preset", ""),
		huh.NewOption("hour", "hour"),
		huh.NewOption("day", "day"),
		huh.NewOption("week", "week"),
		huh.NewOption("month", "month"),
	}
	dimensionOptions := make([]huh.Option[int], len(dimensions))
	for i, dimension := range dimensions {
		label := dimension.Label
		if label == "" {
			label = dimension.ID
		}
		dimensionOptions[i] = huh.NewOption(fmt.Sprintf("%s  %s", label, tuiDimStyle.Render("("+dimension.Type+":"+dimension.ID+")")), i)
	}
	var groupSelection []int
	metric := "cost"
	dropZeroRows := true

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[int]().
				Title("Time range").
				Options(presetOptions...).
				Value(&presetIndex),
			huh.NewConfirm().
				Title("Include the current (partial) period?").
				Affirmative("Include").
				Negative("Exclude").
				Value(&includeCurrent),
			huh.NewSelect[string]().
				Title("Time interval").
				Options(intervalOptions...).
				Value(&interval),
		),
		huh.NewGroup(
			huh.NewMultiSelect[int]().
				Title(fmt.Sprintf("Group by (%d dimensions — type / to filter, x to toggle)", len(dimensionOptions))).
				Options(dimensionOptions...).
				Filterable(true).
				Height(14).
				Value(&groupSelection),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Metric").
				Options(
					huh.NewOption("cost", "cost"),
					huh.NewOption("usage", "usage"),
					huh.NewOption("savings", "savings"),
				).
				Value(&metric),
			huh.NewConfirm().
				Title("Drop zero-metric rows? (server-side metricFilter > 0)").
				Affirmative("Drop").
				Negative("Keep").
				Value(&dropZeroRows),
		),
	).WithOutput(os.Stderr).WithInput(os.Stdin).WithWidth(tuiWidth()).WithShowHelp(true)
	if err := form.Run(); err != nil {
		return nil, 0, err
	}

	groups := make([]queryDimension, 0, len(groupSelection))
	for _, index := range groupSelection {
		groups = append(groups, dimensions[index])
	}
	configJSON, err := composeQueryConfig(queryTimePresets[presetIndex], includeCurrent, interval, groups, metric, dropZeroRows)
	if err != nil {
		return nil, 0, err
	}

	fmt.Fprintln(os.Stderr, "\n"+string(configJSON))
	action := queryActionRun
	review := huh.NewSelect[int]().
		Title("Query config composed — what next?").
		Options(
			huh.NewOption("Run now", queryActionRun),
			huh.NewOption("Save as query.json and run", queryActionSaveAndRun),
			huh.NewOption("Print the JSON and exit", queryActionPrint),
		).
		Value(&action)
	if err := tuiForm(review).Run(); err != nil {
		return nil, 0, err
	}
	return configJSON, action, nil
}

// composeQueryConfig renders the report config JSON (the `dci query` request
// body) from the builder's answers, using the shapes documented in
// skills/dci-cli/references/query-patterns.md.
func composeQueryConfig(preset queryTimePreset, includeCurrent bool, interval string, groups []queryDimension, metric string, dropZeroRows bool) ([]byte, error) {
	if interval == "" {
		interval = preset.interval
	}
	config := map[string]interface{}{
		"dataSource":   "billing",
		"timeInterval": interval,
		"timeRange": map[string]interface{}{
			"mode":           "last",
			"amount":         preset.amount,
			"unit":           preset.unit,
			"includeCurrent": includeCurrent,
		},
		"metrics": []interface{}{
			map[string]interface{}{"type": "basic", "value": metric},
		},
	}
	if len(groups) > 0 {
		groupEntries := make([]interface{}, len(groups))
		for i, group := range groups {
			groupEntries[i] = map[string]interface{}{"id": group.ID, "type": group.Type}
		}
		config["group"] = groupEntries
	}
	if dropZeroRows {
		config["metricFilter"] = map[string]interface{}{
			"metric":   map[string]interface{}{"type": "basic", "value": metric},
			"operator": "gt",
			"values":   []interface{}{0},
		}
	}
	return json.MarshalIndent(map[string]interface{}{"config": config}, "", "  ")
}

// saveQueryConfig writes the composed config next to the invocation without
// clobbering an existing file: query.json, then query-2.json, and so on.
func saveQueryConfig(configJSON []byte) (string, error) {
	path := "query.json"
	for counter := 2; ; counter++ {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			break
		}
		path = "query-" + strconv.Itoa(counter) + ".json"
	}
	if err := os.WriteFile(path, append(configJSON, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// fetchQueryDimensions pages through the dimensions collection with the same
// programmatic-call pattern as fetchResourceNames (bearer token, tenant
// header, legacy query param). The collection holds ~1,000 entries at 500 per
// page. A var so tests can fake it.
var fetchQueryDimensions = fetchQueryDimensionsLive

func fetchQueryDimensionsLive(context string) ([]queryDimension, error) {
	token := authenticationToken()
	if token == "" {
		return nil, authenticationRequiredPreflightError()
	}
	base, err := apiBase()
	if err != nil {
		return nil, err
	}
	const listPath = "/analytics/v1/dimensions"
	dimensions := []queryDimension{}
	pageToken := ""
	for page := 0; page < 10; page++ {
		requestURL, err := url.Parse(base + listPath)
		if err != nil {
			return nil, err
		}
		query := requestURL.Query()
		query.Set("maxResults", "500")
		if context != "" {
			query.Set("customerContext", context)
		}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		requestURL.RawQuery = query.Encode()
		request, err := http.NewRequest(http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("User-Agent", buildUserAgent(agentUAMode))
		if context != "" {
			request.Header.Set("X-Tenant-Id", context)
		}
		response, err := resolverHTTPClient.Do(request)
		if err != nil {
			return nil, nameResolutionNetworkError{err: err}
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized {
			return nil, apiRejectedTokenError(base)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, resolverLookupStatusError(listPath, response)
		}
		if readErr != nil {
			return nil, nameResolutionNetworkError{err: readErr}
		}
		var parsed struct {
			Dimensions []queryDimension `json:"dimensions"`
			PageToken  string           `json:"pageToken"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("parse dimensions listing: %w", err)
		}
		dimensions = append(dimensions, parsed.Dimensions...)
		pageToken = parsed.PageToken
		if pageToken == "" {
			break
		}
	}
	return dimensions, nil
}
