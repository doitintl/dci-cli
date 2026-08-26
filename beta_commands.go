// Beta commands: the `dci beta` surface for not-yet-GA API operations
// (BETA-SPEC.md). The command tree hydrates from an embedded, generator-
// produced OpenAPI spec (beta/openapi.beta.yaml, curated by beta/manifest.yaml
// — see tools/betaspec), so it needs no network fetch, no spec cache, and no
// second restish API entry: beta commands mount under the hidden `dci`
// subcommand and inherit its auth, customer context, output flags, and
// error contract. The endpoints themselves live on the production API,
// gated server-side per customer by early-access feature flags; the CLI
// performs no client-side entitlement check (gcloud model) and instead maps
// the server's 404 to an enrollment hint.
package main

//go:generate go run ./tools/betaspec

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gosimple/slug"
	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"
)

//go:embed beta/openapi.beta.yaml
var embeddedBetaSpec []byte

//go:embed beta/manifest.yaml
var embeddedBetaManifest []byte

const betaShortDescription = "Early-access commands (beta; may change or be removed)"

const betaLongDescription = "Early-access commands for API capabilities that are not yet generally available.\n\n" +
	"Beta commands run against the production API with your normal credentials, but the\n" +
	"endpoints behind them are gated per customer: most require enrollment in an\n" +
	"early-access feature (shown in each command's help) and return 404 until enrolled.\n" +
	"Command names, flags, and output shapes may change or be removed without notice —\n" +
	"pin scripts to GA commands once a feature graduates."

// betaManifestEntry mirrors beta/manifest.yaml — the CLI reads it for the
// early-access flag mapping (keyed by cliName, the command name).
type betaManifestEntry struct {
	OperationID string `yaml:"operationId"`
	CLIName     string `yaml:"cliName"`
	EarlyAccess string `yaml:"earlyAccess"`
}

var betaEarlyAccessByCommand = loadBetaEarlyAccess()

func loadBetaEarlyAccess() map[string]string {
	var manifest struct {
		Operations []betaManifestEntry `yaml:"operations"`
	}
	// The manifest is embedded at build time; a parse failure is a build
	// defect caught by tests, and degrading to no hints keeps the CLI usable.
	if err := yaml.Unmarshal(embeddedBetaManifest, &manifest); err != nil {
		return map[string]string{}
	}
	byCommand := make(map[string]string, len(manifest.Operations))
	for _, entry := range manifest.Operations {
		if entry.CLIName != "" && entry.EarlyAccess != "" {
			byCommand[entry.CLIName] = entry.EarlyAccess
		}
	}
	return byCommand
}

// registerBetaCommands mounts the `beta` subcommand under the hidden `dci`
// API command so normalizeArgs routes `dci beta …` and the children inherit
// every persistent output/context flag. The operation subtree hydrates only
// when the invocation mentions beta — parsing the embedded spec is local but
// not free, and every other invocation skips it.
func registerBetaCommands() {
	dciCommand := findDCICommand()
	if dciCommand == nil {
		return
	}
	betaCommand := &cobra.Command{
		Use:   "beta",
		Short: betaShortDescription,
		Long:  betaLongDescription,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return command.Help()
		},
	}
	dciCommand.AddCommand(betaCommand)
	if !betaInvocationRequested(os.Args) {
		return
	}
	if err := hydrateBetaCommands(betaCommand); err != nil {
		// Surface loudly: an embedded spec that fails to hydrate is a build
		// defect, not a user error, and silence would present an empty tree.
		fmt.Fprintf(os.Stderr, "warning: beta commands unavailable: %v\n", err)
	}
}

// betaInvocationRequested reports whether the invocation plausibly targets the
// beta tree. False positives (a positional argument literally named "beta")
// only cost the embedded-spec parse; false negatives would present an empty
// beta tree, so the scan is deliberately broad: any bare "beta" word before
// the positional-operand separator.
func betaInvocationRequested(args []string) bool {
	for _, argument := range args[1:] {
		if argument == "--" {
			return false
		}
		if argument == "beta" {
			return true
		}
	}
	return false
}

