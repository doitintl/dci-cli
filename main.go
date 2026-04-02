package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexeyco/simpletable"
	"github.com/mattn/go-runewidth"
	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/oauth"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

var version string = "dev"

const defaultAPIBase = "https://api.doit.com"

// apiBase returns the API base URL, allowing override via DCI_API_BASE_URL.
func apiBase() (string, error) {
	v := strings.TrimSpace(os.Getenv("DCI_API_BASE_URL"))
	if v == "" {
		return defaultAPIBase, nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("invalid DCI_API_BASE_URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("DCI_API_BASE_URL must use https:// scheme (got %q)", u.Scheme)
	}
	return strings.TrimRight(v, "/"), nil
}

//go:embed skills/dci-cli
var skillFS embed.FS

// customerContextFlagValue holds the --customer-context / -D flag value when
// set, used to suppress the Doer hint even when no persistent context file exists.
var customerContextFlagValue string

func dciConfigDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		cfgDir := filepath.Join(dir, "dci")

		// Prefer existing config directories to avoid breaking users on macOS.
		if _, err := os.Stat(cfgDir); err == nil {
			return cfgDir
		}
		legacy := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dci")
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
		return cfgDir
	}

	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dci")
}

func ensureConfig(configDir string) (bool, error) {
	configFile := filepath.Join(configDir, "apis.json")

	base, err := apiBase()
	if err != nil {
		return false, err
	}

	if _, err := os.Stat(configFile); err == nil {
		if err := tightenFilePermissions(configFile, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unable to tighten config permissions for %s: %v\n", configFile, err)
		}
		// When DCI_API_BASE_URL is set, update the base URL in the existing config.
		if os.Getenv("DCI_API_BASE_URL") != "" {
			if err := updateConfigBase(configFile, base); err != nil {
				return false, err
			}
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return false, err
	}

	config := map[string]interface{}{
		"$schema": "https://rest.sh/schemas/apis.json",
		"dci": map[string]interface{}{
			"base": base,
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

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		return false, err
	}

	return true, nil
}

// updateConfigBase reads apis.json, updates the dci.base field, and rewrites the file.
func updateConfigBase(configFile, base string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	dci, ok := config["dci"].(map[string]interface{})
	if !ok {
		return nil // unexpected structure, leave as-is
	}
	if dci["base"] == base {
		return nil // already up to date
	}
	dci["base"] = base
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, out, 0o600)
}

func tightenFilePermissions(path string, desired os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	perm := info.Mode().Perm()
	if perm&^desired == 0 {
		return nil
	}

	return os.Chmod(path, desired)
}

func printFirstRunOnboarding(configured bool) {
	if !configured || !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}

	fmt.Fprintln(os.Stderr, "DoiT Cloud Intelligence CLI is ready.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Next steps:")
	fmt.Fprintln(os.Stderr, "  dci status")
	fmt.Fprintln(os.Stderr, "  dci list-budgets")
	fmt.Fprintln(os.Stderr, "  dci list-reports --output table")
	fmt.Fprintln(os.Stderr, "")
}

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	// Reset per-invocation state so repeated calls (e.g. in tests) start clean.
	customerContextFlagValue = ""

	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "dci encountered an internal error: %v\n", r)
			if os.Getenv("DCI_DEBUG_PANIC") == "1" {
				debug.PrintStack()
			}
			exitCode = 1
		}
	}()

	configDir := dciConfigDir()
	configured, err := ensureConfig(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize config: %v\n", err)
		return 1
	}

	cli.Init("dci", version)
	cli.Defaults()
	overrideTableOutput()
	printFirstRunOnboarding(configured)

	cli.AddLoader(openapi.New())
	cli.AddAuth("oauth-authorization-code", &oauth.AuthorizationCodeHandler{})

	if err := rejectProfileFlags(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	// Keep profile fixed until we support multi-profile UX.
	os.Setenv("RSH_PROFILE", "default")
	viper.Set("rsh-profile", "default")

	// Hardcode user-agent so the DCI API can identify CLI traffic.
	// Restish picks this up via rsh-header and skips its own default.
	viper.Set("rsh-header", []string{"user-agent:dci/" + version})

	cli.Load("dci", cli.Root)
	applyAPIKeyAuth()
	brandRootCommand()
	brandDCIRootCommand()
	registerStatusCommands(configDir)
	registerAuthCommands(configDir)
	registerCustomerContextCommands(configDir)
	registerSkillCommands()
	// Unhide the customer-context command for DoiT employees so it appears in help.
	if cachedTokenIsDoer() {
		for _, c := range cli.Root.Commands() {
			if c.Use == "customer-context" {
				c.Hidden = false
				break
			}
		}
	}
	addOutputFlag()
	hideGlobalFlags()
	customizeDCIUsage()
	applyCustomerContext(configDir)
	lockToDCI()
	setupCompletion()
	os.Args = normalizeArgs(os.Args)

	if err := cli.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		maybeHintDoerContext(1, cli.GetLastStatus(), configDir)
		return 1
	}
	code := cli.GetExitCode()
	maybeHintDoerContext(code, cli.GetLastStatus(), configDir)
	return code
}

func rejectProfileFlags(args []string) error {
	flags := cli.Root.PersistentFlags()

	for _, arg := range args[1:] {
		if arg == "--" {
			// Everything after `--` is a positional operand.
			return nil
		}
		if arg == "--profile" || arg == "--rsh-profile" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "--rsh-profile=") {
			return fmt.Errorf("profile selection is currently disabled")
		}
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || arg == "-" {
			continue
		}

		shorts := strings.TrimPrefix(arg, "-")
		if shorts == "" {
			continue
		}
		if beforeEq, _, ok := strings.Cut(shorts, "="); ok {
			shorts = beforeEq
		}

		for i := 0; i < len(shorts); i++ {
			ch := string(shorts[i])
			if ch == "p" {
				return fmt.Errorf("profile selection is currently disabled")
			}
			flag := flags.ShorthandLookup(ch)
			if flag != nil && !isBoolFlag(flag) {
				// Remaining bytes belong to this flag's value.
				break
			}
		}
	}
	return nil
}

