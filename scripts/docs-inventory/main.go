// Command docs-inventory builds the release-1.0 API/CLI documentation truth
// inventory. It compares the generated public OpenAPI route catalog and the
// CLI's own machine-readable command manifest with the documentation tree.
//
// Run from the repository root:
//
//	go run ./cmd/gen-openapi
//	go run ./scripts/docs-inventory
//
// The report is deliberately evidence-oriented. A documentation page existing
// is not treated as proof that every operation is described: an exact route
// mention is stronger evidence than a resource-level page fallback.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	openAPIPath    = "internal/api/openapi.gen.json"
	docsRoot       = "docs"
	docsNavigation = "docs/docs.json"
	reportDir      = "docs/prd/reports"
	jsonReport     = reportDir + "/release-1-0-api-cli-inventory.json"
	markdownReport = reportDir + "/release-1-0-api-cli-inventory.md"
)

type openAPIDocument struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

type openAPIOperation struct {
	OperationID string                     `json:"operationId"`
	Tags        []string                   `json:"tags"`
	Parameters  []json.RawMessage          `json:"parameters"`
	RequestBody *openAPIRequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]openAPIResponse `json:"responses"`
}

type openAPIRequestBody struct {
	Content map[string]openAPIMediaType `json:"content"`
}

type openAPIResponse struct {
	Content map[string]openAPIMediaType `json:"content"`
}

type openAPIMediaType struct {
	Schema map[string]json.RawMessage `json:"schema"`
}

type contractChecks struct {
	Structural      structuralChecks `json:"structural"`
	SemanticRuntime semanticChecks   `json:"semantic_runtime"`
}

// structuralChecks describe evidence present in MDX. They deliberately do
// not claim that the documented behavior is correct at runtime.
type structuralChecks struct {
	CanonicalMethodPath bool     `json:"canonical_method_path"`
	Auth                bool     `json:"auth"`
	Request             bool     `json:"request"`
	Response            bool     `json:"response"`
	Statuses            bool     `json:"statuses"`
	Missing             []string `json:"missing,omitempty"`
}

// semanticChecks are evidence from generated/runtime-facing sources. A test
// signal is not a substitute for documentation and is reported separately.
type semanticChecks struct {
	OpenAPIOperation bool     `json:"openapi_operation"`
	SourceFile       string   `json:"source_file,omitempty"`
	TestSignals      []string `json:"test_signals,omitempty"`
}

type commandManifest struct {
	Commands    []commandNode  `json:"commands"`
	GlobalFlags []flagManifest `json:"global_flags"`
}

type commandNode struct {
	Path     string         `json:"path"`
	Use      string         `json:"use"`
	Short    string         `json:"short"`
	Aliases  []string       `json:"aliases"`
	Flags    []flagManifest `json:"flags"`
	Commands []commandNode  `json:"commands"`
}

type flagManifest struct {
	Name string `json:"name"`
}

type docFile struct {
	Path string `json:"path"`
	Text string `json:"-"`
}

type apiRecord struct {
	Method                 string         `json:"method"`
	Path                   string         `json:"path"`
	OperationID            string         `json:"operation_id"`
	Tag                    string         `json:"tag"`
	SourceFile             string         `json:"source_file,omitempty"`
	ExactDocs              []string       `json:"exact_docs,omitempty"`
	ResourceDocs           []string       `json:"resource_docs,omitempty"`
	Status                 string         `json:"status"`
	TestSignals            []string       `json:"test_signals,omitempty"`
	ConcreteResponseSchema bool           `json:"concrete_response_schema"`
	GenericResponseSchema  bool           `json:"generic_response_schema"`
	RequestBodyPresent     bool           `json:"request_body_present"`
	ConcreteJSONRequest    bool           `json:"concrete_json_request"`
	GenericJSONRequest     bool           `json:"generic_json_request"`
	NonJSONRequestMedia    []string       `json:"non_json_request_media,omitempty"`
	Contract               contractChecks `json:"contract"`
}

type cliRecord struct {
	Path            string   `json:"path"`
	Use             string   `json:"use"`
	Short           string   `json:"short"`
	Aliases         []string `json:"aliases,omitempty"`
	Root            string   `json:"root"`
	ExactDocs       []string `json:"exact_docs,omitempty"`
	RootDocs        []string `json:"root_docs,omitempty"`
	Status          string   `json:"status"`
	TestSignals     []string `json:"test_signals,omitempty"`
	Flags           []string `json:"flags,omitempty"`
	DocumentedFlags []string `json:"documented_flags,omitempty"`
	MissingFlags    []string `json:"missing_flags,omitempty"`
}

type report struct {
	Summary  summary         `json:"summary"`
	API      []apiRecord     `json:"api"`
	CLI      []cliRecord     `json:"cli"`
	Env      []surfaceRecord `json:"environment_variables,omitempty"`
	Manifest []surfaceRecord `json:"manifest_kinds,omitempty"`
	Reverse  reverseChecks   `json:"docs_to_code"`
}

type surfaceRecord struct {
	Name   string   `json:"name"`
	Docs   []string `json:"docs,omitempty"`
	Status string   `json:"status"`
}

