package main

// Collections & pagination: every list command returns one server page per
// request. This chapter makes that visible (a stderr note when a rendering
// format drops the continuation token) and safe (client-side validation of
// --max-results against server caps the API does not enforce sanely — most
// endpoints silently reset out-of-range values to the default page size).

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rest-sh/restish/cli"
)

// pagingCap is the server-side ceiling for a list command's --max-results
// parameter, with the evidence for it. "spec" caps are declared as `maximum`
// in the OpenAPI document (extracted 2026-08-19); "verified" caps were probed
// against the live API where the spec declares no bound.
type pagingCap struct {
	limit  int
	source string
}

var pagingCaps = map[string]pagingCap{
	// Declared in the OpenAPI spec.
	"list-insights":                    {500, "spec"},
	"list-insight-resource-results":    {5000, "spec"},
	"replace-insight-resource-results": {5000, "spec"},
	"list-aws-member-accounts":         {500, "spec"},
	"list-aws-organizations":           {500, "spec"},
	"list-aws-organizations-settings":  {500, "spec"},
	"list-aws-planned-purchases":       {500, "spec"},
	"list-aws-reserved-instances":      {500, "spec"},
	"list-aws-savings-plans":           {500, "spec"},
	"list-cloudflow-connections":       {100, "spec"},
	"list-cloudflow-templates":         {500, "spec"},
	"list-cloudflows":                  {500, "spec"},
	"list-service-quotas":              {200, "spec"},
	"list-tickets":                     {100, "spec"},
	// Not declared in the spec; probed live (2026-08-19): values above the
	// cap are silently reset to the default page size of 50.
	"list-dimensions": {500, "verified"},
	// The endpoints reject values above 250 (see fetchResourceNames).
	"list-budgets": {250, "verified"},
	"list-assets":  {250, "verified"},
}

// validateMaxResults rejects a --max-results value above the endpoint's known
// cap before the request is sent. Forwarding it would not fail loudly: the
// server resets out-of-range values to the default page size (50), so asking
// for 1000 returns fewer rows than asking for 500 — the worst kind of clamp.
func validateMaxResults(commandName string, args []string) error {
	entry, known := pagingCaps[commandName]
	if !known {
		return nil
	}
	raw, passed := flagValueFromArgs(args, "--max-results")
	if !passed {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= entry.limit {
		// Non-numeric values are pflag's parse error to report.
		return nil
	}
	evidence := "declared in the API spec"
	if entry.source == "verified" {
		evidence = "verified against the live API"
	}
	return invocationPreflightError{
		detail: structuredError{
			Code: "USAGE_ERROR",
			Message: fmt.Sprintf("--max-results %d exceeds the maximum of %d for %s (%s); the API silently resets out-of-range values to the default page size instead of clamping them",
				value, entry.limit, commandName, evidence),
			Hint:      fmt.Sprintf("Pass --max-results %d or less and iterate with --page-token to fetch the remaining pages", entry.limit),
			Retryable: false,
		},
		exitCode: exitUsage,
	}
}

// flagValueFromArgs extracts the value of a --flag from raw arguments,
// supporting both "--flag value" and "--flag=value" forms.
func flagValueFromArgs(args []string, name string) (string, bool) {
	for index, argument := range args {
		if argument == name && index+1 < len(args) {
			return args[index+1], true
		}
		if strings.HasPrefix(argument, name+"=") {
			return strings.TrimPrefix(argument, name+"="), true
		}
	}
	return "", false
}

// collectionPageToken returns a list wrapper's continuation token, if any.
func collectionPageToken(body interface{}) string {
	root, ok := body.(map[string]interface{})
	if !ok {
		return ""
	}
	for _, key := range []string{"pageToken", "nextPageToken", "cursor", "nextCursor"} {
		if token, ok := root[key].(string); ok && strings.TrimSpace(token) != "" {
			return token
		}
	}
	return ""
}

// notePageTokenDropped warns on stderr when a paged collection is rendered by
// a format that discards the wrapper metadata (table, csv). TOON and JSON
// carry pageToken in-band and need no note; without one here, a truncated
// collection in table/csv output is indistinguishable from a complete one.
func notePageTokenDropped(body interface{}) {
	token := collectionPageToken(body)
	if token == "" || cli.Stderr == nil {
		return
	}
	root, ok := body.(map[string]interface{})
	if !ok {
		return
	}
	if _, _, isList := listWrapperRows(root); !isList {
		return
	}
	shown := "this page rendered"
	if count, ok := root["rowCount"]; ok {
		shown = fmt.Sprintf("first %v rendered", count)
	}
	_, _ = fmt.Fprintf(cli.Stderr, "note: more results available (%s); re-run with --page-token %s, or raise --max-results\n", shown, token)
}