func normalizeArgs(args []string) []string {
	if len(args) <= 1 {
		return []string{args[0], "--help"}
	}

	cmd := firstCommandArg(args)
	if cmd == "" || cmd == "help" || cmd == "version" || cmd == "completion" || isRootCommand(cmd) {
		return args
	}

	// __complete and __completeNoDesc are hidden cobra commands invoked by
	// shell completion scripts. The args after them mirror user input and
	// need the same "dci" prefix insertion so cobra resolves completions
	// under the API subcommand.
	if cmd == "__complete" || cmd == "__completeNoDesc" {
		return normalizeCompletionArgs(args, cmd)
	}

	return append([]string{args[0], "dci"}, args[1:]...)
}

// normalizeCompletionArgs inserts "dci" after __complete/__completeNoDesc when
// the completion target is an API command (not a root command). This mirrors
// normalizeArgs so that tab-completion resolves under the API subcommand.
func normalizeCompletionArgs(args []string, completionCmd string) []string {
	// Find the position of __complete/__completeNoDesc.
	idx := -1
	for i, a := range args {
		if a == completionCmd {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(args) {
		return args
	}

	// Check the first arg after the completion command — if it's a root
	// command (or empty), let cobra handle it at root level.
	tail := args[idx+1:]
	first := ""
	for _, a := range tail {
		if !strings.HasPrefix(a, "-") {
			first = a
			break
		}
	}
	if first == "" || first == "help" || isRootCommand(first) {
		return args
	}

	// Insert "dci" after the completion command to route into the API subcommand.
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:idx+1]...)
	result = append(result, "dci")
	result = append(result, tail...)
	return result
}

func firstCommandArg(args []string) string {
	flags := cli.Root.PersistentFlags()

	for i := 1; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return arg
		}

		// Long flag.
		if strings.HasPrefix(arg, "--") {
			name, hasValue := splitLongFlag(arg)
			if name == "" {
				continue
			}
			if hasValue {
				continue
			}
			flag := flags.Lookup(name)
			if flag != nil && !isBoolFlag(flag) && i+1 < len(args) {
				i++
			}
			continue
		}

		// Short flag(s), including compact values (e.g. -pfoo).
		shorts := arg[1:]
		for j := 0; j < len(shorts); j++ {
			flag := flags.ShorthandLookup(string(shorts[j]))
			if flag == nil {
				continue
			}
			if isBoolFlag(flag) {
				continue
			}
			if j == len(shorts)-1 && i+1 < len(args) {
				i++
			}
			break
		}
	}

	return ""
}

func splitLongFlag(arg string) (name string, hasValue bool) {
	s := strings.TrimPrefix(arg, "--")
	if s == "" {
		return "", false
	}
	if n, _, ok := strings.Cut(s, "="); ok {
		return n, true
	}
	return s, false
}

func isBoolFlag(flag *pflag.Flag) bool {
	if flag == nil || flag.Value == nil {
		return false
	}
	return flag.Value.Type() == "bool"
}

func isRootCommand(name string) bool {
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() == name {
			return true
		}
		for _, alias := range cmd.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

func hideGlobalFlags() {
	// Keep the flags functional but hide them from help output.
	cli.Root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		f.Hidden = true
	})
}

const dciUsageTemplate = `Usage:{{if .Runnable}}
  {{.Use}}{{if .HasAvailableFlags}} [flags]{{end}}{{end}}{{if .HasAvailableSubCommands}}
  dci [command]
  dci [command] --help{{else}}
  {{.Use}} --help{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}{{if hasVisibleCommandsInGroup $cmds $group.ID}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if or .HasAvailableLocalFlags .HasAvailableInheritedFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{if .HasAvailableInheritedFlags}}
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}
`

const dciLongDescription = "Command-line interface for the DoiT Cloud Intelligence API."

var rootExamples = []string{
	"  dci status",
	"  dci list-budgets",
	"  dci list-reports --output table",
}

var apiExamples = []string{
	"  dci list-budgets",
	"  dci list-reports --output table",
	"  dci query body.query:\"SELECT * FROM aws_cur_2_0 LIMIT 10\"",
}

func findDCICommand() *cobra.Command {
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() == "dci" {
			return cmd
		}
	}
	return nil
}

func customizeDCIUsage() {
	cobra.AddTemplateFunc("hasVisibleCommandsInGroup", func(cmds []*cobra.Command, groupID string) bool {
		for _, cmd := range cmds {
			if cmd.GroupID == groupID && (cmd.IsAvailableCommand() || cmd.Name() == "help") {
				return true
			}
		}
		return false
	})

	dciCmd := findDCICommand()
	if dciCmd == nil {
		return
	}

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.SetUsageTemplate(dciUsageTemplate)
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(dciCmd)
}

func applyCommandBranding(cmd *cobra.Command, short string, examples []string) {
	if cmd == nil {
		return
	}
	cmd.Short = short
	cmd.Long = dciLongDescription
	cmd.Example = strings.Join(examples, "\n")
}

func brandRootCommand() {
	applyCommandBranding(cli.Root, "DoiT Cloud Intelligence CLI", rootExamples)
	cli.Root.SetUsageTemplate(dciUsageTemplate)
}

