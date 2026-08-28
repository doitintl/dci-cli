package main

// Argument-placeholder overlays for the `dci ai` session's input
// (AI-PLACEHOLDER-SPEC, phase 1): the moment the input names a runnable
// command, the rest of the row shows the command's remaining arguments as
// faint ghost text — path parameters first, then the required body fields —
// consumed left-to-right as the user types real values. Everything here is
// pure logic with no Bubble Tea dependency: the TUI recomputes the ghost per
// input change (refreshGhost, ai_tui.go) and splices it into the rendered
// input row (aiSpliceGhost). Vocabulary matches the usage trailer
// (argvUsageTrailer, error_contract.go): path parameters spelled as cobra's
// Use spells them, body fields with their schema `*` required markers. Kept
// in a sibling file per the AGENTS.md chapter-split guidance.

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/pflag"
)

// aiPlaceholder is one unconsumed argument slot in a command's signature.
type aiPlaceholder struct {
	label string // "report-id", "tags*: [string]", "widget-name-or-id"
	body  bool   // body field (vs path parameter)
	name  string // body field name, marker stripped; "" for path slots
}

// aiCommandSignature is the per-command placeholder model, derived from the
// live cobra tree (the argvUsageTrailer descent) plus the resolution
// metadata. Built per keystroke without memoization on purpose: one child
// walk and one schema-block parse are cheaper than the full-catalog scan
// aiCompletionsFor already runs on the same keystroke.
type aiCommandSignature struct {
	words        int // how many argv words name the command ("beta run-report" = 2)
	placeholders []aiPlaceholder
	pathCount    int
	hasBody      bool
	optionalBody bool           // schema has optional top-level fields → the ghost gains "…"
	resolvable   bool           // single path slot accepts names (resolutionIndex)
	flags        *pflag.FlagSet // the leaf's own flag set, for value-flag skipping
}

// aiPlaceholderSignatureFor resolves the typed argv words to a leaf command
// and builds its signature. nil when there is nothing to ghost: an unknown
// first token, a group command (its next word is the popup's job, not the
// ghost's), or a leaf with no Use. Session verbs and user-defined commands
// are the caller's gate (refreshGhost) — they are not cobra commands and
// their argument surfaces are not spec-derived.
func aiPlaceholderSignatureFor(argv []string) *aiCommandSignature {
	if len(argv) == 0 || cli.Root == nil {
		return nil
	}
	command := findChildCommand(findDCICommand(), argv[0])
	if command == nil {
		command = findChildCommand(cli.Root, argv[0])
	}
	if command == nil {
		return nil
	}
	// Descend while the following words name subcommands ("beta run-report");
	// the matched words are the command, everything after them arguments.
	matched := 1
	for matched < len(argv) {
		child := findChildCommand(command, argv[matched])
		if child == nil {
			break
		}
		command = child
		matched++
	}
	if len(command.Commands()) > 0 || strings.TrimSpace(command.Use) == "" {
		return nil
	}
	useWords := strings.Fields(command.Use)
	pathWords := useWords[1:]
	signature := &aiCommandSignature{
		words:     matched,
		pathCount: len(pathWords),
		// The leaf's own flags only — inherited persistent flags (--output)
		// are invisible here, the same limitation the picker's
		// aiOperationFlagSet accepts: an unknown value-taking flag's value
		// can transiently read as a positional, which costs one ghost slot
		// until the line is corrected, never a wrong dispatch.
		flags: command.Flags(),
	}
	// A name-resolvable single-parameter command accepts names in its path
	// slot (ai_picker.go mirrors resolvePathArguments), so the ghost offers
	// the resource noun instead of nagging for the ID. Keyed the session's
	// way: "beta run-report" for beta subcommands.
	target, resolvable := resolutionIndex[strings.Join(argv[:matched], " ")]
	signature.resolvable = resolvable && len(pathWords) == 1
	for index, word := range pathWords {
		label := word
		if signature.resolvable && index == 0 {
			label = singularResourceName(target.resource) + "-name-or-id"
		}
		signature.placeholders = append(signature.placeholders, aiPlaceholder{label: label})
	}
	// Body fields, in schema order, from the same parse body validation and
	// the usage trailer trust. Required fields become placeholders; optional
	// ones collapse into the ghost's trailing "…" — the full list is the
	// trailer's and --help's job, the ghost is a prompt.
	for _, field := range requestSchemaTopLevelFieldSketches(command.Long) {
		signature.hasBody = true
		if !field.required {
			signature.optionalBody = true
			continue
		}
		label := field.name + "*"
		if sketch := aiNormalizeSchemaSketch(field.sketch); sketch != "" {
			label += ": " + sketch
		}
		signature.placeholders = append(signature.placeholders, aiPlaceholder{
			label: label, body: true, name: field.name,
		})
	}
	return signature
}