var endpointPattern = regexp.MustCompile(`(?i)\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)\s+(/[[:alnum:]_./{}~:-]+(?:\?[[:alnum:]_./{}=&%,~:+-]+)?)`)
var endpointHeadingPattern = regexp.MustCompile(`(?i)^#{1,6}\s+.*\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)\s+(/[[:alnum:]_./{}~:-]+(?:\?[[:alnum:]_./{}=&%,~:+-]+)?)`)
var pathPattern = regexp.MustCompile(`/[[:alnum:]_./{}~:-]+(?:\?[[:alnum:]_./{}=&%,~:+-]+)?`)
var statusPattern = regexp.MustCompile(`(?i)\bstatus(?:es)?\s*:`)
var envNamePattern = regexp.MustCompile(`\bCREWSHIP_[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*\b`)
var manifestKindsPattern = regexp.MustCompile(`expected one of: ([A-Za-z0-9_, ]+)`)
var httpStatusPattern = regexp.MustCompile(`(?i)(?:` + "`" + `)?[1-5][0-9]{2}(?:` + "`" + `)?(?:\s+[a-z][a-z -]+)?`)

type endpointEvidence struct {
	Path, Method string
	DocPath      string
	Text         string
}

// inventoryEndpointEvidence parses only documentation structure. It accepts
// the common forms used in the docs: endpoint headings, fenced request lines,
// and Markdown table rows. Query strings are removed because OpenAPI paths do
// not include them.
func inventoryEndpointEvidence(docs []docFile) map[string][]endpointEvidence {
	result := make(map[string][]endpointEvidence)
	for _, doc := range docs {
		lines := strings.Split(strings.ReplaceAll(doc.Text, "\r\n", "\n"), "\n")
		shared := sharedContractSections(lines)
		for i, line := range lines {
			matches := endpointPattern.FindAllStringSubmatch(line, -1)
			if tableMethod, tablePath, ok := endpointTableRow(line); ok {
				matches = append(matches, []string{"", tableMethod, tablePath})
			}
			if len(matches) == 0 {
				continue
			}
			section := endpointSection(lines, i) + "\n" + shared
			for _, match := range matches {
				method, path := strings.ToUpper(match[1]), canonicalDocPath(match[2])
				if path == "" {
					continue
				}
				key := method + " " + path
				result[key] = append(result[key], endpointEvidence{Method: method, Path: path, DocPath: doc.Path, Text: section})
			}
		}
	}
	return result
}

func sharedContractSections(lines []string) string {
	var sections []string
	// Frontmatter, Notes, and introductory paragraphs often define the
	// authentication scope for every operation on a page. They are valid
	// shared evidence, but only before the first Markdown heading so an
	// unrelated later operation cannot satisfy this endpoint's contract.
	for i, line := range lines {
		if markdownHeadingLevel(line) > 0 {
			if i > 0 {
				sections = append(sections, strings.Join(lines[:i], "\n"))
			}
			break
		}
	}
	for i, line := range lines {
		level := markdownHeadingLevel(line)
		if level == 0 {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
		if !strings.Contains(title, "contract") && !strings.Contains(title, "authentication") && title != "auth" {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if next := markdownHeadingLevel(lines[j]); next > 0 && next <= level {
				end = j
				break
			}
		}
		sections = append(sections, strings.Join(lines[i:end], "\n"))
	}
	return strings.Join(sections, "\n")
}

func endpointTableRow(line string) (string, string, bool) {
	if !strings.Contains(line, "|") {
		return "", "", false
	}
	cells := strings.Split(line, "|")
	if len(cells) < 3 {
		return "", "", false
	}
	method := strings.ToUpper(strings.TrimSpace(cells[1]))
	if !isHTTPMethod(method) {
		return "", "", false
	}
	match := pathPattern.FindString(cells[2])
	if match == "" {
		return "", "", false
	}
	return method, canonicalDocPath(match), true
}

func isHTTPMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE":
		return true
	default:
		return false
	}
}

