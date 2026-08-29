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
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/pflag"
)

// aiPlaceholder is one unconsumed argument slot in a command's signature.
type aiPlaceholder struct {
	label string // "report-id", "tags*: [a, b]", "widget-name-or-id"
	body  bool   // body field (vs path parameter)
	name  string // body field name, marker stripped; "" for path slots
	array bool   // body field whose schema value is an array
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
	optionalBody bool              // schema has optional top-level fields → the ghost gains "…"
	resolvable   bool              // single path slot accepts names (resolutionIndex)
	flags        *pflag.FlagSet    // the leaf's own flag set, for value-flag skipping
	fields       []bodyFieldSketch // every top-level body field, optional ones included (value ghost lookups)
	example      string            // the first spec example, session-spelled (body value hints)
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
	signature.fields = requestSchemaTopLevelFieldSketches(command.Long)
	signature.example = firstUsageExample(command.Example, true)
	for _, field := range signature.fields {
		signature.hasBody = true
		if !field.required {
			signature.optionalBody = true
			continue
		}
		label := field.name + "*"
		if sketch := aiSketchLabel(field); sketch != "" {
			label += ": " + sketch
		}
		signature.placeholders = append(signature.placeholders, aiPlaceholder{
			label: label, body: true, name: field.name, array: sketchIsArray(field.sketch),
		})
	}
	return signature
}

