package main

// Chapter: multipart uploads. Operations whose request body is
// multipart/form-data (today: ingest-datahub-events-csv) cannot go through
// restish's body marshaler, which only knows JSON and YAML and panics with
// "not sure how to marshal multipart/form-data" before any request exists.
// This chapter keeps the shorthand syntax every other write command uses —
//
//	dci ingest-datahub-events-csv provider: litellm-usage, file: @events.csv
//
// — and, for a multipart operation only, swaps restish's Run for one that
// encodes the shorthand fields as form parts (`@path` on a binary field
// attaches the file with its name) and hands the request to restish's own
// pipeline, so auth, the tenant header, customer context, TLS, response
// transforms, output formats, and exit codes are untouched. Design:
// MULTIPART-UPLOAD-SPEC.md.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/danielgtaylor/shorthand/v2"
	"github.com/rest-sh/restish/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const multipartMediaType = "multipart/form-data"

// uploadSizeLimits carries the per-command upload ceilings the API states
// only in prose (the `file` field's description), so an oversize file fails
// locally with the compression hint instead of after the whole upload.
// Keyed by command name, like pagingCaps. A multipart operation without an
// entry has no local size check.
var uploadSizeLimits = map[string]int64{
	"ingest-datahub-events-csv": 30 << 20,
}

// multipartOperations indexes the spec's multipart operations by command
// name, filled from the same operation metadata load the destructive gate
// performs (setOperationMetadata).
var multipartOperations = map[string]cli.Operation{}

func setMultipartOperations(operations []cli.Operation) {
	multipartOperations = make(map[string]cli.Operation)
	for _, operation := range operations {
		if strings.Contains(operation.BodyMediaType, multipartMediaType) {
			multipartOperations[operation.Name] = operation
		}
	}
}

var requestSchemaMediaTypePattern = regexp.MustCompile(`## Request Schema \(([^)\s]+)\)`)

// requestSchemaMediaType returns the request body media type restish wrote
// into the command's long help ("## Request Schema (multipart/form-data)"),
// or "" when the command takes no body. Reading the help keeps the check
// offline and free for every non-multipart command.
func requestSchemaMediaType(longHelp string) string {
	match := requestSchemaMediaTypePattern.FindStringSubmatch(longHelp)
	if match == nil {
		return ""
	}
	return match[1]
}

func isMultipartCommand(command *cobra.Command) bool {
	return strings.Contains(requestSchemaMediaType(command.Long), multipartMediaType)
}

// multipartUploadError is a preflight failure: input the encoder cannot turn
// into a request. It is not bypassable — unlike the schema checks, nothing
// downstream could send the request anyway.
type multipartUploadError struct {
	problem string
	hint    string
}

func (uploadError multipartUploadError) Error() string {
	if uploadError.hint == "" {
		return uploadError.problem
	}
	return uploadError.problem + " — " + uploadError.hint
}

func (uploadError multipartUploadError) ExitCode() int { return exitUsage }

func (uploadError multipartUploadError) AgentErrorCode() string { return "USAGE_ERROR" }

func (uploadError multipartUploadError) AgentErrorHint() string {
	if uploadError.hint != "" {
		return uploadError.hint
	}
	return "Run the command with --help to inspect the request schema"
}

func (uploadError multipartUploadError) AgentErrorRetryable() bool { return false }

// installMultipartRunner runs in the dci PersistentPreRunE after body
// validation. For a multipart operation it encodes the body now — so every
// preflight error surfaces before any request — and replaces the command's
// Run with one that sends the encoded request through restish. Every other
// command returns immediately, untouched.
func installMultipartRunner(command *cobra.Command, args []string) error {
	if !isMultipartCommand(command) {
		return nil
	}
	if err := ensureDestructiveOperations(); err != nil {
		return fmt.Errorf("load operation metadata for %s: %w", command.Name(), err)
	}
	operation, ok := multipartOperations[command.Name()]
	if !ok {
		return fmt.Errorf("operation metadata for %s is unavailable; use --rsh-no-cache if the API schema changed recently", command.Name())
	}
	bodyArguments := args
	if pathParameterCount := len(operation.PathParams); pathParameterCount <= len(args) {
		bodyArguments = args[pathParameterCount:]
	}
	body, contentType, err := buildMultipartBody(
		command.Name(),
		requestSchemaTopLevelFieldSketches(command.Long),
		bodyArguments,
		len(bytes.TrimSpace(bufferedRequestBody)) > 0,
	)
	if err != nil {
		return err
	}
	command.Run = nil
	command.RunE = func(command *cobra.Command, args []string) error {
		request, err := buildOperationRequest(operation, command, args, body, contentType)
		if err != nil {
			return err
		}
		cli.MakeRequestAndFormat(request)
		return nil
	}
	return nil
}