// aiNormalizeSchemaSketch condenses a schema line's value sketch for the
// one-row ghost: a nested block opener carries no information beyond its
// shape.
func aiNormalizeSchemaSketch(sketch string) string {
	switch sketch {
	case "{":
		return "object"
	case "[", "[{":
		return "[…]"
	}
	return sketch
}

// aiPlaceholdersRemaining consumes the signature against the typed argv,
// returning the unconsumed tail. Pure and positional — recomputed from the
// whole line every time, so paste, backspace, and history recall need no
// state:
//
//   - flags and their values are skipped (aiPositionalIndexes, the picker's
//     own positional/flag split);
//   - each positional consumes one path slot, left to right;
//   - the first token shaped like body input (shorthand `field:`, a JSON
//     object, @file or <file) switches consumption to name-based: from there
//     placeholders disappear as their field names appear anywhere in the
//     rest of the line, because restish shorthand is order-free;
//   - a whole-body token (@file, <file) consumes every body placeholder;
//   - surplus positionals past the path slots with no body shape consume
//     nothing — words of a multi-word name on a resolvable command, or an
//     argument the child will reject either way.
func aiPlaceholdersRemaining(signature *aiCommandSignature, argv []string) []aiPlaceholder {
	if signature == nil {
		return nil
	}
	pathConsumed := 0
	bodyStarted := false
	wholeBody := false
	consumed := map[string]bool{}
	for _, index := range aiPositionalIndexes(argv, signature.flags, signature.words) {
		token := argv[index]
		switch {
		case bodyStarted:
			whole, fields := aiBodyTokenFields(token)
			wholeBody = wholeBody || whole
			for _, field := range fields {
				consumed[field] = true
			}
		case pathConsumed < signature.pathCount:
			pathConsumed++
		case aiTokenStartsBody(token):
			bodyStarted = true
			whole, fields := aiBodyTokenFields(token)
			wholeBody = whole
			for _, field := range fields {
				consumed[field] = true
			}
		}
	}
	var remaining []aiPlaceholder
	pathSeen := 0
	for _, placeholder := range signature.placeholders {
		if !placeholder.body {
			pathSeen++
			if pathSeen <= pathConsumed {
				continue
			}
			remaining = append(remaining, placeholder)
			continue
		}
		if wholeBody || consumed[placeholder.name] {
			continue
		}
		remaining = append(remaining, placeholder)
	}
	return remaining
}

// aiTokenStartsBody reports whether a positional token is body input:
// restish shorthand naming a field, an inline JSON object, or a whole body
// from a file or stdin redirect.
func aiTokenStartsBody(token string) bool {
	if strings.HasPrefix(token, "@") || strings.HasPrefix(token, "<") {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(token), "{") {
		return true
	}
	return shorthandBodyFieldPattern.MatchString(token)
}

// aiBodyTokenFields names the top-level fields one body token supplies.
// whole marks tokens that provide the entire body (@file, <file), which
// consume every body placeholder. A partial JSON object that does not parse
// yet supplies nothing — the user is mid-token.
func aiBodyTokenFields(token string) (whole bool, fields []string) {
	if strings.HasPrefix(token, "@") || strings.HasPrefix(token, "<") {
		return true, nil
	}
	if strings.HasPrefix(strings.TrimSpace(token), "{") {
		return false, jsonTopLevelFields([]byte(token))
	}
	if match := shorthandBodyFieldPattern.FindStringSubmatch(token); match != nil {
		return false, []string{match[1]}
	}
	return false, nil
}

// aiGhostText renders the unconsumed placeholders as the ghost string for
// the given cell budget: labels joined by spaces, a trailing "…" when the
// schema has optional fields the ghost is not listing, trimmed like the
// status line trims (aiTrimTo — under 4 cells nothing legible fits). Empty
// when nothing remains: silence is the "you can press Enter" signal.
func aiGhostText(remaining []aiPlaceholder, ellipsis bool, width int) string {
	if len(remaining) == 0 {
		return ""
	}
	labels := make([]string, 0, len(remaining)+1)
	for _, placeholder := range remaining {
		labels = append(labels, placeholder.label)
	}
	if ellipsis {
		labels = append(labels, "…")
	}
	return aiTrimTo(strings.Join(labels, " "), width)
}

// aiSpliceGhost composites the ghost into the rendered input row: the row is
// clamped to `keep` cells (prompt + typed text + the virtual cursor's cell —
// dropping the textarea's plain-space padding), the faint ghost fills what
// fits of the rest. The clamp is cell-based (lipgloss MaxWidth), so the
// cursor's ANSI styling survives whichever blink phase rendered it.
func aiSpliceGhost(row string, keep int, ghost string, width int) string {
	if ghost == "" || keep >= width {
		return row
	}
	trimmed := aiTrimTo(ghost, width-keep)
	if trimmed == "" {
		return row
	}
	return lipgloss.NewStyle().MaxWidth(keep).Render(row) + aiEchoStyle.Render(trimmed)
}