func endpointSection(lines []string, endpointLine int) string {
	start, level := endpointLine, 0
	if match := endpointHeadingPattern.FindStringSubmatch(lines[endpointLine]); match != nil {
		level = markdownHeadingLevel(lines[endpointLine])
		start = endpointLine
	} else {
		// Table rows and fenced request lines usually sit below a resource or
		// operation heading. Restrict their evidence to that heading's section;
		// using the rest of the document would let an unrelated endpoint satisfy
		// auth/request/response/status checks by accident.
		for i := endpointLine - 1; i >= 0; i-- {
			if heading := markdownHeadingLevel(lines[i]); heading > 0 {
				start, level = i, heading
				break
			}
		}
	}
	end := len(lines)
	if level > 0 {
		for i := endpointLine + 1; i < len(lines); i++ {
			if hashLevel := markdownHeadingLevel(lines[i]); hashLevel > 0 && hashLevel <= level {
				end = i
				break
			}
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func markdownHeadingLevel(line string) int {
	trimmed := strings.TrimLeft(line, "#")
	level := len(line) - len(trimmed)
	if level == 0 || level > 6 || len(trimmed) == 0 || trimmed[0] != ' ' {
		return 0
	}
	return level
}

func canonicalDocPath(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return strings.TrimRight(path, ".,;:")
}

func contractFor(method, path string, evidence map[string][]endpointEvidence, source string, tests []string, requestRequired bool) contractChecks {
	key := strings.ToUpper(method) + " " + path
	checks := contractChecks{SemanticRuntime: semanticChecks{OpenAPIOperation: true, SourceFile: source, TestSignals: tests}}
	for _, item := range evidence[key] {
		checks.Structural.CanonicalMethodPath = true
		text := strings.ToLower(item.Text)
		checks.Structural.Auth = checks.Structural.Auth || markerPresent(text, "auth", "authentication", "authorization")
		checks.Structural.Request = checks.Structural.Request || markerPresent(text, "request", "request body", "request headers", "query parameters", "path parameters")
		checks.Structural.Response = checks.Structural.Response || markerPresent(text, "response")
		checks.Structural.Statuses = checks.Structural.Statuses || statusMarkerPresent(item.Text)
	}
	if !checks.Structural.CanonicalMethodPath {
		checks.Structural.Missing = append(checks.Structural.Missing, "canonical_method_path")
	}
	if !checks.Structural.Auth {
		checks.Structural.Missing = append(checks.Structural.Missing, "auth")
	}
	if requestRequired && !checks.Structural.Request {
		checks.Structural.Missing = append(checks.Structural.Missing, "request")
	}
	if !checks.Structural.Response {
		checks.Structural.Missing = append(checks.Structural.Missing, "response")
	}
	if !checks.Structural.Statuses {
		checks.Structural.Missing = append(checks.Structural.Missing, "statuses")
	}
	return checks
}

func markerPresent(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func statusMarkerPresent(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "| status |") || strings.Contains(lower, "**status") || statusPattern.MatchString(lower) || httpStatusPattern.MatchString(lower)
}

type summary struct {
	APIOperations                  int `json:"api_operations"`
	APIExactDocs                   int `json:"api_exact_docs"`
	APIResourceDocs                int `json:"api_resource_docs"`
	APIMissingDocs                 int `json:"api_missing_docs"`
	CLIOperations                  int `json:"cli_operations"`
	CLIExactDocs                   int `json:"cli_exact_docs"`
	CLIRootDocsPresent             int `json:"cli_root_docs_present"`
	CLIRootDocsMissing             int `json:"cli_root_docs_missing"`
	APIWithTestSignals             int `json:"api_with_test_signals"`
	APIWithConcreteResponseSchemas int `json:"api_with_concrete_response_schemas"`
	APIGenericResponseSchemas      int `json:"api_generic_response_schemas"`
	APIWithRequestBodies           int `json:"api_with_request_bodies"`
	APIWithConcreteJSONRequests    int `json:"api_with_concrete_json_requests"`
	APIGenericJSONRequests         int `json:"api_generic_json_requests"`
	APINonJSONRequestBodies        int `json:"api_non_json_request_bodies"`
	CLIWithTestSignals             int `json:"cli_with_test_signals"`
	CLIWithFlags                   int `json:"cli_with_flags"`
	CLIWithAllFlagsDocumented      int `json:"cli_with_all_flags_documented"`
	CLIMissingFlagDocs             int `json:"cli_missing_flag_docs"`
	// APIContractGaps counts operations whose docs are missing at least one
	// of the four structural markers (auth, request, response, statuses).
	// The release audit CLAIMED this was zero; without a counter the claim
	// could not be checked by anything but a human reading 532 rows.
	APIContractGaps             int `json:"api_contract_gaps"`
	EnvironmentVariables        int `json:"environment_variables"`
	EnvironmentVariablesMissing int `json:"environment_variables_missing"`
	ManifestKinds               int `json:"manifest_kinds"`
	ManifestKindsMissing        int `json:"manifest_kinds_missing"`
	DocsCommandsMissing         int `json:"docs_commands_missing"`
	DocsAPIPathsMissing         int `json:"docs_api_paths_missing"`
	DocsEnvMissing              int `json:"docs_environment_variables_missing"`
	DocsKindsMissing            int `json:"docs_manifest_kinds_missing"`
	DocsFlagsMissing            int `json:"docs_flags_missing"`
}

func main() {
	var openAPIFile string
	var commandsFile string
	var strict bool
	flag.StringVar(&openAPIFile, "openapi", openAPIPath, "generated OpenAPI document")
	flag.StringVar(&commandsFile, "commands", "", "CLI command manifest JSON; empty runs crewship commands --format json")
	flag.BoolVar(&strict, "strict", false, "exit non-zero when any documentation gap is present (CI gate)")
	flag.Parse()

	if err := run(openAPIFile, commandsFile, strict); err != nil {
		fmt.Fprintln(os.Stderr, "docs-inventory:", err)
		os.Exit(1)
	}
}

// gate is one invariant -strict enforces: a summary counter that must be
// zero, and the name a reader would recognise it by.
//
// The audit that produced this tool reported 100% coverage as a point-in-time
// snapshot with nothing holding it there, which is the same shape of defect
// as a green check that never ran: the next PR could add an undocumented
// route and no signal would fire. -strict turns each published number into a
// ratchet.
type gate struct {
	name  string
	count func(summary) int
	// detail lists the offending rows, so CI says which operation to fix
	// rather than only how many are wrong.
	detail func(report) []string
}

func strictGates() []gate {
	return []gate{
		{"API operations with no documentation", func(s summary) int { return s.APIMissingDocs },
			func(r report) []string {
				return apiRowsWhere(r, func(rec apiRecord) bool { return rec.Status == "missing_docs" })
			}},
		{"API operations missing structural contract evidence", func(s summary) int { return s.APIContractGaps },
			func(r report) []string {
				return apiRowsWhere(r, func(rec apiRecord) bool { return len(rec.Contract.Structural.Missing) > 0 })
			}},
		{"API operations with a generic response schema", func(s summary) int { return s.APIGenericResponseSchemas },
			func(r report) []string {
				return apiRowsWhere(r, func(rec apiRecord) bool { return rec.GenericResponseSchema })
			}},
		{"API operations with a generic JSON request schema", func(s summary) int { return s.APIGenericJSONRequests },
			func(r report) []string {
				return apiRowsWhere(r, func(rec apiRecord) bool { return rec.GenericJSONRequest })
			}},
		{"CLI commands with no documentation page", func(s summary) int { return s.CLIRootDocsMissing },
			func(r report) []string {
				return cliRowsWhere(r, func(rec cliRecord) bool {
					return rec.Status != "documented_root" && rec.Status != "documented_exact"
				})
			}},
		{"CLI commands with undocumented flags", func(s summary) int { return s.CLIMissingFlagDocs },
			func(r report) []string {
				return cliRowsWhere(r, func(rec cliRecord) bool { return len(rec.MissingFlags) > 0 })
			}},
		{"environment variables with no documentation", func(s summary) int { return s.EnvironmentVariablesMissing },
			func(r report) []string {
				return surfaceRowsWhere(r.Env, func(rec surfaceRecord) bool { return rec.Status == "missing_docs" })
			}},
		{"manifest kinds with no documentation", func(s summary) int { return s.ManifestKindsMissing },
			func(r report) []string {
				return surfaceRowsWhere(r.Manifest, func(rec surfaceRecord) bool { return rec.Status == "missing_docs" })
			}},
		{"docs command references missing from the CLI manifest", func(s summary) int { return s.DocsCommandsMissing },
			func(r report) []string { return r.Reverse.missingRows("command") }},
		{"docs API paths missing from the OpenAPI inventory", func(s summary) int { return s.DocsAPIPathsMissing },
			func(r report) []string { return r.Reverse.missingRows("API path") }},
		{"docs environment variables missing from the source inventory", func(s summary) int { return s.DocsEnvMissing },
			func(r report) []string { return r.Reverse.missingRows("environment variable") }},
		{"docs manifest kinds missing from the source inventory", func(s summary) int { return s.DocsKindsMissing },
			func(r report) []string { return r.Reverse.missingRows("manifest kind") }},
		{"docs flags missing from the CLI manifest", func(s summary) int { return s.DocsFlagsMissing },
			func(r report) []string { return r.Reverse.missingRows("flag") }},
	}
}

func surfaceRowsWhere(records []surfaceRecord, match func(surfaceRecord) bool) []string {
	var out []string
	for _, rec := range records {
		if match(rec) {
			out = append(out, rec.Name)
		}
	}
	return out
}

func apiRowsWhere(r report, match func(apiRecord) bool) []string {
	var out []string
	for _, rec := range r.API {
		if match(rec) {
			out = append(out, rec.Method+" "+rec.Path)
		}
	}
	return out
}

func cliRowsWhere(r report, match func(cliRecord) bool) []string {
	var out []string
	for _, rec := range r.CLI {
		if match(rec) {
			row := rec.Path
			if len(rec.MissingFlags) > 0 {
				row += " (--" + strings.Join(rec.MissingFlags, ", --") + ")"
			}
			out = append(out, row)
		}
	}
	return out
}

// enforce renders every violated gate, naming up to maxStrictRows offenders
// each. It reports how many were elided rather than truncating silently — a
// gate that hides its own scope is the thing this whole file is against.
const maxStrictRows = 20

func enforce(r report) error {
	var b strings.Builder
	violations := 0
	for _, g := range strictGates() {
		n := g.count(r.Summary)
		if n == 0 {
			continue
		}
		violations++
		fmt.Fprintf(&b, "\n  %s: %d\n", g.name, n)
		rows := g.detail(r)
		for i, row := range rows {
			if i == maxStrictRows {
				fmt.Fprintf(&b, "    … and %d more\n", len(rows)-maxStrictRows)
				break
			}
			fmt.Fprintf(&b, "    %s\n", row)
		}
	}
	if violations == 0 {
		return nil
	}
	return fmt.Errorf("strict mode: %d documentation gate(s) failed.%s\nDocument the rows above, then re-run 'make docs-inventory'", violations, b.String())
}

func run(openAPIFile, commandsFile string, strict bool) error {
	openAPI, err := readOpenAPI(openAPIFile)
	if err != nil {
		return err
	}
	manifest, err := readCommands(commandsFile)
	if err != nil {
		return err
	}
	docs, err := readDocs()
	if err != nil {
		return err
	}
	sources, tests, err := readSourceSignals()
	if err != nil {
		return err
	}

	evidence := inventoryEndpointEvidence(docs)
	r := report{}
	for path, methods := range openAPI.Paths {
		for method, raw := range methods {
			if method == "parameters" {
				continue
			}
			var op openAPIOperation
			if err := json.Unmarshal(raw, &op); err != nil {
				return fmt.Errorf("decode OpenAPI operation %s %s: %w", method, path, err)
			}
			tag := first(op.Tags)
			if tag == "" {
				tag = apiResource(path)
			}
			rec := apiRecord{Method: strings.ToUpper(method), Path: path, OperationID: op.OperationID, Tag: tag}
			rec.SourceFile = sourceFor(path, sources)
			rec.ExactDocs = docsContaining(path, docs)
			resourcePage := filepath.ToSlash(filepath.Join("docs/api-reference", tag+".mdx"))
			if hasDoc(resourcePage, docs) {
				rec.ResourceDocs = []string{resourcePage}
			}
			switch {
			case len(rec.ExactDocs) > 0:
				rec.Status = "documented_exact"
			case len(rec.ResourceDocs) > 0:
				rec.Status = "documented_resource"
			default:
				rec.Status = "missing_docs"
			}
			rec.TestSignals = testContaining(path, tests)
			rec.Contract = contractFor(rec.Method, path, evidence, rec.SourceFile, rec.TestSignals, op.RequestBody != nil || len(op.Parameters) > 0)
			r.API = append(r.API, rec)
			rec.ConcreteResponseSchema = hasConcreteSuccessSchema(op.Responses)
			rec.GenericResponseSchema = !rec.ConcreteResponseSchema && hasSuccessSchema(op.Responses)
			rec.RequestBodyPresent = op.RequestBody != nil
			if op.RequestBody != nil {
				if jsonBody, ok := op.RequestBody.Content["application/json"]; ok {
					rec.ConcreteJSONRequest = isConcreteSchema(jsonBody.Schema)
					rec.GenericJSONRequest = !rec.ConcreteJSONRequest
				}
				for media := range op.RequestBody.Content {
					if media != "application/json" {
						rec.NonJSONRequestMedia = append(rec.NonJSONRequestMedia, media)
					}
				}
				sort.Strings(rec.NonJSONRequestMedia)
			}
			r.API[len(r.API)-1] = rec
		}
	}
	sort.Slice(r.API, func(i, j int) bool {
		if r.API[i].Path != r.API[j].Path {
			return r.API[i].Path < r.API[j].Path
		}
		return r.API[i].Method < r.API[j].Method
	})

	var walk func([]commandNode)
	walk = func(nodes []commandNode) {
		for _, node := range nodes {
			root := strings.Fields(node.Path)
			if len(root) == 0 {
				continue
			}
			rootPage := filepath.ToSlash(filepath.Join("docs/cli", root[0]+".mdx"))
			rec := cliRecord{Path: node.Path, Use: node.Use, Short: node.Short, Aliases: node.Aliases, Root: root[0]}
			for _, flag := range node.Flags {
				rec.Flags = append(rec.Flags, flag.Name)
			}
			rec.ExactDocs = cliDocsContainingCommand(node, docs)
			if hasDoc(rootPage, docs) {
				rec.RootDocs = []string{rootPage}
				switch {
				case len(rec.ExactDocs) > 0:
					rec.Status = "documented_exact"
				default:
					rec.Status = "documented_root"
				}
			} else {
				switch {
				case len(rec.ExactDocs) > 0:
					rec.Status = "documented_exact_no_root"
				default:
					rec.Status = "missing_root_docs"
				}
			}
			rec.TestSignals = cliTestSignals(node, tests)
			rec.DocumentedFlags, rec.MissingFlags = cliFlagEvidence(node, rec.ExactDocs, rec.RootDocs, docs)
			r.CLI = append(r.CLI, rec)
			walk(node.Commands)
		}
	}
	walk(manifest.Commands)
	sort.Slice(r.CLI, func(i, j int) bool { return r.CLI[i].Path < r.CLI[j].Path })
	r.Env = inventoryEnvironmentVariables(sources, docs)
	r.Manifest = inventoryManifestKinds(sources, docs)
	r.Reverse = inventoryDocsToCode(openAPI, manifest, docs, r.Env, r.Manifest)
	r.Summary = summarize(r)

	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(jsonReport, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", jsonReport, err)
	}
	if err := os.WriteFile(markdownReport, []byte(markdown(r)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", markdownReport, err)
	}
	fmt.Printf("docs-inventory: %d API operations, %d CLI commands\n", len(r.API), len(r.CLI))
	fmt.Printf("docs-inventory: wrote %s and %s\n", jsonReport, markdownReport)
	// Reports are written before the gate runs: a failing CI job should still
	// leave the regenerated inventory on disk for the operator to read.
	if strict {
		if err := enforce(r); err != nil {
			return err
		}
		fmt.Println("docs-inventory: strict mode — every documentation gate is clean.")
	}
	return nil
}

func inventoryEnvironmentVariables(sources, docs []docFile) []surfaceRecord {
	seen := map[string]bool{}
	for _, source := range sources {
		for _, name := range envNamePattern.FindAllString(source.Text, -1) {
			if !strings.HasSuffix(name, "_") {
				seen[name] = true
			}
		}
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]surfaceRecord, 0, len(names))
	for _, name := range names {
		rec := surfaceRecord{Name: name, Docs: docsContainingToken(name, docs), Status: "missing_docs"}
		if len(rec.Docs) > 0 {
			rec.Status = "documented"
		}
		result = append(result, rec)
	}
	return result
}

func inventoryManifestKinds(sources, docs []docFile) []surfaceRecord {
	seen := map[string]bool{}
	for _, source := range sources {
		for _, match := range manifestKindsPattern.FindAllStringSubmatch(source.Text, -1) {
			for _, name := range strings.Split(match[1], ",") {
				name = strings.TrimSpace(name)
				if name != "" {
					seen[name] = true
				}
			}
		}
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]surfaceRecord, 0, len(names))
	for _, name := range names {
		rec := surfaceRecord{Name: name, Docs: docsContainingToken(name, docs), Status: "missing_docs"}
		if len(rec.Docs) > 0 {
			rec.Status = "documented"
		}
		result = append(result, rec)
	}
	return result
}

func docsContainingToken(needle string, docs []docFile) []string {
	var result []string
	for _, doc := range docs {
		if tokenMentioned(doc.Text, needle) {
			result = append(result, doc.Path)
		}
	}
	return result
}

func tokenMentioned(text, needle string) bool {
	if needle == "" {
		return false
	}
	for offset := 0; offset < len(text); {
		idx := strings.Index(text[offset:], needle)
		if idx < 0 {
			return false
		}
		start := offset + idx
		end := start + len(needle)
		if (start == 0 || !isTokenByte(text[start-1])) && (end == len(text) || !isTokenByte(text[end])) {
			return true
		}
		offset = start + 1
	}
	return false
}

func isTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

func hasSuccessSchema(responses map[string]openAPIResponse) bool {
	for status, response := range responses {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		for _, media := range response.Content {
			if len(media.Schema) > 0 {
				return true
			}
		}
	}
	return false
}

func hasConcreteSuccessSchema(responses map[string]openAPIResponse) bool {
	for status, response := range responses {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		for _, media := range response.Content {
			if isConcreteSchema(media.Schema) {
				return true
			}
		}
	}
	return false
}

func isConcreteSchema(schema map[string]json.RawMessage) bool {
	if len(schema) == 0 {
		return false
	}
	if _, refSchema := schema["$ref"]; refSchema {
		return true
	}
	if typ, ok := rawString(schema["type"]); ok && typ != "object" {
		return true
	}
	for _, key := range []string{"properties", "items", "additionalProperties", "oneOf", "anyOf", "allOf", "enum", "format"} {
		if _, ok := schema[key]; ok {
			return true
		}
	}
	return false
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func readOpenAPI(name string) (openAPIDocument, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return openAPIDocument{}, fmt.Errorf("read %s: %w", name, err)
	}
	var doc openAPIDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, fmt.Errorf("decode %s: %w", name, err)
	}
	return doc, nil
}

func readCommands(name string) (commandManifest, error) {
	var data []byte
	var err error
	if name != "" {
		data, err = os.ReadFile(name)
	} else {
		cmd := exec.Command("go", "run", "./cmd/crewship", "commands", "--format", "json")
		data, err = cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return commandManifest{}, fmt.Errorf("run CLI command manifest: %w: %s", err, exitErr.Stderr)
			}
			return commandManifest{}, fmt.Errorf("run CLI command manifest: %w", err)
		}
	}
	if err != nil {
		return commandManifest{}, fmt.Errorf("read command manifest: %w", err)
	}
	var manifest commandManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("decode command manifest: %w", err)
	}
	return manifest, nil
}

