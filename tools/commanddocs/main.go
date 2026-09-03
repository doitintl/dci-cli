// commanddocs maintains the curated command docs in command-docs/ against the
// DCI OpenAPI spec (COMMAND-DOCS-SPEC.md D4/D5):
//
//	go run ./tools/commanddocs coverage -spec <url|path> [-strict]
//	go run ./tools/commanddocs scaffold -spec <url|path> [-only <command>]
//	go run ./tools/commanddocs check    -spec <url|path>
//
// coverage lists every non-hidden operation without a curated (non-draft)
// doc as a GitHub `::warning::` annotation plus a one-line summary; -strict
// exits 1 when anything is missing or still a draft (the release gate).
// scaffold writes a `draft: true` stub for each missing operation with the
// spec-derived arguments, one schema-generated example, and same-group
// related commands, for a human to curate. check downloads the spec when
// given a URL and runs the validator tests in the main package with it.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/openapi"
	"gopkg.in/yaml.v3"
)

const apiBase = "https://api.doit.com"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	specSource := flags.String("spec", apiBase+"/openapi.yaml", "OpenAPI spec URL or file path")
	docsDir := flags.String("docs", "command-docs", "directory of command doc files")
	strict := flags.Bool("strict", false, "coverage: exit 1 when any operation is missing or still a draft")
	only := flags.String("only", "", "scaffold: only this command name")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "coverage":
		err = coverage(*specSource, *docsDir, *strict)
	case "scaffold":
		err = scaffold(*specSource, *docsDir, *only)
	case "check":
		err = check(*specSource)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "commanddocs: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./tools/commanddocs <coverage|scaffold|check> [-spec url|path] [-docs dir] [-strict] [-only command]")
}

// docState is the subset of a doc file the tool needs.
type docState struct {
	Command string `yaml:"command"`
	Draft   bool   `yaml:"draft"`
}

func readSpec(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		response, err := http.Get(source)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: HTTP %d", source, response.StatusCode)
		}
		return io.ReadAll(response.Body)
	}
	return os.ReadFile(source)
}

func loadOperations(source string) ([]cli.Operation, error) {
	data, err := readSpec(source)
	if err != nil {
		return nil, err
	}
	entrypoint, _ := url.Parse(apiBase + "/")
	specURL, _ := url.Parse(apiBase + "/openapi.yaml")
	api, err := openapi.New().Load(*entrypoint, *specURL, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(data)),
	})
	if err != nil {
		return nil, fmt.Errorf("load spec: %w", err)
	}
	operations := make([]cli.Operation, 0, len(api.Operations))
	for _, operation := range api.Operations {
		if !operation.Hidden {
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].Name < operations[j].Name })
	return operations, nil
}

func loadDocStates(docsDir string) (map[string]docState, error) {
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return nil, err
	}
	states := map[string]docState{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(docsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var state docState
		if err := yaml.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if state.Command == "" {
			return nil, fmt.Errorf("%s: missing command", entry.Name())
		}
		states[state.Command] = state
	}
	return states, nil
}

func fileName(command string) string {
	return strings.ReplaceAll(command, " ", "-") + ".yaml"
}

func coverage(specSource, docsDir string, strict bool) error {
	operations, err := loadOperations(specSource)
	if err != nil {
		return err
	}
	states, err := loadDocStates(docsDir)
	if err != nil {
		return err
	}
	var missing, drafts []string
	for _, operation := range operations {
		state, ok := states[operation.Name]
		switch {
		case !ok:
			missing = append(missing, operation.Name)
			fmt.Printf("::warning file=%s/%s::no curated command doc for %s (run: go run ./tools/commanddocs scaffold -only %s)\n", docsDir, fileName(operation.Name), operation.Name, operation.Name)
		case state.Draft:
			drafts = append(drafts, operation.Name)
			fmt.Printf("::warning file=%s/%s::command doc for %s is still a draft\n", docsDir, fileName(operation.Name), operation.Name)
		}
	}
	curated := len(operations) - len(missing) - len(drafts)
	fmt.Printf("Command docs coverage: %d/%d operations curated, %d drafts, %d missing.\n", curated, len(operations), len(drafts), len(missing))
	if summary := os.Getenv("GITHUB_STEP_SUMMARY"); summary != "" {
		line := fmt.Sprintf("Command docs coverage: **%d/%d** operations curated, %d drafts, %d missing.\n", curated, len(operations), len(drafts), len(missing))
		if len(missing) > 0 {
			line += "\nMissing: `" + strings.Join(missing, "`, `") + "`\n"
		}
		_ = os.WriteFile(summary, []byte(line), 0o644)
	}
	if strict && (len(missing) > 0 || len(drafts) > 0) {
		return fmt.Errorf("%d operation(s) lack a curated command doc: %s", len(missing)+len(drafts), strings.Join(append(missing, drafts...), ", "))
	}
	return nil
}

