// Chapter: natural-language command discovery. `dci commands --search "<what
// you want to do>"` ranks the command catalog against a plain-words query so
// an agent (or a human) can find the right command without loading the whole
// catalog — the ~270-entry JSON that `dci commands --json` prints — into
// context. The index is built on the fly from the same catalog entries the
// JSON exposes (names, curated keywords, summaries, curated examples,
// argument and flag text, Help Center notes, related commands), so it needs
// no model, no network beyond the spec fetch the catalog already does, and
// nothing to keep in sync: a new command or a curated doc is searchable the
// moment it exists. When a spec summary is too terse to be found ("Run a
// query"), the fix is a `keywords:` list in the command's doc file
// (command-docs/<command>.yaml), not a rule here.
//
// Ranking is deliberately simple and deterministic: weighted keyword match
// with a small synonym table and prefix matching standing in for stemming.
// Results carry the matched words so a caller can tell a strong hit from a
// coincidental one.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

const (
	commandSearchDefaultLimit = 10
	commandSearchMaxLimit     = 50
)

// commandSearchResult is one ranked hit. Command is the path as typed after
// `dci` ("list-budgets", "beta run-report-async"), so it pastes straight into
// the next invocation.
type commandSearchResult struct {
	Command     string   `json:"command"`
	Summary     string   `json:"summary,omitempty"`
	Score       float64  `json:"score"`
	Matched     []string `json:"matched"`
	Destructive bool     `json:"destructive"`
	Stage       string   `json:"stage,omitempty"`
	EarlyAccess string   `json:"early_access,omitempty"`
	Example     string   `json:"example,omitempty"`
	Related     []string `json:"related,omitempty"`
}

// commandSearchResponse is the JSON envelope for `--search`. Version is the
// catalog schema version: the results are a projection of catalog entries.
type commandSearchResponse struct {
	Version    string                `json:"version"`
	CLIVersion string                `json:"cli_version"`
	Query      string                `json:"query"`
	Results    []commandSearchResult `json:"results"`
	Hint       string                `json:"hint"`
}

const commandSearchNextStepHint = "Run `dci <command> --help` for arguments and flags, " +
	"or `dci commands --json` for the full catalog."

const commandSearchNoMatchHint = "No command matched. Try other words for the resource or action " +
	"(e.g. \"budget\", \"anomaly\", \"report\", \"create\", \"delete\"), " +
	"or browse the full catalog with `dci commands --json`."

// commandSearchFieldWeights ranks where a query word was found. A hit in the
// command name outranks one in a flag description by design: names are the
// resource-and-verb vocabulary the API itself uses.
var commandSearchFieldWeights = map[string]float64{
	"name":      6.0,
	"keywords":  6.0,
	"summary":   3.0,
	"examples":  1.5,
	"arguments": 0.75,
	"notes":     0.25,
	"related":   0.25,
}

// commandSearchPrefixScore is the credit a prefix match earns relative to an
// exact one ("anomal" against "anomaly", "report" against "reports").
const commandSearchPrefixScore = 0.6

// commandSearchSynonymScore scales a hit reached through the synonym table.
const commandSearchSynonymScore = 0.8

// commandSearchMinPrefixLength keeps short query words from matching every
// token that happens to start with them ("re" against "report", "remove").
const commandSearchMinPrefixLength = 4

// commandSearchStopwords are query words that carry no command meaning.
var commandSearchStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "of": true, "for": true,
	"in": true, "on": true, "my": true, "me": true, "i": true, "is": true,
	"are": true, "what": true, "which": true, "how": true, "do": true, "did": true,
	"does": true, "can": true, "want": true, "need": true, "all": true,
	"with": true, "and": true, "or": true, "by": true, "from": true,
	"at": true, "this": true, "that": true, "it": true, "its": true, "am": true,
	"dci": true, "command": true, "commands": true, "cli": true, "please": true,
	"should": true, "would": true, "could": true, "use": true, "using": true,
	"via": true, "into": true, "about": true, "some": true, "any": true,
	"be": true, "our": true, "your": true, "we": true, "you": true,
}

