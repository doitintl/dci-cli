package main

// Name → ID resolution: positional resource arguments that look like names are
// resolved against the parent collection's list endpoint before the request is
// built. Kept in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"encoding/json"
	"errors"
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
// hasBody marks operations whose surplus positional arguments are body
// shorthand rather than words of an unquoted multi-word name.
type resolutionListTarget struct {
	listPath      string
	resource      string
	listOperation string
	hasBody       bool
}

// resolutionExcludedResources removes collections from the derived index. The
// legacy attributions/attributiongroups endpoints are superseded by allocations.
var resolutionExcludedResources = map[string]bool{
	"attributions":      true,
	"attributiongroups": true,
}

// versionSegmentPattern rejects API version segments masquerading as
// collection nouns when they immediately precede the path parameter (e.g.
// /anomalies/v1/{id}): the derived "v1" collection is meaningless, and the
// real collection's entries carry no resolvable names, so such operations
// must pass their IDs through untouched.
var versionSegmentPattern = regexp.MustCompile(`^v\d+$`)

var resolutionIndex = map[string]resolutionListTarget{}

type resolvedTarget struct {
	input    string
	resource string
	name     string
	id       string
	// owner and description are best-effort display context from the list
	// payload, shown by the destructive confirmation; empty when unavailable.
	owner       string
	description string
}

type resolvedTargetPayload struct {
	Input string `json:"input"`
	Name  string `json:"name"`
	ID    string `json:"id"`
}

// resolvedTargets records successful resolutions per command so the
// destructive confirmation can display the true target.
var resolvedTargets = map[string]resolvedTarget{}

// idShapedPathArgument remembers a positional argument that reached the API
// verbatim — it matched the ID shape, or resolution degraded to passing it
// through — so a later 404 can hint at --name.
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
		if resolutionExcludedResources[resource] || versionSegmentPattern.MatchString(resource) {
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
			hasBody:       operation.BodyMediaType != "",
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
	if !ok {
		return nil
	}
	if len(args) == 0 {
		// Zero-argument interactive invocation: open the picker (TUI-SPEC
		// F1). Runs before the destructive gate below in the same pre-run,
		// so the confirmation can display the picked target.
		return pickPathArgument(cmd, target)
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return nil
	}
	if boolFlagSet(cmd, "id") {
		return nil
	}
	input := strings.TrimSpace(args[0])
	if joinableNameArguments(cmd, args) {
		// The shell word-split an unquoted multi-word name: rejoin the
		// positionals into the single name the user typed.
		input = strings.TrimSpace(strings.Join(args, " "))
	}
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
		if !boolFlagSet(cmd, "name") && resolutionFallsBackToVerbatim(err, input) {
			idShapedPathArgument = input
			announceResolutionFallback(input, singularResourceName(target.resource), err)
			return nil
		}
		return err
	}
	args[0] = resolved.id
	resolvedTargets[cmd.Name()] = resolved
	announceResolution(resolved)
	return nil
}

// resolutionFallsBackToVerbatim reports whether a failed resolution should
// degrade to sending the positional argument verbatim instead of failing the
// command. Only a whitespace-free argument can fall back: it may simply be a
// resource id whose shape the strict Firestore ID gate does not recognize
// (asset ids like "g-suite-2319621428"), while an argument with spaces can
// only be a name, so its resolution errors stay fatal and descriptive. The
// fallback covers a lookup request that itself failed — some list endpoints
// reject the lookup's paging parameters, and the real request will surface
// the underlying problem if it persists — and a lookup that answered but
// matched nothing. An ambiguous match or a cancelled selection means the
// argument matched real names and must not be sent verbatim, and an
// authentication failure keeps its actionable error.
func resolutionFallsBackToVerbatim(err error, input string) bool {
	if len(strings.Fields(input)) > 1 {
		return false
	}
	var resolutionError nameResolutionError
	if errors.As(err, &resolutionError) {
		return resolutionError.detail.Code == "NAME_NOT_FOUND"
	}
	var networkError nameResolutionNetworkError
	var statusError consoleAPIError
	return errors.As(err, &networkError) || errors.As(err, &statusError)
}

func announceResolutionFallback(input, resource string, lookupErr error) {
	if agentMode {
		return
	}
	fmt.Fprintf(os.Stderr, "note: %s; using %q as the %s id\n", lookupErr.Error(), input, resource)
}

// boolFlagSet reports whether a boolean flag is set to true. An operation-local
// flag of the same name but a different type (e.g. a `name` query parameter)
// shadows the persistent flag and must not be misread as the escape hatch.
func boolFlagSet(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Value.Type() == "bool" && flag.Value.String() == "true"
}