func lockToDCI() {
	// Remove API management commands, generic RESTish commands, and any
	// additional API entrypoints so users can only call the DCI API.
	allowed := map[string]bool{
		"completion": true,
		"dci":        true,
		"help":       true,
		"login":      true,
		"logout":     true,
	}
	toRemove := make([]*cobra.Command, 0)
	for _, cmd := range cli.Root.Commands() {
		if allowed[cmd.Name()] {
			continue
		}

		if cmd.Name() == "api" || cmd.GroupID == "generic" || (cmd.GroupID == "api" && cmd.Name() != "dci") {
			toRemove = append(toRemove, cmd)
		}
	}
	for _, cmd := range toRemove {
		cli.Root.RemoveCommand(cmd)
	}
}

// setupCompletion configures shell completion and root help so that API
// commands appear at root level (alongside status, login, etc.).
//
// The "dci" API subcommand is hidden since users access its commands directly
// via normalizeArgs. Its ValidArgsFunction (which returns URL paths from
// restish) is cleared so completions show command names instead.
//
// Restish lazily loads API operations inside cli.Run() by inspecting os.Args
// for the API name. For root-level completion and help, restish skips loading
// because __complete and --help args are filtered out. We work around this by
// triggering the load on demand.
func setupCompletion() {
	var dciCmd *cobra.Command
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() == "dci" {
			dciCmd = cmd
			break
		}
	}
	if dciCmd == nil {
		return
	}

	// Hide the "dci" namespace — users interact with API commands at root level.
	dciCmd.Hidden = true

	// Clear restish's ValidArgsFunction that returns URL paths.
	dciCmd.ValidArgsFunction = nil

	// loadAPI triggers lazy API loading into the dci subcommand. Restish's
	// cli.Run() normally does this by parsing os.Args, but --help and
	// __complete are filtered out so we must load explicitly.
	//
	// To avoid triggering OAuth when no auth is cached, we only call
	// cli.Load when restish's API cache file exists. If it doesn't, the
	// user hasn't authenticated yet and API commands won't be shown until
	// they run "dci login".
	var apiLoaded bool
	loadAPI := func() {
		if apiLoaded {
			return
		}
		apiLoaded = true
		cacheDir, _ := os.UserCacheDir()
		cacheFile := filepath.Join(cacheDir, "dci", "dci.cbor")
		if _, err := os.Stat(cacheFile); err != nil {
			return
		}
		base, err := apiBase()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return
		}
		cli.Load(base, dciCmd)
	}

	// Surface API subcommands in root-level completion so "dci <Tab>"
	// shows list-budgets, list-reports, etc. alongside status, login, etc.
	cli.Root.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		loadAPI()
		var completions []string
		for _, sub := range dciCmd.Commands() {
			if sub.Hidden {
				continue
			}
			if strings.HasPrefix(sub.Name(), toComplete) {
				completions = append(completions, sub.Name()+"\t"+sub.Short)
			}
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}

	// Override root help to include API commands. Load the API, move its
	// commands to root so the standard usage template renders them, then
	// show help normally.
	defaultHelp := cli.Root.HelpFunc()
	cli.Root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		hasAPICommands := false
		if cmd == cli.Root {
			loadAPI()
			hasAPICommands = len(dciCmd.Commands()) > 0
			// Copy command groups from the API subcommand to root so the
			// usage template can render grouped commands.
			for _, g := range dciCmd.Groups() {
				if !cli.Root.ContainsGroup(g.ID) {
					cli.Root.AddGroup(g)
				}
			}
			// Collect first — iterating Commands() while removing mutates the slice.
			subs := make([]*cobra.Command, len(dciCmd.Commands()))
			copy(subs, dciCmd.Commands())
			for _, sub := range subs {
				dciCmd.RemoveCommand(sub)
				cli.Root.AddCommand(sub)
			}
		}
		defaultHelp(cmd, args)
		if cmd == cli.Root && !hasAPICommands {
			hint := "\n! To get started, authenticate with: dci login (or set DCI_API_KEY)\n\n"
			if term.IsTerminal(int(os.Stdout.Fd())) {
				hint = "\n\033[1;33m!\033[0m To get started, authenticate with: \033[1mdci login\033[0m (or set \033[1mDCI_API_KEY\033[0m)\n\n"
			}
			fmt.Fprint(os.Stdout, hint)
		}
	})
}

