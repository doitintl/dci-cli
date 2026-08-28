package main

// Argument-placeholder overlays for the `dci ai` session's input
// (AI-PLACEHOLDER-SPEC, phases 1–2): the moment the input names a runnable
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
	"fmt"
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
	// pickable marks a resolvable path slot whose empty submission opens the
	// session's zero-argument name picker (aiPickerIntentFor: single path
	// parameter, no request body) — the ghost cues it so the picker stops
	// being a feature users only find by accident.
	pickable bool
}

// aiCommandSignature is the per-command placeholder model, derived from the
// live cobra tree (the argvUsageTrailer descent) plus the resolution
// metadata. Built per keystroke without memoization on purpose: one child
// walk and one schema-block parse are cheaper than the full-catalog scan
// aiCompletionsFor already runs on the same keystroke.
type aiCommandSignature struct {
	words        int    // how many argv words name the command ("beta run-report" = 2)
	name         string // the session-spelled command key ("get-report", "beta run-report")
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
		name:      strings.Join(argv[:matched], " "),
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
	target, resolvable := resolutionIndex[signature.name]
	signature.resolvable = resolvable && len(pathWords) == 1
	for index, word := range pathWords {
		placeholder := aiPlaceholder{label: word}
		if signature.resolvable && index == 0 {
			placeholder.label = singularResourceName(target.resource) + "-name-or-id"
			// The zero-argument picker only opens for body-less operations
			// (aiPickerIntentFor mirrors zeroArgPickerApplies), so only
			// those earn the cue.
			placeholder.pickable = !target.hasBody
		}
		signature.placeholders = append(signature.placeholders, placeholder)
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
	remaining, _ := aiConsumePlaceholders(signature, argv)
	return remaining
}

// aiConsumePlaceholders is the consumption proper; bodyStarted additionally
// reports whether any body-shaped token has been typed, which decides the
// separator a Tab field-prefix insertion needs (aiTabActionFor): shorthand
// properties are comma-separated, so mid-body the prefix arrives as
// ", field: " while the first one needs only a space.
func aiConsumePlaceholders(signature *aiCommandSignature, argv []string) (remaining []aiPlaceholder, bodyStarted bool) {
	if signature == nil {
		return nil, false
	}
	pathConsumed := 0
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
	return remaining, bodyStarted
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

// aiPickerCue is what the ghost appends when submitting the line as-is would
// open the name picker — the same vocabulary as the picker's own transcript
// line ("picked Dev Widget").
const aiPickerCue = "(enter to pick from a list)"

// aiPickerCueApplies reports whether the ghost should carry the picker cue:
// the one unconsumed slot is a pickable path slot AND the line as typed
// would actually open the zero-argument picker on submit. The intent check
// (aiPickerIntentFor) re-applies the picker's own gates — --id typed on the
// line, DCI_NO_RESOLVE — so the cue never promises a picker a gate would
// suppress; intent.input stays empty exactly when no name words are typed,
// which is the "enter lists everything" case the cue describes.
func aiPickerCueApplies(remaining []aiPlaceholder, argv []string) bool {
	if len(remaining) != 1 || !remaining[0].pickable {
		return false
	}
	intent := aiPickerIntentFor(argv)
	return intent != nil && intent.input == ""
}

// aiGhostText renders the unconsumed placeholders as the ghost string for
// the given cell budget: labels joined by spaces, the picker cue when
// applicable, a trailing "…" when the schema has optional fields the ghost
// is not listing, trimmed like the status line trims (aiTrimTo — under 4
// cells nothing legible fits; the cue sits at the tail, so a narrow pane
// drops it before any argument name). Empty when nothing remains: silence
// is the "you can press Enter" signal.
func aiGhostText(remaining []aiPlaceholder, ellipsis, pickerCue bool, width int) string {
	if len(remaining) == 0 {
		return ""
	}
	labels := make([]string, 0, len(remaining)+1)
	for _, placeholder := range remaining {
		labels = append(labels, placeholder.label)
	}
	if pickerCue {
		labels = append(labels, aiPickerCue)
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

// --- Tab in argument position (AI-PLACEHOLDER-SPEC P2) -----------------------

type aiTabActionKind int

const (
	aiTabNone   aiTabActionKind = iota
	aiTabInsert                 // insert text at the end of the line (a body-field prefix)
	aiTabPicker                 // submit the line as-is: it opens the zero-argument name picker
	aiTabHint                   // replace the ghost with a value hint; insert nothing
)

// aiTabAction is what Tab should do with the popup closed, decided per
// keypress from the same pure model the ghost renders from.
type aiTabAction struct {
	kind   aiTabActionKind
	insert string // aiTabInsert: the text, separator included
	hint   string // aiTabHint: the replacement ghost
}

// aiTabActionFor decides Tab's argument-position behavior (P2): accept what
// the ghost offers next. In order:
//
//   - the empty pickable slot → submit as-is, which opens the picker (Tab
//     and Enter agree there, so Tab always means "accept the offer");
//   - a path value slot → a hint, never an insertion: the cursor is already
//     where the value goes, and text the user didn't type must never become
//     submittable input (the value hint is the parameter's spec example or
//     type, from the same offline metadata the picker loads);
//   - the next required body field → insert its fixed "name: " prefix, with
//     the comma separator shorthand needs once a body property is already
//     on the line.
//
// Everything else — verbs, user-defined commands, unknown or fully-satisfied
// commands, unparseable lines — is aiTabNone, keeping Tab inert exactly as
// it was before P2.
func aiTabActionFor(input string, userCommands map[string]aiUserCommand) aiTabAction {
	// The raw input (trailing whitespace included) decides the insertion's
	// separator; only the parse works on the trimmed line.
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return aiTabAction{}
	}
	argv, err := splitCommandLine(strings.TrimPrefix(trimmed, "/"))
	if err != nil || len(argv) == 0 {
		return aiTabAction{}
	}
	if _, isVerb := aiLookupVerb(argv[0]); isVerb {
		return aiTabAction{}
	}
	if _, isUserCommand := userCommands[argv[0]]; isUserCommand {
		return aiTabAction{}
	}
	signature := aiPlaceholderSignatureFor(argv)
	if signature == nil {
		return aiTabAction{}
	}
	remaining, bodyStarted := aiConsumePlaceholders(signature, argv)
	if len(remaining) == 0 {
		return aiTabAction{}
	}
	if aiPickerCueApplies(remaining, argv) {
		return aiTabAction{kind: aiTabPicker}
	}
	next := remaining[0]
	if !next.body {
		if hint := aiPathValueHint(signature, remaining, next); hint != "" {
			return aiTabAction{kind: aiTabHint, hint: hint}
		}
		return aiTabAction{}
	}
	return aiTabAction{kind: aiTabInsert, insert: aiFieldPrefixInsertion(input, bodyStarted, next.name)}
}

// aiPathValueHint builds the value hint for the next path slot: the
// parameter's spec example when the metadata carries one, its type
// otherwise, from operationPathParameters — GA operations only (beta ops
// never populate the map, §5.3), keyed by the plain command name. "" means
// no metadata: Tab stays inert rather than hinting nothing.
func aiPathValueHint(signature *aiCommandSignature, remaining []aiPlaceholder, next aiPlaceholder) string {
	parameters := operationPathParameters[signature.name]
	// The next slot's parameter index: how many path slots are already
	// consumed = declared minus still-remaining.
	pathRemaining := 0
	for _, placeholder := range remaining {
		if !placeholder.body {
			pathRemaining++
		}
	}
	index := signature.pathCount - pathRemaining
	if index < 0 || index >= len(parameters) {
		return ""
	}
	parameter := parameters[index]
	if parameter.Example != nil {
		return fmt.Sprintf("%s — e.g. %v", next.label, parameter.Example)
	}
	if parameter.Type != "" {
		return next.label + " (" + parameter.Type + ")"
	}
	return ""
}

// aiFieldPrefixInsertion spells the "name: " token Tab inserts for the next
// required body field, separator included: a space after the path arguments,
// a comma once a body property is already on the line (restish shorthand
// separates properties with commas), and just a space when the line already
// ends with one.
func aiFieldPrefixInsertion(input string, bodyStarted bool, field string) string {
	prefix := field + ": "
	trimmed := strings.TrimRight(input, " \t")
	switch {
	case bodyStarted && !strings.HasSuffix(trimmed, ","):
		return ", " + prefix
	case len(trimmed) == len(input):
		return " " + prefix
	default:
		return prefix
	}
}