// loadBetaAPI parses the embedded beta spec through the same restish OpenAPI
// loader the GA surface uses, resolved against the configured API base so
// every operation URI targets prod (or DCI_API_BASE_URL when set).
func loadBetaAPI() (cli.API, error) {
	base, err := apiBase()
	if err != nil {
		return cli.API{}, err
	}
	entrypoint, err := url.Parse(base + "/")
	if err != nil {
		return cli.API{}, err
	}
	// The spec URL is synthetic — content comes from the embedded file — but
	// the loader uses it to resolve relative references.
	specURL, err := url.Parse(base + "/openapi.beta.yaml")
	if err != nil {
		return cli.API{}, err
	}
	response := &http.Response{
		Proto:      "HTTP/1.1",
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(embeddedBetaSpec)),
	}
	return openapi.New().Load(*entrypoint, *specURL, response)
}

func hydrateBetaCommands(betaCommand *cobra.Command) error {
	api, err := loadBetaAPI()
	if err != nil {
		return err
	}
	if len(api.Operations) == 0 {
		return fmt.Errorf("embedded beta spec contains no operations")
	}
	installBetaResponseInspector()
	for _, operation := range api.Operations {
		if operation.Hidden {
			continue
		}
		if operation.Group != "" && !betaCommand.ContainsGroup(operation.Group) {
			title := cases.Title(language.Und, cases.NoLower).String(operation.Group)
			betaCommand.AddGroup(&cobra.Group{ID: operation.Group, Title: title + " Commands (beta):"})
		}
		betaCommand.AddCommand(betaOperationCommand(operation))
	}
	return nil
}

// registerBetaResolutionMetadata wires the resolvable beta operations into
// the name-resolution metadata after the GA index is built (called from
// setOperationMetadata, so every rebuild carries the keys). Only run-report
// resolves: its path parameter is a saved report ID, and reports have names
// and a GA list endpoint — reusing get-report's target keeps the list path
// spec-derived instead of hardcoded. The other beta operations take ephemeral
// operation IDs, which have no name source. Two keys per command: the AI
// session routes and dispatches on the "beta run-report" spelling, while the
// in-process hooks (resolvePathArguments, the zero-argument picker) look up
// the cobra command name — registered only while no GA operation claims it.
func registerBetaResolutionMetadata() {
	target, ok := resolutionIndex["get-report"]
	if !ok {
		return
	}
	resolutionIndex["beta run-report"] = target
	if _, claimed := resolutionIndex["run-report"]; !claimed {
		resolutionIndex["run-report"] = target
	}
}

// betaResolutionReady loads the operation metadata behind name resolution
// (offline, from the cached GA spec — registerBetaResolutionMetadata rides
// setOperationMetadata). False, never an error, when the metadata is
// unavailable (no cached spec yet): resolution and the picker simply stay
// off, exactly like the GA surface before first login.
func betaResolutionReady() bool {
	return ensureDestructiveOperations() == nil
}

