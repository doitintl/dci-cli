package main

// Name → ID resolution: positional resource arguments that look like names are
// resolved against the parent collection's list endpoint before the request is
// built. Kept in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// resolutionListTarget describes where a resolvable operation's names live: the
// parent collection list endpoint derived from URI templates in the cached spec.
type resolutionListTarget struct {
	listPath      string
	resource      string
	listOperation string
}

// resolutionExcludedResources removes collections from the derived index. The
// legacy attributions/attributiongroups endpoints are superseded by allocations.
var resolutionExcludedResources = map[string]bool{
	"attributions":      true,
	"attributiongroups": true,
}

var resolutionIndex = map[string]resolutionListTarget{}

type resolvedTarget struct {
	input    string
	resource string
	name     string
	id       string
}

type resolvedTargetPayload struct {
	Input string `json:"input"`
	Name  string `json:"name"`
	ID    string `json:"id"`
}

// resolvedTargets records successful resolutions per command so the
// destructive confirmation can display the true target.
var resolvedTargets = map[string]resolvedTarget{}

// idShapedPathArgument remembers a positional argument that skipped resolution
// because it matched the ID shape, so a later 404 can hint at --name.
var idShapedPathArgument string

func resetNameResolutionState() {
	resolutionIndex = map[string]resolutionListTarget{}
	resolvedTargets = map[string]resolvedTarget{}
	idShapedPathArgument = ""
}

func setResolutionIndex(operations []cli.Operation) {
	resolutionIndex = buildResolutionIndex(operations)
}

func buildResolutionIndex(operations []cli.Operation) map[string]resolutionListTarget {
	listOperations := map[string]string{}
	for _, operation := range operations {
		if !strings.EqualFold(operation.Method, http.MethodGet) || len(operation.PathParams) > 0 {
			continue
		}
		path := uriTemplatePath(operation.URITemplate)
		if path == "" || strings.Contains(path, "{") {
			continue
		}
		listOperations[path] = operation.Name
	}
	index := map[string]resolutionListTarget{}
	for _, operation := range operations {
		if len(operation.PathParams) != 1 {
			continue
		}
		path := uriTemplatePath(operation.URITemplate)
		segments := strings.Split(strings.Trim(path, "/"), "/")
		if len(segments) < 2 {
			continue
		}
		last := segments[len(segments)-1]
		if !strings.HasPrefix(last, "{") || !strings.HasSuffix(last, "}") {
			continue
		}
		parent := "/" + strings.Join(segments[:len(segments)-1], "/")
		if strings.Contains(parent, "{") {
			continue
		}
		resource := segments[len(segments)-2]
		if resolutionExcludedResources[resource] {
			continue
		}
		listOperation, ok := listOperations[parent]
		if !ok {
			continue
		}
		index[operation.Name] = resolutionListTarget{
			listPath:      parent,
			resource:      resource,
			listOperation: listOperation,
		}
	}
	return index
}

func uriTemplatePath(template string) string {
	parsed, err := url.Parse(template)
	if err != nil {
		return ""
	}
	return parsed.Path
}

// resourceIDPattern is the Firestore auto-ID shape. Deliberately stricter than
// looksLikeCustomerID: a false "looks like an ID" here silently skips resolution.
var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// resolvePathArguments rewrites a name-shaped positional argument into the
// resource ID it resolves to. Cobra passes the same args slice to Run, so the
// in-place mutation propagates into restish's URI substitution.
func resolvePathArguments(cmd *cobra.Command, args []string) error {
	target, ok := resolutionIndex[cmd.Name()]
	if !ok || len(args) == 0 {
		return nil
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return nil
	}
	if boolFlagSet(cmd, "id") {
		return nil
	}
	input := strings.TrimSpace(args[0])
	if input == "" {
		return nil
	}
	if !boolFlagSet(cmd, "name") && resourceIDPattern.MatchString(input) {
		idShapedPathArgument = input
		return nil
	}
	if params := operationPathParameters[cmd.Name()]; len(params) > 0 {
		if declared := params[0].Type; declared != "" && declared != "string" {
			return nil
		}
	}
	resolved, err := resolveResourceName(input, target, resolutionCustomerContext(cmd))
	if err != nil {
		return err
	}
	args[0] = resolved.id
	resolvedTargets[cmd.Name()] = resolved
	announceResolution(resolved)
	return nil
}

// boolFlagSet reports whether a boolean flag is set to true. An operation-local
// flag of the same name but a different type (e.g. a `name` query parameter)
// shadows the persistent flag and must not be misread as the escape hatch.
func boolFlagSet(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Value.Type() == "bool" && flag.Value.String() == "true"
}

