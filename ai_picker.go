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
	cacheDir, _ := os.UserCacheDir()
	if _, err := os.Stat(filepath.Join(cacheDir, "dci", "dci.cbor")); err != nil {
		return
	}
	_ = ensureDestructiveOperations()
}

// aiNameSelectionFor decides whether dispatching argv needs an in-session
// selection. nil means dispatch as-is.
func aiNameSelectionFor(argv []string, configDir string) *aiNameSelection {
	if len(argv) == 0 {
		return nil
	}
	target, ok := resolutionIndex[argv[0]]
	if !ok {
		return nil
	}
	if on, valid := parseBoolish(os.Getenv("DCI_NO_RESOLVE")); valid && on {
		return nil
	}
	if aiArgvHasBoolFlag(argv, "id") {
		return nil
	}
	positionals := aiPositionalIndexes(argv, aiOperationFlagSet(argv[0]))
	context := readCustomerContext(configDir)
	cache, _ := readNameCache(configDir, context, time.Now())
	entries := cache.Resources[target.resource]
	if len(entries) == 0 {
		return nil
	}
	resource := singularResourceName(target.resource)

	if len(positionals) == 0 {
		// F1: zero-argument invocation — pick from everything. Operations
		// with a request body keep their usage error (same gate as
		// zeroArgPickerApplies).
		if target.hasBody {
			return nil
		}
		return &aiNameSelection{argv: argv, resource: resource, candidates: entries}
	}

	// Several positionals are a shell-split multi-word name only when the
	// operation takes a single path parameter (resolvePathArguments' joinable
	// rule); with more path parameters, stay out of the way.
	if len(positionals) > 1 && len(operationPathParameters[argv[0]]) > 1 {
		return nil
	}
	words := make([]string, len(positionals))
	for i, index := range positionals {
		words[i] = argv[index]
	}
	input := strings.TrimSpace(strings.Join(words, " "))
	if input == "" || resourceIDPattern.MatchString(input) {
		return nil
	}
	// F2: the CLI's own matcher decides ambiguity; a unique or absent match
	// dispatches unchanged — the child resolves it identically.
	matches := matchNameCandidates(input, entries)
	if len(matches) <= 1 {
		return nil
	}
	return &aiNameSelection{argv: argv, positionals: positionals, resource: resource, candidates: matches}
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

// aiOperationFlagSet finds the flag set for a command name so the positional
// scan knows which flags consume values.
func aiOperationFlagSet(name string) *pflag.FlagSet {
	if cli.Root == nil {
		return nil
	}
	if apiCommand := findDCICommand(); apiCommand != nil {
		for _, operation := range apiCommand.Commands() {
			if operation.Name() == name || operation.HasAlias(name) {
				return operation.Flags()
			}
		}
	}
	for _, command := range cli.Root.Commands() {
		if command.Name() == name || command.HasAlias(name) {
			return command.Flags()
		}
	}
	return nil
}

// aiPositionalIndexes returns the argv indexes of positional arguments,
// mirroring commandArg's flag-value skipping — plus the NoOptDefVal rule:
// a flag with an optional value (--chart) never consumes the next word.
func aiPositionalIndexes(argv []string, flags *pflag.FlagSet) []int {
	var indexes []int
	for i := 1; i < len(argv); i++ {
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
