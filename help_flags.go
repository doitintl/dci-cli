// Chapter: per-command flag visibility in help. Every presentation flag is
// a persistent flag of the hidden dci API command, so each command's --help
// used to append all ~30 of them — most inert for that command — and bury
// the command's own flags under --chart, --pivot, --for-reimport and
// friends. This chapter keeps every flag accepted everywhere (no script
// breaks) and hides from a command's help the ones that cannot act on it,
// deciding by the same signals the pipeline uses when it acts: the
// report/query result shape (charts, pivot, rollup, row caps), the
// file-shaped exports (export_pagination.go's fileExportOperations), the
// page-token paging of list commands (pagination.go), the list-insights
// view (response_transform.go), and a resolvable positional argument
// (name_resolution.go). The table-presentation flags that apply to every
// command fold into one pointer line; --help-full lists everything. `dci
// commands --json` is unaffected: flags are hidden only while help renders
// and restored right after.
package main

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// helpFlagScope names what a dci persistent flag acts on. Every flag
// addOutputFlag registers must appear in helpFlagScopes (help_flags_test.go
// enforces it) so a new flag is classified deliberately rather than
// defaulting to inline everywhere.
type helpFlagScope int

const (
	// helpFlagAlways is the agent contract plus the flags that act on every
	// API call: inline in every command's help.
	helpFlagAlways helpFlagScope = iota
	// helpFlagPresentation shapes table/TOON rendering of any response:
	// applies everywhere, so it folds into the one-line pointer instead of
	// repeating under every command.
	helpFlagPresentation
	// helpFlagReport acts on report/query result rows (schema + rows).
	helpFlagReport
	// helpFlagExport acts on a file-shaped export body (CSV/NDJSON).
	helpFlagExport
	// helpFlagAllPages is --all: follows page tokens on paged lists and
	// walks the time windows of a file export.
	helpFlagAllPages
	// helpFlagListSearch is --search: filters the items of a list response.
	helpFlagListSearch
	// helpFlagInsights is the list-insights view's own switch.
	helpFlagInsights
	// helpFlagResolution governs name resolution of a positional argument.
	helpFlagResolution
)

var helpFlagScopes = map[string]helpFlagScope{
	"output":           helpFlagAlways,
	"fields":           helpFlagAlways,
	"exclude":          helpFlagAlways,
	"full":             helpFlagAlways,
	"dry-run":          helpFlagAlways,
	"yes":              helpFlagAlways,
	"customer-context": helpFlagAlways,

	"table-mode":          helpFlagPresentation,
	"table-columns":       helpFlagPresentation,
	"table-width":         helpFlagPresentation,
	"table-max-col-width": helpFlagPresentation,
	"no-truncate":         helpFlagPresentation,
	"raw-numbers":         helpFlagPresentation,
	"utc":                 helpFlagPresentation,
	"output-order":        helpFlagPresentation,
	// --output-file writes any command's response to a file, but it is the
	// primary way to keep a file-shaped export: inline there, folded elsewhere.
	"output-file": helpFlagPresentation,

	"chart":               helpFlagReport,
	"pivot":               helpFlagReport,
	"flat":                helpFlagReport,
	"rollup":              helpFlagReport,
	"max-rows":            helpFlagReport,
	"rows":                helpFlagReport,
	"include-empty-rows":  helpFlagReport,
	"drop-unlabeled-rows": helpFlagReport,
	"heatmap":             helpFlagReport,

	"for-reimport": helpFlagExport,

	"all":    helpFlagAllPages,
	"search": helpFlagListSearch,

	"include-dismissed": helpFlagInsights,

	"id":   helpFlagResolution,
	"name": helpFlagResolution,
}

// reportResultOperations are the operations whose response carries report
// rows (`result.rows`/`results.rows` beside a `schema`) — the shape
// nestedReportRows detects at render time and every report flag acts on.
// Help needs the answer before any response exists, so the set is spelled
// out here, keyed by command name — the beta subtree names its commands by
// x-cli-name, so the async results operation appears twice (GA spelling and
// beta spelling). help_flags_test.go checks the set against the response
// schemas of the live GA spec (DCI_COMMAND_DOCS_SPEC, fetched in CI) and
// the embedded beta spec.
var reportResultOperations = map[string]bool{
	"query":                       true,
	"get-report":                  true,
	"get-async-operation-results": true,
	"get-report-results":          true, // beta: getAsyncOperationResults
}

// helpFlagPlacement is where a dci persistent flag lands in one command's
// help: inline in the Flags block, folded into the output-flags pointer
// line, or hidden because it cannot act on that command.
type helpFlagPlacement int

const (
	helpFlagInline helpFlagPlacement = iota
	helpFlagCollapsed
	helpFlagHidden
)

func helpFlagPlacementFor(flagName string, cmd *cobra.Command) helpFlagPlacement {
	shown := func(applies bool) helpFlagPlacement {
		if applies {
			return helpFlagInline
		}
		return helpFlagHidden
	}
	scope, known := helpFlagScopes[flagName]
	if !known {
		return helpFlagInline
	}
	switch scope {
	case helpFlagPresentation:
		if flagName == "output-file" && helpCommandIsFileExport(cmd) {
			return helpFlagInline
		}
		return helpFlagCollapsed
	case helpFlagReport:
		return shown(helpCommandIsReportShaped(cmd))
	case helpFlagExport:
		return shown(helpCommandIsFileExport(cmd))
	case helpFlagAllPages:
		return shown(helpCommandPages(cmd) || helpCommandIsFileExport(cmd))
	case helpFlagListSearch:
		// A file body has no items to filter; the preflight rejects --search
		// on exports even though they page.
		return shown(helpCommandIsList(cmd) && !helpCommandIsFileExport(cmd))
	case helpFlagInsights:
		return shown(cmd.Name() == "list-insights")
	case helpFlagResolution:
		return shown(helpCommandResolvesNames(cmd))
	}
	return helpFlagInline
}

