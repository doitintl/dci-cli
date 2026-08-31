// Question-shaped commands (CMP-50046): stable, agent-friendly entry points
// for the two workflows the underlying API already answers in one call —
// at-risk budgets (CMP-48954) and recently-ranked anomalies (CMP-48957) —
// so an agent does not need to know the list-budgets/list-anomalies filter
// and sort syntax to ask "what's at risk" or "what's recent". Each command
// fixes the query parameters that answer the question and otherwise behaves
// like the underlying list operation: same pagination, same output flags
// (--output/--fields/--exclude are persistent on the dci subcommand), same
// error contract.
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
)

// questionCommandWrappedOperation maps each hand-registered question command
// to the GA-spec operation it wraps. Every response-presentation feature
// keyed on invokedCommandName (list_views.go's curated views, charts.go's
// utilization-bar column, response_transform.go's UTC-label columns for
// anomaly usage windows) is a property of the wrapped operation, not of the
// command name the caller typed — main.go's PersistentPreRunE resolves
// through this map so those features apply unchanged instead of silently
// no-op'ing for budgets-at-risk/anomalies-recent.
var questionCommandWrappedOperation = map[string]string{
	"budgets-at-risk":  "list-budgets",
	"anomalies-recent": "list-anomalies",
}

// registerQuestionCommands mounts budgets-at-risk and anomalies-recent under
// the hidden dci API command, alongside the generated list-budgets/
// list-anomalies commands, so they inherit auth, customer context, and every
// persistent output flag.
func registerQuestionCommands() {
	dciCommand := findDCICommand()
	if dciCommand == nil {
		return
	}
	dciCommand.AddCommand(newBudgetsAtRiskCommand())
	dciCommand.AddCommand(newAnomaliesRecentCommand())
}

func newBudgetsAtRiskCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "budgets-at-risk",
		Short: "List budgets projected to breach their configured amount before the current period ends",
		Long: "Equivalent to `list-budgets --filter riskStatus:atRisk`: budgets already over their\n" +
			"configured amount, or forecast to breach it before the current period ends, sorted by\n" +
			"earliest projected breach date. The response's riskAggregations block covers the full\n" +
			"filtered result set (all pages), not just the returned page.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			operation, err := findGAOperation("list-budgets")
			if err != nil {
				return err
			}
			return issueQuestionRequest(*operation, budgetsAtRiskQuery(command))
		},
	}
	command.Flags().Int64("max-results", 0, "Maximum number of results to return in a single page (server cap: 250)")
	command.Flags().String("page-token", "", "Page token, returned by a previous call, to request the next page of results")
	return command
}

func newAnomaliesRecentCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "anomalies-recent",
		Short: "List the most recent anomalies, ranked, with severity and cost aggregates",
		Long: "Equivalent to `list-anomalies --sort-by startTime --sort-order desc` bounded to a recent\n" +
			"time window: newest first. The response's anomalySummary and totalCount cover the full\n" +
			"filtered result set (all pages), not just the returned page.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			operation, err := findGAOperation("list-anomalies")
			if err != nil {
				return err
			}
			query, err := anomaliesRecentQuery(command)
			if err != nil {
				return err
			}
			return issueQuestionRequest(*operation, query)
		},
	}
	command.Flags().String("window", "24h", "How far back to look, as a Go duration (e.g. 24h, 168h for 7d)")
	command.Flags().String("severity", "", "Filter to one severity level: information, warning, or critical")
	command.Flags().Int64("max-results", 0, "Maximum number of results to return in a single page")
	command.Flags().String("page-token", "", "Page token, returned by a previous call, to request the next page of results")
	return command
}

// budgetsAtRiskQuery builds the fixed list-budgets query for budgets-at-risk:
// the riskStatus:atRisk filter plus any paging flags the caller set.
func budgetsAtRiskQuery(command *cobra.Command) url.Values {
	query := url.Values{"filter": {"riskStatus:atRisk"}}
	if maxResults, _ := command.Flags().GetInt64("max-results"); maxResults > 0 {
		query.Set("maxResults", strconv.FormatInt(maxResults, 10))
	}
	if pageToken, _ := command.Flags().GetString("page-token"); pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	return query
}