func readDocs() ([]docFile, error) {
	var docs []docFile
	err := filepath.Walk(docsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".mdx") {
			return nil
		}
		slashPath := filepath.ToSlash(path)
		if slashPath == reportDir || strings.HasPrefix(slashPath, reportDir+"/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		docs = append(docs, docFile{Path: filepath.ToSlash(path), Text: string(data)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read docs: %w", err)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, nil
}

func readSourceSignals() ([]docFile, []docFile, error) {
	var sources, tests []docFile
	// Do not walk the repository root: node_modules and generated assets make
	// that needlessly expensive. These are the source roots that can contain
	// API or CLI registrations/tests, plus installer/service source that owns
	// documented environment variables.
	for _, root := range []string{"internal", "cmd", "scripts", "tools", "web", "packaging"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !isInventorySourceFile(path) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file := docFile{Path: filepath.ToSlash(path), Text: string(data)}
			if strings.HasSuffix(path, "_test.go") {
				tests = append(tests, file)
			}
			// Keep every non-test source here: API source lookup is a consumer of
			// this list, and the inventory derives environment variables and
			// manifest kinds from it.
			if !strings.HasSuffix(path, "_test.go") {
				sources = append(sources, file)
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("read Go source root %s: %w", root, err)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	sort.Slice(tests, func(i, j int) bool { return tests[i].Path < tests[j].Path })
	return sources, tests, nil
}

func isInventorySourceFile(name string) bool {
	if strings.HasSuffix(name, ".go") {
		return true
	}
	return name == "scripts/install.sh" || name == "packaging/crewship.env.example"
}

func sourceFor(path string, sources []docFile) string {
	for _, source := range sources {
		if strings.Contains(source.Text, `"`+path+`"`) || strings.Contains(source.Text, `"`+path+`",`) {
			return source.Path
		}
	}
	return ""
}

func docsContaining(needle string, docs []docFile) []string {
	return docsContainingWithin(needle, "", docs)
}

func docsContainingWithin(needle, prefix string, docs []docFile) []string {
	if needle == "" {
		return nil
	}
	var result []string
	for _, doc := range docs {
		if prefix != "" && !strings.HasPrefix(doc.Path, prefix) {
			continue
		}
		if strings.Contains(doc.Text, needle) {
			result = appendUnique(result, doc.Path)
		}
	}
	return result
}

func cliDocsContainingCommand(node commandNode, docs []docFile) []string {
	if node.Path == "" {
		return nil
	}
	// Bare words such as "get" or "status" occur on many pages. Count only
	// executable-looking references that include the CLI name and full path.
	return docsContainingWithin("crewship "+node.Path, "docs/cli/", docs)
}

func cliFlagEvidence(node commandNode, exactDocs, rootDocs []string, docs []docFile) (documented, missing []string) {
	if len(node.Flags) == 0 {
		return nil, nil
	}
	paths := append(append([]string{}, exactDocs...), rootDocs...)
	for _, flag := range node.Flags {
		found := false
		for _, path := range paths {
			for _, doc := range docs {
				if doc.Path == path && flagMentioned(doc.Text, flag.Name) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			documented = append(documented, flag.Name)
		} else {
			missing = append(missing, flag.Name)
		}
	}
	return documented, missing
}

// flagMentioned reports whether text documents the flag `--name` itself,
// rather than merely containing it as the prefix of a longer flag.
//
// A plain strings.Contains inflates the coverage number in exactly the case
// this repo has: `--server` and `--server-allow-mismatch` are both global
// flags on every command, so a page that documents only the latter satisfied
// the former too, and "343/343 commands document all flags" could be true
// while `--server` appeared on no page at all. The boundary is checked on
// both sides — a longer flag name must not match a shorter one, and a match
// inside a word (or after another dash) is not a mention.
func flagMentioned(text, name string) bool {
	needle := "--" + name
	for offset := 0; offset < len(text); {
		idx := strings.Index(text[offset:], needle)
		if idx < 0 {
			return false
		}
		start := offset + idx
		end := start + len(needle)
		leftOK := start == 0 || !isFlagNameByte(text[start-1])
		rightOK := end == len(text) || !isFlagNameByte(text[end])
		if leftOK && rightOK {
			return true
		}
		offset = start + 1
	}
	return false
}

// isFlagNameByte reports whether b can appear inside a long-flag name (or is
// the dash that would make the surrounding token a different flag).
func isFlagNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_':
		return true
	}
	return false
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if !seen[value] {
			values = append(values, value)
			seen[value] = true
		}
	}
	return values
}