func registerCustomerContextCommands(configDir string) {
	cmd := &cobra.Command{
		Use:    "customer-context",
		Short:  "Manage default customerContext for requests",
		Hidden: true,
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

// installSkill copies embedded skill files into targetDir/skills/dci-cli/.
func installSkill(targetDir string) error {
	const srcRoot = "skills/dci-cli"
	destRoot := filepath.Join(targetDir, "skills", "dci-cli")

	return fs.WalkDir(skillFS, srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(destRoot, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
}

func registerSkillCommands() {
	agents := []struct {
		name string
		dir  string
	}{
		{"claude", ".claude"},
		{"codex", ".codex"},
		{"kiro", ".kiro"},
		{"gemini", ".gemini"},
		{"opencode", ".config/opencode"},
	}

	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install the dci skill for an AI agent",
	}

	for _, a := range agents {
		agentName := a.name
		agentDir := a.dir
		cmd.AddCommand(&cobra.Command{
			Use:   agentName,
			Short: fmt.Sprintf("Install skill into ~/%s/skills/dci-cli/", agentDir),
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("cannot determine home directory: %w", err)
				}
				targetDir := filepath.Join(home, agentDir)
				if err := installSkill(targetDir); err != nil {
					return fmt.Errorf("failed to install skill: %w", err)
				}
				fmt.Fprintf(os.Stdout, "Skill installed to %s\n", filepath.Join(targetDir, "skills", "dci-cli"))
				return nil
			},
		})
	}

	cli.Root.AddCommand(cmd)
}

func brandDCIRootCommand() {
	applyCommandBranding(findDCICommand(), "DoiT Cloud Intelligence API CLI", apiExamples)
}

func registerStatusCommands(configDir string) {
	currentOutput := func() string {
		output := strings.TrimSpace(viper.GetString("rsh-output-format"))
		if output == "" || output == "auto" {
			output = "table"
		}
		return output
	}

	renderStatus := func(cmd *cobra.Command, args []string) error {
		ctx := readCustomerContext(configDir)

		base, err := apiBase()
		if err != nil {
			return err
		}

		fmt.Fprintln(os.Stdout, "DoiT Cloud Intelligence")
		if os.Getenv("DCI_API_BASE_URL") != "" {
			fmt.Fprintf(os.Stdout, "API Base: %s (DCI_API_BASE_URL)\n", base)
		} else {
			fmt.Fprintf(os.Stdout, "API Base: %s\n", base)
		}
		fmt.Fprintf(os.Stdout, "Auth: %s\n", authSource())
		fmt.Fprintf(os.Stdout, "Default Output: %s\n", currentOutput())
		fmt.Fprintf(os.Stdout, "Config Dir: %s\n", configDir)
		if ctx != "" {
			if os.Getenv("DCI_CUSTOMER_CONTEXT") != "" {
				fmt.Fprintf(os.Stdout, "Customer context: %s (DCI_CUSTOMER_CONTEXT)\n", ctx)
			} else {
				fmt.Fprintf(os.Stdout, "Customer context: %s\n", ctx)
			}
		}
		return nil
	}

	cli.Root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show DoiT CLI configuration and active context",
		Args:  cobra.NoArgs,
		RunE:  renderStatus,
	})
}

// applyAPIKeyAuth injects DCI_API_KEY into restish's auth cache as a Bearer
// token. Restish's OAuth TokenHandler checks the cache before triggering a
// browser flow, so pre-populating it bypasses interactive login. We use
// cli.Cache (an exported *viper.Viper) because restish does not export its
// config internals (the configs map and apis viper instance are private).
func applyAPIKeyAuth() {
	apiKey := os.Getenv("DCI_API_KEY")
	if apiKey == "" {
		return
	}

	profile := viper.GetString("rsh-profile")
	key := "dci:" + profile
	cli.Cache.Set(key+".token", apiKey)
	cli.Cache.Set(key+".type", "Bearer")
	cli.Cache.Set(key+".expires", "9999-12-31T23:59:59Z")
	cli.Cache.Set(key+".refresh", "")
}

func authSource() string {
	if os.Getenv("DCI_API_KEY") != "" {
		return "API key (DCI_API_KEY)"
	}
	return "OAuth (DoiT Console)"
}

// maybeHintDoerContext prints a targeted hint when a @doit.com user hits a 403
// without a customer context set — covering both interactive and CI/CD usage.
// status is the HTTP status code from the last request (pass cli.GetLastStatus()).
func maybeHintDoerContext(exitCode int, status int, configDir string) {
	if exitCode == 0 || (status != 401 && status != 403) {
		return
	}
	if !cachedTokenIsDoer() {
		return
	}
	if readCustomerContext(configDir) != "" || customerContextFlagValue != "" {
		return
	}
	if term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "\033[1;33m!\033[0m DoiT employees need a customer context for API calls.\n")
		fmt.Fprintf(os.Stderr, "  Interactive:  \033[1mdci customer-context set doit.com\033[0m\n")
		fmt.Fprintf(os.Stderr, "  CI/scripts:   \033[1mexport DCI_CUSTOMER_CONTEXT=doit.com\033[0m\n")
		fmt.Fprintln(os.Stderr, "")
	} else {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "! DoiT employees need a customer context for API calls.")
		fmt.Fprintln(os.Stderr, "  Interactive:  dci customer-context set doit.com")
		fmt.Fprintln(os.Stderr, "  CI/scripts:   export DCI_CUSTOMER_CONTEXT=doit.com")
		fmt.Fprintln(os.Stderr, "")
	}
}

// applyDoerContext auto-configures the customer context to "doit.com" for
// @doit.com accounts that haven't set one yet. The validate endpoint requires
// customerContext for DoiT employees; calling this after the OAuth token is
// cached fixes the chicken-and-egg problem on first login. Returns true if the
// context was written so the caller can clear a 403 error from validate.
func applyDoerContext(configDir string) bool {
	if !cachedTokenIsDoer() {
		return false
	}
	if readCustomerContext(configDir) != "" {
		return false // already configured, don't overwrite
	}
	err := os.WriteFile(customerContextPath(configDir), []byte("doit.com\n"), 0o600)
	if err != nil {
		return false
	}
	fmt.Fprintln(os.Stderr, "Detected DoiT account. Set default customer context to 'doit.com'.")
	fmt.Fprintln(os.Stderr, "To use a different context: dci customer-context set <CONTEXT>")
	return true
}

// cachedTokenIsDoer reports whether the cached OAuth JWT contains
// DoitEmployee: true. This is more reliable than email-domain matching because
// it is an explicit claim set by the DoiT auth server and is domain-independent.
// Returns false if the cache is empty, the token is absent, or the JWT is malformed.
func cachedTokenIsDoer() bool {
	if cli.Cache == nil {
		return false
	}
	token := cli.Cache.GetString("dci:default.token")
	if token == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	// JWT payload is base64url-encoded without padding.
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		DoitEmployee bool `json:"DoitEmployee"`
	}
	if err := json.Unmarshal(b, &claims); err != nil {
		return false
	}
	return claims.DoitEmployee
}