// resolutionCustomerContext reads the -D override straight off the flag set:
// the lookup runs before the --customer-context handling block populates
// customerContextFlagValue, and it must target the same tenant as the eventual
// request.
func resolutionCustomerContext(cmd *cobra.Command) string {
	if flag := cmd.Flags().Lookup("customer-context"); flag != nil && flag.Changed {
		if value := strings.TrimSpace(flag.Value.String()); value != "" {
			return value
		}
	}
	return activeCustomerContext()
}

func commandResolvedTarget(name string) *resolvedTarget {
	if target, ok := resolvedTargets[name]; ok {
		return &target
	}
	return nil
}

func announceResolution(resolved resolvedTarget) {
	if agentMode {
		return
	}
	fmt.Fprintf(os.Stderr, "resolved %q → %s %q (%s)\n", resolved.input, resolved.resource, resolved.name, resolved.id)
}

func singularResourceName(resource string) string {
	return strings.TrimSuffix(resource, "s")
}

func idShapedNotFoundHint() string {
	if idShapedPathArgument == "" {
		return ""
	}
	return fmt.Sprintf("If %q was a resource name, it matched the ID format; re-run with --name to force name lookup.", idShapedPathArgument)
}

const resolverMaxPages = 3

var resolverListFetch = fetchResourceNames
var resolverCachedEntries = cachedResolverEntries
var resolverHTTPClient = &http.Client{Timeout: 10 * time.Second}

type resolverListResult struct {
	entries   []nameCacheEntry
	truncated bool
}

func resolveResourceName(input string, target resolutionListTarget, context string) (resolvedTarget, error) {
	resource := singularResourceName(target.resource)
	if entries, ok := resolverCachedEntries(target.resource, context); ok {
		if match, unique := uniqueCachedNameMatch(input, entries); unique {
			return resolvedTarget{input: input, resource: resource, name: match.Name, id: match.ID}, nil
		}
	}
	result, err := resolverListFetch(target.listPath, context, resolverMaxPages)
	if err != nil {
		return resolvedTarget{}, err
	}
	matches := matchNameCandidates(input, result.entries)
	switch len(matches) {
	case 0:
		return resolvedTarget{}, nameNotFoundError(input, resource, target, result.truncated)
	case 1:
		return resolvedTarget{input: input, resource: resource, name: matches[0].Name, id: matches[0].ID}, nil
	}
	if nameSelectionInteractive() {
		chosen, err := nameSelectionPrompt(input, resource, capNameCandidates(matches, 10))
		if err != nil {
			return resolvedTarget{}, err
		}
		return resolvedTarget{input: input, resource: resource, name: chosen.Name, id: chosen.ID}, nil
	}
	return resolvedTarget{}, nameAmbiguousError(input, resource, matches)
}