func testContaining(needle string, tests []docFile) []string {
	var result []string
	for _, test := range tests {
		if strings.Contains(test.Text, needle) || strings.Contains(test.Text, strings.ReplaceAll(needle, "/api/v1", "")) {
			result = append(result, test.Path)
		}
	}
	return limitSignals(result)
}

func cliTestSignals(node commandNode, tests []docFile) []string {
	root := first(strings.Fields(node.Path))
	var result []string
	for _, test := range tests {
		if !strings.HasPrefix(test.Path, "cmd/crewship/") {
			continue
		}
		base := filepath.Base(test.Path)
		rootFileSignal := strings.Contains(base, "cmd_"+strings.ReplaceAll(root, "-", "_"))
		exactSignal := strings.Contains(test.Text, `"`+node.Path+`"`) ||
			strings.Contains(test.Text, "`"+node.Path+"`") ||
			(node.Use != "" && strings.Contains(test.Text, `"`+node.Use+`"`))
		if rootFileSignal || exactSignal {
			result = append(result, test.Path)
		}
	}
	return limitSignals(result)
}

func limitSignals(values []string) []string {
	if len(values) <= 8 {
		return values
	}
	return append(values[:8:8], "…")
}

func hasDoc(path string, docs []docFile) bool {
	for _, doc := range docs {
		if doc.Path == path {
			return true
		}
	}
	return false
}

