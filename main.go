package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/oauth"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var version string = "dev"

func dciConfigDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dci")
}

func ensureConfig(configDir string) {
	configFile := filepath.Join(configDir, "apis.json")

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		os.MkdirAll(configDir, 0755)

		config := map[string]interface{}{
			"$schema": "https://rest.sh/schemas/apis.json",
			"dci": map[string]interface{}{
				"base": "https://api.doit.com",
				"profiles": map[string]interface{}{
					"default": map[string]interface{}{
						"auth": map[string]interface{}{
							"name": "oauth-authorization-code",
							"params": map[string]interface{}{
								"authorize_url": "https://console.doit.com/sign-in/oauth",
								"client_id":     "cli",
								"token_url":     "https://console.doit.com/api/auth/token",
							},
						},
					},
				},
				"tls": map[string]interface{}{},
			},
		}

		data, _ := json.MarshalIndent(config, "", "  ")
		os.WriteFile(configFile, data, 0644)
	}
}

func main() {
	configDir := dciConfigDir()
	ensureConfig(configDir)

	cli.Init("dci", version)
	cli.Defaults()

	cli.AddLoader(openapi.New())
	cli.AddAuth("oauth-authorization-code", &oauth.AuthorizationCodeHandler{})

	cli.Load("dci", cli.Root)
	registerCustomerContextCommands(configDir)
	hideGlobalFlags()
	customizeDCIUsage()
	applyCustomerContext(configDir)
	lockToDCI()

	// Prepend "dci" to args if not a known root command so users can call
	// API commands directly without the restish prefix.
	if len(os.Args) > 1 {
		firstArg := os.Args[1]

		if len(os.Args) == 2 && (firstArg == "--help" || firstArg == "-h") {
			// Make `./dci --help` show the DCI commands instead of the generic restish help.
			os.Args = []string{os.Args[0], "dci", "dci", "--help"}
		} else if firstArg != "--help" && firstArg != "-h" && firstArg != "--version" {
			isRootCmd := false
			for _, cmd := range cli.Root.Commands() {
				if cmd.Name() == firstArg {
					isRootCmd = true
					break
				}
			}

			if !isRootCmd {
				os.Args = append([]string{os.Args[0], "dci"}, os.Args[1:]...)
			}
		}
	}

	if err := cli.Run(); err != nil {
		os.Exit(1)
	}
	os.Exit(cli.GetExitCode())
}

func hideGlobalFlags() {
	// Keep the flags functional but hide them from help output.
	cli.Root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		f.Hidden = true
	})
}

func customizeDCIUsage() {
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() != "dci" {
			continue
		}

		cmd.SetUsageTemplate(`Usage:{{if .Runnable}}
  {{.Use}}{{if .HasAvailableFlags}} [flags]{{end}}{{end}}{{if .HasAvailableSubCommands}}
  {{.Use}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.Use}} [command] --help" for more information about a command.{{end}}
`)
		break
	}
}

func lockToDCI() {
	// Remove API management commands, generic RESTish commands, and any
	// additional API entrypoints so users can only call the DCI API.
	allowed := map[string]bool{
		"dci":  true,
		"help": true,
	}
	for _, cmd := range cli.Root.Commands() {
		if allowed[cmd.Name()] {
			continue
		}

		if cmd.Name() == "api" || cmd.Name() == "completion" || cmd.GroupID == "generic" || (cmd.GroupID == "api" && cmd.Name() != "dci") {
			cli.Root.RemoveCommand(cmd)
		}
	}
}

func registerCustomerContextCommands(configDir string) {
	cmd := &cobra.Command{
		Use:   "customer-context",
		Short: "Manage default customerContext for requests",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "set TOKEN",
		Short: "Set the default customerContext",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token := strings.TrimSpace(args[0])
			if token == "" {
				return fmt.Errorf("customerContext cannot be empty")
			}
			if err := os.WriteFile(customerContextPath(configDir), []byte(token+"\n"), 0o600); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "customerContext saved")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear the default customerContext",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.Remove(customerContextPath(configDir)); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintln(os.Stdout, "customerContext cleared")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the current default customerContext",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ctx := readCustomerContext(configDir); ctx != "" {
				fmt.Fprintln(os.Stdout, ctx)
			} else {
				fmt.Fprintln(os.Stdout, "customerContext not set")
			}
			return nil
		},
	})

	cli.Root.AddCommand(cmd)
}

func customerContextPath(configDir string) string {
	return filepath.Join(configDir, "customer_context")
}

func readCustomerContext(configDir string) string {
	data, err := os.ReadFile(customerContextPath(configDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func applyCustomerContext(configDir string) {
	ctx := readCustomerContext(configDir)
	if ctx == "" {
		return
	}

	existing := viper.GetStringSlice("rsh-query")
	for _, q := range existing {
		if strings.HasPrefix(q, "customerContext=") {
			return
		}
	}

	viper.Set("rsh-query", append(existing, "customerContext="+ctx))
}
