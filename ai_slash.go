package main

// P1 of AI-SPEC: the `dci ai` session's slash grammar — line routing, the
// shell-words splitter, the completion catalog and its filtering, and the
// persisted input history. Everything here is pure logic with no Bubble Tea
// dependency so the grammar stays unit-testable; the terminal program lives
// in ai_tui.go. Kept in a sibling file per the AGENTS.md chapter-split
// guidance.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

// --- Session verbs ----------------------------------------------------------

// aiSessionVerb is a session-level command: it acts on the session itself and
// never dispatches to the CLI. Per AI-SPEC §4.2 these resolve before
// user-defined commands (P2) and before the CLI catalog.
type aiSessionVerb struct {
	name    string
	usage   string
	summary string
}

var aiSessionVerbs = []aiSessionVerb{
	{name: "customer", usage: "/customer [name|id]", summary: "Show or set the customer context"},
	{name: "model", usage: "/model [id]", summary: "Show or set the AI model"},
	{name: "export", usage: "/export [file]", summary: "Save the transcript to a file"},
	{name: "mouse", usage: "/mouse", summary: "Toggle mouse capture (off = select/copy text)"},
	{name: "clear", usage: "/clear", summary: "Clear the transcript and start a new conversation"},
	{name: "help", usage: "/help", summary: "Show how the session works"},
	{name: "quit", usage: "/quit", summary: "Leave the session"},
}

// aiVerbAliases maps alternate spellings onto session verbs.
var aiVerbAliases = map[string]string{
	"exit": "quit",
}

func aiLookupVerb(name string) (aiSessionVerb, bool) {
	if canonical, ok := aiVerbAliases[name]; ok {
		name = canonical
	}
	for _, verb := range aiSessionVerbs {
		if verb.name == name {
			return verb, true
		}
	}
	return aiSessionVerb{}, false
}

// --- Line routing -----------------------------------------------------------

type aiRouteKind int

const (
	aiRouteEmpty    aiRouteKind = iota
	aiRouteChat                 // plain text: the AI path (a notice until P2)
	aiRouteVerb                 // a session verb
	aiRouteDispatch             // a CLI command, dispatched verbatim after the slash
	aiRouteUnknown              // a slash line matching nothing: suggestions, never dispatch
	aiRouteInvalid              // a slash line that does not parse (unterminated quote)
)

type aiRoute struct {
	kind        aiRouteKind
	text        string   // aiRouteChat: the question; aiRouteInvalid: the parse error
	verb        string   // aiRouteVerb: canonical verb name
	args        []string // aiRouteVerb: arguments after the verb
	argv        []string // aiRouteDispatch: the full argv, as the outer CLI would see it
	suggestions []string // aiRouteUnknown: closest catalog paths
}

// aiRouteLine implements the input grammar (AI-SPEC §4): `/` is deterministic,
// plain text is AI, and a failed `/` never falls through to the model.
// Resolution order per §4.2: session verbs, then user-defined commands (D5),
// then the CLI catalog. The catalog may be empty (no cached spec yet — e.g.
// before first login); then an unrecognized first token dispatches
// optimistically, because the child process's own error is still a
// deterministic outcome, which is all the grammar promises.
func aiRouteLine(line string, catalog []aiCatalogEntry, userCommands map[string]aiUserCommand) aiRoute {
	line = strings.TrimSpace(line)
	if line == "" {
		return aiRoute{kind: aiRouteEmpty}
	}
	if !strings.HasPrefix(line, "/") {
		// A bare "exit"/"quit" is someone leaving, not a question for the
		// model — honor the shell instinct.
		if lower := strings.ToLower(line); lower == "exit" || lower == "quit" {
			return aiRoute{kind: aiRouteVerb, verb: "quit"}
		}
		return aiRoute{kind: aiRouteChat, text: line}
	}
	argv, err := splitCommandLine(strings.TrimPrefix(line, "/"))
	if err != nil {
		return aiRoute{kind: aiRouteInvalid, text: err.Error()}
	}
	if len(argv) == 0 {
		return aiRoute{kind: aiRouteEmpty}
	}
	if verb, ok := aiLookupVerb(argv[0]); ok {
		return aiRoute{kind: aiRouteVerb, verb: verb.name, args: argv[1:]}
	}
	if command, ok := userCommands[argv[0]]; ok {
		if route, expanded := aiExpandUserCommand(command, argv[1:]); expanded {
			return route
		}
	}
	if len(catalog) == 0 || aiCatalogHasCommand(catalog, argv[0]) {
		return aiRoute{kind: aiRouteDispatch, argv: argv}
	}
	return aiRoute{kind: aiRouteUnknown, text: argv[0], suggestions: aiSuggestions(catalog, argv[0], 5)}
}

