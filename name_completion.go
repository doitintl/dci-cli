package main

// Resource-name shell completion: __complete requests for resolvable path
// parameters are intercepted early and served from an on-disk name cache with
// zero network and zero OAuth on the Tab path. Kept in a sibling file per the
// AGENTS.md chapter-split guidance.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	nameCacheVersion     = 1
	nameCacheFreshTTL    = 10 * time.Minute
	nameCacheServableTTL = 24 * time.Hour
	nameCacheMaxNameLen  = 120
)

type nameCacheEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type nameCacheFile struct {
	Version   int                         `json:"version"`
	Context   string                      `json:"context"`
	FetchedAt time.Time                   `json:"fetchedAt"`
	Resources map[string][]nameCacheEntry `json:"resources"`
}

type nameCacheState int

const (
	nameCacheAbsent nameCacheState = iota
	nameCacheFresh
	nameCacheStale
)

func nameCachePath(configDir, context string) string {
	sum := sha256.Sum256([]byte(context))
	return filepath.Join(configDir, "names-"+hex.EncodeToString(sum[:])[:12]+".json")
}

func readNameCache(configDir, context string, now time.Time) (nameCacheFile, nameCacheState) {
	data, err := os.ReadFile(nameCachePath(configDir, context))
	if err != nil {
		return nameCacheFile{}, nameCacheAbsent
	}
	var cache nameCacheFile
	if err := json.Unmarshal(data, &cache); err != nil || cache.Version != nameCacheVersion {
		return nameCacheFile{}, nameCacheAbsent
	}
	return cache, nameCacheFreshness(cache.FetchedAt, now)
}

// nameCacheFreshness treats a FetchedAt in the future (clock rolled back) as
// absent, so a bad timestamp cannot serve stale names indefinitely.
func nameCacheFreshness(fetchedAt, now time.Time) nameCacheState {
	age := now.Sub(fetchedAt)
	switch {
	case age < 0 || age > nameCacheServableTTL:
		return nameCacheAbsent
	case age <= nameCacheFreshTTL:
		return nameCacheFresh
	default:
		return nameCacheStale
	}
}

// writeNameCache writes via temp file + rename so concurrent dci invocations
// cannot interleave partial writes; CreateTemp yields the required 0600 mode.
func writeNameCache(configDir string, cache nameCacheFile) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(configDir, "names-*.json.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), nameCachePath(configDir, cache.Context))
}

