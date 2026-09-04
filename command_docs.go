// Chapter: curated command docs (COMMAND-DOCS-SPEC.md D1–D3). One YAML file
// per command under command-docs/ carries the hand-written usage examples,
// positional-argument notes, Help Center notes, and related commands that
// neither the OpenAPI spec nor restish's schema-synthesized example can
// express (the synthesized example for patch-anomaly pairs a resolution with
// a review status the API forbids). The files are embedded so `dci <cmd>
// --help` (D2), `dci commands --json` (D3), and the Help Center generator
// (delivered by scribe, D7) all read the same content. Validation against the
// live spec lives in command_docs_test.go (D4); scaffolding and coverage
// reporting in tools/commanddocs (D5).
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/danielgtaylor/shorthand/v2"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

//go:embed command-docs/*.yaml
var commandDocsFS embed.FS

const commandDocsDir = "command-docs"

// commandDoc is one command-docs/<command>.yaml file. Command is the CLI
// command path ("patch-anomaly", "skill list"); the file is named by that
// path with spaces replaced by dashes.
type commandDoc struct {
	Command string `yaml:"command" json:"command"`
	// Draft marks a scaffolded stub (tools/commanddocs scaffold) whose only
	// example is schema-generated. Drafts are treated as absent by --help and
	// the catalog so an uncurated example never reaches a user.
	Draft bool `yaml:"draft,omitempty" json:"draft,omitempty"`
	// Arguments describes positional arguments, keyed by the argument name in
	// the command's usage line.
	Arguments map[string]string   `yaml:"arguments,omitempty" json:"arguments,omitempty"`
	Examples  []commandDocExample `yaml:"examples,omitempty" json:"examples,omitempty"`
	// Notes is Help Center MDX rendered after the operation description; it
	// replaces omni's former command-notes/<command>.mdx overlays.
	Notes   string   `yaml:"notes,omitempty" json:"notes,omitempty"`
	Related []string `yaml:"related,omitempty" json:"related,omitempty"`
}

type commandDocExample struct {
	Description string `yaml:"description" json:"description"`
	Command     string `yaml:"command" json:"command"`
	Output      string `yaml:"output,omitempty" json:"output,omitempty"`
}

// commandDocFileName is the embedded file name for a command path.
func commandDocFileName(command string) string {
	return strings.ReplaceAll(command, " ", "-") + ".yaml"
}

const commandDocQuoteHint = "quote every command: value (double quotes or a >- block) — " +
	"shorthand bodies contain \"field: value\", which YAML reads as a nested mapping unless quoted"

// parseCommandDoc decodes one file and checks the invariants every consumer
// relies on. fileName is the base name, used for the name/command match and
// for error messages.
func parseCommandDoc(fileName string, data []byte) (commandDoc, error) {
	var doc commandDoc
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		if strings.Contains(err.Error(), "mapping values are not allowed") {
			return doc, fmt.Errorf("%s: %v (%s)", fileName, err, commandDocQuoteHint)
		}
		return doc, fmt.Errorf("%s: %v", fileName, err)
	}
	if strings.TrimSpace(doc.Command) == "" {
		return doc, fmt.Errorf("%s: missing command", fileName)
	}
	if want := commandDocFileName(doc.Command); fileName != want {
		return doc, fmt.Errorf("%s: command %q belongs in %s", fileName, doc.Command, want)
	}
	if len(doc.Examples) == 0 && !doc.Draft {
		return doc, fmt.Errorf("%s: at least one example is required (or mark the file draft: true)", fileName)
	}
	commandTokens := strings.Fields(doc.Command)
	for index, example := range doc.Examples {
		if strings.TrimSpace(example.Description) == "" {
			return doc, fmt.Errorf("%s: example %d has no description", fileName, index+1)
		}
		words := exampleInvocationWords(example.Command)
		if len(words) < 1+len(commandTokens) || words[0] != "dci" {
			return doc, fmt.Errorf("%s: example %d must start with %q", fileName, index+1, "dci "+doc.Command)
		}
		for offset, token := range commandTokens {
			if words[1+offset] != token {
				return doc, fmt.Errorf("%s: example %d must start with %q", fileName, index+1, "dci "+doc.Command)
			}
		}
	}
	for _, related := range doc.Related {
		if related == doc.Command {
			return doc, fmt.Errorf("%s: related lists the command itself", fileName)
		}
	}
	return doc, nil
}