func apiResource(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "v1" && i+1 < len(parts) {
			if parts[i+1] == "internal" && i+2 < len(parts) {
				return parts[i+2]
			}
			return parts[i+1]
		}
	}
	if len(parts) >= 2 && parts[0] == "api" {
		return parts[1]
	}
	return first(parts)
}

func summarize(r report) summary {
	s := summary{APIOperations: len(r.API), CLIOperations: len(r.CLI)}
	s.EnvironmentVariables = len(r.Env)
	s.ManifestKinds = len(r.Manifest)
	s.DocsCommandsMissing = r.Reverse.MissingCommands
	s.DocsAPIPathsMissing = r.Reverse.MissingAPIPaths
	s.DocsEnvMissing = r.Reverse.MissingEnv
	s.DocsKindsMissing = r.Reverse.MissingKinds
	s.DocsFlagsMissing = r.Reverse.MissingFlags
	for _, rec := range r.Env {
		if rec.Status == "missing_docs" {
			s.EnvironmentVariablesMissing++
		}
	}
	for _, rec := range r.Manifest {
		if rec.Status == "missing_docs" {
			s.ManifestKindsMissing++
		}
	}
	for _, rec := range r.API {
		switch rec.Status {
		case "documented_exact":
			s.APIExactDocs++
		case "documented_resource":
			s.APIResourceDocs++
		case "missing_docs":
			s.APIMissingDocs++
		}
		if len(rec.TestSignals) > 0 {
			s.APIWithTestSignals++
		}
		if rec.ConcreteResponseSchema {
			s.APIWithConcreteResponseSchemas++
		}
		if rec.GenericResponseSchema {
			s.APIGenericResponseSchemas++
		}
		if rec.RequestBodyPresent {
			s.APIWithRequestBodies++
		}
		if rec.ConcreteJSONRequest {
			s.APIWithConcreteJSONRequests++
		}
		if rec.GenericJSONRequest {
			s.APIGenericJSONRequests++
		}
		if len(rec.NonJSONRequestMedia) > 0 {
			s.APINonJSONRequestBodies++
		}
		if len(rec.Contract.Structural.Missing) > 0 {
			s.APIContractGaps++
		}
	}
	for _, rec := range r.CLI {
		if rec.Status == "documented_exact" || rec.Status == "documented_root" || rec.Status == "documented_exact_no_root" {
			if len(rec.ExactDocs) > 0 {
				s.CLIExactDocs++
			}
		}
		if rec.Status == "documented_root" || rec.Status == "documented_exact" {
			s.CLIRootDocsPresent++
		} else {
			s.CLIRootDocsMissing++
		}
		if len(rec.TestSignals) > 0 {
			s.CLIWithTestSignals++
		}
		if len(rec.Flags) > 0 {
			s.CLIWithFlags++
			if len(rec.MissingFlags) == 0 {
				s.CLIWithAllFlagsDocumented++
			} else {
				s.CLIMissingFlagDocs++
			}
		}
	}
	return s
}