// helpCommandUnderAPI reports whether cmd is a command under the hidden dci
// API command — a GA operation, a question command, or the beta subtree —
// i.e. one that inherits the dci persistent flags. The dci command itself
// and the local root commands (status, login, docs …) render unchanged.
func helpCommandUnderAPI(cmd, dciCmd *cobra.Command) bool {
	if cmd == nil || dciCmd == nil || cmd == dciCmd {
		return false
	}
	for parent := cmd.Parent(); parent != nil; parent = parent.Parent() {
		if parent == dciCmd {
			return true
		}
	}
	return false
}

func helpCommandIsReportShaped(cmd *cobra.Command) bool {
	return reportResultOperations[cmd.Name()]
}

func helpCommandIsFileExport(cmd *cobra.Command) bool {
	_, ok := fileExportOperations[cmd.Name()]
	return ok
}

// helpCommandPages mirrors operationHasQueryParam(op, "pageToken"): restish
// registers every query parameter as a local flag named after it, and the
// question commands register --page-token by hand.
func helpCommandPages(cmd *cobra.Command) bool {
	return cmd.LocalFlags().Lookup("page-token") != nil
}

// helpCommandIsList reports whether the command answers with a collection
// --search can filter: anything that pages, the list-* and search-*
// operations (some return their whole collection in one response), the
// curated list views, and the question commands wrapping a list.
func helpCommandIsList(cmd *cobra.Command) bool {
	name := cmd.Name()
	if helpCommandPages(cmd) || questionCommandNames[name] {
		return true
	}
	if _, ok := listViews[name]; ok {
		return true
	}
	return strings.HasPrefix(name, "list-") || strings.HasPrefix(name, "search-")
}

// helpCommandResolvesNames reports whether --id/--name have a positional
// argument to govern. The resolution index is authoritative when loaded;
// help usually renders before any operation metadata is read, so the
// fallback is the command's own usage line: exactly one path parameter
// after the name ("get-budget id"), the only arity buildResolutionIndex
// ever resolves — a two-parameter command (create-ticket-comment
// customerid ticketid) is never resolvable.
func helpCommandResolvesNames(cmd *cobra.Command) bool {
	if len(resolutionIndex) > 0 {
		_, ok := resolutionIndex[cmd.Name()]
		return ok
	}
	return len(strings.Fields(cmd.Use)) == 2
}

// helpFlagsFolded is true between applyHelpFlagVisibility and its restore:
// the pointer line renders only when the flags it stands in for were
// actually hidden. The usage template is shared with cobra's own usage dump
// on a flag-parse error, where nothing was folded and the full inherited
// list follows — a pointer line there would contradict it.
var helpFlagsFolded bool

// applyHelpFlagVisibility hides, for the duration of one help render, the
// dci persistent flags that cannot act on cmd (and the presentation flags
// the pointer line stands in for). It returns the restore function; the
// flags are shared with every other command and with the catalog, so the
// caller defers it. --help-full lists every flag and so does nothing here.
func applyHelpFlagVisibility(cmd *cobra.Command) func() {
	dciCmd := findDCICommand()
	if helpFullRequested || !helpCommandUnderAPI(cmd, dciCmd) {
		return func() {}
	}
	var hidden []*pflag.Flag
	dciCmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || helpFlagPlacementFor(flag.Name, cmd) == helpFlagInline {
			return
		}
		flag.Hidden = true
		hidden = append(hidden, flag)
	})
	helpFlagsFolded = true
	return func() {
		helpFlagsFolded = false
		for _, flag := range hidden {
			flag.Hidden = false
		}
	}
}

// helpCollapsedFlagsNote renders the pointer line for the presentation
// flags applyHelpFlagVisibility folded away: the usage template appends it
// after the Flags block. Empty whenever nothing was folded — commands
// outside the API subtree, --help-full, and the usage dump cobra prints on
// a flag-parse error.
func helpCollapsedFlagsNote(cmd *cobra.Command) string {
	dciCmd := findDCICommand()
	if !helpFlagsFolded || helpFullRequested || !helpCommandUnderAPI(cmd, dciCmd) {
		return ""
	}
	labels := make([]string, 0)
	dciCmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if helpFlagPlacementFor(flag.Name, cmd) != helpFlagCollapsed {
			return
		}
		label := "--" + flag.Name
		if flag.Shorthand != "" {
			label = "-" + flag.Shorthand + "/" + label
		}
		labels = append(labels, label)
	})
	if len(labels) == 0 {
		return ""
	}
	sort.Strings(labels)
	return "Output flags (apply to every command; add --help-full to list them):\n" +
		wrapHelpItems(labels, "  ", 100)
}

// wrapHelpItems joins items with ", " into lines no wider than width, each
// prefixed with indent.
func wrapHelpItems(items []string, indent string, width int) string {
	var lines []string
	current := indent
	for _, item := range items {
		if current != indent && len(current)+len(", ")+len(item) > width {
			lines = append(lines, current)
			current = indent
		}
		if current != indent {
			current += ", "
		}
		current += item
	}
	if current != indent {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n")
}
