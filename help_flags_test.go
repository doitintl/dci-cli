package main

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newHelpFlagTestTree builds the offline command tree help renders against:
// the local root commands, the hidden dci API command carrying the
// persistent flags, the question commands, and stand-ins for GA operations
// of every shape the visibility predicates key on.
func newHelpFlagTestTree(t *testing.T) (root, dciCmd *cobra.Command) {
	t.Helper()
	root = newLocalCommandTree(t)
	dciCmd = findDCICommand()
	add := func(use, group string, queryFlags ...string) {
		command := &cobra.Command{Use: use, GroupID: group, Short: use, Run: func(*cobra.Command, []string) {}}
		for _, flag := range queryFlags {
			command.Flags().String(flag, "", flag)
		}
		if group != "" && !dciCmd.ContainsGroup(group) {
			dciCmd.AddGroup(&cobra.Group{ID: group, Title: group + " Commands:"})
		}
		dciCmd.AddCommand(command)
	}
	add("invite-user", "Users")
	add("query", "Reports")
	add("get-budget id", "Budgets")
	add("list-budgets", "Budgets", "page-token", "max-results")
	add("list-anomalies", "Anomalies", "page-token", "max-results")
	add("list-roles", "Roles") // returns its whole collection in one response
	add("list-insights", "Insights", "page-token")
	add("export-datahub-dataset-records name", "DataHub", "page-token", "start-time", "end-time")
	customizeDCIUsage()

	previousIndex := resolutionIndex
	resolutionIndex = map[string]resolutionListTarget{}
	previousFull := helpFullRequested
	helpFullRequested = false
	t.Cleanup(func() {
		resolutionIndex = previousIndex
		helpFullRequested = previousFull
	})
	return root, dciCmd
}

func findAPICommand(t *testing.T, dciCmd *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, command := range dciCmd.Commands() {
		if command.Name() == name {
			return command
		}
	}
	t.Fatalf("command %q not registered under dci", name)
	return nil
}

// Every persistent flag addOutputFlag registers must be classified, and
// every classification must name a real flag: a new flag gets a deliberate
// scope instead of silently landing inline under every command.
func TestHelpFlagScopesCoverEveryPersistentFlag(t *testing.T) {
	_, dciCmd := newHelpFlagTestTree(t)
	registered := map[string]bool{}
	dciCmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		registered[flag.Name] = true
		if _, ok := helpFlagScopes[flag.Name]; !ok {
			t.Errorf("persistent flag --%s has no help scope in helpFlagScopes", flag.Name)
		}
	})
	for name := range helpFlagScopes {
		if !registered[name] {
			t.Errorf("helpFlagScopes names --%s, which addOutputFlag does not register", name)
		}
	}
}