// anomaliesRecentQuery builds the fixed list-anomalies query for
// anomalies-recent: newest-first sort, the --window time bound, and any
// optional severity filter or paging flags the caller set.
func anomaliesRecentQuery(command *cobra.Command) (url.Values, error) {
	window, _ := command.Flags().GetString("window")
	minCreationTime, err := recentWindowStart(window)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"sortBy":          {"startTime"},
		"sortOrder":       {"desc"},
		"minCreationTime": {strconv.FormatInt(minCreationTime, 10)},
	}
	if severity, _ := command.Flags().GetString("severity"); severity != "" {
		query.Set("filter", "severityLevel:"+severity)
	}
	if maxResults, _ := command.Flags().GetInt64("max-results"); maxResults > 0 {
		query.Set("maxResults", strconv.FormatInt(maxResults, 10))
	}
	if pageToken, _ := command.Flags().GetString("page-token"); pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	return query, nil
}

// findGAOperation loads the live GA operation catalog and returns the named
// operation, resolved against the configured API base — the same catalog
// invocation_preflight.go validates command names against.
func findGAOperation(name string) (*cli.Operation, error) {
	api, err := loadDCIOperationAPI()
	if err != nil {
		return nil, fmt.Errorf("load DCI operation catalog: %w", err)
	}
	operation := invocationOperation(api, name)
	if operation == nil {
		return nil, fmt.Errorf("operation %q is unavailable in the current API spec", name)
	}
	return operation, nil
}

// recentWindowStart parses a Go duration string into a minCreationTime epoch
// millisecond bound. list-anomalies documents minCreationTime as an inclusive
// lower bound on the anomaly's usage start time.
func recentWindowStart(window string) (int64, error) {
	duration, err := time.ParseDuration(window)
	if err != nil {
		return 0, fmt.Errorf("invalid --window %q: %w", window, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid --window %q: must be positive", window)
	}
	return time.Now().Add(-duration).UnixMilli(), nil
}

// questionRequestURI appends the given fixed query parameters to operation's
// URI, preserving any query string the template already carries.
func questionRequestURI(operation cli.Operation, query url.Values) string {
	uri := operation.URITemplate
	separator := "?"
	if strings.Contains(uri, "?") {
		separator = "&"
	}
	return uri + separator + query.Encode()
}

// issueQuestionRequest builds and sends a GET against operation's URI with
// the given fixed query parameters, then hands the response to restish's
// normal formatting pipeline — the same path list-budgets/list-anomalies
// use, so output flags, TOON, agent-mode shaping, and the error contract
// all apply unchanged.
func issueQuestionRequest(operation cli.Operation, query url.Values) error {
	request, err := http.NewRequest(operation.Method, questionRequestURI(operation, query), nil)
	if err != nil {
		return err
	}
	cli.MakeRequestAndFormat(request)
	return nil
}

// questionCommandCatalogEntries reports budgets-at-risk and anomalies-recent
// to the machine-readable catalog (command_catalog.go). They are hand-
// registered under the hidden dci command rather than GA-spec operations, so
// buildCommandCatalog's two generic loops (api.Operations, and cli.Root's
// non-"dci" children) both miss them; unlike localCatalogEntries' generic
// local-command shape, these answer with the same api_response envelope and
// authentication requirement as the list-budgets/list-anomalies operation
// they wrap.
func questionCommandCatalogEntries() []commandCatalogEntry {
	dciCommand := findDCICommand()
	if dciCommand == nil {
		return nil
	}
	entries := make([]commandCatalogEntry, 0, len(questionCommandNames))
	for _, command := range dciCommand.Commands() {
		if !questionCommandNames[command.Name()] {
			continue
		}
		flags := appendUniqueCatalogFlags(catalogFlagsFromFlagSet(command.Flags()), agentContractCatalogFlags())
		sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
		entries = append(entries, commandCatalogEntry{
			Path:          []string{command.Name()},
			Summary:       command.Short,
			Flags:         flags,
			OutputShape:   "api_response",
			Destructive:   false,
			RequiresAuth:  true,
			AgentFriendly: true,
		})
	}
	return entries
}
