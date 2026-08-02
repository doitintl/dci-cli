package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const catalogSchemaVersion = "1"

type commandCatalog struct {
	Version    string                    `json:"version"`
	CLIVersion string                    `json:"cli_version"`
	Commands   []commandCatalogEntry     `json:"commands"`
	Shapes     map[string]map[string]any `json:"shapes"`
}

type commandCatalogEntry struct {
	Path          []string             `json:"path"`
	Summary       string               `json:"summary,omitempty"`
	Flags         []commandCatalogFlag `json:"flags,omitempty"`
	OutputShape   string               `json:"output_shape"`
	Destructive   bool                 `json:"destructive"`
	RequiresAuth  bool                 `json:"requires_auth"`
	AgentFriendly bool                 `json:"agent_friendly"`
}

type commandCatalogFlag struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Default     interface{} `json:"default,omitempty"`
	Description string      `json:"description,omitempty"`
	Example     interface{} `json:"example,omitempty"`
}

func registerCommandCatalog() {
	command := &cobra.Command{
		Use:   "commands",
		Short: "Print the machine-readable command catalog",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			apiCommand := findDCICommand()
			if apiCommand == nil {
				return errors.New("DCI API command tree is unavailable")
			}
			api, err := loadCatalogAPI(command)
			if err != nil {
				return fmt.Errorf("load DCI command catalog: %w", err)
			}
			catalog := buildCommandCatalog(api)
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(catalog)
		},
	}
	command.Flags().Bool("json", false, "Emit JSON (the catalog's stable wire format)")
	cli.Root.AddCommand(command)
}

func loadCatalogAPI(command *cobra.Command) (cli.API, error) {
	base, err := apiBase()
	if err != nil {
		return cli.API{}, err
	}
	entrypoint, err := url.Parse(base + "/")
	if err != nil {
		return cli.API{}, err
	}
	specURL, err := url.Parse(base + "/openapi.yaml")
	if err != nil {
		return cli.API{}, err
	}
	request, err := http.NewRequestWithContext(command.Context(), http.MethodGet, specURL.String(), nil)
	if err != nil {
		return cli.API{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return cli.API{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return cli.API{}, fmt.Errorf("OpenAPI endpoint returned HTTP %d", response.StatusCode)
	}
	return openapi.New().Load(*entrypoint, *specURL, response)
}

func buildCommandCatalog(api cli.API) commandCatalog {
	entries := make([]commandCatalogEntry, 0, len(api.Operations)+len(cli.Root.Commands()))
	for _, operation := range api.Operations {
		if operation.Hidden {
			continue
		}
		flags := make([]commandCatalogFlag, 0, len(operation.QueryParams)+len(operation.HeaderParams)+6)
		for _, parameter := range append(append([]*cli.Param{}, operation.QueryParams...), operation.HeaderParams...) {
			flags = append(flags, commandCatalogFlag{
				Name:        "--" + parameter.OptionName(),
				Type:        parameter.Type,
				Default:     parameter.Default,
				Description: parameter.Description,
				Example:     parameter.Example,
			})
		}
		flags = append(flags, agentContractCatalogFlags()...)
		sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
		entries = append(entries, commandCatalogEntry{
			Path:          []string{operation.Name},
			Summary:       operation.Short,
			Flags:         flags,
			OutputShape:   "api_response",
			Destructive:   strings.EqualFold(operation.Method, "DELETE"),
			RequiresAuth:  true,
			AgentFriendly: true,
		})
	}
	for _, command := range cli.Root.Commands() {
		if command.Hidden || command.Name() == "dci" || command.Name() == "help" || command.Name() == "completion" {
			continue
		}
		entries = append(entries, localCatalogEntries(command, nil)...)
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.Join(entries[i].Path, " ") < strings.Join(entries[j].Path, " ")
	})
	return commandCatalog{
		Version:    catalogSchemaVersion,
		CLIVersion: version,
		Commands:   entries,
		Shapes: map[string]map[string]any{
			"api_response": {
				"description": "The response shape declared by the DCI OpenAPI operation",
			},
			"structured_error": {
				"required": []string{"error.code", "error.message", "error.retryable"},
			},
		},
	}
}

func localCatalogEntries(command *cobra.Command, parent []string) []commandCatalogEntry {
	path := append(append([]string{}, parent...), command.Name())
	children := command.Commands()
	entries := make([]commandCatalogEntry, 0)
	if command.Runnable() || len(children) == 0 {
		entries = append(entries, commandCatalogEntry{
			Path:          path,
			Summary:       command.Short,
			Flags:         catalogFlagsFromFlagSet(command.Flags()),
			OutputShape:   "local_command_response",
			Destructive:   false,
			RequiresAuth:  false,
			AgentFriendly: true,
		})
	}
	for _, child := range children {
		if child.Hidden || child.Name() == "help" {
			continue
		}
		entries = append(entries, localCatalogEntries(child, path)...)
	}
	return entries
}

func catalogFlagsFromFlagSet(flags *pflag.FlagSet) []commandCatalogFlag {
	result := make([]commandCatalogFlag, 0)
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || flag.Name == "help" {
			return
		}
		result = append(result, commandCatalogFlag{
			Name:        "--" + flag.Name,
			Type:        flag.Value.Type(),
			Default:     flag.DefValue,
			Description: flag.Usage,
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func agentContractCatalogFlags() []commandCatalogFlag {
	flags := []commandCatalogFlag{
		{Name: "--dry-run", Type: "bool", Default: false, Description: "Preview a destructive operation without executing it"},
		{Name: "--output", Type: "string", Description: "Select table, JSON, YAML, automatic, or TOON output"},
		{Name: "--yes", Type: "bool", Default: false, Description: "Confirm a destructive operation"},
	}
	apiCommand := findDCICommand()
	if apiCommand == nil {
		return flags
	}
	for _, optional := range []commandCatalogFlag{
		{Name: "--exclude", Type: "string", Description: "Exclude response fields"},
		{Name: "--fields", Type: "string", Description: "Select response fields"},
		{Name: "--full", Type: "bool", Default: false, Description: "Disable agent-oriented truncation"},
		{Name: "--no-truncate", Type: "bool", Default: false, Description: "Disable long-value truncation"},
	} {
		if apiCommand.PersistentFlags().Lookup(strings.TrimPrefix(optional.Name, "--")) != nil {
			flags = append(flags, optional)
		}
	}
	return flags
}