func TestHelpFlagPlacementFollowsCommandShape(t *testing.T) {
	_, dciCmd := newHelpFlagTestTree(t)
	cases := []struct {
		command   string
		inline    []string
		collapsed []string
		hidden    []string
	}{
		{
			command:   "invite-user",
			inline:    []string{"output", "fields", "exclude", "full", "dry-run", "yes", "customer-context"},
			collapsed: []string{"table-mode", "table-columns", "table-width", "table-max-col-width", "no-truncate", "raw-numbers", "utc", "output-order", "output-file"},
			hidden:    []string{"chart", "pivot", "flat", "rollup", "max-rows", "rows", "include-empty-rows", "drop-unlabeled-rows", "heatmap", "for-reimport", "all", "search", "include-dismissed", "id", "name"},
		},
		{
			command: "query",
			inline:  []string{"chart", "pivot", "flat", "rollup", "max-rows", "rows", "include-empty-rows", "drop-unlabeled-rows", "heatmap", "output"},
			hidden:  []string{"all", "search", "for-reimport", "include-dismissed", "id", "name"},
		},
		{
			command: "list-budgets",
			inline:  []string{"all", "search"},
			hidden:  []string{"chart", "max-rows", "for-reimport", "include-dismissed", "id", "name"},
		},
		{
			command: "list-roles",
			inline:  []string{"search"},
			hidden:  []string{"all", "chart"},
		},
		{
			command: "list-insights",
			inline:  []string{"include-dismissed", "all", "search"},
			hidden:  []string{"chart"},
		},
		{
			command: "export-datahub-dataset-records",
			inline:  []string{"all", "for-reimport", "output-file", "id", "name"},
			hidden:  []string{"search", "chart", "max-rows"},
		},
		{
			command:   "get-budget",
			inline:    []string{"id", "name"},
			collapsed: []string{"output-file"},
			hidden:    []string{"all", "search", "chart"},
		},
		{
			command: "budgets-at-risk",
			inline:  []string{"all", "search", "output"},
			hidden:  []string{"chart", "for-reimport", "id"},
		},
		{
			command: "anomalies-recent",
			inline:  []string{"all", "search"},
			hidden:  []string{"chart", "max-rows"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.command, func(t *testing.T) {
			command := findAPICommand(t, dciCmd, testCase.command)
			check := func(names []string, want helpFlagPlacement) {
				for _, name := range names {
					if got := helpFlagPlacementFor(name, command); got != want {
						t.Errorf("--%s on %s: placement %d, want %d", name, testCase.command, got, want)
					}
				}
			}
			check(testCase.inline, helpFlagInline)
			check(testCase.collapsed, helpFlagCollapsed)
			check(testCase.hidden, helpFlagHidden)
		})
	}
}

// The resolution index, once loaded, is authoritative over the usage-line
// heuristic: a command with a positional argument the resolver skips (a
// numeric id) hides --id/--name, and one it covers shows them.
func TestHelpResolutionFlagsFollowTheIndexWhenLoaded(t *testing.T) {
	_, dciCmd := newHelpFlagTestTree(t)
	getBudget := findAPICommand(t, dciCmd, "get-budget")
	if !helpCommandResolvesNames(getBudget) {
		t.Fatal("get-budget id: positional argument not recognized without an index")
	}
	resolutionIndex = map[string]resolutionListTarget{"list-anomalies": {}}
	if helpCommandResolvesNames(getBudget) {
		t.Fatal("get-budget: the loaded index does not cover it, yet --id/--name apply")
	}
	resolutionIndex["get-budget"] = resolutionListTarget{}
	if !helpCommandResolvesNames(getBudget) {
		t.Fatal("get-budget: covered by the loaded index, yet --id/--name hidden")
	}
}

// Hiding is scoped to one render: the flags are shared with every other
// command and with `dci commands --json`, which must keep listing them.
func TestApplyHelpFlagVisibilityRestoresSharedFlags(t *testing.T) {
	_, dciCmd := newHelpFlagTestTree(t)
	before := catalogFlagsFromFlagSet(dciCmd.PersistentFlags())
	command := findAPICommand(t, dciCmd, "invite-user")

	restore := applyHelpFlagVisibility(command)
	if !dciCmd.PersistentFlags().Lookup("chart").Hidden || !dciCmd.PersistentFlags().Lookup("table-mode").Hidden {
		t.Fatal("--chart/--table-mode stayed visible on invite-user help")
	}
	if dciCmd.PersistentFlags().Lookup("output").Hidden {
		t.Fatal("--output was hidden on invite-user help")
	}
	restore()

	dciCmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			t.Errorf("--%s still hidden after the help render", flag.Name)
		}
	})
	after := catalogFlagsFromFlagSet(dciCmd.PersistentFlags())
	if len(after) != len(before) {
		t.Fatalf("catalog flags after help = %d, want %d", len(after), len(before))
	}
}

func TestHelpFullListsEveryFlag(t *testing.T) {
	_, dciCmd := newHelpFlagTestTree(t)
	command := findAPICommand(t, dciCmd, "invite-user")
	helpFullRequested = true
	restore := applyHelpFlagVisibility(command)
	defer restore()
	dciCmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			t.Errorf("--help-full hid --%s", flag.Name)
		}
	})
	if note := helpCollapsedFlagsNote(command); note != "" {
		t.Fatalf("--help-full rendered the folded-flags note: %q", note)
	}
}

