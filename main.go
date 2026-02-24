package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

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

	if _, err := os.Stat(configFile); err == nil {
		if err := tightenFilePermissions(configFile, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unable to tighten config permissions for %s: %v\n", configFile, err)
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

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		return false, err
	}
	if err := tightenFilePermissions(configFile, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "warning: unable to tighten config permissions for %s: %v\n", configFile, err)
	}

	return true, nil
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

	cli.Load("dci", cli.Root)
	brandDCIRootCommand()
	registerStatusCommands(configDir)
	registerCustomerContextCommands(configDir)
	addOutputFlag()
	hideGlobalFlags()
	customizeDCIUsage()
	applyCustomerContext(configDir)
	lockToDCI()
	os.Args = normalizeArgs(os.Args)

	if err := cli.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	return cli.GetExitCode()
}

func rejectProfileFlags(args []string) error {
	for _, arg := range args[1:] {
		if arg == "--profile" || arg == "--rsh-profile" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "--rsh-profile=") {
			return fmt.Errorf("profile selection is currently disabled")
		}
		if arg == "-p" || strings.HasPrefix(arg, "-p=") || strings.HasPrefix(arg, "-p") {
			return fmt.Errorf("profile selection is currently disabled")
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg[1:], 'p') {
			return fmt.Errorf("profile selection is currently disabled")
		}
	}
	return nil
}