// aiSketchLabel condenses a schema field's value sketch for the one-row
// ghost, shaped like the input to type rather than type notation — dogfood
// (2026-08-29): "[string]" read as an annotation, so the brackets were not
// understood as literal syntax. Arrays render example items ("[a, b]"),
// scalars their bare type word, nested objects the one word "object".
func aiSketchLabel(field bodyFieldSketch) string {
	switch {
	case field.sketch == "":
		return ""
	case sketchIsArray(field.sketch):
		if items := arrayItemsExample(field.elem); items != "" {
			return "[" + items + "]"
		}
		return "[…]"
	case sketchIsObject(field.sketch):
		return "object"
	}
	if word := schemaTypeWord(field.sketch); word != "" {
		return word
	}
	return field.sketch
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

// --- Body value entry (the tail of the line is inside a field's value) --------

// aiBodyValueState says the line's tail sits inside a body field's value —
// exactly where the ghost used to go silent the moment the field *name*
// appeared, before any value existed (dogfood, 2026-08-29: with "tags: " on
// the line, "silence = Enter is valid" was simply false for an array field).
type aiBodyValueState struct {
	field  string // top-level body field owning the value being entered
	opener byte   // '[' or '{' when inside an unclosed bracket; 0 at a bare "name:" prefix
}

// aiBareFieldPrefixPattern matches a lone "name:" token — a field prefix
// with no value yet. Top-level names only: a dotted path's leaf type is not
// in the top-level sketches, so it gets no value ghost.
var aiBareFieldPrefixPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*:$`)

// aiBodyValueStateFor walks the typed body tokens and reports whether the
// line's tail is entering a field's value: inside an unclosed array or
// object bracket, or right after a bare "name:" prefix. Pure and positional
// like the consumption walk; JSON and whole-body styles (@file, <file, a
// leading "{") are never in a value-entry state — they are not shorthand
// composing.
func aiBodyValueStateFor(signature *aiCommandSignature, argv []string) (aiBodyValueState, bool) {
	if signature == nil || !signature.hasBody {
		return aiBodyValueState{}, false
	}
	var openers []byte
	field := ""
	pathSeen := 0
	lastIndex := -1
	for _, index := range aiPositionalIndexes(argv, signature.flags, signature.words) {
		token := argv[index]
		if pathSeen < signature.pathCount {
			pathSeen++
			continue
		}
		if strings.HasPrefix(token, "@") || strings.HasPrefix(token, "<") ||
			(field == "" && len(openers) == 0 && strings.HasPrefix(strings.TrimSpace(token), "{")) {
			return aiBodyValueState{}, false
		}
		if len(openers) == 0 {
			if match := shorthandBodyFieldPattern.FindStringSubmatch(token); match != nil {
				field, _, _ = strings.Cut(match[1], ".")
			}
		}
		for position := 0; position < len(token); position++ {
			switch token[position] {
			case '[', '{':
				openers = append(openers, token[position])
			case ']', '}':
				if len(openers) > 0 {
					openers = openers[:len(openers)-1]
				}
			}
		}
		lastIndex = index
	}
	if field == "" {
		return aiBodyValueState{}, false
	}
	if len(openers) > 0 {
		return aiBodyValueState{field: field, opener: openers[len(openers)-1]}, true
	}
	// A bare "name:" prefix counts only as the very tail of the line — a
	// flag typed after it means the user has moved on.
	if lastIndex != len(argv)-1 || !aiBareFieldPrefixPattern.MatchString(argv[lastIndex]) {
		return aiBodyValueState{}, false
	}
	return aiBodyValueState{field: strings.TrimSuffix(argv[lastIndex], ":")}, true
}

// aiValueGhost renders the value-syntax ghost while the line's tail is
// entering a body field's value: the field's literal syntax template right
// after its bare prefix ("[a, b]" for an array of strings), and the closing
// guidance inside an unclosed bracket (", …]"). "" outside value entry —
// then silence means "you can press Enter" again, which the old behavior
// broke for array fields. The raw input decides spacing; the parse works on
// argv.
func aiValueGhost(signature *aiCommandSignature, argv []string, input string) string {
	state, active := aiBodyValueStateFor(signature, argv)
	if !active {
		return ""
	}
	var sketch bodyFieldSketch
	for _, field := range signature.fields {
		if field.name == state.field {
			sketch = field
			break
		}
	}
	trimmed := strings.TrimRight(input, " \t")
	trailingSpace := trimmed != input
	if state.opener != 0 {
		closer := "]"
		items := arrayItemsExample(sketch.elem)
		if state.opener == '{' {
			closer = "}"
			items = ""
		}
		if items == "" {
			items = "…"
		}
		switch {
		case strings.HasSuffix(trimmed, string(state.opener)) && !trailingSpace:
			return items + closer
		case trailingSpace:
			return "…" + closer
		case strings.HasSuffix(trimmed, ","):
			return " …" + closer
		default:
			return ", …" + closer
		}
	}
	core := ""
	switch {
	case sketch.name == "":
		return ""
	case sketchIsArray(sketch.sketch):
		items := arrayItemsExample(sketch.elem)
		if items == "" {
			items = "…"
		}
		core = "[" + items + "]"
	case sketchIsObject(sketch.sketch):
		core = "{…}"
	default:
		core = aiSketchLabel(sketch)
	}
	if core == "" {
		return ""
	}
	if trailingSpace {
		return core
	}
	return " " + core
}

// aiFieldExampleExcerpt pulls one field's assignment out of a spec example
// line ("/add-ticket-tags 318240 tags: [prod]" → "tags: [prod]"), cut at the
// top-level comma that starts the next property. "" when the example never
// assigns the field.
func aiFieldExampleExcerpt(example, field string) string {
	if example == "" || field == "" {
		return ""
	}
	marker := field + ":"
	start := -1
	for from := 0; from <= len(example)-len(marker); {
		found := strings.Index(example[from:], marker)
		if found < 0 {
			break
		}
		position := from + found
		if position == 0 || example[position-1] == ' ' || example[position-1] == ',' {
			start = position
			break
		}
		from = position + len(marker)
	}
	if start < 0 {
		return ""
	}
	depth := 0
	end := len(example)
scan:
	for index := start; index < len(example); index++ {
		switch example[index] {
		case '[', '{':
			depth++
		case ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 && shorthandBodyFieldPattern.MatchString(strings.TrimSpace(example[index+1:])) {
				end = index
				break scan
			}
		}
	}
	return strings.TrimSpace(example[start:end])
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
	// Mid-value, Tab must not insert the next field's prefix into the value.
	// It hints with the spec example's assignment for this field when there
	// is one — the same offer path value slots make — and otherwise stays
	// inert: the value ghost is already showing the syntax template.
	if state, entering := aiBodyValueStateFor(signature, argv); entering {
		if excerpt := aiFieldExampleExcerpt(signature.example, state.field); excerpt != "" {
			return aiTabAction{kind: aiTabHint, hint: "e.g. " + excerpt}
		}
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
	return aiTabAction{kind: aiTabInsert, insert: aiFieldPrefixInsertion(input, bodyStarted, next)}
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

// aiFieldPrefixInsertion spells the token Tab inserts for the next required
// body field, separator included: a space after the path arguments, a comma
// once a body property is already on the line (restish shorthand separates
// properties with commas), and just a space when the line already ends with
// one. An array field's prefix carries its opening bracket ("tags: [") — the
// user physically cannot forget the bracket they never had to type (dogfood,
// 2026-08-29); the value ghost then shows the items and the closing "]".
func aiFieldPrefixInsertion(input string, bodyStarted bool, next aiPlaceholder) string {
	prefix := next.name + ": "
	if next.array {
		prefix = next.name + ": ["
	}
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