func TestHelpCollapsedFlagsNote(t *testing.T) {
	root, dciCmd := newHelpFlagTestTree(t)

	note := helpCollapsedFlagsNote(findAPICommand(t, dciCmd, "invite-user"))
	for _, expected := range []string{"Output flags (apply to every command; add --help-full to list them):", "-M/--table-mode", "-C/--table-columns", "-O/--output-file", "--utc", "--output-order"} {
		if !strings.Contains(note, expected) {
			t.Errorf("invite-user note missing %q:\n%s", expected, note)
		}
	}
	for _, line := range strings.Split(note, "\n")[1:] {
		if !strings.HasPrefix(line, "  ") || len(line) > 100 {
			t.Errorf("note line not indented or too wide: %q", line)
		}
	}

	if note := helpCollapsedFlagsNote(findAPICommand(t, dciCmd, "export-datahub-dataset-records")); strings.Contains(note, "output-file") {
		t.Fatalf("--output-file folded on a file export, where it is inline:\n%s", note)
	}

	status, _, err := root.Find([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if note := helpCollapsedFlagsNote(status); note != "" {
		t.Fatalf("local command status rendered the API note: %q", note)
	}
	if note := helpCollapsedFlagsNote(dciCmd); note != "" {
		t.Fatalf("the dci command itself rendered the note: %q", note)
	}
}

func TestWrapHelpItems(t *testing.T) {
	got := wrapHelpItems([]string{"--aaaa", "--bbbb", "--cccc", "--dddd"}, "  ", 20)
	want := "  --aaaa, --bbbb\n  --cccc, --dddd"
	if got != want {
		t.Fatalf("wrapHelpItems = %q, want %q", got, want)
	}
	if got := wrapHelpItems(nil, "  ", 20); got != "" {
		t.Fatalf("wrapHelpItems(nil) = %q, want empty", got)
	}
}

// Rendered help, end to end through the usage template: the command's own
// flags and the agent contract are listed, the inert presentation flags are
// not, and the pointer line stands in for the folded ones.
func TestRenderedAPICommandHelpShowsOnlyApplicableFlags(t *testing.T) {
	_, dciCmd := newHelpFlagTestTree(t)
	render := func(name string) string {
		command := findAPICommand(t, dciCmd, name)
		restore := applyHelpFlagVisibility(command)
		defer restore()
		var output bytes.Buffer
		command.SetOut(&output)
		if err := command.Help(); err != nil {
			t.Fatalf("help %s: %v", name, err)
		}
		return output.String()
	}

	inviteUser := render("invite-user")
	for _, flag := range []string{"output", "fields", "exclude", "dry-run", "yes", "customer-context"} {
		if !helpListsFlag(inviteUser, flag) {
			t.Errorf("invite-user help does not list --%s:\n%s", flag, inviteUser)
		}
	}
	for _, flag := range []string{"chart", "pivot", "rollup", "max-rows", "for-reimport", "all", "search", "include-dismissed", "table-mode", "output-file", "utc"} {
		if helpListsFlag(inviteUser, flag) {
			t.Errorf("invite-user help lists --%s, which cannot act on it:\n%s", flag, inviteUser)
		}
	}
	if !strings.Contains(inviteUser, "Output flags (apply to every command; add --help-full to list them):") {
		t.Fatalf("invite-user help lacks the folded-flags pointer:\n%s", inviteUser)
	}

	query := render("query")
	for _, flag := range []string{"chart", "pivot", "max-rows", "rollup"} {
		if !helpListsFlag(query, flag) {
			t.Errorf("query help does not list --%s:\n%s", flag, query)
		}
	}
	if helpListsFlag(query, "all") || helpListsFlag(query, "search") {
		t.Fatalf("query help lists the paging flags:\n%s", query)
	}
}

// helpListsFlag reports whether help output has a Flags-block entry for
// --name: a line of its own, optionally with a shorthand. Mentions inside
// another flag's usage text or the folded-flags pointer line do not count.
func helpListsFlag(output, name string) bool {
	return regexp.MustCompile(`(?m)^\s+(-[A-Za-z], )?--` + regexp.QuoteMeta(name) + `(\s|$)`).MatchString(output)
}

func TestAdoptQuestionCommandGroups(t *testing.T) {
	_, dciCmd := newHelpFlagTestTree(t)
	for _, name := range []string{"budgets-at-risk", "anomalies-recent"} {
		if group := findAPICommand(t, dciCmd, name).GroupID; group != "" {
			t.Fatalf("%s grouped (%q) before adoption", name, group)
		}
	}
	adoptQuestionCommandGroups(dciCmd)
	if got := findAPICommand(t, dciCmd, "budgets-at-risk").GroupID; got != "Budgets" {
		t.Fatalf("budgets-at-risk group = %q, want Budgets", got)
	}
	if got := findAPICommand(t, dciCmd, "anomalies-recent").GroupID; got != "Anomalies" {
		t.Fatalf("anomalies-recent group = %q, want Anomalies", got)
	}

	// A wrapped operation missing from the loaded spec leaves its question
	// command ungrouped rather than pointing at a group cobra would reject.
	parent := &cobra.Command{Use: "dci"}
	parent.AddGroup(&cobra.Group{ID: "Budgets", Title: "Budgets Commands:"})
	parent.AddCommand(&cobra.Command{Use: "list-budgets", GroupID: "Budgets", Run: func(*cobra.Command, []string) {}})
	parent.AddCommand(newBudgetsAtRiskCommand())
	parent.AddCommand(newAnomaliesRecentCommand())
	adoptQuestionCommandGroups(parent)
	if got := findAPICommand(t, parent, "budgets-at-risk").GroupID; got != "Budgets" {
		t.Fatalf("budgets-at-risk group = %q, want Budgets", got)
	}
	if got := findAPICommand(t, parent, "anomalies-recent").GroupID; got != "" {
		t.Fatalf("anomalies-recent grouped as %q with list-anomalies absent", got)
	}
	adoptQuestionCommandGroups(nil) // tolerated
}

func TestOrderedGroupCommandsPlacesQuestionAfterWrappedOperation(t *testing.T) {
	command := func(name, group string) *cobra.Command {
		return &cobra.Command{Use: name, GroupID: group, Run: func(*cobra.Command, []string) {}}
	}
	names := func(commands []*cobra.Command) []string {
		result := make([]string, len(commands))
		for index, command := range commands {
			result[index] = command.Name()
		}
		return result
	}
	// cobra hands the template its commands sorted by name.
	commands := []*cobra.Command{
		command("anomalies-recent", "Anomalies"),
		command("budgets-at-risk", "Budgets"),
		command("get-anomaly", "Anomalies"),
		command("list-anomalies", "Anomalies"),
		command("list-budgets", "Budgets"),
		command("patch-anomaly", "Anomalies"),
	}
	hidden := command("hidden-anomaly", "Anomalies")
	hidden.Hidden = true
	commands = append(commands, hidden)
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name() < commands[j].Name() })

	got := names(orderedGroupCommands(commands, "Anomalies"))
	want := []string{"get-anomaly", "list-anomalies", "anomalies-recent", "patch-anomaly"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Anomalies order = %v, want %v", got, want)
	}
	if got := names(orderedGroupCommands(commands, "Budgets")); strings.Join(got, ",") != "list-budgets,budgets-at-risk" {
		t.Fatalf("Budgets order = %v", got)
	}

	// Wrapped operation absent from the group: the question command keeps
	// its alphabetical slot instead of vanishing.
	without := []*cobra.Command{command("anomalies-recent", "Anomalies"), command("get-anomaly", "Anomalies")}
	if got := names(orderedGroupCommands(without, "Anomalies")); strings.Join(got, ",") != "anomalies-recent,get-anomaly" {
		t.Fatalf("order without list-anomalies = %v", got)
	}
	if got := orderedGroupCommands(commands, "Nothing"); len(got) != 0 {
		t.Fatalf("empty group returned %v", names(got))
	}
}