func registerAuthCommands(configDir string) {
	cli.Root.AddCommand(&cobra.Command{
		Use:     "login",
		Aliases: []string{"auth", "init"},
		Short:   "Authenticate with the DoiT Console",
		Long:    "Opens a browser window to sign in via the DoiT Console. Credentials are cached locally for subsequent commands.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("DCI_API_KEY") != "" {
				return fmt.Errorf("login is not needed when DCI_API_KEY is set")
			}
			// Trigger the OAuth flow by calling a lightweight endpoint.
			// Suppress the validate response body — login only needs the OAuth
			// side effect (token cached), not the API output.
			os.Args = []string{os.Args[0], "dci", "validate"}
			oldOut := cli.Stdout
			cli.Stdout = io.Discard
			err := cli.Run()
			cli.Stdout = oldOut

			// Auto-configure DoiT employees who have no customer context set.
			// The validate endpoint requires customerContext for @doit.com accounts,
			// causing a 403 on first login before any context is configured. The OAuth
			// token exchange succeeds (token is cached) even when validate returns 403,
			// so we can inspect the token here and fix the chicken-and-egg problem.
			if applyDoerContext(configDir) {
				err = nil // the 403 was due to missing context; auth itself succeeded
				// Reset the HTTP status so GetExitCode() returns 0 for this process.
				viper.Set("rsh-ignore-status-code", true)
			}

			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Authenticated successfully.")
			return nil
		},
	})

	cli.Root.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Clear stored authentication credentials",
		Long:  "Removes cached OAuth tokens. You will need to sign in again on the next API call.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("DCI_API_KEY") != "" {
				return fmt.Errorf("logout has no effect when DCI_API_KEY is set; unset the environment variable instead")
			}
			profile := viper.GetString("rsh-profile")
			key := "dci:" + profile
			cli.Cache.Set(key+".token", "")
			cli.Cache.Set(key+".refresh", "")
			cli.Cache.Set(key+".type", "")
			cli.Cache.Set(key+".expires", nil)
			if err := cli.Cache.WriteConfig(); err != nil {
				return fmt.Errorf("failed to clear credentials: %w", err)
			}
			fmt.Fprintln(os.Stdout, "Logged out. Credentials cleared.")
			return nil
		},
	})
}

// customerContextPath returns the path to the custom file that stores the
// default customer context. We use a dedicated file instead of restish's
// apis.json profile query params because restish's config internals are
// private — there is no exported API to read/write profile settings
// programmatically, and writing apis.json directly risks conflicts with
// restish's in-memory config state.
func customerContextPath(configDir string) string {
	return filepath.Join(configDir, "customer_context")
}

func readCustomerContext(configDir string) string {
	if ctx := os.Getenv("DCI_CUSTOMER_CONTEXT"); ctx != "" {
		return strings.TrimSpace(ctx)
	}
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

func addOutputFlag() {
	dciCmd := findDCICommand()
	if dciCmd == nil {
		return
	}

	dciCmd.PersistentFlags().String("output", "", "Output format: table, json, yaml, auto (default: table)")
	dciCmd.PersistentFlags().StringP("table-mode", "M", "fit", "Table rendering: fit (truncate) or wrap (multi-line)")
	dciCmd.PersistentFlags().StringP("table-columns", "C", "", "Comma-separated list of columns to include (default: all)")
	dciCmd.PersistentFlags().IntP("table-width", "W", 0, "Table width in columns (default: auto-detect terminal width)")
	dciCmd.PersistentFlags().IntP("table-max-col-width", "X", 0, "Maximum width per column when fitting or wrapping (0 = auto)")
	dciCmd.PersistentFlags().StringP("customer-context", "D", "", "Override the active customer context for this command (e.g. acme.com)")

	// Bind table flags into viper so the renderer can pick them up.
	prev := dciCmd.PersistentPreRunE
	dciCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}

		outFlag := cmd.Flags().Lookup("output")
		if outFlag == nil || !outFlag.Changed {
			viper.Set("rsh-output-format", "table")
		} else {
			out := strings.TrimSpace(outFlag.Value.String())
			switch out {
			case "table", "json", "yaml", "auto":
				viper.Set("rsh-output-format", out)
			default:
				return fmt.Errorf("invalid --output %q (supported: table, json, yaml, auto)", out)
			}
		}
		defaultToBodyOutput()

		if flag := cmd.Flags().Lookup("table-mode"); flag != nil {
			v := strings.TrimSpace(flag.Value.String())
			if v == "" {
				v = "fit"
			}
			viper.Set("table-mode", v)
		}
		if flag := cmd.Flags().Lookup("table-columns"); flag != nil {
			v := strings.TrimSpace(flag.Value.String())
			viper.Set("table-columns", v)
		}
		bindNonNegativeIntFlag(cmd, "table-width")
		bindNonNegativeIntFlag(cmd, "table-max-col-width")

		// If --customer-context / -D was explicitly passed, override whatever
		// applyCustomerContext() injected from the file or env var.
		if flag := cmd.Flags().Lookup("customer-context"); flag != nil && flag.Changed {
			val := strings.TrimSpace(flag.Value.String())
			if val == "" {
				return fmt.Errorf("--customer-context requires a non-empty domain name")
			}
			existing := viper.GetStringSlice("rsh-query")
			filtered := existing[:0]
			for _, q := range existing {
				if !strings.HasPrefix(q, "customerContext=") {
					filtered = append(filtered, q)
				}
			}
			viper.Set("rsh-query", append(filtered, "customerContext="+val))
			customerContextFlagValue = val
		}

		return nil
	}
}

func bindNonNegativeIntFlag(cmd *cobra.Command, name string) {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		v, _ := strconv.Atoi(flag.Value.String())
		if v < 0 {
			v = 0
		}
		viper.Set(name, v)
	}
}

