// betaspec generates beta/openapi.beta.yaml — the self-contained OpenAPI spec
// embedded into the dci binary as the `dci beta` command surface.
//
// It reads beta/manifest.yaml (the hand-curated operation allowlist), fetches
// the source spec (the public dev spec, a strict superset of prod), extracts
// the manifest's operations plus the transitive $ref closure of everything
// they reference, injects x-cli-name / x-dci-early-access extensions, and
// writes a deterministic output file. Output is content-derived only (no
// timestamps) so regenerating against an unchanged source is a no-op diff.
//
// Usage: go run ./tools/betaspec [-manifest path] [-out path] [-source url|path]
// The -source flag overrides the manifest's source (a local file path works,
// which is how tests run without the network).
package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type manifest struct {
	Source     string              `yaml:"source"`
	Operations []manifestOperation `yaml:"operations"`
}

type manifestOperation struct {
	OperationID string `yaml:"operationId"`
	CLIName     string `yaml:"cliName"`
	EarlyAccess string `yaml:"earlyAccess"`
}

func main() {
	manifestPath := flag.String("manifest", "beta/manifest.yaml", "path to the beta manifest")
	outPath := flag.String("out", "beta/openapi.beta.yaml", "path to write the generated spec")
	sourceOverride := flag.String("source", "", "override the manifest's source spec URL or file path")
	flag.Parse()

	if err := run(*manifestPath, *outPath, *sourceOverride); err != nil {
		fmt.Fprintf(os.Stderr, "betaspec: %v\n", err)
		os.Exit(1)
	}
}

func run(manifestPath, outPath, sourceOverride string) error {
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var m manifest
	if err := yaml.Unmarshal(manifestBytes, &m); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Operations) == 0 {
		return fmt.Errorf("manifest lists no operations")
	}
	source := m.Source
	if sourceOverride != "" {
		source = sourceOverride
	}
	if source == "" {
		return fmt.Errorf("no source spec configured")
	}

	specBytes, err := readSource(source)
	if err != nil {
		return fmt.Errorf("read source spec %s: %w", source, err)
	}

	output, err := generate(specBytes, m, source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, output, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Printf("betaspec: wrote %s (%d operations, source sha256 %.12x)\n", outPath, len(m.Operations), sha256.Sum256(specBytes))
	return nil
}

func readSource(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		request, err := http.NewRequest(http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		// The prod edge rejects Go's default user agent (Cloudflare rule);
		// identify the generator explicitly.
		request.Header.Set("User-Agent", "dci-cli-betaspec-generator")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d", response.StatusCode)
		}
		return io.ReadAll(response.Body)
	}
	return os.ReadFile(source)
}

// generate builds the beta spec document from the source spec and manifest.
func generate(specBytes []byte, m manifest, source string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(specBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse source spec: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("source spec is not a YAML document")
	}
	root := doc.Content[0]

	wanted := make(map[string]manifestOperation, len(m.Operations))
	for _, op := range m.Operations {
		if op.OperationID == "" || op.CLIName == "" {
			return nil, fmt.Errorf("manifest entry missing operationId or cliName: %+v", op)
		}
		wanted[op.OperationID] = op
	}

	paths := mapValue(root, "paths")
	if paths == nil {
		return nil, fmt.Errorf("source spec has no paths")
	}

	outPaths := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	found := make(map[string]bool, len(wanted))
	for i := 0; i+1 < len(paths.Content); i += 2 {
		pathKey, pathItem := paths.Content[i], paths.Content[i+1]
		keptItem := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for j := 0; j+1 < len(pathItem.Content); j += 2 {
			methodKey, operation := pathItem.Content[j], pathItem.Content[j+1]
			switch methodKey.Value {
			case "get", "post", "put", "patch", "delete":
				id := scalarValue(operation, "operationId")
				entry, ok := wanted[id]
				if !ok {
					continue
				}
				found[id] = true
				injectExtensions(operation, entry)
				keptItem.Content = append(keptItem.Content, methodKey, operation)
			case "parameters":
				// Path-level parameters apply to every method under the path;
				// carried along whenever any method is kept (below).
			}
		}
		if len(keptItem.Content) == 0 {
			continue
		}
		if pathParams := mapValue(pathItem, "parameters"); pathParams != nil {
			keptItem.Content = append(keptItem.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "parameters"}, pathParams)
		}
		outPaths.Content = append(outPaths.Content, pathKey, keptItem)
	}

	var missing []string
	for id := range wanted {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("operations not found in source spec: %s", strings.Join(missing, ", "))
	}

	components, err := componentClosure(root, outPaths)
	if err != nil {
		return nil, err
	}

	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendMap(out, "openapi", cloneOr(mapValue(root, "openapi"), "3.0.0"))
	info := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendMap(info, "title", scalarNode("DoiT Cloud Intelligence API — beta surface"))
	appendMap(info, "version", cloneOr(mapValue(mapValue(root, "info"), "version"), "beta"))
	appendMap(out, "info", info)
	// A rooted server URL makes restish resolve every path against the
	// configured API base (prod), regardless of the source spec's dev host.
	servers := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	server := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendMap(server, "url", scalarNode("/"))
	servers.Content = append(servers.Content, server)
	appendMap(out, "servers", servers)
	appendMap(out, "paths", outPaths)
	if components != nil {
		appendMap(out, "components", components)
	}

	var buf strings.Builder
	buf.WriteString("# Code generated by tools/betaspec from " + source + " — DO NOT EDIT.\n")
	buf.WriteString(fmt.Sprintf("# Source spec sha256: %x\n", sha256.Sum256(specBytes)))
	buf.WriteString("# Curated by beta/manifest.yaml; regenerate with: go run ./tools/betaspec\n")
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(out); err != nil {
		return nil, fmt.Errorf("encode output: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close encoder: %w", err)
	}
	return []byte(buf.String()), nil
}