// betaOperationCommand builds the cobra command for one beta operation. It
// mirrors restish's unexported Operation.command() — path params as
// positionals, query/header params as flags, body via stdin or shorthand
// args, MakeRequestAndFormat for transport and rendering — with three beta
// additions: a stderr notice, auto-generated Idempotency-Key headers, and an
// early-access hint on 404.
func betaOperationCommand(operation cli.Operation) *cobra.Command {
	flagValues := map[string]interface{}{}

	use := slug.Make(operation.Name)
	for _, parameter := range operation.PathParams {
		use += " " + slug.Make(parameter.Name)
	}

	argSpec := cobra.ExactArgs(len(operation.PathParams))
	if operation.BodyMediaType != "" {
		argSpec = cobra.MinimumNArgs(len(operation.PathParams))
	}

	command := &cobra.Command{
		Use:     use,
		GroupID: operation.Group,
		Aliases: operation.Aliases,
		Short:   "(beta) " + operation.Short,
		Long:    betaOperationLong(operation),
		// Args is assigned below the literal: the relaxed validator needs the
		// command value itself for the GA-parity resolution gates.
		Hidden:     operation.Hidden,
		Deprecated: operation.Deprecated,
		RunE: func(command *cobra.Command, args []string) error {
			printBetaNotice(command.Name())

			// Name resolution (name → ID, joined multi-word names, the
			// zero-argument picker) already ran: beta commands inherit the
			// dci command's PersistentPreRunE, whose resolvePathArguments
			// call finds them through the keys registerBetaResolutionMetadata
			// added. Never resolve again here — a second pass would rejoin a
			// resolved ID with a multi-word name's leftover words and re-open
			// the picker. Only the picker's selection needs injecting: GA
			// leaves get installPickerArgInjection, beta does it inline.
			if len(args) == 0 && pickedPathArgument != "" {
				args = []string{pickedPathArgument}
			}
			if len(args) < len(operation.PathParams) {
				// The relaxed Args validator admits a zero-argument resolvable
				// invocation for the picker's sake; with no selection made the
				// original arity error stands ("accepts " keeps exit 2 via
				// isUsageError).
				return fmt.Errorf("accepts %d arg(s), received %d", len(operation.PathParams), len(args))
			}

			uri := operation.URITemplate
			for index, parameter := range operation.PathParams {
				value, err := parameter.Parse(args[index])
				if err != nil {
					return fmt.Errorf("could not parse argument %s: %w", parameter.Name, err)
				}
				uri = strings.Replace(uri, "{"+parameter.Name+"}", fmt.Sprintf("%v", value), 1)
			}

			query := url.Values{}
			for _, parameter := range operation.QueryParams {
				if !command.Flags().Changed(parameter.OptionName()) {
					continue
				}
				for _, value := range parameter.Serialize(flagValues[parameter.Name]) {
					query.Add(parameter.Name, value)
				}
			}
			if encoded := query.Encode(); encoded != "" {
				separator := "?"
				if strings.Contains(uri, "?") {
					separator = "&"
				}
				uri += separator + encoded
			}

			headers := http.Header{}
			for _, parameter := range operation.HeaderParams {
				if !command.Flags().Changed(parameter.OptionName()) {
					// The async endpoints require an Idempotency-Key on every
					// submit/cancel; deduplication is content-based server-side,
					// so a fresh generated key per invocation is correct and the
					// flag stays available for explicit replay semantics.
					if isIdempotencyKeyParam(parameter.Name) {
						key, err := generateBetaIdempotencyKey()
						if err != nil {
							return err
						}
						headers.Add(parameter.Name, key)
					}
					continue
				}
				for _, value := range parameter.Serialize(flagValues[parameter.Name]) {
					headers.Add(parameter.Name, value)
				}
			}

			var body io.Reader
			if operation.BodyMediaType != "" {
				content, err := cli.GetBody(operation.BodyMediaType, args[len(operation.PathParams):])
				if err != nil {
					return err
				}
				body = strings.NewReader(content)
			}

			request, err := http.NewRequest(operation.Method, uri, body)
			if err != nil {
				return err
			}
			request.Header = headers
			cli.MakeRequestAndFormat(request)
			maybeHintBetaEarlyAccess(betaEarlyAccessByCommand[operation.Name])
			return nil
		},
	}

	for _, parameter := range operation.QueryParams {
		flagValues[parameter.Name] = parameter.AddFlag(command.Flags())
	}
	for _, parameter := range operation.HeaderParams {
		flagValues[parameter.Name] = parameter.AddFlag(command.Flags())
	}

	// GA-parity relaxation of the arity check (relaxResolvableArgsValidation
	// only walks the GA operations): a resolvable beta command accepts zero
	// arguments when the picker can supply one, and surplus positionals when
	// they are the shell-split words of an unquoted name. Metadata loads
	// lazily here because the gates need the resolution index, which does not
	// exist at mount time.
	command.Args = func(command *cobra.Command, args []string) error {
		if betaResolutionReady() {
			if len(args) == 0 && zeroArgPickerApplies(command) {
				return nil
			}
			if joinableNameArguments(command, args) {
				return nil
			}
		}
		return argSpec(command, args)
	}

	return command
}

func betaOperationLong(operation cli.Operation) string {
	long := strings.TrimSpace(operation.Long)
	notice := "BETA: this command may change or be removed without notice."
	if flag := betaEarlyAccessByCommand[operation.Name]; flag != "" {
		notice += fmt.Sprintf(" Requires early-access enrollment: %s.", flag)
	}
	if long == "" {
		return notice + "\n"
	}
	return notice + "\n\n" + long + "\n"
}

func isIdempotencyKeyParam(name string) bool {
	return strings.EqualFold(name, "Idempotency-Key")
}

func generateBetaIdempotencyKey() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return "dci-" + hex.EncodeToString(buffer), nil
}

// printBetaNotice emits the per-invocation instability reminder. Stderr only
// so piped stdout stays clean; suppressed in agent mode where it is chatter
// outside the structured contract.
func printBetaNotice(commandName string) {
	if agentMode {
		return
	}
	fmt.Fprintf(os.Stderr, "note: %s is a beta command — early access; behavior may change.\n", commandName)
}