// buildMultipartBody encodes shorthand body arguments as multipart/form-data
// following the request schema sketches: a `format:binary` field becomes a
// file part from its `@path` value; every other field becomes a text part
// (objects and arrays JSON-encoded). Parts follow schema order.
func buildMultipartBody(commandName string, sketches []bodyFieldSketch, bodyArguments []string, stdinHasBody bool) ([]byte, string, error) {
	fields, err := parseMultipartFields(bodyArguments)
	if err != nil {
		return nil, "", err
	}
	if stdinHasBody {
		return nil, "", multipartUploadError{
			problem: "this upload needs a filename, which stdin does not carry",
			hint:    "Pass the file as an argument: " + binaryFieldExample(sketches),
		}
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for _, field := range sketches {
		value, present := fields[field.name]
		if sketchIsBinary(field.sketch) {
			if err := writeFilePart(writer, commandName, field.name, value, present); err != nil {
				return nil, "", err
			}
			continue
		}
		if !present {
			continue
		}
		text, err := formFieldText(value)
		if err != nil {
			return nil, "", fmt.Errorf("encode %s: %w", field.name, err)
		}
		if err := writer.WriteField(field.name, text); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

// parseMultipartFields parses the shorthand without file input: the encoder
// handles `@path` itself so the file keeps its name and is read once.
func parseMultipartFields(bodyArguments []string) (map[string]any, error) {
	if len(bodyArguments) == 0 {
		return map[string]any{}, nil
	}
	parsed, err := shorthand.Unmarshal(strings.Join(bodyArguments, " "), shorthand.ParseOptions{EnableObjectDetection: true}, nil)
	if err != nil {
		return nil, multipartUploadError{problem: "request body shorthand did not parse: " + err.Error()}
	}
	fields, ok := parsed.(map[string]any)
	if !ok {
		return nil, multipartUploadError{
			problem: "request body must be field: value pairs",
			hint:    "For example: provider: <dataset>, file: @events.csv",
		}
	}
	return fields, nil
}

func sketchIsBinary(sketch string) bool {
	return strings.Contains(sketch, "format:binary")
}

func binaryFieldExample(sketches []bodyFieldSketch) string {
	for _, field := range sketches {
		if sketchIsBinary(field.sketch) {
			return field.name + ": @events.csv"
		}
	}
	return "file: @events.csv"
}

// writeFilePart validates and attaches the file named by a binary field's
// `@path` value: the part keeps the file's base name (the API echoes it in
// the batch id and reads the extension to detect GZ/ZIP) and a content type
// derived from that extension.
func writeFilePart(writer *multipart.Writer, commandName, fieldName string, value any, present bool) error {
	if !present {
		return multipartUploadError{
			problem: fieldName + " is required for this upload",
			hint:    "Add " + fieldName + ": @<path>",
		}
	}
	text, isString := value.(string)
	if !isString || !strings.HasPrefix(text, "@") || len(text) == 1 {
		shown := fmt.Sprint(value)
		if shown == "" || strings.ContainsAny(shown, " \t") {
			shown = "events.csv"
		}
		return multipartUploadError{
			problem: fieldName + " must name a file to upload",
			hint:    "Use " + fieldName + ": @" + strings.TrimPrefix(shown, "@"),
		}
	}
	path := text[1:]
	info, err := os.Stat(path)
	if err != nil {
		return multipartUploadError{
			problem: fmt.Sprintf("cannot read %s: %v", path, err),
			hint:    "Check the path; relative paths resolve from the current directory",
		}
	}
	if info.IsDir() {
		return multipartUploadError{
			problem: path + " is a directory",
			hint:    "Point " + fieldName + ": @<path> at the CSV (or its .gz/.zip) itself",
		}
	}
	if info.Size() == 0 {
		return multipartUploadError{problem: path + " is empty"}
	}
	if limit, limited := uploadSizeLimits[commandName]; limited && info.Size() > limit {
		return multipartUploadError{
			problem: fmt.Sprintf("%s is %s; the API accepts up to %s", path, formatUploadSize(info.Size()), formatUploadSize(limit)),
			hint:    fmt.Sprintf("Compress it: gzip -k %s, then %s: @%s.gz (one CSV per archive)", path, fieldName, path),
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return multipartUploadError{
			problem: fmt.Sprintf("cannot read %s: %v", path, err),
			hint:    "Check the path and its permissions",
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
		escapeQuotes(fieldName), escapeQuotes(filepath.Base(path))))
	header.Set("Content-Type", uploadContentType(path))
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func escapeQuotes(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

// uploadContentType picks the part's Content-Type from the file extension;
// the upload endpoint accepts a plain CSV or a GZ/ZIP archive of one.
func uploadContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return "text/csv"
	case ".gz", ".gzip":
		return "application/gzip"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

func formatUploadSize(size int64) string {
	const megabyte = 1 << 20
	if size >= megabyte {
		return fmt.Sprintf("%.1f MB", float64(size)/megabyte)
	}
	return fmt.Sprintf("%.1f KB", float64(size)/1024)
}

// formFieldText renders a non-binary field as form text: scalars as typed,
// objects and arrays as JSON (the spec has no such multipart fields today;
// this is the documented fallback).
func formFieldText(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case nil:
		return "", nil
	case map[string]any, []any:
		encoded, err := json.Marshal(typed)
		return string(encoded), err
	default:
		return fmt.Sprint(typed), nil
	}
}

// buildOperationRequest reproduces what restish's generated Run builds
// (cli/operation.go): path parameters substituted into the URI template,
// changed query and header flags applied, the --rsh-server override
// honored — with the encoded multipart body and its Content-Type in place
// of restish's marshaled body.
func buildOperationRequest(operation cli.Operation, command *cobra.Command, args []string, body []byte, contentType string) (*http.Request, error) {
	uri := operation.URITemplate
	for index, param := range operation.PathParams {
		if index >= len(args) {
			return nil, fmt.Errorf("missing path parameter %s", param.Name)
		}
		uri = strings.Replace(uri, "{"+param.Name+"}", args[index], 1)
	}

	query := url.Values{}
	for _, param := range operation.QueryParams {
		for _, value := range changedFlagValues(command.Flags(), param.OptionName()) {
			query.Add(param.Name, value)
		}
	}
	if encoded := query.Encode(); encoded != "" {
		separator := "?"
		if strings.Contains(uri, "?") {
			separator = "&"
		}
		uri += separator + encoded
	}

	if customServer := viper.GetString("rsh-server"); customServer != "" {
		original, err := url.Parse(uri)
		if err != nil {
			return nil, err
		}
		custom, err := url.Parse(customServer)
		if err != nil {
			return nil, err
		}
		original.Scheme = custom.Scheme
		original.Host = custom.Host
		if custom.Path != "" && custom.Path != "/" {
			original.Path = strings.TrimSuffix(custom.Path, "/") + original.Path
		}
		uri = original.String()
	}

	request, err := http.NewRequest(operation.Method, uri, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for _, param := range operation.HeaderParams {
		for _, value := range changedFlagValues(command.Flags(), param.OptionName()) {
			request.Header.Add(param.Name, value)
		}
	}
	request.Header.Set("Content-Type", contentType)
	return request, nil
}

// changedFlagValues returns the values of a flag the user set on the command
// line, one per element for slice flags, nothing when the flag is untouched.
func changedFlagValues(flags *pflag.FlagSet, name string) []string {
	flag := flags.Lookup(name)
	if flag == nil || !flag.Changed {
		return nil
	}
	if slice, ok := flag.Value.(pflag.SliceValue); ok {
		return slice.GetSlice()
	}
	return []string{flag.Value.String()}
}