// commandSearchSynonyms maps a normalized query word to the catalog
// vocabulary it most often stands for. Values are already normalized.
var commandSearchSynonyms = map[string][]string{
	// Verbs: everyday English → the API's operation verbs.
	"spend": {"cost"}, "spending": {"cost"}, "cost": {"spend"}, "charge": {"cost", "invoice"},
	"bill": {"invoice", "cost"}, "billing": {"invoice", "cost"},
	"remove": {"delete"}, "destroy": {"delete"}, "drop": {"delete"},
	"show": {"get", "list"}, "view": {"get", "list"}, "fetch": {"get"}, "describe": {"get"},
	"detail": {"get"}, "read": {"get"}, "lookup": {"get", "search"},
	"find": {"list", "search"}, "browse": {"list"}, "enumerate": {"list"},
	"make": {"create"}, "add": {"create"}, "new": {"create"},
	"edit": {"update"}, "change": {"update"}, "modify": {"update"}, "rename": {"update"},
	"upload": {"ingest", "import"}, "download": {"export"}, "dump": {"export"},
	"explain": {"explanation", "explainer"}, "why": {"explanation", "explainer"},
	"recommend": {"recommendation"}, "suggestion": {"recommendation"},
	// Resources: common aliases → the catalog's nouns.
	"warning": {"alert"}, "notification": {"alert"}, "notify": {"alert"},
	"spike": {"anomaly"}, "unusual": {"anomaly"}, "unexpected": {"anomaly"},
	"forecast": {"budget"}, "breach": {"budget", "risk"}, "overspend": {"budget", "risk"},
	"tenant": {"customer"}, "org": {"customer", "organization"},
	"saving": {"recommendation", "optimization"}, "savings": {"recommendation", "optimization"},
	"optimize": {"recommendation", "optimization"}, "optimise": {"recommendation", "optimization"},
	"waste": {"recommendation", "optimization"}, "cheaper": {"recommendation", "optimization"},
	"workflow": {"cloudflow", "flow"}, "automation": {"cloudflow", "flow"},
	"signin": {"login"}, "authenticate": {"login", "validate"}, "auth": {"login", "validate"},
	"logged": {"login", "status", "validate"}, "signed": {"login", "status", "validate"},
	"authenticated": {"login", "status", "validate"}, "whoami": {"status", "validate"},
	"credential": {"login", "service", "account"}, "apikey": {"token", "service", "account"},
	"key": {"token"}, "secret": {"token"},
	"current": {"context", "status"}, "active": {"context", "status"}, "selected": {"context", "status"},
	"doc": {"docs"}, "documentation": {"docs"},
	"upgrade": {"update"}, "outdated": {"update"},
	"people": {"user"}, "member": {"user"}, "teammate": {"user"},
	"analytics": {"report", "query"}, "analysis": {"report", "query"}, "breakdown": {"report", "query"},
	"tag": {"label"}, "chargeback": {"allocation"}, "showback": {"allocation"},
	"commitment": {"commitment", "cud", "reserved"}, "reservation": {"reserved", "commitment"},
	"subscription": {"contract"},
	"region":       {"geographic", "region"}, "country": {"geographic", "country"}, "geo": {"geographic"},
	"permission": {"role", "permission"}, "rbac": {"role"},
	"google": {"gcp"}, "amazon": {"aws"}, "microsoft": {"azure"},
	"outage": {"incident"}, "downtime": {"incident"},
	"architecture": {"diagram"}, "topology": {"diagram"},
	"note":       {"annotation"},
	"background": {"async", "operation"}, "poll": {"async", "operation"},
	"directory": {"folder"}, "dashboard": {"widget", "report"},
	"console": {"open"}, "browser": {"open"},
	"agent": {"skill"}, "llm": {"skill"},
}

// registerCommandSearchFlags adds --search and --limit to the `commands`
// command (command_catalog.go owns the command itself).
func registerCommandSearchFlags(command *cobra.Command) {
	command.Flags().String("search", "", "Find commands for a task described in plain words (e.g. --search \"budgets about to overspend\"); prints the best matches instead of the full catalog")
	command.Flags().Int("limit", commandSearchDefaultLimit, "Maximum number of --search results (1-50)")
}

// commandSearchRequested reports whether this `commands` invocation is a
// search, and returns the query.
func commandSearchRequested(command *cobra.Command) (string, bool) {
	flag := command.Flags().Lookup("search")
	if flag == nil || !flag.Changed {
		return "", false
	}
	return flag.Value.String(), true
}

