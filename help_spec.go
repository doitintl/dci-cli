package main

// Help without credentials.
//
// Every `--help` under the API subcommand renders from the OpenAPI
// description: restish hydrates the operation commands from it inside
// cli.Run, and that hydration (cli.Load) fetches the description through
// MakeRequest — the same path a data request takes, auth handler included.
// With no cached token and no browser to open, the OAuth handler fails and
// restish panics, so `dci list-budgets --help` on a fresh install died with
// "no credentials available" (and `dci --help` with an expired spec cache
// did the same). The description itself is public — `dci commands` has
// always fetched /openapi.yaml without a token — so this chapter fetches it
// the same way for help invocations and writes it into restish's own spec
// cache (dci.cbor plus the dci.expires stamp in cache.json, shaped exactly
// as cli.Load writes them) before cli.Run starts. The in-run cli.Load then
// takes its cache path and never reaches the auth handler.
//
// Data commands are untouched: their spec load still goes through restish,
// and their credential checks (invocation_preflight.go) run as before.

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// specCacheTTL mirrors cli.Load's cacheAPI: a fetched description is good
// for a day before the next invocation refreshes it.
const specCacheTTL = 24 * time.Hour

// helpInvocationNeedsSpec reports whether args (after normalizeArgs, so an
// API invocation reads `dci dci <command> ...`) is a help request that
// renders from the API description. Everything under the API subcommand
// qualifies — restish's cli.Run loads the description for any `dci dci ...`
// argv before cobra dispatches, so even `dci beta --help` and the local
// question commands' help need a warm cache to get past that load. Root
// help, `dci help`, and local commands such as `dci status --help` never
// reach that load and stay as they are.
func helpInvocationNeedsSpec(args []string) bool {
	if len(args) < 3 || args[1] != "dci" {
		return false
	}
	return invocationRequestsHelp(args)
}

// refreshSpecCacheUnauthenticated fetches the public API description the
// way the command catalog does (no credentials attached) and writes it into
// restish's spec cache for the CURRENT cache directory — under a
// DCI_API_BASE_URL override that is applyAPIBaseOverride's temp dir, which
// is exactly where the in-run cli.Load will look. A var so tests can stand
// in for the network.
var refreshSpecCacheUnauthenticated = func() error {
	api, err := loadCatalogAPI(&cobra.Command{})
	if err != nil {
		return err
	}
	if len(api.Operations) == 0 {
		return errors.New("the API description lists no operations")
	}
	return writeSpecCache(restishCacheDir(), api)
}

// writeSpecCache persists api as restish's spec cache in cacheDir: the CBOR
// document cli.Load reads back, stamped with the version it compares
// against (the API subcommand carries none, so the stamp is empty — the
// same value cli.Load's own cacheAPI records), plus the dci.expires key in
// cache.json that gates the cache path. The expiry lands in the in-memory
// cache first so this very invocation's cli.Load honors it even if the
// write-through to disk fails; a failed write-through only costs the next
// invocation a refetch, which is what restish itself settles for.
func writeSpecCache(cacheDir string, api cli.API) error {
	if cli.Cache == nil {
		return errors.New("restish CLI is not initialized")
	}
	if cacheDir == "" {
		return errors.New("no cache directory is available for the API description")
	}
	api.RestishVersion = ""
	if cli.Root != nil {
		if dciCommand := findDCICommand(); dciCommand != nil {
			api.RestishVersion = dciCommand.Version
		}
	}
	blob, err := cbor.Marshal(&api)
	if err != nil {
		return fmt.Errorf("encode API description: %w", err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "dci.cbor"), blob, 0o600); err != nil {
		return err
	}
	cli.Cache.Set("dci.expires", time.Now().Add(specCacheTTL))
	_ = cli.Cache.WriteConfig()
	return nil
}

// prepareSpecForHelp makes sure a help invocation under the API subcommand
// finds a warm spec cache, so restish never has to fetch (and so never has
// to authenticate) to render it. A fetch failure is fatal only when there
// are no stored credentials to fall back on: with credentials, restish's
// own authenticated load gets its turn — the public fetch may legitimately
// fail against a host that guards its description, and the authenticated
// path is the one data commands already rely on there. Without them, the
// old behavior was the browser-login wait (human terminal) or a bogus "no
// credentials" error (headless); the real cause — the description could
// not be fetched — is what the user needs to see.
func prepareSpecForHelp() error {
	if specCacheWarm(restishCacheDir()) {
		return nil
	}
	err := refreshSpecCacheUnauthenticated()
	if err == nil || invocationCredentialsAvailable() {
		return nil
	}
	return helpSpecUnavailableError(err)
}

func helpSpecUnavailableError(cause error) error {
	base, baseErr := apiBase()
	if baseErr != nil {
		base = "the API"
	}
	detail := structuredErrorForExecution(cause, 0)
	detail.Message = fmt.Sprintf("could not load the command reference from %s: %v", base, cause)
	detail.Hint = "Check network connectivity and retry. Help renders without credentials; with DCI_API_KEY set or after dci login the CLI can also load the reference through the authenticated API"
	return invocationPreflightError{
		detail:   detail,
		exitCode: exitCodeForExecutionError(cause, 0),
	}
}

// configureSpecHTTPClientTLS makes the unauthenticated description fetches
// (this chapter, `dci commands`, the help-context enrichment) honor the
// same TLS trust decision restish applies to every data request: the
// `dci.tls.insecure` flag in apis.json, or --rsh-insecure for the
// invocation. Without it a self-signed dev or test host that data commands
// reach fine would fail every public fetch with a certificate error.
func configureSpecHTTPClientTLS(configDir string) {
	if !viper.GetBool("rsh-insecure") && !configTLSInsecure(filepath.Join(configDir, "apis.json")) {
		return
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	transport = transport.Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opted into by the user's own apis.json, exactly as restish honors it
	specHTTPClient.Transport = transport
}

func configTLSInsecure(configFile string) bool {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return false
	}
	var config struct {
		DCI struct {
			TLS struct {
				Insecure bool `json:"insecure"`
			} `json:"tls"`
		} `json:"dci"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return false
	}
	return config.DCI.TLS.Insecure
}

// recoveringRun turns an error restish panics with before its own recovery
// is armed into a returned error. cli.Run arms its recover only around
// Root.Execute; the API description load it performs first (Load →
// MakeRequest → the auth handler) panics on failure outside it, so a plain
// "no credentials" condition surfaced as "dci encountered an internal
// error" with exit 1 on one path and as a proper error envelope on the
// other, depending on which load hit it. Non-error panics stay panics:
// those are bugs, and run()'s handler labels them as such.
func recoveringRun(run func() error) func() error {
	return func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recoveredErr, ok := recovered.(error); ok {
					err = recoveredErr
					return
				}
				panic(recovered)
			}
		}()
		return run()
	}
}