func normalizeArgs(args []string) []string {
	if len(args) <= 1 {
		return []string{args[0], "--help"}
	}

	cmd := firstCommandArg(args)
	if cmd == "" || cmd == "help" || cmd == "version" || isRootCommand(cmd) {
		return args
	}

	return append([]string{args[0], "dci"}, args[1:]...)
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

func customizeDCIUsage() {
	cobra.AddTemplateFunc("hasVisibleCommandsInGroup", func(cmds []*cobra.Command, groupID string) bool {
		for _, cmd := range cmds {
			if cmd.GroupID == groupID && (cmd.IsAvailableCommand() || cmd.Name() == "help") {
				return true
			}
		}
		return false
	})

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

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.SetUsageTemplate(dciUsageTemplate)
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(dciCmd)
}

func lockToDCI() {
	// Remove API management commands, generic RESTish commands, and any
	// additional API entrypoints so users can only call the DCI API.
	allowed := map[string]bool{
		"dci":  true,
		"help": true,
	}
	toRemove := make([]*cobra.Command, 0)
	for _, cmd := range cli.Root.Commands() {
		if allowed[cmd.Name()] {
			continue
		}

		if cmd.Name() == "api" || cmd.Name() == "completion" || cmd.GroupID == "generic" || (cmd.GroupID == "api" && cmd.Name() != "dci") {
			toRemove = append(toRemove, cmd)
		}
	}
	for _, cmd := range toRemove {
		cli.Root.RemoveCommand(cmd)
	}
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

func brandDCIRootCommand() {
	for _, cmd := range cli.Root.Commands() {
		if cmd.Name() != "dci" {
			continue
		}

		cmd.Short = "DoiT Cloud Intelligence API CLI"
		cmd.Long = "Command-line interface for the DoiT Cloud Intelligence API."

		cmd.Example = strings.Join([]string{
			"  dci list-budgets",
			"  dci list-reports --output table",
			"  dci query body.query:\"SELECT * FROM aws_cur_2_0 LIMIT 10\"",
		}, "\n")
		return
	}
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

		fmt.Fprintln(os.Stdout, "DoiT Cloud Intelligence")
		fmt.Fprintln(os.Stdout, "API Base: https://api.doit.com")
		fmt.Fprintf(os.Stdout, "Default Output: %s\n", currentOutput())
		fmt.Fprintf(os.Stdout, "Config Dir: %s\n", configDir)
		if ctx != "" {
			fmt.Fprintf(os.Stdout, "Internal customer context: %s\n", ctx)
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

func addOutputFlag() {
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

	dciCmd.PersistentFlags().String("output", "", "Output format: table, json, yaml, auto (default: table)")
	dciCmd.PersistentFlags().StringP("table-mode", "M", "fit", "Table rendering: fit (truncate) or wrap (multi-line)")
	dciCmd.PersistentFlags().StringP("table-columns", "C", "", "Comma-separated list of columns to include (default: all)")
	dciCmd.PersistentFlags().IntP("table-width", "W", 0, "Table width in columns (default: auto-detect terminal width)")
	dciCmd.PersistentFlags().IntP("table-max-col-width", "X", 0, "Maximum width per column when fitting or wrapping (0 = auto)")

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
			if err := defaultToBodyOutput(); err != nil {
				return err
			}
		} else {
			out := strings.TrimSpace(outFlag.Value.String())
			switch out {
			case "table", "json", "yaml", "auto":
				viper.Set("rsh-output-format", out)
				if err := defaultToBodyOutput(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("invalid --output %q (supported: table, json, yaml, auto)", out)
			}
		}

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
		if flag := cmd.Flags().Lookup("table-width"); flag != nil {
			width, _ := strconv.Atoi(flag.Value.String())
			if width < 0 {
				width = 0
			}
			viper.Set("table-width", width)
		}
		if flag := cmd.Flags().Lookup("table-max-col-width"); flag != nil {
			maxw, _ := strconv.Atoi(flag.Value.String())
			if maxw < 0 {
				maxw = 0
			}
			viper.Set("table-max-col-width", maxw)
		}

		return nil
	}
}

func defaultToBodyOutput() error {
	// By default restish prints response status + headers for TTY output when no
	// filter is specified. This CLI is primarily focused on the response body,
	// so default to `body` unless the user explicitly requested raw output or a
	// filter was already set.
	if viper.GetBool("rsh-raw") {
		return nil
	}
	if viper.GetString("rsh-filter") == "" {
		viper.Set("rsh-filter", "body")
	}
	return nil
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
		return nil, err
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

	maxColWidth := opts.maxColWidth
	if maxColWidth < 0 {
		maxColWidth = 0
	}

	terminalWidth := detectTerminalWidth(opts.width)
	colWidths := computeColumnWidths(len(keys), terminalWidth, maxColWidth)
	out, err := buildTableString(rows, keys, colWidths, opts.mode, terminalWidth)
	if err != nil {
		return nil, err
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

var tableOverheadCache = map[int]int{}

func computeColumnWidths(cols int, terminalWidth int, maxColWidth int) []int {
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

	widths := make([]int, cols)
	base := available / cols
	rem := available % cols
	for i := 0; i < cols; i++ {
		w := base
		if i < rem {
			w++
		}
		if w < 1 {
			w = 1
		}
		if maxColWidth > 0 && w > maxColWidth {
			w = maxColWidth
		}
		widths[i] = w
	}
	return widths
}

func tableOverhead(cols int) int {
	if cols <= 0 {
		return 0
	}
	if v, ok := tableOverheadCache[cols]; ok {
		return v
	}

	keys := make([]string, cols)
	rows := []map[string]interface{}{{}}
	for i := 0; i < cols; i++ {
		keys[i] = fmt.Sprintf("c%d", i)
		rows[0][keys[i]] = "a"
	}

	widths := make([]int, cols)
	for i := 0; i < cols; i++ {
		widths[i] = 1
	}

	s, _ := buildTableString(rows, keys, widths, "fit", 0)
	overhead := tableDisplayWidth(s) - cols
	if overhead < 0 {
		overhead = 0
	}
	tableOverheadCache[cols] = overhead
	return overhead
}

func buildTableString(rows []map[string]interface{}, keys []string, colWidths []int, mode string, targetWidth int) (string, error) {
	if len(keys) == 0 {
		return "No results\n", nil
	}
	if len(colWidths) != len(keys) {
		return "", fmt.Errorf("internal error: mismatched column widths")
	}

	table := simpletable.New()

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
				for i := 0; i < len(s); i++ {
					converted[i] = fmt.Sprintf("%v", s[i])
				}
				val = strings.Join(converted, ", ")
			}
			cellText := fmt.Sprintf("%v", val)
			cellText = formatCell(cellText, colWidths[i], mode)
			cellText = padMultilineCell(cellText, colWidths[i])
			body = append(body, &simpletable.Cell{Align: simpletable.AlignRight, Text: cellText})
		}
		table.Body.Cells = append(table.Body.Cells, body)
	}

	table.SetStyle(simpletable.StyleUnicode)
	out := table.String()
	if targetWidth > 0 {
		// Fine-tune: if the calculated overhead was slightly off, adjust the last
		// column to avoid overflow or excess slack.
		w := tableDisplayWidth(out)
		if len(colWidths) > 0 {
			if w > targetWidth {
				delta := w - targetWidth
				colWidths[len(colWidths)-1] = max(1, colWidths[len(colWidths)-1]-delta)
				return buildTableString(rows, keys, colWidths, mode, 0)
			}
			if w < targetWidth {
				delta := targetWidth - w
				colWidths[len(colWidths)-1] = colWidths[len(colWidths)-1] + delta
				return buildTableString(rows, keys, colWidths, mode, 0)
			}
		}
	}
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
	return s + strings.Repeat(" ", width-cur)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