// lastBetaResponseErrorCode holds the typed error code (RFC 9457 `code`
// field) of the most recent 404 rendered during a beta invocation, or "" when
// the 404 carried no typed code. A typed code means a specific handler
// answered — the target genuinely doesn't exist — while the early-access
// gate's 404 is codeless, so the enrollment hint applies only to the latter.
var lastBetaResponseErrorCode string

type betaResponseInspector struct {
	next cli.ResponseFormatter
}

func (inspector betaResponseInspector) Format(response cli.Response) error {
	lastBetaResponseErrorCode = ""
	if response.Status == http.StatusNotFound {
		if body, ok := response.Body.(map[string]interface{}); ok {
			if code, ok := body["code"].(string); ok {
				lastBetaResponseErrorCode = code
			}
		}
	}
	return inspector.next.Format(response)
}

func installBetaResponseInspector() {
	if cli.Formatter == nil {
		return
	}
	if _, installed := cli.Formatter.(betaResponseInspector); installed {
		return
	}
	cli.Formatter = betaResponseInspector{next: cli.Formatter}
}

// maybeHintBetaEarlyAccess explains the most likely cause of a 404 from a
// flag-gated beta endpoint: the customer is not enrolled in the early-access
// feature. Genuine typed 404s (e.g. an unknown operation id) carry an error
// code and skip the hint; the gate's 404 is codeless. Suppressed under the
// agent error contract, mirroring maybeHintDoerContext.
func maybeHintBetaEarlyAccess(earlyAccessFlag string) {
	if earlyAccessFlag == "" || agentErrorContractEnabled() || cli.GetLastStatus() != http.StatusNotFound {
		return
	}
	if lastBetaResponseErrorCode != "" {
		return
	}
	context := activeCustomerContext()
	if context == "" {
		context = "<customer>"
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "! This 404 usually means the customer is not enrolled in the early-access\n")
	fmt.Fprintf(os.Stderr, "  feature gating this beta endpoint (%s).\n", earlyAccessFlag)
	fmt.Fprintf(os.Stderr, "  Enroll at https://console.doit.com/customers/%s/early-access-features\n", context)
	fmt.Fprintf(os.Stderr, "  or ask your DoiT account team, then retry.\n")
}

// preflightBetaInvocation validates `dci dci beta <command>` invocations the
// way preflightAPIInvocation validates GA ones: authentication availability
// first, then command-name validation against the embedded beta surface with
// a did-you-mean. args are the normalized os.Args ([dci, dci, beta, ...]).
func preflightBetaInvocation(args []string) error {
	commandName := betaInvocationCommandName(args)
	authenticated := invocationCredentialsAvailable()
	interactive := invocationInteractive()
	if commandName == "" || invocationRequestsHelp(args) {
		return nil
	}
	api, err := loadBetaAPI()
	if err != nil || len(api.Operations) == 0 {
		// Hydration already warned; let cobra produce its own error.
		return nil
	}
	operation := invocationOperation(api, commandName)
	if operation == nil {
		return unknownBetaCommandPreflightError(api, commandName)
	}
	if authenticated || interactive || invocationHasFlag(args, "--dry-run") {
		return nil
	}
	return authenticationRequiredPreflightError()
}

func betaInvocationCommandName(args []string) string {
	if cli.Root == nil {
		return commandArg(args, 3)
	}
	rootFlags := cli.Root.PersistentFlags()
	if dciCommand := findDCICommand(); dciCommand != nil {
		return commandArg(args, 3, rootFlags, dciCommand.PersistentFlags())
	}
	return commandArg(args, 3, rootFlags)
}

func unknownBetaCommandPreflightError(api cli.API, name string) error {
	suggestion := closestAPICommand(api, name)
	message := fmt.Sprintf("unknown beta command %q", name)
	hint := "Run dci beta --help to list available beta commands"
	if suggestion != "" {
		message = fmt.Sprintf("unknown beta command %q (did you mean %q?)", name, suggestion)
		hint = fmt.Sprintf("Did you mean %q? Run dci beta --help to list available beta commands", suggestion)
	}
	return invocationPreflightError{
		detail:   structuredError{Code: "USAGE_ERROR", Message: message, Hint: hint, Retryable: false},
		exitCode: exitUsage,
	}
}