func defaultToBodyOutput() {
	// By default restish prints response status + headers for TTY output when no
	// filter is specified. This CLI is primarily focused on the response body,
	// so default to `body` unless the user explicitly requested raw output or a
	// filter was already set.
	if !viper.GetBool("rsh-raw") && viper.GetString("rsh-filter") == "" {
		viper.Set("rsh-filter", "body")
	}
}

type dciTableContentType struct{}

func overrideTableOutput() {
	// Restish's built-in table output expects the response body to be a JSON
	// array of objects. Many DCI list endpoints return an object that contains
	// the array under a field (e.g. `budgets: [...]`). This keeps `--output table`
	// ergonomic by extracting the most likely array or wrapping single objects.
	cli.AddContentType("table", "", -1, &dciTableContentType{})
}

func (t dciTableContentType) Detect(contentType string) bool { return false }

func (t dciTableContentType) Marshal(value interface{}) ([]byte, error) {
	jsonSafe, err := toJSONSafe(value)
	if err != nil {
		return nil, err
	}

	rows, err := toTableRows(jsonSafe)
	if err != nil {
		// Response is not table-friendly; fall back to indented JSON.
		b, jsonErr := json.MarshalIndent(jsonSafe, "", "  ")
		if jsonErr != nil {
			return nil, err // return original table error
		}
		return append(b, '\n'), nil
	}
	return renderTable(rows)
}

func (t dciTableContentType) Unmarshal(data []byte, value interface{}) error {
	return fmt.Errorf("unimplemented")
}

func toJSONSafe(value interface{}) (interface{}, error) {
	// Roundtrip through encoding/json to normalize map/slice types.
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func toTableRows(value interface{}) ([]map[string]interface{}, error) {
	switch v := value.(type) {
	case []interface{}:
		rows := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			obj, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("error building table. Must be array of objects")
			}
			rows = append(rows, obj)
		}
		return rows, nil
	case map[string]interface{}:
		// Special case for get-report responses where rows are in
		// result.rows/results.rows and each row can be an array.
		if rows, handled, err := extractGetReportRows(v); handled {
			return rows, err
		}

		// If this is a list response wrapper, pull out the most likely list field.
		if list := pickObjectArrayField(v); list != nil {
			return toTableRows(list)
		}
		// Otherwise treat it as a single-row table.
		return []map[string]interface{}{v}, nil
	default:
		return nil, fmt.Errorf("error building table. Must be array of objects")
	}
}

func extractGetReportRows(root map[string]interface{}) ([]map[string]interface{}, bool, error) {
	containers := []string{"result", "results"}
	for _, key := range containers {
		rawContainer, ok := root[key]
		if !ok {
			continue
		}

		container, ok := rawContainer.(map[string]interface{})
		if !ok {
			continue
		}

		rawRows, ok := container["rows"]
		if !ok {
			continue
		}

		rowItems, ok := rawRows.([]interface{})
		if !ok {
			// It looked like a get-report container, but rows is malformed.
			return nil, true, fmt.Errorf("error building table. result.rows must be an array")
		}

		colNames := readReportSchemaColumnNames(container["schema"])
		rows := make([]map[string]interface{}, 0, len(rowItems))
		for _, item := range rowItems {
			switch row := item.(type) {
			case map[string]interface{}:
				rows = append(rows, row)
			case []interface{}:
				obj := map[string]interface{}{}
				for i, cell := range row {
					obj[reportColumnName(colNames, i)] = cell
				}
				rows = append(rows, obj)
			default:
				// Defensive fallback for unexpected scalar rows.
				obj := map[string]interface{}{
					reportColumnName(colNames, 0): row,
				}
				rows = append(rows, obj)
			}
		}
		return rows, true, nil
	}

	return nil, false, nil
}

func readReportSchemaColumnNames(rawSchema interface{}) []string {
	schema, ok := rawSchema.([]interface{})
	if !ok {
		return nil
	}

	names := make([]string, 0, len(schema))
	for _, col := range schema {
		if m, ok := col.(map[string]interface{}); ok {
			if n, ok := m["name"].(string); ok && strings.TrimSpace(n) != "" {
				names = append(names, n)
			}
		}
	}
	return names
}

func reportColumnName(schemaCols []string, i int) string {
	if i >= 0 && i < len(schemaCols) {
		return schemaCols[i]
	}
	return fmt.Sprintf("col_%d", i+1)
}

func pickObjectArrayField(m map[string]interface{}) interface{} {
	// Prefer common patterns if present.
	if v, ok := m["items"]; ok {
		if isObjectArray(v) {
			return v
		}
	}

	// Otherwise pick the largest array-of-objects field.
	bestKey := ""
	bestLen := -1
	for k, v := range m {
		arr, ok := v.([]interface{})
		if !ok {
			continue
		}
		if !isObjectArray(arr) {
			continue
		}
		if len(arr) > bestLen {
			bestKey = k
			bestLen = len(arr)
		}
	}
	if bestKey == "" {
		return nil
	}
	return m[bestKey]
}

func isObjectArray(v interface{}) bool {
	arr, ok := v.([]interface{})
	if !ok {
		return false
	}
	// Empty array is ambiguous; treat as acceptable so table doesn't error.
	if len(arr) == 0 {
		return true
	}
	_, ok = arr[0].(map[string]interface{})
	return ok
}