var (
	commandDocsOnce  sync.Once
	commandDocsIndex map[string]commandDoc
	commandDocsErr   error
)

// loadCommandDocs parses every embedded file once. Errors are aggregated so
// a test run names every broken file; at runtime a failure simply leaves the
// callers on their existing fallbacks (restish's example, no notes).
func loadCommandDocs() (map[string]commandDoc, error) {
	commandDocsOnce.Do(func() {
		commandDocsIndex, commandDocsErr = parseCommandDocsFS(commandDocsFS, commandDocsDir)
	})
	return commandDocsIndex, commandDocsErr
}

func parseCommandDocsFS(fsys fs.FS, dir string) (map[string]commandDoc, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	docs := make(map[string]commandDoc, len(entries))
	var problems []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, readErr := fs.ReadFile(fsys, path.Join(dir, entry.Name()))
		if readErr != nil {
			problems = append(problems, readErr.Error())
			continue
		}
		doc, parseErr := parseCommandDoc(entry.Name(), data)
		if parseErr != nil {
			problems = append(problems, parseErr.Error())
			continue
		}
		docs[doc.Command] = doc
	}
	if len(problems) > 0 {
		return docs, fmt.Errorf("command docs:\n  %s", strings.Join(problems, "\n  "))
	}
	return docs, nil
}

// commandDocKey derives the command path used to key doc files from a cobra
// command: the names from the root down, minus the root itself and the
// hidden `dci` API parent (which shares the root's name). API operations
// therefore key by bare name ("patch-anomaly"), local subcommands by path
// ("skill list"), beta operations by "beta <name>".
func commandDocKey(cmd *cobra.Command) string {
	var names []string
	for current := cmd; current != nil && current.HasParent(); current = current.Parent() {
		if current.Name() == "dci" {
			continue
		}
		names = append([]string{current.Name()}, names...)
	}
	return strings.Join(names, " ")
}

// commandDocFor returns the curated, non-draft doc for a command.
func commandDocFor(cmd *cobra.Command) (commandDoc, bool) {
	return lookupCommandDoc(commandDocKey(cmd))
}

func lookupCommandDoc(key string) (commandDoc, bool) {
	docs, _ := loadCommandDocs()
	doc, ok := docs[key]
	if !ok || doc.Draft {
		return commandDoc{}, false
	}
	return doc, true
}

