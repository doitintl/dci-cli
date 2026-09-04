package main

// The `dci ai` session's name selection: the session-side twin of the CLI's
// F1 zero-argument picker and F2 ambiguity selection (TUI-SPEC). A slash
// dispatch runs the child with piped stdio, so the child's own interactive
// prompts can never fire; instead the session detects the two cases before
// spawning — a resolvable command with no positional argument, or one whose
// name argument matches several cached entries — and offers the selection in
// its own UI, then dispatches with the chosen ID (ID-shaped arguments skip
// the child's re-resolution). Gates mirror resolvePathArguments: --id,
// DCI_NO_RESOLVE, ID-shaped input, multi-path-param operations. Cache-only
// by design: an absent cache degrades to today's behavior — the child's own
// error. Kept in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// aiNameSelection is one pending selection: which argv to rewrite and the
// candidates to choose from.
type aiNameSelection struct {
	argv        []string
	positionals []int // argv indexes of the name words; empty = zero-argument
	resource    string
	candidates  []nameCacheEntry
}

// aiEnsureResolutionMetadata populates resolutionIndex (and the operation
// metadata around it) from restish's cached spec, mirroring the loadAPI
// policy: offline, never OAuth, skip when the cache file is absent.
func aiEnsureResolutionMetadata() {
	if len(resolutionIndex) > 0 {
		return
	}
	if _, err := os.Stat(filepath.Join(restishCacheDir(), "dci.cbor")); err != nil {
		return
	}
	_ = ensureDestructiveOperations()
}

// aiPickerIntent is a dispatch that would want a selection if names are
// available: the gates have passed, but no candidate source is consulted yet.
type aiPickerIntent struct {
	argv        []string
	positionals []int  // argv indexes of the name words; empty = zero-argument
	input       string // the joined name words; "" for zero-argument
	resource    string
	target      resolutionListTarget
}

// aiPickerIntentFor applies resolvePathArguments' gates without touching any
// name source. nil means dispatch as-is, always.
func aiPickerIntentFor(argv []string) *aiPickerIntent {
	if len(argv) == 0 {
		return nil
	}
	// Beta subcommands key the resolution metadata as "beta <name>", and
	// their operation arguments start one word later.
	name := argv[0]
	start := 1
	if name == "beta" && len(argv) > 1 {
		name = "beta " + argv[1]
		start = 2
	}
	target, ok := resolutionIndex[name]
	if !ok {
		return nil
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return nil
	}
	if aiArgvHasBoolFlag(argv, "id") {
		return nil
	}
	positionals := aiPositionalIndexes(argv, aiOperationFlagSet(name), start)
	resource := singularResourceName(target.resource)

	if len(positionals) == 0 {
		// F1: zero-argument invocation — pick from everything. Operations
		// with a request body keep their usage error (same gate as
		// zeroArgPickerApplies).
		if target.hasBody {
			return nil
		}
		return &aiPickerIntent{argv: argv, resource: resource, target: target}
	}

	if target.hasBody {
		words := make([]string, 0, len(positionals))
		for _, index := range positionals {
			words = append(words, argv[index])
		}
		if command := aiOperationCommand(name); command != nil && bodyOnlyPositionals(command, words) {
			// The body is there and the name is missing (the CLI's own
			// pathArgumentPickerApplies rule): pick from everything, and let
			// the child place the selection in front of the body.
			return &aiPickerIntent{argv: argv, resource: resource, target: target}
		}
	}

	// Several positionals are a shell-split multi-word name only when the
	// operation takes a single path parameter (resolvePathArguments' joinable
	// rule); with more path parameters, stay out of the way.
	if len(positionals) > 1 && len(operationPathParameters[name]) > 1 {
		return nil
	}
	if target.hasBody && len(positionals) > 1 {
		// Surplus positionals on a bodied operation are body shorthand, not
		// the words of an unquoted name (joinableNameArguments' rule): only
		// the first one fills the path slot.
		positionals = positionals[:1]
	}
	words := make([]string, len(positionals))
	for i, index := range positionals {
		words[i] = argv[index]
	}
	input := strings.TrimSpace(strings.Join(words, " "))
	if input == "" || resourceIDPattern.MatchString(input) {
		return nil
	}
	return &aiPickerIntent{argv: argv, positionals: positionals, input: input, resource: resource, target: target}
}

// selection turns candidates into the picker's selection: everything for a
// zero-argument intent, the CLI matcher's candidates for a name — nil when a
// picker would not help (unique or absent match dispatches unchanged; the
// child resolves it identically).
func (intent *aiPickerIntent) selection(entries []nameCacheEntry) *aiNameSelection {
	if len(entries) == 0 {
		return nil
	}
	if intent.input == "" {
		return &aiNameSelection{argv: intent.argv, resource: intent.resource, candidates: entries}
	}
	matches := matchNameCandidates(intent.input, entries)
	if len(matches) <= 1 {
		return nil
	}
	return &aiNameSelection{argv: intent.argv, positionals: intent.positionals, resource: intent.resource, candidates: matches}
}

