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
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	OperationID string   `json:"operationId"`
	Tags        []string `json:"tags"`
}

type commandManifest struct {
	Commands []commandNode `json:"commands"`
}

type commandNode struct {
	Path     string        `json:"path"`
	Use      string        `json:"use"`
	Short    string        `json:"short"`
	Aliases  []string      `json:"aliases"`
	Commands []commandNode `json:"commands"`
}

type docFile struct {
	Path string `json:"path"`
	Text string `json:"-"`
}

type apiRecord struct {
	Method       string   `json:"method"`
	Path         string   `json:"path"`
	OperationID  string   `json:"operation_id"`
	Tag          string   `json:"tag"`
	SourceFile   string   `json:"source_file,omitempty"`
	ExactDocs    []string `json:"exact_docs,omitempty"`
	ResourceDocs []string `json:"resource_docs,omitempty"`
	Status       string   `json:"status"`
	TestSignals  []string `json:"test_signals,omitempty"`
}

type cliRecord struct {
	Path        string   `json:"path"`
	Use         string   `json:"use"`
	Short       string   `json:"short"`
	Aliases     []string `json:"aliases,omitempty"`
	Root        string   `json:"root"`
	ExactDocs   []string `json:"exact_docs,omitempty"`
	RootDocs    []string `json:"root_docs,omitempty"`
	Status      string   `json:"status"`
	TestSignals []string `json:"test_signals,omitempty"`
}

type report struct {
	GeneratedAt string      `json:"generated_at"`
	GitRef      string      `json:"git_ref,omitempty"`
	Summary     summary     `json:"summary"`
	API         []apiRecord `json:"api"`
	CLI         []cliRecord `json:"cli"`
}

type summary struct {
	APIOperations      int `json:"api_operations"`
	APIExactDocs       int `json:"api_exact_docs"`
	APIResourceDocs    int `json:"api_resource_docs"`
	APIMissingDocs     int `json:"api_missing_docs"`
	CLIOperations      int `json:"cli_operations"`
	CLIExactDocs       int `json:"cli_exact_docs"`
	CLIRootDocsPresent int `json:"cli_root_docs_present"`
	CLIRootDocsMissing int `json:"cli_root_docs_missing"`
	APIWithTestSignals int `json:"api_with_test_signals"`
	CLIWithTestSignals int `json:"cli_with_test_signals"`
}

func main() {
	var openAPIFile string
	var commandsFile string
	flag.StringVar(&openAPIFile, "openapi", openAPIPath, "generated OpenAPI document")
	flag.StringVar(&commandsFile, "commands", "", "CLI command manifest JSON; empty runs crewship commands --format json")
	flag.Parse()

	if err := run(openAPIFile, commandsFile); err != nil {
		fmt.Fprintln(os.Stderr, "docs-inventory:", err)
		os.Exit(1)
	}
}

func run(openAPIFile, commandsFile string) error {
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

	r := report{GeneratedAt: time.Now().UTC().Format(time.RFC3339), GitRef: gitRef()}
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
			r.API = append(r.API, rec)
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
			r.CLI = append(r.CLI, rec)
			walk(node.Commands)
		}
	}
	walk(manifest.Commands)
	sort.Slice(r.CLI, func(i, j int) bool { return r.CLI[i].Path < r.CLI[j].Path })
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
	return nil
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
	return docs, nil
}

func readSourceSignals() ([]docFile, []docFile, error) {
	var sources, tests []docFile
	// Do not walk the repository root: node_modules and generated assets make
	// that needlessly expensive. These are the source roots that can contain
	// API or CLI registrations/tests.
	for _, root := range []string{"internal", "cmd", "scripts", "tools", "web"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
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
			if strings.HasPrefix(filepath.ToSlash(path), "internal/api/") {
				sources = append(sources, file)
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("read Go source root %s: %w", root, err)
		}
	}
	return sources, tests, nil
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
	}
	return s
}

func markdown(r report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Release 1.0 API/CLI documentation inventory\n\n")
	fmt.Fprintf(&b, "Generated: `%s`\n\n", r.GeneratedAt)
	if r.GitRef != "" {
		fmt.Fprintf(&b, "Git ref: `%s`\n\n", r.GitRef)
	}
	fmt.Fprintf(&b, "This report is generated by `go run ./scripts/docs-inventory`. The machine-readable full inventory is next to this file as `release-1-0-api-cli-inventory.json`.\n\n")
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "| Surface | Count | Documentation signal | Test signal |\n|---|---:|---|---|\n")
	fmt.Fprintf(&b, "| API operations | %d | %d exact, %d resource-level, %d missing | %d with a test signal |\n", r.Summary.APIOperations, r.Summary.APIExactDocs, r.Summary.APIResourceDocs, r.Summary.APIMissingDocs, r.Summary.APIWithTestSignals)
	fmt.Fprintf(&b, "| CLI commands | %d | %d exact mentions, %d root pages present, %d root pages missing | %d with a test signal |\n\n", r.Summary.CLIOperations, r.Summary.CLIExactDocs, r.Summary.CLIRootDocsPresent, r.Summary.CLIRootDocsMissing, r.Summary.CLIWithTestSignals)

	fmt.Fprintf(&b, "## API operations needing attention\n\n")
	fmt.Fprintf(&b, "These are missing a resource-level API reference page or have no exact route mention. Exactness is a review signal, not a final correctness verdict.\n\n")
	fmt.Fprintf(&b, "| Method | Path | Operation | Status | Source | Tests |\n|---|---|---|---|---|---|\n")
	for _, rec := range r.API {
		if rec.Status == "documented_exact" {
			continue
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` | `%s` | %s |\n", rec.Method, rec.Path, rec.OperationID, rec.Status, rec.SourceFile, joinOrDash(rec.TestSignals))
	}
	fmt.Fprintf(&b, "\n## CLI commands needing attention\n\n")
	fmt.Fprintf(&b, "| Command | Use | Status | Documentation | Tests |\n|---|---|---|---|---|\n")
	for _, rec := range r.CLI {
		if (rec.Status == "documented_root" || rec.Status == "documented_exact") && len(rec.TestSignals) > 0 {
			continue
		}
		docs := appendUnique(append([]string{}, rec.ExactDocs...), rec.RootDocs...)
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s |\n", rec.Path, rec.Use, rec.Status, joinOrDash(docs), joinOrDash(rec.TestSignals))
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

func gitRef() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes.TrimSpace(data)))
}
