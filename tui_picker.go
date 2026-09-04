package main

// F1 (TUI-SPEC): the zero-argument fuzzy resource picker. A resolvable
// command invoked with no positional argument on an interactive terminal
// opens a filter-as-you-type list over the resource's name cache instead of
// erroring. The selection flows into the same downstream path as a typed
// argument. Kept in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
)

// pickedPathArgument carries the picker's selection from the dci
// PersistentPreRunE to the RunE injection wrapper: cobra passes the same args
// slice to every hook, so a zero-length slice cannot be grown in place.
var pickedPathArgument string

// pickedArgumentPrepends records that the selection fills a path slot in
// front of existing positionals (body shorthand) rather than an empty slice.
var pickedArgumentPrepends bool

// zeroArgPickerApplies reports whether a zero-argument invocation of cmd may
// open the picker. Shared by the relaxed Args validator (which must accept
// zero args before the pre-run hook can act) and the picker itself.
func zeroArgPickerApplies(cmd *cobra.Command) bool {
	return pathArgumentPickerApplies(cmd, nil)
}

// pathArgumentPickerApplies reports whether this invocation is missing its
// path argument in a way the picker can fill. Without a request body that is
// simply the zero-argument form. With one, surplus positionals are body
// shorthand rather than the words of a name, so zero positionals cannot be
// told from a missing body and keep their usage error — but positionals that
// are *all* body shorthand say precisely that the body is there and the name
// is the one thing missing (`dci update-datahub-dataset description: x`).
func pathArgumentPickerApplies(cmd *cobra.Command, args []string) bool {
	if !tuiActive() {
		return false
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return false
	}
	target, ok := resolutionIndex[cmd.Name()]
	if !ok {
		return false
	}
	if !target.hasBody {
		return len(args) == 0
	}
	return bodyOnlyPositionals(cmd, args)
}

// pickPathArgument runs the picker for a zero-argument resolvable command,
// recording the selection for the RunE injection wrapper and the destructive
// confirmation display. A var-called selector so tests can fake the terminal.
var pickerSelectEntry = tuiSelectEntry

func pickPathArgument(cmd *cobra.Command, target resolutionListTarget, args []string) error {
	pickedPathArgument = ""
	pickedArgumentPrepends = false
	if !pathArgumentPickerApplies(cmd, args) {
		// Non-TUI zero-arg invocations were already rejected by the Args
		// validator; reaching here without the gate means nothing to do.
		return nil
	}
	resource := singularResourceName(target.resource)
	entries, err := pickerEntries(target, resolutionCustomerContext(cmd))
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nameResolutionError{
			detail: structuredError{
				Code:      "NAME_NOT_FOUND",
				Message:   fmt.Sprintf("no %ss available to pick from", resource),
				Hint:      fmt.Sprintf("Run dci %s to inspect the collection", target.listOperation),
				Retryable: false,
			},
			exitCode: exitNotFound,
		}
	}
	entry, err := pickerSelectEntry(fmt.Sprintf("Select a %s — type to filter (%d)", resource, len(entries)), entries)
	if err != nil {
		if !errors.Is(err, huh.ErrUserAborted) {
			// Renderer failure on an exotic terminal: without a selection the
			// command cannot proceed, so it exits the same way a cancel does.
			fmt.Fprintf(os.Stderr, "picker unavailable: %v\n", err)
		}
		return nameSelectionCancelledError(resource)
	}
	pickedPathArgument = entry.ID
	// Body shorthand already occupies the positional slots: the picked name
	// goes in front of it, not over it.
	pickedArgumentPrepends = len(args) > 0
	resolved := resolvedFromEntry(entry.Name, resource, entry)
	resolvedTargets[cmd.Name()] = resolved
	announceResolution(resolved)
	return nil
}

// pickerEntries serves the picker from the on-disk name cache, including
// stale-but-servable entries (readNameCache's 24h TTL), arming a background
// refresh when the cache is not fresh — the same policy Tab completion
// implements. An absent cache falls back to a synchronous fetch behind a
// spinner.
func pickerEntries(target resolutionListTarget, context string) ([]nameCacheEntry, error) {
	cache, state := readNameCache(dciConfigDir(), context, time.Now())
	if entries := cache.Resources[target.resource]; len(entries) > 0 {
		if state != nameCacheFresh {
			spawnNameCacheRefresh()
		}
		return entries, nil
	}
	stopSpinner := startTUISpinner(fmt.Sprintf("Fetching %s names…", singularResourceName(target.resource)))
	result, err := resolverListFetch(target.listPath, context, resolverMaxPages)
	stopSpinner()
	if err != nil {
		return nil, err
	}
	return result.entries, nil
}

// installPickerArgInjection wraps a resolvable command's run function so a
// picker selection made in the pre-run hook reaches restish's URI
// substitution: the pre-run cannot grow the empty args slice cobra hands to
// the run function. Restish operation commands use Run; RunE is wrapped too
// for symmetry with custom commands.
func installPickerArgInjection(command *cobra.Command) {
	injected := func(args []string) []string {
		return withPickedPathArgument(args)
	}
	if originalRun := command.Run; originalRun != nil {
		command.Run = func(cmd *cobra.Command, args []string) {
			originalRun(cmd, injected(args))
		}
	}
	if originalRunE := command.RunE; originalRunE != nil {
		command.RunE = func(cmd *cobra.Command, args []string) error {
			return originalRunE(cmd, injected(args))
		}
	}
}

// withPickedPathArgument places a picker selection into the argument slice:
// into an empty one for a zero-argument invocation, in front of the body
// shorthand otherwise. Cobra hands the same backing slice to every hook, so
// an empty one cannot be grown in place — every caller takes the result.
func withPickedPathArgument(args []string) []string {
	if pickedPathArgument == "" {
		return args
	}
	if len(args) == 0 {
		return []string{pickedPathArgument}
	}
	if !pickedArgumentPrepends {
		return args
	}
	return append([]string{pickedPathArgument}, args...)
}

// pickOpenResourceID is open's one-argument picker trigger (TUI-SPEC F1):
// `dci open <resource>` with no id — today a usage error — opens the picker
// for that resource type. handled=false keeps the usage error.
func pickOpenResourceID(resource string) (string, error, bool) {
	listPath, ok := openResourceListPaths[resource]
	if !ok || !tuiActive() {
		return "", nil, false
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return "", nil, false
	}
	collection := listPath[strings.LastIndex(listPath, "/")+1:]
	target := resolutionListTarget{
		listPath:      listPath,
		resource:      collection,
		listOperation: "list-" + collection,
	}
	context := activeCustomerContext()
	if context == "" {
		context = readCustomerContext(dciConfigDir())
	}
	entries, err := pickerEntries(target, context)
	if err != nil {
		return "", err, true
	}
	if len(entries) == 0 {
		return "", nil, false
	}
	entry, err := pickerSelectEntry(fmt.Sprintf("Select a %s — type to filter (%d)", resource, len(entries)), entries)
	if err != nil {
		return "", nameSelectionCancelledError(resource), true
	}
	return entry.ID, nil, true
}