// cachedEntries serves the intent from the on-disk name cache; empty means a
// fetch is needed. customerContext must be the session's effective tenant —
// an agent session-scoped switch included — or names would resolve against
// the persisted tenant's cache and dispatch another customer's IDs.
func (intent *aiPickerIntent) cachedEntries(configDir, customerContext string) []nameCacheEntry {
	cache, _ := readNameCache(configDir, customerContext, time.Now())
	return cache.Resources[intent.target.resource]
}

// aiNameSelectionFor is the cache-only path: intent gates plus the on-disk
// cache. The session falls back to an async fetch when this returns nil with
// a live intent.
func aiNameSelectionFor(argv []string, configDir, customerContext string) *aiNameSelection {
	intent := aiPickerIntentFor(argv)
	if intent == nil {
		return nil
	}
	return intent.selection(intent.cachedEntries(configDir, customerContext))
}

// apply rewrites the argv with the chosen entry: the ID replaces the first
// name word, the rest of a multi-word name is dropped, and a zero-argument
// invocation appends it.
func (s *aiNameSelection) apply(entry nameCacheEntry) []string {
	argv := append([]string{}, s.argv...)
	if len(s.positionals) == 0 {
		return append(argv, entry.ID)
	}
	argv[s.positionals[0]] = entry.ID
	if len(s.positionals) == 1 {
		return argv
	}
	drop := make(map[int]bool, len(s.positionals)-1)
	for _, index := range s.positionals[1:] {
		drop[index] = true
	}
	rewritten := make([]string, 0, len(argv))
	for index, arg := range argv {
		if !drop[index] {
			rewritten = append(rewritten, arg)
		}
	}
	return rewritten
}

// filtered narrows the candidates with the CLI's own forgiving matcher; an
// empty filter shows everything.
func (s *aiNameSelection) filtered(filter string) []nameCacheEntry {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return s.candidates
	}
	return matchNameCandidates(filter, s.candidates)
}

// aiOperationFlagSet finds the flag set for a command name — a GA operation,
// a "beta <name>" subcommand, or a custom root command — so the positional
// scan knows which flags consume values.
func aiOperationFlagSet(name string) *pflag.FlagSet {
	command := aiOperationCommand(name)
	if command == nil {
		return nil
	}
	return command.Flags()
}

// aiOperationCommand resolves a session-spelled command name to the cobra
// command behind it: a GA operation, a "beta <name>" subcommand, or a custom
// root command.
func aiOperationCommand(name string) *cobra.Command {
	if cli.Root == nil {
		return nil
	}
	if apiCommand := findDCICommand(); apiCommand != nil {
		if sub, isBeta := strings.CutPrefix(name, "beta "); isBeta {
			for _, betaCommand := range apiCommand.Commands() {
				if betaCommand.Name() != "beta" {
					continue
				}
				for _, operation := range betaCommand.Commands() {
					if operation.Name() == sub || operation.HasAlias(sub) {
						return operation
					}
				}
			}
			return nil
		}
		for _, operation := range apiCommand.Commands() {
			if operation.Name() == name || operation.HasAlias(name) {
				return operation
			}
		}
	}
	for _, command := range cli.Root.Commands() {
		if command.Name() == name || command.HasAlias(name) {
			return command
		}
	}
	return nil
}

// aiPositionalIndexes returns the argv indexes of positional arguments,
// mirroring commandArg's flag-value skipping — plus the NoOptDefVal rule:
// a flag with an optional value (--chart) never consumes the next word.
// start is the index of the first operation argument: 1 after a top-level
// command word, 2 after a "beta <name>" pair.
func aiPositionalIndexes(argv []string, flags *pflag.FlagSet, start int) []int {
	var indexes []int
	for i := start; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			for j := i + 1; j < len(argv); j++ {
				indexes = append(indexes, j)
			}
			return indexes
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			indexes = append(indexes, i)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, hasValue := splitLongFlag(arg)
			if name == "" || hasValue || flags == nil {
				continue
			}
			if flag := flags.Lookup(name); flag != nil && !isBoolFlag(flag) && flag.NoOptDefVal == "" && i+1 < len(argv) {
				i++
			}
			continue
		}
		shorts := strings.TrimPrefix(arg, "-")
		if before, _, ok := strings.Cut(shorts, "="); ok {
			shorts = before
		}
		for j := 0; j < len(shorts) && flags != nil; j++ {
			flag := flags.ShorthandLookup(string(shorts[j]))
			if flag == nil || isBoolFlag(flag) {
				continue
			}
			if flag.NoOptDefVal == "" && j == len(shorts)-1 && i+1 < len(argv) {
				i++
			}
			break
		}
	}
	return indexes
}

func aiArgvHasBoolFlag(argv []string, name string) bool {
	for _, arg := range argv {
		if arg == "--"+name || arg == "--"+name+"=true" {
			return true
		}
	}
	return false
}