func scaffold(specSource, docsDir, only string) error {
	operations, err := loadOperations(specSource)
	if err != nil {
		return err
	}
	states, err := loadDocStates(docsDir)
	if err != nil {
		return err
	}
	siblings := map[string][]string{}
	for _, operation := range operations {
		siblings[operation.Group] = append(siblings[operation.Group], operation.Name)
	}
	written := 0
	for _, operation := range operations {
		if only != "" && operation.Name != only {
			continue
		}
		if _, exists := states[operation.Name]; exists {
			continue
		}
		path := filepath.Join(docsDir, fileName(operation.Name))
		if err := os.WriteFile(path, []byte(renderStub(operation, siblings[operation.Group])), 0o644); err != nil {
			return err
		}
		fmt.Println("scaffolded", path)
		written++
	}
	if only != "" && written == 0 {
		return fmt.Errorf("no missing operation named %q", only)
	}
	fmt.Printf("Scaffolded %d stub(s); curate each one, then remove draft: true.\n", written)
	return nil
}

// renderStub writes the draft file by hand rather than through a YAML
// marshaller so the layout matches the curated files and every command value
// is double-quoted (COMMAND-DOCS-SPEC.md D1 quoting rule).
func renderStub(operation cli.Operation, groupSiblings []string) string {
	var builder strings.Builder
	builder.WriteString("# Scaffolded by tools/commanddocs from the OpenAPI spec. Curate the examples\n")
	builder.WriteString("# (two to four, common case first), then remove draft: true.\n")
	builder.WriteString("command: " + operation.Name + "\n")
	builder.WriteString("draft: true\n")

	var placeholders []string
	if len(operation.PathParams) > 0 {
		builder.WriteString("\narguments:\n")
		for _, parameter := range operation.PathParams {
			description := strings.TrimSpace(parameter.Description)
			if description == "" {
				description = "TODO: what this identifies and which command lists it."
			}
			builder.WriteString("  " + parameter.OptionName() + ": " + yamlQuote(description) + "\n")
			placeholders = append(placeholders, "<"+placeholderName(operation.Name, parameter.OptionName())+">")
		}
	}

	command := "dci " + operation.Name
	if len(placeholders) > 0 {
		command += " " + strings.Join(placeholders, " ")
	}
	if operation.BodyMediaType != "" {
		body := "< body.json"
		if len(operation.Examples) > 0 && operation.Examples[0] != "<input.json" && len(operation.Examples[0]) < 150 {
			body = operation.Examples[0]
		}
		command += " " + body
	}
	builder.WriteString("\nexamples:\n")
	builder.WriteString("  - description: \"TODO: describe the common case (schema-generated example; verify every value).\"\n")
	builder.WriteString("    command: " + yamlQuote(command) + "\n")

	var related []string
	for _, sibling := range groupSiblings {
		if sibling != operation.Name && len(related) < 5 {
			related = append(related, sibling)
		}
	}
	if len(related) > 0 {
		builder.WriteString("\nrelated: [" + strings.Join(related, ", ") + "]\n")
	}
	return builder.String()
}

// placeholderName turns get-anomaly + id into anomaly-id: the resource noun
// is the command name minus its leading verb.
func placeholderName(command, parameter string) string {
	parts := strings.SplitN(command, "-", 2)
	if len(parts) == 2 && parameter == "id" {
		return parts[1] + "-id"
	}
	return parameter
}

func yamlQuote(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
}

func check(specSource string) error {
	specPath := specSource
	if strings.HasPrefix(specSource, "http://") || strings.HasPrefix(specSource, "https://") {
		data, err := readSpec(specSource)
		if err != nil {
			return err
		}
		file, err := os.CreateTemp("", "dci-openapi-*.yaml")
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			return err
		}
		file.Close()
		defer os.Remove(file.Name())
		specPath = file.Name()
	}
	command := exec.Command("go", "test", "-run", "TestCommandDocs", "-count=1", "-v", ".")
	command.Env = append(os.Environ(), "DCI_COMMAND_DOCS_SPEC="+specPath)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