func markdown(r report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Release 1.0 API/CLI documentation inventory\n\n")
	fmt.Fprintf(&b, "This report is generated by `go run ./scripts/docs-inventory`; identical inputs produce identical output. The machine-readable full inventory is next to this file as `release-1-0-api-cli-inventory.json`.\n\n")
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "| Surface | Count | Documentation signal | Test signal |\n|---|---:|---|---|\n")
	fmt.Fprintf(&b, "| API operations | %d | %d exact, %d resource-level, %d missing | %d with a test signal |\n", r.Summary.APIOperations, r.Summary.APIExactDocs, r.Summary.APIResourceDocs, r.Summary.APIMissingDocs, r.Summary.APIWithTestSignals)
	fmt.Fprintf(&b, "| CLI commands | %d | %d exact mentions, %d root pages present, %d root pages missing | %d with a test signal |\n\n", r.Summary.CLIOperations, r.Summary.CLIExactDocs, r.Summary.CLIRootDocsPresent, r.Summary.CLIRootDocsMissing, r.Summary.CLIWithTestSignals)
	fmt.Fprintf(&b, "Response schema quality: %d API operations have a concrete 2xx schema; %d still use the generic object fallback.\n\n", r.Summary.APIWithConcreteResponseSchemas, r.Summary.APIGenericResponseSchemas)
	fmt.Fprintf(&b, "Request schema quality: %d operations have request bodies; %d have concrete JSON schemas, %d use non-JSON media types, and %d still use a generic JSON fallback.\n\n", r.Summary.APIWithRequestBodies, r.Summary.APIWithConcreteJSONRequests, r.Summary.APINonJSONRequestBodies, r.Summary.APIGenericJSONRequests)
	fmt.Fprintf(&b, "CLI flag quality: %d commands define flags; %d document all of their flags and %d still have undocumented flag(s).\n\n", r.Summary.CLIWithFlags, r.Summary.CLIWithAllFlagsDocumented, r.Summary.CLIMissingFlagDocs)
	fmt.Fprintf(&b, "Environment variables: %d discovered, %d missing documentation. Manifest kinds: %d discovered, %d missing documentation.\n\n", r.Summary.EnvironmentVariables, r.Summary.EnvironmentVariablesMissing, r.Summary.ManifestKinds, r.Summary.ManifestKindsMissing)
	fmt.Fprintf(&b, "Docs → code references: %d commands, %d API paths, %d environment variables, %d manifest kinds, and %d flags; missing symbols: %d, %d, %d, %d, and %d respectively.\n\n", r.Reverse.CommandReferences, r.Reverse.APIPathReferences, r.Reverse.EnvReferences, r.Reverse.KindReferences, r.Reverse.FlagReferences, r.Reverse.MissingCommands, r.Reverse.MissingAPIPaths, r.Reverse.MissingEnv, r.Reverse.MissingKinds, r.Reverse.MissingFlags)

	fmt.Fprintf(&b, "## API operations needing attention\n\n")
	fmt.Fprintf(&b, "These are missing a resource-level API reference page or have no exact route mention. Exactness is a review signal, not a final correctness verdict.\n\n")
	fmt.Fprintf(&b, "| Method | Path | Operation | Status | Missing structural fields | Source | Runtime/test signals |\n|---|---|---|---|---|---|---|\n")
	for _, rec := range r.API {
		if len(rec.Contract.Structural.Missing) == 0 && rec.Status == "documented_exact" {
			continue
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` | %s | `%s` | %s |\n", rec.Method, rec.Path, rec.OperationID, rec.Status, joinOrDash(rec.Contract.Structural.Missing), rec.SourceFile, joinOrDash(rec.Contract.SemanticRuntime.TestSignals))
	}
	fmt.Fprintf(&b, "\n## CLI commands needing attention\n\n")
	fmt.Fprintf(&b, "| Command | Use | Status | Documentation | Tests | Flag gaps |\n|---|---|---|---|---|---|\n")
	for _, rec := range r.CLI {
		if (rec.Status == "documented_root" || rec.Status == "documented_exact") && len(rec.TestSignals) > 0 && len(rec.MissingFlags) == 0 {
			continue
		}
		docs := appendUnique(append([]string{}, rec.ExactDocs...), rec.RootDocs...)
		missing := joinOrDash(rec.MissingFlags)
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s | missing flags: %s |\n", rec.Path, rec.Use, rec.Status, joinOrDash(docs), joinOrDash(rec.TestSignals), missing)
	}
	return b.String()
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	return strings.Join(values, ", ")
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