// runCommandSearch ranks entries against query and writes the result to out,
// as JSON (agent mode, or --json) or as a short text listing.
func runCommandSearch(command *cobra.Command, entries []commandCatalogEntry, query string, out io.Writer) error {
	limit, _ := command.Flags().GetInt("limit")
	if limit < 1 || limit > commandSearchMaxLimit {
		return fmt.Errorf("--limit must be between 1 and %d", commandSearchMaxLimit)
	}
	results, err := searchCommandCatalog(entries, query, limit)
	if err != nil {
		return err
	}
	asJSON, _ := command.Flags().GetBool("json")
	if asJSON || agentMode {
		response := commandSearchResponse{
			Version:    catalogSchemaVersion,
			CLIVersion: version,
			Query:      query,
			Results:    results,
			Hint:       commandSearchNextStepHint,
		}
		if len(results) == 0 {
			response.Hint = commandSearchNoMatchHint
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		// Examples carry "<report-id>" placeholders; the HTML-safe \u003c
		// form is noise for the agent reading them.
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	}
	renderCommandSearchText(out, query, results)
	return nil
}

// renderCommandSearchText prints results for a human: name, summary, and
// the first curated example when there is one.
func renderCommandSearchText(out io.Writer, query string, results []commandSearchResult) {
	if len(results) == 0 {
		fmt.Fprintf(out, "No commands match %q.\n%s\n", query, strings.ReplaceAll(commandSearchNoMatchHint, "No command matched. ", ""))
		return
	}
	width := 0
	for _, result := range results {
		if len(result.Command) > width {
			width = len(result.Command)
		}
	}
	for _, result := range results {
		summary := result.Summary
		if result.Destructive {
			summary += " (destructive)"
		}
		if result.Stage != "" {
			summary += " [" + result.Stage + "]"
		}
		fmt.Fprintf(out, "%-*s  %s\n", width, result.Command, strings.TrimSpace(summary))
		if result.Example != "" {
			fmt.Fprintf(out, "%-*s  e.g. %s\n", width, "", result.Example)
		}
	}
	fmt.Fprintf(out, "\n%s\n", commandSearchNextStepHint)
}

// searchCommandCatalog scores every entry against query and returns the top
// limit hits, best first. An empty or stopword-only query is an error.
func searchCommandCatalog(entries []commandCatalogEntry, query string, limit int) ([]commandSearchResult, error) {
	terms := commandSearchQueryTerms(query)
	if len(terms) == 0 {
		return nil, errors.New("--search needs at least one word describing the task (e.g. --search \"budgets at risk\")")
	}
	results := make([]commandSearchResult, 0, limit)
	for _, entry := range entries {
		score, matched := scoreCommandEntry(entry, terms)
		if score <= 0 {
			continue
		}
		result := commandSearchResult{
			Command:     strings.Join(entry.Path, " "),
			Summary:     entry.Summary,
			Score:       math.Round(score*100) / 100,
			Matched:     matched,
			Destructive: entry.Destructive,
			Stage:       entry.Stage,
			EarlyAccess: entry.EarlyAccess,
			Related:     entry.Related,
		}
		if len(entry.Examples) > 0 {
			result.Example = entry.Examples[0].Command
		}
		results = append(results, result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		// Shorter names first on a tie: `list-budgets` before
		// `list-budget-suggestions` when both match "budgets" equally.
		if len(results[i].Command) != len(results[j].Command) {
			return len(results[i].Command) < len(results[j].Command)
		}
		return results[i].Command < results[j].Command
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// commandSearchTerm is one normalized query word plus the synonyms it
// expands to.
type commandSearchTerm struct {
	word     string
	synonyms []string
}

// commandSearchQueryTerms normalizes a query into search terms: tokenized,
// lower-cased, stopwords dropped, lightly stemmed, deduplicated, with
// synonyms attached. A synonym that is itself another query word is dropped:
// "budgets about to overspend" must not let "overspend" re-score "budget" on
// every budget command — its distinctive expansion is "risk".
func commandSearchQueryTerms(query string) []commandSearchTerm {
	seen := map[string]bool{}
	terms := make([]commandSearchTerm, 0)
	for _, raw := range commandSearchTokenize(query) {
		if commandSearchStopwords[raw] {
			continue
		}
		word := commandSearchStem(raw)
		if word == "" || seen[word] {
			continue
		}
		seen[word] = true
		term := commandSearchTerm{word: word}
		for _, source := range []string{raw, word} {
			for _, synonym := range commandSearchSynonyms[source] {
				synonym = commandSearchStem(synonym)
				if synonym != word && !slices.Contains(term.synonyms, synonym) {
					term.synonyms = append(term.synonyms, synonym)
				}
			}
		}
		terms = append(terms, term)
	}
	for index := range terms {
		kept := terms[index].synonyms[:0]
		for _, synonym := range terms[index].synonyms {
			if !seen[synonym] {
				kept = append(kept, synonym)
			}
		}
		terms[index].synonyms = kept
	}
	return terms
}

// commandSearchTokenize splits text into lower-case word tokens on anything
// that is not a letter or digit, and additionally at camelCase boundaries
// so "riskStatus" and "atRisk" yield "risk", "status", "at".
func commandSearchTokenize(text string) []string {
	tokens := make([]string, 0, 16)
	var current strings.Builder
	flush := func() {
		if current.Len() >= 2 {
			tokens = append(tokens, strings.ToLower(current.String()))
		}
		current.Reset()
	}
	var previous rune
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && previous != 0 && unicode.IsLower(previous) {
				flush()
			}
			current.WriteRune(r)
		default:
			flush()
		}
		previous = r
	}
	flush()
	return tokens
}

// commandSearchStem trims the plural and gerund endings that separate a
// query word from the catalog's vocabulary ("budgets" → "budget",
// "anomalies" → "anomaly", "listing" → "list"). It is intentionally crude:
// prefix matching in scoreTokens covers what it misses.
func commandSearchStem(word string) string {
	word = strings.TrimSuffix(word, "'s")
	switch {
	case len(word) > 4 && strings.HasSuffix(word, "ies"):
		return word[:len(word)-3] + "y"
	case len(word) > 5 && strings.HasSuffix(word, "ing"):
		return word[:len(word)-3]
	case len(word) > 3 && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss"):
		return word[:len(word)-1]
	}
	return word
}

// commandSearchGenericFlags are present on every API command through the
// agent contract; indexing them would make every query about output or
// confirmation match every command equally.
var commandSearchGenericFlags = map[string]bool{
	"--dry-run": true, "--output": true, "--yes": true, "--fields": true, "--exclude": true,
	"--help": true, "--json": true,
}

// commandEntryFields assembles the weighted text fields of one entry, each
// already tokenized and stemmed.
func commandEntryFields(entry commandCatalogEntry) map[string][]string {
	fields := map[string][]string{}
	add := func(field, text string) {
		for _, token := range commandSearchTokenize(text) {
			fields[field] = append(fields[field], commandSearchStem(token))
		}
	}
	add("name", strings.Join(entry.Path, " "))
	add("keywords", strings.Join(entry.Keywords, " "))
	add("summary", entry.Summary)
	for _, example := range entry.Examples {
		add("examples", example.Description)
		add("examples", example.Command)
	}
	for _, argument := range entry.Arguments {
		add("arguments", argument.Name)
		add("arguments", argument.Description)
	}
	for _, flag := range entry.Flags {
		if commandSearchGenericFlags[flag.Name] {
			continue
		}
		add("arguments", flag.Name)
		add("arguments", flag.Description)
	}
	add("notes", entry.Notes)
	add("related", strings.Join(entry.Related, " "))
	return fields
}

// scoreCommandEntry returns the entry's relevance to terms and the query
// words that matched. Each term earns, per field, the field's weight scaled
// by its best match in that field (exact 1.0, prefix 0.6, synonym ×0.8);
// the sum is then scaled by how much of the query matched, so an entry that
// covers every word outranks one that nails a single word.
func scoreCommandEntry(entry commandCatalogEntry, terms []commandSearchTerm) (float64, []string) {
	fields := commandEntryFields(entry)
	total := 0.0
	matched := make([]string, 0, len(terms))
	for _, term := range terms {
		termScore := 0.0
		for field, tokens := range fields {
			best := scoreTokens(term.word, tokens)
			for _, synonym := range term.synonyms {
				if synonymScore := scoreTokens(synonym, tokens) * commandSearchSynonymScore; synonymScore > best {
					best = synonymScore
				}
			}
			termScore += commandSearchFieldWeights[field] * best
		}
		if termScore > 0 {
			matched = append(matched, term.word)
			total += termScore
		}
	}
	if total == 0 {
		return 0, nil
	}
	coverage := float64(len(matched)) / float64(len(terms))
	return total * (0.5 + coverage), matched
}

// scoreTokens is the best match of word against tokens: 1 for an exact
// token, commandSearchPrefixScore when one is a prefix of the other and the
// shorter side is long enough to mean something, 0 otherwise.
func scoreTokens(word string, tokens []string) float64 {
	best := 0.0
	for _, token := range tokens {
		switch {
		case token == word:
			return 1
		case len(word) >= commandSearchMinPrefixLength && strings.HasPrefix(token, word),
			len(token) >= commandSearchMinPrefixLength && strings.HasPrefix(word, token):
			if commandSearchPrefixScore > best {
				best = commandSearchPrefixScore
			}
		}
	}
	return best
}