// The report and file-export sets help decides by must match the shapes
// the live spec declares: an operation that gains `result.rows` (or a
// CSV/NDJSON body) must be added here, or its help hides the flags that
// act on it. Spec-gated like the command docs checks (DCI_COMMAND_DOCS_SPEC;
// CI fetches the production description).
func TestHelpFlagShapeSetsMatchSpec(t *testing.T) {
	spec := loadCommandDocsSpec(t)
	reportShaped := map[string]bool{}
	fileShaped := map[string]bool{}
	for _, operation := range spec.api.Operations {
		raw := spec.rawOperation(operation)
		if raw == nil {
			continue
		}
		responses, _ := raw["responses"].(map[string]any)
		for status, response := range responses {
			if status != "200" && status != "201" {
				continue
			}
			content, _ := spec.resolve(response)["content"].(map[string]any)
			for mediaType, body := range content {
				if rawPassthroughContentType(mediaType) {
					fileShaped[operation.Name] = true
				}
				bodyMap, _ := body.(map[string]any)
				if specSchemaCarriesReportRows(spec, bodyMap["schema"], 0) {
					reportShaped[operation.Name] = true
				}
			}
		}
	}
	if len(reportShaped) == 0 {
		t.Fatal("no report-shaped operation found in the spec; the walker is wrong")
	}
	assertSameNameSet(t, "report-shaped operations (reportResultOperations)", reportShaped, reportResultOperations)
	exports := map[string]bool{}
	for name := range fileExportOperations {
		exports[name] = true
	}
	assertSameNameSet(t, "file-shaped exports (fileExportOperations)", fileShaped, exports)
}

// specSchemaCarriesReportRows mirrors nestedReportRows: a `result` or
// `results` object with a `rows` property.
func specSchemaCarriesReportRows(spec commandDocsSpec, node any, depth int) bool {
	schema := spec.resolve(node)
	if schema == nil || depth > 8 {
		return false
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, key := range []string{"result", "results"} {
		container := spec.resolve(properties[key])
		if container == nil {
			continue
		}
		if innerProperties, ok := container["properties"].(map[string]any); ok {
			if _, hasRows := innerProperties["rows"]; hasRows {
				return true
			}
		}
	}
	for _, combinator := range []string{"allOf", "oneOf", "anyOf"} {
		members, _ := schema[combinator].([]any)
		for _, member := range members {
			if specSchemaCarriesReportRows(spec, member, depth+1) {
				return true
			}
		}
	}
	return false
}

func assertSameNameSet(t *testing.T, what string, fromSpec, inCode map[string]bool) {
	t.Helper()
	for name := range fromSpec {
		if !inCode[name] {
			t.Errorf("%s: the spec declares %s but the CLI set omits it", what, name)
		}
	}
	for name := range inCode {
		if !fromSpec[name] {
			t.Errorf("%s: the CLI set names %s but the spec does not declare that shape", what, name)
		}
	}
}