func purgeNameCaches(configDir string) error {
	matches, err := filepath.Glob(filepath.Join(configDir, "names-*.json"))
	if err != nil {
		return err
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func cachedResolverEntries(resource, context string) ([]nameCacheEntry, bool) {
	cache, state := readNameCache(dciConfigDir(), context, time.Now())
	if state != nameCacheFresh {
		return nil, false
	}
	entries, ok := cache.Resources[resource]
	return entries, ok && len(entries) > 0
}

var spawnNameCacheRefresh = spawnDetachedNameRefresh

// spawnDetachedNameRefresh re-execs the binary as a released background
// process: even a short inline fetch is a perceptible Tab stall, so the
// current process serves whatever it has and the refresh arms the next Tab.
func spawnDetachedNameRefresh() {
	command := exec.Command(os.Args[0], "__refresh-names")
	if err := command.Start(); err != nil {
		return
	}
	_ = command.Process.Release()
}

// completionPreflight intercepts __complete/__completeNoDesc for resolvable
// positional arguments before cobra dispatch. It only ever reads the CBOR spec
// cache and the name cache — a cold spec cache falls through to cobra, and no
// path here may load the API from the network or trigger OAuth.
func completionPreflight(args []string) (handled bool, exitCode int) {
	if len(args) < 4 || (args[1] != "__complete" && args[1] != "__completeNoDesc") || args[2] != "dci" {
		return false, 0
	}
	if !invocationCachedSpecAvailable() {
		return false, 0
	}
	words := args[3:]
	toComplete := words[len(words)-1]
	if strings.HasPrefix(toComplete, "-") {
		return false, 0
	}
	commandName, positionals, ok := completionPositionalWords(words[:len(words)-1], completionFlagSets()...)
	if !ok || commandName == "" {
		return false, 0
	}
	api, err := loadDCIOperationAPI()
	if err != nil || len(api.Operations) == 0 {
		return false, 0
	}
	operation := invocationOperation(api, commandName)
	if operation == nil {
		return false, 0
	}
	target, ok := buildResolutionIndex(api.Operations)[operation.Name]
	if !ok {
		return false, 0
	}
	// Surplus positionals are the shell word-splitting an unquoted in-progress
	// name on spaces — except on a body operation, where extra words are body
	// shorthand and must fall through to cobra.
	if len(positionals) > 0 && target.hasBody {
		return false, 0
	}
	cache, state := readNameCache(dciConfigDir(), activeCustomerContext(), time.Now())
	if state != nameCacheFresh {
		spawnNameCacheRefresh()
	}
	entries := []nameCacheEntry{}
	if state != nameCacheAbsent {
		entries = cache.Resources[target.resource]
	}
	printNameCompletions(os.Stdout, entries, positionals, toComplete, args[1] == "__completeNoDesc")
	return true, 0
}

// printNameCompletions emits candidates relative to the shell's current word.
// Shells word-split an unquoted in-progress name on spaces, so the positional
// words preceding the current one are matched against the head of each cached
// name and only the tail from the current word onward is emitted — mirroring
// how zsh completes the last segment of a multi-segment path.
func printNameCompletions(writer *os.File, entries []nameCacheEntry, preceding []string, toComplete string, noDescriptions bool) {
	head := strings.Join(preceding, " ") + " "
	prefix := strings.ToLower(toComplete)
	for _, entry := range entries {
		candidate := entry.Name
		if len(preceding) > 0 {
			tail, matched := trimPrefixFold(candidate, head)
			if !matched || tail == "" {
				continue
			}
			candidate = tail
		}
		if !strings.HasPrefix(strings.ToLower(candidate), prefix) {
			continue
		}
		if noDescriptions {
			fmt.Fprintln(writer, candidate)
			continue
		}
		fmt.Fprintf(writer, "%s\t%s\n", candidate, entry.ID)
	}
	fmt.Fprintf(writer, ":%d\n", cobra.ShellCompDirectiveNoFileComp)
}

// trimPrefixFold trims prefix from s rune-wise and case-insensitively,
// returning the remainder of s with its original casing. Byte-offset trimming
// after ToLower would misalign on runes whose lowercase form changes width.
func trimPrefixFold(s, prefix string) (string, bool) {
	for _, want := range prefix {
		got, size := utf8.DecodeRuneInString(s)
		if size == 0 || unicode.ToLower(got) != unicode.ToLower(want) {
			return "", false
		}
		s = s[size:]
	}
	return s, true
}

func completionFlagSets() []*pflag.FlagSet {
	flagSets := []*pflag.FlagSet{}
	if cli.Root != nil {
		flagSets = append(flagSets, cli.Root.PersistentFlags())
		if dciCommand := findDCICommand(); dciCommand != nil {
			flagSets = append(flagSets, dciCommand.PersistentFlags())
		}
	}
	return flagSets
}

// completionPositionalWords mirrors commandArg's flag-skipping walk over the
// words preceding the completion word: it returns the command word and the
// positional words preceding the one being completed. ok is false when the
// completion word is actually a flag's value (a dangling value-taking flag
// ends the word list), so cobra's flag completion takes over. Flags unknown to
// the given flag sets (operation-local query params) are pragmatically assumed
// to take a value when one follows.
func completionPositionalWords(words []string, flagSets ...*pflag.FlagSet) (command string, positionals []string, ok bool) {
	lookupFlag := func(name string) *pflag.Flag {
		for _, flags := range flagSets {
			if flags != nil {
				if flag := flags.Lookup(name); flag != nil {
					return flag
				}
			}
		}
		return nil
	}
	lookupShorthand := func(name string) *pflag.Flag {
		for _, flags := range flagSets {
			if flags != nil {
				if flag := flags.ShorthandLookup(name); flag != nil {
					return flag
				}
			}
		}
		return nil
	}
	countPositional := func(word string) {
		if command == "" {
			command = word
			return
		}
		positionals = append(positionals, word)
	}

	afterTerminator := false
	for index := 0; index < len(words); index++ {
		word := words[index]
		if afterTerminator {
			countPositional(word)
			continue
		}
		if word == "--" {
			afterTerminator = true
			continue
		}
		if !strings.HasPrefix(word, "-") || word == "-" {
			countPositional(word)
			continue
		}
		if strings.HasPrefix(word, "--") {
			name, hasValue := splitLongFlag(word)
			if name == "" || hasValue {
				continue
			}
			flag := lookupFlag(name)
			takesValue := flag == nil || !isBoolFlag(flag)
			if flag == nil && index+1 < len(words) && strings.HasPrefix(words[index+1], "-") {
				takesValue = false
			}
			if !takesValue {
				continue
			}
			if index+1 >= len(words) {
				return "", nil, false
			}
			index++
			continue
		}
		shorts := word[1:]
		for j := 0; j < len(shorts); j++ {
			flag := lookupShorthand(string(shorts[j]))
			if flag == nil {
				continue
			}
			if isBoolFlag(flag) {
				continue
			}
			if j == len(shorts)-1 {
				if index+1 >= len(words) {
					return "", nil, false
				}
				index++
			}
			break
		}
	}
	return command, positionals, true
}

func registerNameRefreshCommand(configDir string) {
	command := &cobra.Command{
		Use:    "__refresh-names",
		Short:  "Refresh the cached resource names used by shell completion",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if purge, _ := command.Flags().GetBool("purge"); purge {
				return purgeNameCaches(configDir)
			}
			refreshNameCache(configDir)
			return nil
		},
	}
	command.Flags().Bool("purge", false, "Delete all cached resource names")
	cli.Root.AddCommand(command)
}

// refreshNameCache fetches the first page of every resolvable collection and
// rewrites the per-context name cache. It exits silently on any failure — no
// credentials, cold spec cache, or a failed call — because it runs detached
// from a Tab press and must never OAuth or surface errors.
func refreshNameCache(configDir string) {
	if authenticationToken() == "" {
		return
	}
	if !invocationCachedSpecAvailable() {
		return
	}
	api, err := loadDCIOperationAPI()
	if err != nil || len(api.Operations) == 0 {
		return
	}
	context := activeCustomerContext()
	resources := map[string][]nameCacheEntry{}
	for _, target := range sortedResolutionTargets(buildResolutionIndex(api.Operations)) {
		result, err := resolverListFetch(target.listPath, context, 1)
		if err != nil {
			// A single list can fail for reasons specific to that resource —
			// an entitlement 403, an unprovisioned collection — and must not
			// cost the tenant every other resource's completions. Only a
			// credential failure invalidates the whole run.
			if isAuthenticationRequiredError(err) {
				return
			}
			continue
		}
		resources[target.resource] = truncateEntryNames(result.entries)
	}
	if len(resources) == 0 {
		return
	}
	_ = writeNameCache(configDir, nameCacheFile{
		Version:   nameCacheVersion,
		Context:   context,
		FetchedAt: time.Now(),
		Resources: resources,
	})
}

func isAuthenticationRequiredError(err error) bool {
	var preflightError invocationPreflightError
	return errors.As(err, &preflightError) && preflightError.detail.Code == "AUTHENTICATION_REQUIRED"
}

func sortedResolutionTargets(index map[string]resolutionListTarget) []resolutionListTarget {
	seen := map[string]bool{}
	targets := []resolutionListTarget{}
	for _, target := range index {
		if seen[target.listPath] {
			continue
		}
		seen[target.listPath] = true
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].listPath < targets[j].listPath })
	return targets
}

func truncateEntryNames(entries []nameCacheEntry) []nameCacheEntry {
	for index, entry := range entries {
		if runes := []rune(entry.Name); len(runes) > nameCacheMaxNameLen {
			entries[index].Name = string(runes[:nameCacheMaxNameLen])
		}
	}
	return entries
}

func registerStaticFlagCompletions(command *cobra.Command) {
	for flagName, values := range map[string][]string{
		"output":     {"table", "json", "yaml", "csv", "auto", "toon"},
		"rows":       {"positional", "keyed"},
		"table-mode": {"fit", "wrap"},
	} {
		completions := values
		_ = command.RegisterFlagCompletionFunc(flagName, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return completions, cobra.ShellCompDirectiveNoFileComp
		})
	}
}