// renderCommandDocExamples formats examples for cobra's Examples: section —
// two-space indent, a `# description` line above each command, a blank line
// between examples. Output text stays on the web page; --help shows the
// command lines only.
func renderCommandDocExamples(doc commandDoc) string {
	var builder strings.Builder
	for index, example := range doc.Examples {
		if index > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("  # " + strings.TrimSpace(example.Description) + "\n")
		builder.WriteString("  " + strings.Join(strings.Fields(example.Command), " ") + "\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

// renderCommandDocArguments formats the Arguments: block for --help, in the
// order the usage line declares the positionals; any documented argument the
// usage line does not name follows alphabetically.
func renderCommandDocArguments(cmd *cobra.Command) string {
	doc, ok := commandDocFor(cmd)
	if !ok || len(doc.Arguments) == 0 {
		return ""
	}
	ordered := make([]string, 0, len(doc.Arguments))
	seen := map[string]bool{}
	for _, token := range strings.Fields(cmd.Use)[1:] {
		name := strings.Trim(token, "[]<>")
		if _, documented := doc.Arguments[name]; documented && !seen[name] {
			ordered = append(ordered, name)
			seen[name] = true
		}
	}
	var rest []string
	for name := range doc.Arguments {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)
	width := 0
	for _, name := range ordered {
		if len(name) > width {
			width = len(name)
		}
	}
	var builder strings.Builder
	for _, name := range ordered {
		description := strings.Join(strings.Fields(doc.Arguments[name]), " ")
		builder.WriteString(fmt.Sprintf("  %-*s  %s\n", width, name, description))
	}
	return strings.TrimRight(builder.String(), "\n")
}

// applyCommandDocHelp swaps a command's Example for its curated examples and
// returns the function that restores the original — the same save-and-defer
// shape the help hook uses for terse long text. A missing or draft doc is a
// no-op, leaving restish's synthesized example as the fallback.
func applyCommandDocHelp(cmd *cobra.Command) func() {
	doc, ok := commandDocFor(cmd)
	if !ok || len(doc.Examples) == 0 {
		return func() {}
	}
	original := cmd.Example
	cmd.Example = renderCommandDocExamples(doc)
	return func() { cmd.Example = original }
}

// exampleBodyTokens splits an example command line into its parts relative
// to the command: the positional tokens (the first `positional` non-flag
// words after the command path), the flag tokens, and the remaining body
// tokens. Flags with an inline value (`--output=json`) stay one token; a flag
// whose value follows as a separate word is not disambiguated here — the
// callers that care (the validator) resolve values against the flag set.
type exampleParts struct {
	Command     []string // the command path tokens after "dci"
	Positionals []string
	Flags       []string // "--name" or "-x", inline values stripped
	FlagValues  map[string]string
	Body        []string
	Stdin       string // file after a bare "<", "" when none
}

// exampleInvocationWords reduces an example line to the dci invocation
// itself: leading `NAME=value` environment assignments are dropped and the
// words stop at the first shell operator (`|`, `>`, `>>`, `&&`, `;`), so a
// pipe into jq or a redirect to a file never reads as flags or body.
func exampleInvocationWords(line string) []string {
	words := splitShellWords(line)
	for len(words) > 0 && isEnvAssignment(words[0]) {
		words = words[1:]
	}
	for index, word := range words {
		switch word {
		case "|", ">", ">>", "&&", ";", "||":
			return words[:index]
		}
	}
	return words
}

func isEnvAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	if !found || name == "" {
		return false
	}
	for index, char := range name {
		if !(char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func splitExampleCommand(line string, commandTokens int, positional int, isFlagWithValue func(string) bool) exampleParts {
	words := exampleInvocationWords(line)
	parts := exampleParts{FlagValues: map[string]string{}}
	if len(words) < 1+commandTokens {
		return parts
	}
	parts.Command = words[1 : 1+commandTokens]
	rest := words[1+commandTokens:]
	for index := 0; index < len(rest); index++ {
		word := rest[index]
		switch {
		case word == "<" && index+1 < len(rest):
			parts.Stdin = rest[index+1]
			index++
		case strings.HasPrefix(word, "<") && strings.HasSuffix(word, ">") && !strings.Contains(word, " "):
			// An angle-bracket placeholder is a positional when positionals
			// remain, otherwise it is a placeholder body value.
			if len(parts.Positionals) < positional {
				parts.Positionals = append(parts.Positionals, word)
			} else {
				parts.Body = append(parts.Body, word)
			}
		case strings.HasPrefix(word, "-") && len(word) > 1 && !strings.Contains(word, ":"):
			name, value, inline := strings.Cut(word, "=")
			parts.Flags = append(parts.Flags, name)
			if inline {
				parts.FlagValues[name] = value
			} else if isFlagWithValue != nil && isFlagWithValue(name) && index+1 < len(rest) && rest[index+1] != "<" && !strings.HasPrefix(rest[index+1], "-") {
				parts.FlagValues[name] = rest[index+1]
				index++
			}
		case len(parts.Positionals) < positional && !strings.ContainsAny(word, ":{["):
			parts.Positionals = append(parts.Positionals, word)
		default:
			parts.Body = append(parts.Body, word)
		}
	}
	return parts
}

// exampleBody parses the shorthand body of an example into a value, the way
// restish would at request time. ok is false when the example carries no
// inline body (stdin, @file, or none).
func exampleBody(parts exampleParts) (any, bool, error) {
	if len(parts.Body) == 0 || parts.Stdin != "" {
		return nil, false, nil
	}
	if strings.HasPrefix(parts.Body[0], "@") {
		// A leading @file is the whole body read from a file. After a field
		// (`file: @events.csv`) it is that field's value — a file attached to
		// a multipart upload — and parses as a plain string below.
		return nil, false, nil
	}
	options := bodyShorthandParseOptions
	options.EnableFileInput = false // never read a file while validating docs
	parsed, err := shorthand.Unmarshal(strings.Join(parts.Body, " "), options, nil)
	if err != nil {
		return nil, true, err
	}
	return parsed, true, nil
}

// commandDocBodyExample returns the first curated example's parsed inline
// body for the catalog's `body` argument, replacing the schema-synthesized
// one. positional is the command's path-parameter count.
func commandDocBodyExample(doc commandDoc, positional int) (any, bool) {
	commandTokens := len(strings.Fields(doc.Command))
	for _, example := range doc.Examples {
		parts := splitExampleCommand(example.Command, commandTokens, positional, nil)
		body, ok, err := exampleBody(parts)
		if ok && err == nil && body != nil {
			return body, true
		}
	}
	return nil, false
}

// splitShellWords splits a command line the way a POSIX shell would for the
// cases doc examples use: whitespace separation, single and double quotes
// (removed), and backslash escapes inside double quotes and bare text.
func splitShellWords(line string) []string {
	var words []string
	var current strings.Builder
	inWord := false
	quote := rune(0)
	runes := []rune(line)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		switch {
		case quote == '\'':
			if char == '\'' {
				quote = 0
			} else {
				current.WriteRune(char)
			}
		case quote == '"':
			if char == '"' {
				quote = 0
			} else if char == '\\' && index+1 < len(runes) && strings.ContainsRune(`"\$`, runes[index+1]) {
				index++
				current.WriteRune(runes[index])
			} else {
				current.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote = char
			inWord = true
		case char == '\\' && index+1 < len(runes):
			index++
			current.WriteRune(runes[index])
			inWord = true
		case char == ' ' || char == '\t' || char == '\n':
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(char)
			inWord = true
		}
	}
	if inWord {
		words = append(words, current.String())
	}
	return words
}

func isCommandDocPlaceholder(token string) bool {
	if len(token) < 3 || token[0] != '<' || token[len(token)-1] != '>' {
		return false
	}
	inner := token[1 : len(token)-1]
	if inner[0] < 'a' || inner[0] > 'z' {
		return false
	}
	for _, char := range inner {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return true
}

// mdxUnsafeNoteLines reports lines of a notes block that MDX would not render
// literally: an unescaped `{` or `<` outside fenced code and inline code. The
// rule mirrors scribe's findMdxUnsafeLines so a note that passes here renders
// on the Help Center without breaking the omni build.
func mdxUnsafeNoteLines(notes string) []int {
	var unsafe []int
	inFence := false
	for index, line := range strings.Split(notes, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		withoutCode := line
		for {
			start := strings.Index(withoutCode, "`")
			if start < 0 {
				break
			}
			end := strings.Index(withoutCode[start+1:], "`")
			if end < 0 {
				break
			}
			withoutCode = withoutCode[:start] + withoutCode[start+1+end+1:]
		}
		withoutEscapes := strings.ReplaceAll(strings.ReplaceAll(withoutCode, `\{`, ""), `\<`, "")
		if strings.ContainsAny(withoutEscapes, "{<") {
			unsafe = append(unsafe, index+1)
		}
	}
	return unsafe
}

// admonitionFencesBalanced checks that every `:::type` opener in a notes block
// has its closing `:::`.
func admonitionFencesBalanced(notes string) bool {
	open := false
	for _, line := range strings.Split(notes, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, ":::") {
			continue
		}
		if trimmed == ":::" {
			if !open {
				return false
			}
			open = false
			continue
		}
		if open {
			return false
		}
		open = true
	}
	return !open
}