// injectExtensions adds (or overwrites) the dci extensions on an operation node.
func injectExtensions(operation *yaml.Node, entry manifestOperation) {
	setMapEntry(operation, "x-cli-name", entry.CLIName)
	if entry.EarlyAccess != "" {
		setMapEntry(operation, "x-dci-early-access", entry.EarlyAccess)
	}
}

// componentClosure returns a components node containing every component
// transitively referenced from the kept paths, or nil when nothing is
// referenced. Component names are emitted in sorted order per section for
// deterministic output.
func componentClosure(root, outPaths *yaml.Node) (*yaml.Node, error) {
	components := mapValue(root, "components")
	// section -> name -> node
	index := map[string]map[string]*yaml.Node{}
	if components != nil {
		for i := 0; i+1 < len(components.Content); i += 2 {
			section, sectionMap := components.Content[i].Value, components.Content[i+1]
			index[section] = map[string]*yaml.Node{}
			for j := 0; j+1 < len(sectionMap.Content); j += 2 {
				index[section][sectionMap.Content[j].Value] = sectionMap.Content[j+1]
			}
		}
	}

	type ref struct{ section, name string }
	seen := map[ref]bool{}
	queue := collectRefs(outPaths)
	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]
		parts := strings.Split(strings.TrimPrefix(target, "#/components/"), "/")
		if !strings.HasPrefix(target, "#/components/") || len(parts) != 2 {
			return nil, fmt.Errorf("unsupported $ref %q (only #/components/<section>/<name> is supported)", target)
		}
		r := ref{parts[0], parts[1]}
		if seen[r] {
			continue
		}
		seen[r] = true
		node, ok := index[r.section][r.name]
		if !ok {
			return nil, fmt.Errorf("$ref %q not found in source components", target)
		}
		queue = append(queue, collectRefs(node)...)
	}

	if len(seen) == 0 {
		return nil, nil
	}
	sections := map[string][]string{}
	for r := range seen {
		sections[r.section] = append(sections[r.section], r.name)
	}
	sectionNames := make([]string, 0, len(sections))
	for section := range sections {
		sectionNames = append(sectionNames, section)
	}
	sort.Strings(sectionNames)

	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, section := range sectionNames {
		names := sections[section]
		sort.Strings(names)
		sectionMap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, name := range names {
			appendMap(sectionMap, name, index[section][name])
		}
		appendMap(out, section, sectionMap)
	}
	return out, nil
}

// collectRefs returns every local $ref target under node.
func collectRefs(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	var refs []string
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "$ref" && node.Content[i+1].Kind == yaml.ScalarNode {
				refs = append(refs, node.Content[i+1].Value)
				continue
			}
			refs = append(refs, collectRefs(node.Content[i+1])...)
		}
		return refs
	}
	for _, child := range node.Content {
		refs = append(refs, collectRefs(child)...)
	}
	return refs
}

// --- yaml.Node helpers ------------------------------------------------------

func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func scalarValue(node *yaml.Node, key string) string {
	value := mapValue(node, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func appendMap(node *yaml.Node, key string, value *yaml.Node) {
	node.Content = append(node.Content, scalarNode(key), value)
}

func setMapEntry(node *yaml.Node, key, value string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = scalarNode(value)
			return
		}
	}
	appendMap(node, key, scalarNode(value))
}

func cloneOr(node *yaml.Node, fallback string) *yaml.Node {
	if node == nil {
		return scalarNode(fallback)
	}
	return node
}
