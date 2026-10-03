package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
)

// Discover the same generated contract served by the binary. No network or
// credentials are needed, and new generated operations are included automatically.
type apiOperation struct {
	ID          string   `json:"operation_id" yaml:"operation_id"`
	Method      string   `json:"method" yaml:"method"`
	Path        string   `json:"path" yaml:"path"`
	Summary     string   `json:"summary,omitempty" yaml:"summary,omitempty"`
	Tags        []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	RequiresYes bool     `json:"requires_yes" yaml:"requires_yes"`
}

type apiDocument struct {
	OpenAPI    string                                `json:"openapi"`
	Info       map[string]any                        `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components map[string]map[string]json.RawMessage `json:"components"`
}

func loadAPIDocument() (*apiDocument, error) {
	var doc apiDocument
	if err := json.Unmarshal(api.OpenAPISpecJSON(), &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func apiHTTPMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

func apiRequiresYes(method string) bool {
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}

func (d *apiDocument) operations(search, method string) ([]apiOperation, error) {
	ops := make([]apiOperation, 0)
	for path, item := range d.Paths {
		for verb, raw := range item {
			verb = strings.ToUpper(verb)
			if !apiHTTPMethod(verb) || (method != "" && method != verb) {
				continue
			}
			var op struct {
				ID      string   `json:"operationId"`
				Summary string   `json:"summary"`
				Tags    []string `json:"tags"`
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				return nil, err
			}
			if !strings.Contains(strings.ToLower(path+" "+op.ID+" "+op.Summary+" "+strings.Join(op.Tags, " ")), strings.ToLower(search)) {
				continue
			}
			ops = append(ops, apiOperation{op.ID, verb, path, op.Summary, op.Tags, apiRequiresYes(verb)})
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path == ops[j].Path {
			return ops[i].Method < ops[j].Method
		}
		return ops[i].Path < ops[j].Path
	})
	return ops, nil
}

// A selected schema remains a standalone OpenAPI document. Include only the
// transitive component closure, retaining refs (including cycles) without
// expanding thousands of unrelated schemas into the agent's context window.
func (d *apiDocument) selectOperation(op apiOperation) (map[string]any, error) {
	item := map[string]json.RawMessage{strings.ToLower(op.Method): d.Paths[op.Path][strings.ToLower(op.Method)]}
	for _, key := range []string{"parameters", "servers", "summary", "description"} {
		if raw, ok := d.Paths[op.Path][key]; ok {
			item[key] = raw
		}
	}
	components := map[string]map[string]json.RawMessage{}
	var visit func(any) error
	visit = func(v any) error {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				parts := strings.Split(ref, "/")
				if len(parts) != 4 || parts[0] != "#" || parts[1] != "components" {
					return fmt.Errorf("unsupported schema reference %q", ref)
				}
				kind, name := parts[2], strings.ReplaceAll(strings.ReplaceAll(parts[3], "~1", "/"), "~0", "~")
				if components[kind] == nil {
					components[kind] = map[string]json.RawMessage{}
				}
				if _, seen := components[kind][name]; !seen {
					raw, ok := d.Components[kind][name]
					if !ok {
						return fmt.Errorf("missing schema reference %q", ref)
					}
					components[kind][name] = raw
					var next any
					if err := json.Unmarshal(raw, &next); err != nil {
						return err
					}
					if err := visit(next); err != nil {
						return err
					}
				}
			}
			for _, next := range v {
				if err := visit(next); err != nil {
					return err
				}
			}
		case []any:
			for _, next := range v {
				if err := visit(next); err != nil {
					return err
				}
			}
		}
		return nil
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if err := visit(value); err != nil {
		return nil, err
	}
	// Security requirements reference schemes by name, not by $ref.
	if schemes := d.Components["securitySchemes"]; len(schemes) > 0 {
		components["securitySchemes"] = schemes
	}
	return map[string]any{"openapi": d.OpenAPI, "info": d.Info, "paths": map[string]any{op.Path: item}, "components": components}, nil
}

func apiStructuredOutput(cmd *cobra.Command, value any) error {
	f := newFormatter()
	f.Writer = cmd.OutOrStdout()
	switch f.Format {
	case "table", "", "quiet":
		f.Format = "json"
	case "json", "yaml", "ndjson":
	default:
		return apiValidation("unsupported output format")
	}
	// Normalize RawMessage and json.Number through JSON before YAML encoding.
	// yaml.v3 otherwise renders raw schemas as byte arrays and numbers as strings.
	if f.Format == "yaml" {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var node yaml.Node
		if err := yaml.Unmarshal(raw, &node); err != nil {
			return err
		}
		var blockStyle func(*yaml.Node)
		blockStyle = func(n *yaml.Node) {
			n.Style = 0
			for _, child := range n.Content {
				blockStyle(child)
			}
		}
		blockStyle(&node)
		return f.YAML(&node)
	}
	return f.Auto(value, nil, nil)
}

func newAPICommand() *cobra.Command {
	root := &cobra.Command{SilenceUsage: true, SilenceErrors: true, Use: "api", Short: "Discover API operations, inspect schemas, and send authenticated requests", Long: `Discover the API embedded in this CLI version, without connecting to a server.
Use request for HTTP endpoints, including those without a dedicated CLI command.
Server permissions still apply. Prefer dedicated commands for interactive flows
and streaming. The server's /openapi.json describes its installed version.`}
	list := &cobra.Command{Use: "operations [search]", Short: "Search the complete embedded API operation catalog", ValidArgsFunction: cobra.NoFileCompletions, Args: apiArgs(cobra.MaximumNArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		method, _ := cmd.Flags().GetString("method")
		method = strings.ToUpper(method)
		if method != "" && !apiHTTPMethod(method) {
			return apiValidation("unsupported HTTP method")
		}
		search := ""
		if len(args) > 0 {
			search = args[0]
		}
		doc, err := loadAPIDocument()
		if err != nil {
			return err
		}
		ops, err := doc.operations(search, method)
		if err != nil {
			return err
		}
		rows := make([][]string, 0, len(ops))
		for _, op := range ops {
			rows = append(rows, []string{op.ID, op.Method, op.Path})
		}
		f := newFormatter()
		f.Writer = cmd.OutOrStdout()
		switch f.Format {
		case "", "table", "quiet", "json", "yaml", "ndjson":
		default:
			return apiValidation("unsupported output format")
		}
		if f.Format == "table" || f.Format == "" {
			for i := range rows {
				rows[i] = []string{ops[i].Method, ops[i].Path}
			}
			return f.Auto(ops, []string{"METHOD", "PATH"}, rows)
		}
		return f.Auto(ops, []string{"OPERATION", "METHOD", "PATH"}, rows)
	}}
	list.Flags().String("method", "", "Filter by HTTP method")
	_ = list.RegisterFlagCompletionFunc("method", completeAPIMethod)
	schema := &cobra.Command{Use: "schema [operation-id | METHOD path]", Short: "Print OpenAPI with request, response, and referenced schemas", Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 2 {
			return apiValidation("expected an operation ID or METHOD path")
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			var v any
			dec := json.NewDecoder(bytes.NewReader(api.OpenAPISpecJSON()))
			dec.UseNumber()
			if err := dec.Decode(&v); err != nil {
				return err
			}
			return apiStructuredOutput(cmd, v)
		}
		doc, err := loadAPIDocument()
		if err != nil {
			return err
		}
		ops, err := doc.operations("", "")
		if err != nil {
			return err
		}
		for _, op := range ops {
			if (len(args) == 1 && args[0] == op.ID) || (len(args) == 2 && strings.EqualFold(args[0], op.Method) && args[1] == op.Path) {
				value, err := doc.selectOperation(op)
				if err != nil {
					return err
				}
				return apiStructuredOutput(cmd, value)
			}
		}
		return cli.NotFoundf("API operation not found; run 'crewship api operations'")
	}}
	schema.ValidArgsFunction = completeAPISchema
	root.AddCommand(list, schema, newAPIRequestCommand())
	return root
}

func apiValidation(message string) error {
	return cli.WithExitCode(fmt.Errorf("%s", message), cli.ExitValidation)
}

func apiArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		return cli.WithExitCode(validate(cmd, args), cli.ExitValidation)
	}
}

func completeAPIMethod(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	var matches []string
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"} {
		if strings.HasPrefix(method, strings.ToUpper(prefix)) {
			matches = append(matches, method)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func completeAPISchema(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	var matches []string
	if len(args) == 0 {
		matches, _ = completeAPIMethod(cmd, args, prefix)
	}
	if len(args) > 1 || len(args) == 1 && !apiHTTPMethod(strings.ToUpper(args[0])) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	doc, err := loadAPIDocument()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	method := ""
	if len(args) == 1 {
		method = strings.ToUpper(args[0])
	}
	ops, err := doc.operations("", method)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	for _, op := range ops {
		value := op.ID
		if len(args) == 1 {
			value = op.Path
		}
		if strings.HasPrefix(value, prefix) {
			matches = append(matches, value)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func init() { rootCmd.AddCommand(newAPICommand()) }