func renderTable(rows []map[string]interface{}) ([]byte, error) {
	opts := getTableOptions()

	if len(rows) == 0 {
		return []byte("No results\n"), nil
	}

	keys := collectKeys(rows, opts.columns)
	if len(keys) == 0 {
		return []byte("No results\n"), nil
	}

	// Auto-hide columns containing object values (map[...]) unless the user
	// explicitly selected columns via -C.
	var hidden []string
	if len(opts.columns) == 0 {
		keys, hidden = filterObjectColumns(rows, keys)
	}

	if len(keys) == 0 {
		return []byte("No results\n"), nil
	}

	maxColWidth := opts.maxColWidth
	if maxColWidth < 0 {
		maxColWidth = 0
	}

	terminalWidth := detectTerminalWidth(opts.width)
	contentW := measureContentWidths(rows, keys)

	colWidths := computeColumnWidths(contentW, terminalWidth, maxColWidth)
	out, err := buildTableString(rows, keys, colWidths, opts.mode)
	if err != nil {
		return nil, err
	}

	if len(hidden) > 0 {
		out += fmt.Sprintf("\nHidden columns (object values): %s\n", strings.Join(hidden, ", "))
		out += fmt.Sprintf("Use -C to include them, e.g.: -C %s\n", strings.Join(append(keys, hidden...), ","))
	}
	return []byte(out), nil
}

type tableOptions struct {
	mode        string
	columns     []string
	width       int
	maxColWidth int
}

func getTableOptions() tableOptions {
	mode := strings.ToLower(strings.TrimSpace(viper.GetString("table-mode")))
	if mode == "" {
		mode = "fit"
	}
	switch mode {
	case "fit", "wrap":
	default:
		mode = "fit"
	}

	colsRaw := strings.TrimSpace(viper.GetString("table-columns"))
	var cols []string
	if colsRaw != "" {
		for _, c := range strings.Split(colsRaw, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				cols = append(cols, c)
			}
		}
	}

	width := viper.GetInt("table-width")
	maxColWidth := viper.GetInt("table-max-col-width")

	return tableOptions{
		mode:        mode,
		columns:     cols,
		width:       width,
		maxColWidth: maxColWidth,
	}
}

func detectTerminalWidth(forced int) int {
	if forced > 0 {
		return forced
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if n, err := strconv.Atoi(cols); err == nil && n > 0 {
			return n
		}
	}
	return 120
}

func tableDisplayWidth(s string) int {
	max := 0
	for _, line := range strings.Split(s, "\n") {
		if w := runewidth.StringWidth(line); w > max {
			max = w
		}
	}
	return max
}

