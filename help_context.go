// Chapter: context-rich help. restish renders the CLI's help surface from
// the DoiT OpenAPI spec, but drops two things the spec already carries: the
// one-line description on each tag (so `dci --help` shows bare category
// headers like "Budgets Commands:" with no explanation) and each
// parameter's `example` (so `dci <cmd> --help` never shows one, even though
// restish parses it into cli.Param.Example and command_catalog.go already
// surfaces it in the JSON catalog). This file fills in both, cached
// alongside restish's own 24h API cache and bypassed by the same
// --rsh-no-cache flag.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// helpContext holds the two spec-carried fields restish's help rendering
// drops on the floor. Cached to disk verbatim (as YAML) between refreshes.
type helpContext struct {
	// TagDescriptions maps an OpenAPI tag name to its one-line description.
	TagDescriptions map[string]string `yaml:"tag_descriptions,omitempty"`
	// FlagExamples maps "operation-name/--flag-name" to the flag's example.
	FlagExamples map[string]string `yaml:"flag_examples,omitempty"`
}

type specTagIndex struct {
	Tags []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"tags"`
}

// loadHelpContext fetches the tag descriptions and flag examples the spec
// carries but restish's help rendering drops. Cached for 24h next to
// restish's own API cache and bypassed by --rsh-no-cache, same as the rest
// of the CLI's spec loading. Keyed by the API base URL, the same way restish
// keys its own API cache, so switching DCI_API_BASE_URL (staging vs prod)
// can't serve one environment's tag descriptions under another's help. A
// failed refresh falls back to stale cached data rather than leaving the
// help text blank; if there's no cache either, it returns a zero-value
// helpContext and callers render the plain restish help unchanged.
func loadHelpContext(command *cobra.Command) helpContext {
	base, err := apiBase()
	if err != nil {
		return helpContext{}
	}
	cacheKey := helpContextCacheKeyFor(base)
	cacheFile := helpContextCacheFile(base)
	if !viper.GetBool("rsh-no-cache") {
		if expires := cli.Cache.GetTime(cacheKey); !expires.IsZero() && expires.After(time.Now()) {
			if cached, ok := readHelpContextCache(cacheFile); ok {
				return cached
			}
		}
	}

	fresh, err := fetchHelpContext(command, base)
	if err != nil {
		if cached, ok := readHelpContextCache(cacheFile); ok {
			return cached
		}
		return helpContext{}
	}

	writeHelpContextCache(cacheFile, fresh)
	cli.Cache.Set(cacheKey, time.Now().Add(24*time.Hour))
	cli.Cache.WriteConfig()
	return fresh
}

func helpContextCacheKeyFor(base string) string {
	return "dci-help-context." + apiBaseDigest(base) + ".expires"
}

func apiBaseDigest(base string) string {
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:])[:16]
}

func fetchHelpContext(command *cobra.Command, base string) (helpContext, error) {
	response, err := fetchOpenAPISpec(command, base)
	if err != nil {
		return helpContext{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return helpContext{}, err
	}

	var index specTagIndex
	if err := yaml.Unmarshal(body, &index); err != nil {
		return helpContext{}, err
	}
	tagDescriptions := make(map[string]string, len(index.Tags))
	for _, tag := range index.Tags {
		if tag.Description != "" {
			tagDescriptions[tag.Name] = tag.Description
		}
	}

	flagExamples := map[string]string{}
	entrypoint, entrypointErr := url.Parse(base + "/")
	specURL, specURLErr := url.Parse(base + "/openapi.yaml")
	if entrypointErr == nil && specURLErr == nil {
		replay := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
		}
		if api, loadErr := openapi.New().Load(*entrypoint, *specURL, replay); loadErr == nil {
			for _, operation := range api.Operations {
				params := append(append([]*cli.Param{}, operation.QueryParams...), operation.HeaderParams...)
				for _, parameter := range params {
					if parameter.Example == nil {
						continue
					}
					flagExamples[operation.Name+"/--"+parameter.OptionName()] = renderExample(parameter.Example)
				}
			}
		}
	}

	return helpContext{TagDescriptions: tagDescriptions, FlagExamples: flagExamples}, nil
}

func renderExample(example interface{}) string {
	if s, ok := example.(string); ok {
		return s
	}
	out, err := yaml.Marshal(example)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func helpContextCacheFile(base string) string {
	cacheDir := os.Getenv("DCI_CACHE_DIR")
	if cacheDir == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		cacheDir = filepath.Join(userCacheDir, "dci")
	}
	return filepath.Join(cacheDir, "help-context."+apiBaseDigest(base)+".yaml")
}

func readHelpContextCache(path string) (helpContext, bool) {
	if path == "" {
		return helpContext{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return helpContext{}, false
	}
	var cached helpContext
	if err := yaml.Unmarshal(data, &cached); err != nil {
		return helpContext{}, false
	}
	return cached, true
}

func writeHelpContextCache(path string, context helpContext) {
	if path == "" {
		return
	}
	data, err := yaml.Marshal(context)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// groupTitleWithDescription appends a tag's one-line description to a group
// title restish generated (e.g. "Budgets Commands:"), so it reads as
// "Budgets Commands: Track actual cloud spend against planned spend." One
// line, no wrapping — stays inside the brevity budget of the root help
// (CMP-48648 / #14).
func groupTitleWithDescription(title, groupID string, descriptions map[string]string) string {
	description := descriptions[groupID]
	if description == "" || strings.Contains(title, description) {
		return title
	}
	return strings.TrimRight(title, ":") + ": " + description
}

// appendFlagExamples adds "Example: ..." to a command's flag usage text for
// every flag with a spec-declared example, unless that text is already
// present (e.g. augmentVerifiedFlagHelp already covered it).
func appendFlagExamples(cmd *cobra.Command, flagExamples map[string]string) {
	if len(flagExamples) == 0 {
		return
	}
	annotate := func(flag *pflag.Flag) {
		example, ok := flagExamples[cmd.Name()+"/--"+flag.Name]
		if !ok || example == "" || strings.Contains(flag.Usage, example) {
			return
		}
		flag.Usage = strings.TrimRight(flag.Usage, " \n") + "\nExample: " + example
	}
	cmd.LocalFlags().VisitAll(annotate)
}