// uniqueCachedNameMatch is the advisory fast path over the fresh name cache:
// only an unambiguous exact or case-insensitive-exact hit short-circuits the
// live fetch, since the cache may hold a truncated first page.
func uniqueCachedNameMatch(input string, entries []nameCacheEntry) (nameCacheEntry, bool) {
	trimmed := strings.TrimSpace(input)
	matches := filterNameEntries(entries, func(name string) bool { return name == trimmed })
	if len(matches) == 0 {
		matches = filterNameEntries(entries, func(name string) bool { return strings.EqualFold(name, trimmed) })
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return nameCacheEntry{}, false
}

// matchNameCandidates walks the ladder — exact, case-insensitive exact,
// case-insensitive substring, fuzzy — stopping at the first stage with a hit.
func matchNameCandidates(input string, entries []nameCacheEntry) []nameCacheEntry {
	trimmed := strings.TrimSpace(input)
	if matches := filterNameEntries(entries, func(name string) bool { return name == trimmed }); len(matches) > 0 {
		return matches
	}
	lowered := strings.ToLower(trimmed)
	if matches := filterNameEntries(entries, func(name string) bool { return strings.ToLower(name) == lowered }); len(matches) > 0 {
		return matches
	}
	if matches := filterNameEntries(entries, func(name string) bool { return strings.Contains(strings.ToLower(name), lowered) }); len(matches) > 0 {
		return matches
	}
	return fuzzyNameMatches(lowered, entries)
}

const fuzzyNameDistanceThreshold = 3

// fuzzyNameMatches returns the entries at the minimum edit distance within the
// threshold. A unique minimum resolves; a tie surfaces as ambiguous.
func fuzzyNameMatches(loweredInput string, entries []nameCacheEntry) []nameCacheEntry {
	best := fuzzyNameDistanceThreshold + 1
	matches := []nameCacheEntry{}
	for _, entry := range entries {
		distance := editDistance(loweredInput, strings.ToLower(entry.Name))
		if distance < best {
			best = distance
			matches = []nameCacheEntry{entry}
			continue
		}
		if distance == best {
			matches = append(matches, entry)
		}
	}
	if best > fuzzyNameDistanceThreshold {
		return nil
	}
	return matches
}

func filterNameEntries(entries []nameCacheEntry, keep func(string) bool) []nameCacheEntry {
	matches := []nameCacheEntry{}
	for _, entry := range entries {
		if keep(entry.Name) {
			matches = append(matches, entry)
		}
	}
	return matches
}

func capNameCandidates(matches []nameCacheEntry, limit int) []nameCacheEntry {
	if len(matches) > limit {
		return matches[:limit]
	}
	return matches
}

type nameResolutionError struct {
	detail   structuredError
	exitCode int
}

func (resolutionError nameResolutionError) Error() string {
	return resolutionError.detail.Message
}

func (resolutionError nameResolutionError) ExitCode() int {
	return resolutionError.exitCode
}

func (resolutionError nameResolutionError) StructuredError() structuredError {
	return resolutionError.detail
}

func nameAmbiguousError(input, resource string, matches []nameCacheEntry) error {
	shown := capNameCandidates(matches, 10)
	candidates := make([]string, 0, len(shown))
	for _, match := range shown {
		candidates = append(candidates, fmt.Sprintf("%s (%s)", match.Name, match.ID))
	}
	return nameResolutionError{
		detail: structuredError{
			Code:      "NAME_AMBIGUOUS",
			Message:   fmt.Sprintf("%q matches %d %ss: %s", input, len(matches), resource, strings.Join(candidates, ", ")),
			Hint:      fmt.Sprintf("Re-run with the exact %s id, e.g. %s", resource, shown[0].ID),
			Retryable: false,
		},
		exitCode: exitUsage,
	}
}

func nameNotFoundError(input, resource string, target resolutionListTarget, truncated bool) error {
	hint := fmt.Sprintf("Run dci %s to inspect available %s names", target.listOperation, resource)
	if truncated {
		hint += fmt.Sprintf("; the search was capped at the first %d pages and may have missed it", resolverMaxPages)
	}
	return nameResolutionError{
		detail: structuredError{
			Code:      "NAME_NOT_FOUND",
			Message:   fmt.Sprintf("no %s found matching %q", resource, input),
			Hint:      hint,
			Retryable: false,
		},
		exitCode: exitNotFound,
	}
}

type nameResolutionNetworkError struct {
	err error
}

func (networkError nameResolutionNetworkError) Error() string {
	return "name lookup failed: " + networkError.err.Error()
}

func (networkError nameResolutionNetworkError) Unwrap() error {
	return networkError.err
}

func (networkError nameResolutionNetworkError) ExitCode() int {
	return exitNetwork
}

func (networkError nameResolutionNetworkError) StructuredError() structuredError {
	return structuredError{
		Code:      "NETWORK_ERROR",
		Message:   networkError.Error(),
		Hint:      "Check network connectivity, or pass the resource id directly",
		Retryable: true,
	}
}

var nameSelectionInteractive = func() bool {
	return !agentMode && stdoutIsTTY() && term.IsTerminal(int(os.Stdin.Fd()))
}

var nameSelectionInput io.Reader = os.Stdin
var nameSelectionPrompt = promptNameSelection

func promptNameSelection(input, resource string, candidates []nameCacheEntry) (nameCacheEntry, error) {
	fmt.Fprintf(os.Stderr, "%q matches multiple %ss:\n", input, resource)
	for index, candidate := range candidates {
		fmt.Fprintf(os.Stderr, "%d) %s  (%s)\n", index+1, candidate.Name, candidate.ID)
	}
	fmt.Fprintln(os.Stderr, "0) cancel")
	fmt.Fprintf(os.Stderr, "Select a %s: ", resource)
	var selection int
	if _, err := fmt.Fscanln(nameSelectionInput, &selection); err != nil || selection <= 0 || selection > len(candidates) {
		return nameCacheEntry{}, nameSelectionCancelledError(resource)
	}
	return candidates[selection-1], nil
}

func nameSelectionCancelledError(resource string) error {
	return nameResolutionError{
		detail: structuredError{
			Code:      "USAGE_ERROR",
			Message:   fmt.Sprintf("%s selection cancelled", resource),
			Hint:      "Re-run with the exact resource id",
			Retryable: false,
		},
		exitCode: exitUsage,
	}
}

// fetchResourceNames pages through the collection list endpoint, mirroring the
// open command's programmatic-call pattern: bearer token, 10s client, and the
// customer context on both transports during the API migration.
func fetchResourceNames(listPath, context string, maxPages int) (resolverListResult, error) {
	token := authenticationToken()
	if token == "" {
		return resolverListResult{}, authenticationRequiredPreflightError()
	}
	base, err := apiBase()
	if err != nil {
		return resolverListResult{}, err
	}
	maxResults := "500"
	if strings.HasSuffix(listPath, "/budgets") {
		maxResults = "250"
	}
	entries := []nameCacheEntry{}
	pageToken := ""
	for page := 0; page < maxPages; page++ {
		requestURL, err := url.Parse(base + listPath)
		if err != nil {
			return resolverListResult{}, err
		}
		query := requestURL.Query()
		query.Set("maxResults", maxResults)
		if context != "" {
			query.Set("customerContext", context)
		}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		requestURL.RawQuery = query.Encode()
		request, err := http.NewRequest(http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return resolverListResult{}, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("User-Agent", buildUserAgent(agentUAMode))
		if context != "" {
			request.Header.Set("X-Tenant-Id", context)
		}
		response, err := resolverHTTPClient.Do(request)
		if err != nil {
			return resolverListResult{}, nameResolutionNetworkError{err: err}
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized {
			return resolverListResult{}, authenticationRequiredPreflightError()
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return resolverListResult{}, resolverLookupStatusError(listPath, response)
		}
		if readErr != nil {
			return resolverListResult{}, nameResolutionNetworkError{err: readErr}
		}
		pageEntries, nextToken := parseResourceNamePage(body)
		entries = append(entries, pageEntries...)
		pageToken = nextToken
		if pageToken == "" {
			return resolverListResult{entries: entries}, nil
		}
	}
	return resolverListResult{entries: entries, truncated: true}, nil
}

func resolverLookupStatusError(listPath string, response *http.Response) error {
	return consoleAPIError{
		status:  response.StatusCode,
		message: fmt.Sprintf("name lookup on %s failed: API returned %s", listPath, response.Status),
		headers: diagnosticResponseHeaders(response),
	}
}

// resourceNameFieldPriority discovers each item's display name at runtime: the
// CBOR spec cache carries no response schemas, so the field cannot come from
// the spec. Covers reports (reportName), budgets (budgetName),
// allocations/alerts/labels (name), and annotations (content).
var resourceNameFieldPriority = []string{"name", "reportName", "budgetName", "displayName", "title", "content"}

func parseResourceNamePage(body []byte) ([]nameCacheEntry, string) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, ""
	}
	items := firstArrayValue(parsed)
	entries := make([]nameCacheEntry, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := object["id"].(string)
		if id == "" {
			continue
		}
		name := discoverItemName(object)
		if name == "" {
			continue
		}
		entries = append(entries, nameCacheEntry{ID: id, Name: name})
	}
	nextToken, _ := parsed["pageToken"].(string)
	return entries, nextToken
}

func discoverItemName(object map[string]any) string {
	for _, field := range resourceNameFieldPriority {
		if value, ok := object[field].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// firstArrayValue returns the first array value in the object by sorted key,
// so discovery is deterministic across map iteration orders.
func firstArrayValue(parsed map[string]any) []any {
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if array, ok := parsed[key].([]any); ok {
			return array
		}
	}
	return nil
}

// openResourceListPaths backs `dci open <resource> <name>`: open is a local
// command with no operation metadata, so its list paths are static.
var openResourceListPaths = map[string]string{
	"report":     "/analytics/v1/reports",
	"budget":     "/analytics/v1/budgets",
	"allocation": "/analytics/v1/allocations",
}

func resolveOpenResourceID(resource, argument, configDir string) (string, error) {
	listPath, ok := openResourceListPaths[strings.ToLower(resource)]
	if !ok {
		return argument, nil
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return argument, nil
	}
	input := strings.TrimSpace(argument)
	if input == "" || resourceIDPattern.MatchString(input) {
		return argument, nil
	}
	context := activeCustomerContext()
	if context == "" {
		context = readCustomerContext(configDir)
	}
	collection := listPath[strings.LastIndex(listPath, "/")+1:]
	target := resolutionListTarget{
		listPath:      listPath,
		resource:      collection,
		listOperation: "list-" + collection,
	}
	resolved, err := resolveResourceName(input, target, context)
	if err != nil {
		return "", err
	}
	announceResolution(resolved)
	return resolved.id, nil
}