// measureContentWidths returns the max display width of each column's content
// across all rows (including the header key name).
func measureContentWidths(rows []map[string]interface{}, keys []string) []int {
	widths := make([]int, len(keys))
	for i, k := range keys {
		widths[i] = runewidth.StringWidth(k)
	}
	for _, row := range rows {
		for i, k := range keys {
			val := row[k]
			if s, ok := val.([]interface{}); ok {
				converted := make([]string, len(s))
				for j := range s {
					converted[j] = formatValue(s[j])
				}
				val = strings.Join(converted, ", ")
			}
			w := runewidth.StringWidth(formatValue(val))
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}

// computeColumnWidths distributes terminal width across columns. Columns that
// fit within an equal share get exactly their content width, freeing surplus
// space for columns that need more. This repeats until stable, so narrow
// columns (dates, IDs) stay compact while wider columns share the remainder.
func computeColumnWidths(contentWidths []int, terminalWidth int, maxColWidth int) []int {
	cols := len(contentWidths)
	if cols <= 0 {
		return nil
	}
	if terminalWidth <= 0 {
		terminalWidth = 120
	}

	overhead := tableOverhead(cols)
	available := terminalWidth - overhead
	if available < cols {
		available = cols
	}

	capped := cappedContentWidths(contentWidths, maxColWidth)
	widths := make([]int, cols)
	settled := make([]bool, cols)
	remaining, unsettled := settleNarrowColumns(widths, settled, capped, available, cols)
	if unsettled > 0 {
		distributeRemainder(widths, settled, remaining, unsettled, maxColWidth)
	} else if remaining > 0 {
		// All columns fit their content — distribute leftover evenly.
		distributeEvenly(widths, remaining, maxColWidth)
	}
	return widths
}

// cappedContentWidths returns content widths capped by maxColWidth (if set).
func cappedContentWidths(contentWidths []int, maxColWidth int) []int {
	capped := make([]int, len(contentWidths))
	for i, cw := range contentWidths {
		if maxColWidth > 0 && cw > maxColWidth {
			cw = maxColWidth
		}
		capped[i] = cw
	}
	return capped
}

// settleNarrowColumns assigns exact content width to columns that fit within
// an equal share, iterating until no more columns can be settled.
func settleNarrowColumns(widths []int, settled []bool, capped []int, available int, unsettled int) (remaining int, unsettledCount int) {
	remaining = available
	for unsettled > 0 {
		share := remaining / unsettled
		changed := false
		for i, cw := range capped {
			if settled[i] || cw > share {
				continue
			}
			widths[i] = cw
			remaining -= cw
			settled[i] = true
			unsettled--
			changed = true
		}
		if !changed {
			break
		}
	}
	return remaining, unsettled
}

// distributeRemainder divides leftover space evenly among unsettled columns.
func distributeRemainder(widths []int, settled []bool, remaining int, unsettled int, maxColWidth int) {
	if unsettled <= 0 {
		return
	}
	share := remaining / unsettled
	rem := remaining % unsettled
	for i := range widths {
		if settled[i] {
			continue
		}
		widths[i] = share
		if rem > 0 {
			widths[i]++
			rem--
		}
		if maxColWidth > 0 && widths[i] > maxColWidth {
			widths[i] = maxColWidth
		}
		if widths[i] < 1 {
			widths[i] = 1
		}
	}
}

// distributeEvenly spreads remaining space across all columns, respecting maxColWidth.
func distributeEvenly(widths []int, remaining int, maxColWidth int) {
	share := remaining / len(widths)
	rem := remaining % len(widths)
	for i := range widths {
		add := share
		if rem > 0 {
			add++
			rem--
		}
		widths[i] += add
		if maxColWidth > 0 && widths[i] > maxColWidth {
			widths[i] = maxColWidth
		}
	}
}

func tableOverhead(cols int) int {
	if cols <= 0 {
		return 0
	}
	// simpletable StyleUnicode: 1 right border + (1 left pad + 2 separator) per column = 1 + 3*cols
	return 1 + 3*cols
}

// formatValue converts a raw cell value to a display string. Large float64
// values that look like Unix timestamps (milliseconds since epoch, roughly
// 2001–2099) are formatted as ISO 8601 in UTC.
func formatValue(val interface{}) string {
	f, ok := val.(float64)
	if ok && f >= 1e12 && f < 4.1e12 {
		sec := int64(f) / 1000
		ms := int64(f) % 1000
		return time.Unix(sec, ms*1e6).UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%v", val)
}

func buildTableString(rows []map[string]interface{}, keys []string, colWidths []int, mode string) (string, error) {
	if len(keys) == 0 {
		return "No results\n", nil
	}
	if len(colWidths) != len(keys) {
		return "", fmt.Errorf("internal error: mismatched column widths")
	}

	table := simpletable.New()

	// Pad headers with U+2800 (Braille Pattern Blank) to enforce column widths.
	// simpletable auto-sizes columns to the widest cell; since its newContent
	// calls strings.TrimSpace (which strips regular spaces), we use U+2800 which
	// is not considered whitespace by Go's unicode.IsSpace. Body cells are left
	// unpadded so simpletable's AlignRight can position them within the column.
	header := make([]*simpletable.Cell, 0, len(keys))
	for i, k := range keys {
		header = append(header, &simpletable.Cell{
			Align: simpletable.AlignCenter,
			Text:  padCell(truncateText(k, colWidths[i]), colWidths[i]),
		})
	}
	table.Header = &simpletable.Header{Cells: header}

	for _, row := range rows {
		body := make([]*simpletable.Cell, 0, len(keys))
		for i, k := range keys {
			val := row[k]
			if s, ok := val.([]interface{}); ok {
				converted := make([]string, len(s))
				for j := range s {
					converted[j] = formatValue(s[j])
				}
				val = strings.Join(converted, ", ")
			}
			cellText := formatValue(val)
			cellText = formatCell(cellText, colWidths[i], mode)
			body = append(body, &simpletable.Cell{Align: simpletable.AlignRight, Text: cellText})
		}
		table.Body.Cells = append(table.Body.Cells, body)
	}

	table.SetStyle(simpletable.StyleUnicode)
	out := table.String()
	// Replace the U+2800 padding placeholder with real spaces.
	out = strings.ReplaceAll(out, "\u2800", " ")
	return out, nil
}

func padMultilineCell(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = padCell(lines[i], width)
	}
	return strings.Join(lines, "\n")
}

func padCell(s string, width int) string {
	if width <= 0 {
		return s
	}
	cur := runewidth.StringWidth(s)
	if cur >= width {
		return s
	}
	// Use Braille Pattern Blank (U+2800) instead of spaces because simpletable's
	// newContent calls strings.TrimSpace on cell text, which would strip regular
	// space padding and cause columns to shrink to content width. U+2800 is not
	// considered whitespace by Go's unicode.IsSpace, so it survives the trim.
	// buildTableString replaces U+2800 back to spaces in the final output.
	return s + strings.Repeat("\u2800", width-cur)
}

// filterObjectColumns splits keys into visible and hidden. A column is hidden
// if any row contains a nested object (map) either directly or inside an array.
func filterObjectColumns(rows []map[string]interface{}, keys []string) (visible, hidden []string) {
	for _, k := range keys {
		isObject := false
		for _, row := range rows {
			if containsObject(row[k]) {
				isObject = true
				break
			}
		}
		if isObject {
			hidden = append(hidden, k)
		} else {
			visible = append(visible, k)
		}
	}
	return visible, hidden
}

// containsObject returns true if val is a map or an array containing a map.
func containsObject(val interface{}) bool {
	switch v := val.(type) {
	case map[string]interface{}:
		return true
	case []interface{}:
		for _, item := range v {
			if _, ok := item.(map[string]interface{}); ok {
				return true
			}
		}
	}
	return false
}

func collectKeys(rows []map[string]interface{}, preferred []string) []string {
	if len(preferred) > 0 {
		return preferred
	}

	keys := make([]string, 0, 16)
	seen := map[string]bool{}
	for _, row := range rows {
		for k := range row {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func formatCell(val string, width int, mode string) string {
	if width <= 0 {
		return val
	}
	switch mode {
	case "wrap":
		return wrapText(val, width)
	default:
		return truncateText(val, width)
	}
}

func wrapText(s string, width int) string {
	var lines []string
	var current strings.Builder
	currentWidth := 0
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
			continue
		}

		rw := runewidth.RuneWidth(r)
		if rw < 0 {
			rw = 0
		}
		if currentWidth+rw > width && current.Len() > 0 {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
		}

		current.WriteRune(r)
		currentWidth += rw
		if currentWidth >= width {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
		}
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return strings.Join(lines, "\n")
}

func truncateText(s string, width int) string {
	if width <= 0 {
		return s
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}

	ellipsis := "…"
	ellipsisWidth := runewidth.StringWidth(ellipsis)
	if width <= ellipsisWidth {
		return ellipsis
	}

	// Leave room for ellipsis.
	target := width - ellipsisWidth
	if target < 1 {
		target = 1
	}
	var b strings.Builder
	curWidth := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if rw < 0 {
			rw = 0
		}
		if curWidth+rw > target {
			break
		}
		b.WriteRune(r)
		curWidth += rw
	}
	b.WriteString(ellipsis)
	return b.String()
}