// joinableNameArguments reports whether surplus positional arguments can be
// treated as the space-split words of one unquoted name: the operation
// resolves names into its only path parameter, takes no request body (surplus
// words there are body shorthand), resolution is not switched off, and no
// word looks like a flag.
func joinableNameArguments(cmd *cobra.Command, args []string) bool {
	target, ok := resolutionIndex[cmd.Name()]
	if !ok || target.hasBody || len(args) < 2 {
		return false
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return false
	}
	if boolFlagSet(cmd, "id") {
		return false
	}
	for _, argument := range args {
		if strings.HasPrefix(argument, "-") {
			return false
		}
	}
	if params := operationPathParameters[cmd.Name()]; len(params) > 0 {
		if declared := params[0].Type; declared != "" && declared != "string" {
			return false
		}
	}
	return true
}

const joinableArgsAnnotation = "dci-joinable-args"

// relaxResolvableArgsValidation loosens cobra's generated ExactArgs(1) on
// resolvable no-body commands: an unquoted multi-word name arrives as several
// positionals, and cobra validates argument counts before the pre-run hook can
// rejoin them. The original validator still applies whenever the join does not
// (--id, DCI_NO_RESOLVE, flag-shaped words), so those paths keep their exact
// arity errors. Restish's Run only reads args[0] for the path parameter, so
// the surplus words left in the slice after the join are inert.
//
// Besides running when operation metadata loads, this is registered as a
// cobra initializer: restish hydrates the operation subcommands inside
// cli.Run, after the invocation preflight, and initializers are the one hook
// cobra fires after hydration but before ValidateArgs.
func relaxResolvableArgsValidation() {
	if cli.Root == nil {
		return
	}
	dciCommand := findDCICommand()
	if dciCommand == nil {
		return
	}
	for _, command := range dciCommand.Commands() {
		target, ok := resolutionIndex[command.Name()]
		if !ok || target.hasBody || command.Annotations[joinableArgsAnnotation] == "true" {
			continue
		}
		if command.Annotations == nil {
			command.Annotations = map[string]string{}
		}
		command.Annotations[joinableArgsAnnotation] = "true"
		original := command.Args
		command.Args = func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && zeroArgPickerApplies(cmd) {
				// The picker in the pre-run hook will supply the argument.
				return nil
			}
			if joinableNameArguments(cmd, args) {
				return nil
			}
			if original == nil {
				return nil
			}
			return original(cmd, args)
		}
		installPickerArgInjection(command)
	}
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
	return fmt.Sprintf("If %q was a resource name rather than an id, re-run with --name to force name lookup.", idShapedPathArgument)
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
			return resolvedFromEntry(input, resource, match), nil
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
		return resolvedFromEntry(input, resource, matches[0]), nil
	}
	if nameSelectionInteractive() {
		chosen, err := nameSelectionPrompt(input, resource, capNameCandidates(matches, 10))
		if err != nil {
			return resolvedTarget{}, err
		}
		return resolvedFromEntry(input, resource, chosen), nil
	}
	return resolvedTarget{}, nameAmbiguousError(input, resource, matches)
}

func resolvedFromEntry(input, resource string, entry nameCacheEntry) resolvedTarget {
	return resolvedTarget{
		input:       input,
		resource:    resource,
		name:        entry.Name,
		id:          entry.ID,
		owner:       entry.Owner,
		description: entry.Description,
	}
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
	if entry, err, handled := tuiNameSelection(input, resource, candidates); handled {
		return entry, err
	}
	return promptNameSelectionBasic(input, resource, candidates)
}

// promptNameSelectionBasic is the plain numbered prompt, kept as the fallback
// for terminals the interactive select cannot render on.
func promptNameSelectionBasic(input, resource string, candidates []nameCacheEntry) (nameCacheEntry, error) {
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
	// The budgets and assets endpoints reject maxResults above 250.
	maxResults := "500"
	if strings.HasSuffix(listPath, "/budgets") || strings.HasSuffix(listPath, "/assets") {
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
			return resolverListResult{}, apiRejectedTokenError(base)
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
		owner, _ := object["owner"].(string)
		description, _ := object["description"].(string)
		entries = append(entries, nameCacheEntry{ID: id, Name: name, Owner: owner, Description: strings.TrimSpace(description)})
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

// openJoinableArgs reports whether surplus `open` positionals are the
// space-split words of one unquoted resource name, mirroring
// joinableNameArguments for operation commands: the resource must resolve
// names, resolution must not be switched off, and no word may look like a
// flag. Every other surplus keeps open's 0-2 arity error.
func openJoinableArgs(args []string) bool {
	if len(args) < 3 {
		return false
	}
	if _, ok := openResourceListPaths[strings.ToLower(args[0])]; !ok {
		return false
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return false
	}
	for _, argument := range args {
		if strings.HasPrefix(argument, "-") {
			return false
		}
	}
	return true
}

// openResourceArgument returns open's name/ID argument, rejoining the words
// of a shell word-split unquoted multi-word name.
func openResourceArgument(args []string) string {
	if openJoinableArgs(args) {
		return strings.TrimSpace(strings.Join(args[1:], " "))
	}
	return args[1]
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