// --- User-defined slash commands (D5) ----------------------------------------

const aiUserCommandsFileName = "ai_commands.json"

// aiUserCommand is one saved command: either a prompt (expands to an AI
// question) or a command line (expands to a dispatch). Exactly one is set;
// prompt wins if both are.
type aiUserCommand struct {
	Prompt  string `json:"prompt,omitempty"`
	Command string `json:"command,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// loadAIUserCommands reads the user's saved commands. Names shadowed by
// session verbs are dropped — the resolution order makes them unreachable,
// so surfacing them in completion would lie. Best-effort: a broken file is
// an empty set.
func loadAIUserCommands(configDir string) map[string]aiUserCommand {
	data, err := os.ReadFile(filepath.Join(configDir, aiUserCommandsFileName))
	if err != nil {
		return nil
	}
	var commands map[string]aiUserCommand
	if err := json.Unmarshal(data, &commands); err != nil {
		return nil
	}
	for name := range commands {
		if _, shadowed := aiLookupVerb(name); shadowed || strings.ContainsAny(name, " \t/") || name == "" {
			delete(commands, name)
		}
	}
	return commands
}

// aiExpandUserCommand turns a saved command plus trailing args into a route:
// prompts become chat text (args appended), command lines become dispatches
// (args appended to the argv). expanded=false means the entry was empty.
func aiExpandUserCommand(command aiUserCommand, args []string) (aiRoute, bool) {
	if command.Prompt != "" {
		text := command.Prompt
		if len(args) > 0 {
			text += " " + strings.Join(args, " ")
		}
		return aiRoute{kind: aiRouteChat, text: text}, true
	}
	if command.Command != "" {
		argv, err := splitCommandLine(command.Command)
		if err != nil || len(argv) == 0 {
			return aiRoute{kind: aiRouteInvalid, text: "saved command does not parse: " + command.Command}, true
		}
		return aiRoute{kind: aiRouteDispatch, argv: append(argv, args...)}, true
	}
	return aiRoute{}, false
}

// splitCommandLine splits a command line into argv the way a POSIX-ish shell
// would: whitespace separates words; single quotes preserve everything;
// double quotes preserve everything but allow \" and \\; a backslash outside
// quotes escapes the next rune. No expansion of any kind.
func splitCommandLine(line string) ([]string, error) {
	var (
		argv    []string
		current strings.Builder
		inWord  bool
		quote   rune // 0, '\'' or '"'
		escaped bool
	)
	for _, r := range line {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '\'' && r != '\'':
			current.WriteRune(r)
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == '\\':
			escaped = true
			inWord = true
		case r == ' ' || r == '\t':
			if inWord {
				argv = append(argv, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if inWord {
		argv = append(argv, current.String())
	}
	return argv, nil
}

// --- Completion catalog -----------------------------------------------------

// aiCatalogEntry is one completable command, spelled the way the user types it
// at the outer CLI ("list-budgets", "customer-context set").
type aiCatalogEntry struct {
	Path    string
	Summary string
}

// aiSessionCatalog builds the completion catalog from the live cobra tree:
// API operations (hydrated lazily from restish's cached spec, same policy as
// setupCompletion's loadAPI — never a network fetch, never OAuth) plus the
// custom root commands. Called once per session; before first login the API
// half is simply absent.
func aiSessionCatalog() []aiCatalogEntry {
	if cli.Root == nil {
		return nil
	}
	hydrateAPICommandsForAI()
	entries := make([]aiCatalogEntry, 0, 64)
	if apiCommand := findDCICommand(); apiCommand != nil {
		for _, operation := range apiCommand.Commands() {
			if operation.Hidden || operation.Name() == "help" {
				continue
			}
			entries = append(entries, aiCatalogEntry{Path: operation.Name(), Summary: operation.Short})
		}
	}
	for _, command := range cli.Root.Commands() {
		if command.Hidden || command.Name() == "dci" || command.Name() == "help" ||
			command.Name() == "completion" || command.Name() == "ai" {
			continue
		}
		entries = append(entries, aiCatalogWalk(command, "")...)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func aiCatalogWalk(command *cobra.Command, parent string) []aiCatalogEntry {
	path := command.Name()
	if parent != "" {
		path = parent + " " + path
	}
	entries := make([]aiCatalogEntry, 0, 1)
	children := command.Commands()
	if command.Runnable() || len(children) == 0 {
		entries = append(entries, aiCatalogEntry{Path: path, Summary: command.Short})
	}
	for _, child := range children {
		if child.Hidden || child.Name() == "help" {
			continue
		}
		entries = append(entries, aiCatalogWalk(child, path)...)
	}
	return entries
}

// hydrateAPICommandsForAI mirrors setupCompletion's lazy loadAPI: restish only
// hydrates the operation commands inside cli.Run when os.Args[1] is the API
// name, which "ai" is not. Loading from the cached spec is offline; when the
// cache file does not exist (never logged in) we skip rather than trigger
// OAuth.
func hydrateAPICommandsForAI() {
	if cli.Root == nil {
		return
	}
	cacheDir, _ := os.UserCacheDir()
	cacheFile := filepath.Join(cacheDir, "dci", "dci.cbor")
	if _, err := os.Stat(cacheFile); err != nil {
		return
	}
	if apiCommand := findDCICommand(); apiCommand != nil && len(apiCommand.Commands()) == 0 {
		if base, err := apiBase(); err == nil {
			cli.Load(base, apiCommand)
		}
	}
	// The resolution metadata (resolutionIndex, path parameters) powers the
	// session's name picker (ai_picker.go) — same cached spec, still offline.
	aiEnsureResolutionMetadata()
}

func aiCatalogHasCommand(catalog []aiCatalogEntry, first string) bool {
	for _, entry := range catalog {
		if entry.Path == first || strings.HasPrefix(entry.Path, first+" ") {
			return true
		}
	}
	return false
}

// aiSuggestions returns the closest catalog paths for an unknown token:
// prefix matches first, then substring matches, then subsequence matches
// (which catch dropped-letter typos like "lst-budgets"), capped.
func aiSuggestions(catalog []aiCatalogEntry, token string, limit int) []string {
	token = strings.ToLower(token)
	var prefix, substring, subsequence []string
	for _, entry := range catalog {
		lower := strings.ToLower(entry.Path)
		switch {
		case strings.HasPrefix(lower, token):
			prefix = append(prefix, entry.Path)
		case strings.Contains(lower, token):
			substring = append(substring, entry.Path)
		case isSubsequence(token, lower):
			subsequence = append(subsequence, entry.Path)
		}
	}
	merged := append(append(prefix, substring...), subsequence...)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// isSubsequence reports whether every rune of needle appears in haystack in
// order (not necessarily adjacent).
func isSubsequence(needle, haystack string) bool {
	if needle == "" {
		return false
	}
	runes := []rune(needle)
	index := 0
	for _, r := range haystack {
		if r == runes[index] {
			index++
			if index == len(runes) {
				return true
			}
		}
	}
	return false
}

// --- Completion popup -------------------------------------------------------

type aiCompletion struct {
	Value   string // what Tab inserts after the slash
	Summary string
}

// aiCompletionsFor returns popup candidates for the current input. The popup
// only completes the first token: once a space follows a committed token, it
// stays hidden. Session verbs list first, then user-defined commands, then
// catalog prefix matches, then catalog substring matches (the §4.2 order).
func aiCompletionsFor(input string, catalog []aiCatalogEntry, userCommands map[string]aiUserCommand, limit int) []aiCompletion {
	if !strings.HasPrefix(input, "/") || strings.ContainsAny(input, " \t") {
		return nil
	}
	token := strings.ToLower(strings.TrimPrefix(input, "/"))
	var verbs, prefix, substring []aiCompletion
	for _, verb := range aiSessionVerbs {
		if strings.HasPrefix(verb.name, token) {
			verbs = append(verbs, aiCompletion{Value: verb.name, Summary: verb.summary})
		}
	}
	userNames := make([]string, 0, len(userCommands))
	for name := range userCommands {
		userNames = append(userNames, name)
	}
	sort.Strings(userNames)
	for _, name := range userNames {
		if strings.HasPrefix(strings.ToLower(name), token) {
			summary := userCommands[name].Summary
			if summary == "" {
				summary = "saved command"
			}
			verbs = append(verbs, aiCompletion{Value: name, Summary: summary})
		}
	}
	for _, entry := range catalog {
		lower := strings.ToLower(entry.Path)
		switch {
		case strings.HasPrefix(lower, token):
			prefix = append(prefix, aiCompletion{Value: entry.Path, Summary: entry.Summary})
		case token != "" && strings.Contains(lower, token):
			substring = append(substring, aiCompletion{Value: entry.Path, Summary: entry.Summary})
		}
	}
	merged := append(append(verbs, prefix...), substring...)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// --- History ----------------------------------------------------------------

const (
	aiHistoryFileName = "ai_history"
	aiHistoryMax      = 1000
)

func aiHistoryPath(configDir string) string {
	return filepath.Join(configDir, aiHistoryFileName)
}

// loadAIHistory returns the persisted input history, oldest first, capped to
// the most recent aiHistoryMax entries. A missing file is an empty history.
func loadAIHistory(configDir string) []string {
	data, err := os.ReadFile(aiHistoryPath(configDir))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	history := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			history = append(history, line)
		}
	}
	if len(history) > aiHistoryMax {
		history = history[len(history)-aiHistoryMax:]
	}
	return history
}

// appendAIHistory records one submitted line, skipping consecutive
// duplicates, and rewrites the file when it grows past twice the cap so it
// cannot grow without bound. Best-effort: history failures never surface.
func appendAIHistory(configDir string, history []string, line string) []string {
	line = strings.TrimSpace(line)
	if line == "" || (len(history) > 0 && history[len(history)-1] == line) {
		return history
	}
	history = append(history, line)
	if len(history) > 2*aiHistoryMax {
		history = history[len(history)-aiHistoryMax:]
		_ = os.WriteFile(aiHistoryPath(configDir), []byte(strings.Join(history, "\n")+"\n"), 0o600)
		return history
	}
	file, err := os.OpenFile(aiHistoryPath(configDir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return history
	}
	defer file.Close()
	_, _ = file.WriteString(line + "\n")
	return history
}

// --- /customer --------------------------------------------------------------

// aiHandleCustomer implements the /customer session verb: no argument shows
// the current context, one argument validates and persists it — the same
// write path as `dci customer-context set`. Subprocesses read the file, so
// every later dispatch picks the new context up automatically.
func aiHandleCustomer(configDir string, args []string) (string, error) {
	switch len(args) {
	case 0:
		if context := readCustomerContext(configDir); context != "" {
			return "Customer context: " + context, nil
		}
		return "Customer context not set. Set one with /customer <name|id>.", nil
	case 1:
		token := strings.TrimSpace(args[0])
		if err := validateCustomerContextValue(token); err != nil {
			return "", err
		}
		if err := os.WriteFile(customerContextPath(configDir), []byte(token+"\n"), 0o600); err != nil {
			return "", err
		}
		return "Customer context set to " + token, nil
	default:
		return "", fmt.Errorf("usage: /customer [name|id]")
	}
}
