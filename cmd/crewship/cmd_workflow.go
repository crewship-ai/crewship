package main

// Workflow-template admin CLI — list / get / create / delete the reusable
// issue-lifecycle templates defined per workspace. Backed by the
// /api/v1/workflow-templates REST surface (see
// internal/api/workflow_templates_handler.go).
//
// The CLI takes a <slug> argument on `get` and `delete`, even though the
// underlying DB column is `name`. SPEC-2 manifests use `metadata.slug` as
// the workspace-unique identifier across every kind; mapping slug→name
// keeps the CLI consistent with `crewship apply`.
//
// IMPORTANT: this file does NOT register itself in cmd/crewship/main.go;
// the orchestrator wires `workflowCmd` after the parallel agents finish.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// workflowTemplateItem mirrors the JSON returned by the server's List/Get/
// Create handlers. Optional columns surface as pointers so we can render
// them as "-" instead of an empty cell.
type workflowTemplateItem struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	TemplateJSON string  `json:"template_json"`
	Icon         *string `json:"icon"`
	Color        *string `json:"color"`
	IsBuiltin    bool    `json:"is_builtin"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// workflowManifestStage is the per-stage shape under `spec.stages` in the
// manifest YAML. We re-serialise this list as JSON for the API's
// `template_json` field, which is what the handler stores in
// workflow_templates.template_json.
type workflowManifestStage struct {
	Name     string `yaml:"name"     json:"name"`
	Type     string `yaml:"type"     json:"type"`
	Position int    `yaml:"position" json:"position"`
	Color    string `yaml:"color,omitempty" json:"color,omitempty"`
}

// workflowManifestDoc is the SPEC-2 envelope for `kind: WorkflowTemplate`.
// Only the fields the CLI actually consumes are declared — unknown fields
// are silently ignored by go-yaml so the CLI tolerates spec extensions.
type workflowManifestDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
		Slug string `yaml:"slug"`
	} `yaml:"metadata"`
	Spec struct {
		Description string                  `yaml:"description"`
		Icon        string                  `yaml:"icon"`
		Color       string                  `yaml:"color"`
		Stages      []workflowManifestStage `yaml:"stages"`
	} `yaml:"spec"`
}

var workflowCmd = &cobra.Command{
	Use:     "workflow",
	Aliases: []string{"workflows", "workflow-template", "workflow-templates"},
	Short:   "Manage workspace workflow templates",
}

var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workflow templates in the active workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/workflow-templates")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var templates []workflowTemplateItem
		if err := cli.ReadJSON(resp, &templates); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "NAME", "BUILTIN", "STAGES", "ICON", "COLOR"}
		var rows [][]string
		for _, t := range templates {
			builtin := "no"
			if t.IsBuiltin {
				builtin = "yes"
			}
			rows = append(rows, []string{
				truncateID(t.ID, 12),
				t.Name,
				builtin,
				fmt.Sprintf("%d", countStages(t.TemplateJSON)),
				derefStr(t.Icon, "-"),
				derefStr(t.Color, "-"),
			})
		}
		return f.Auto(templates, headers, rows)
	},
}

var workflowGetCmd = &cobra.Command{
	Use:   "get <slug>",
	Short: "Show one workflow template by slug (matches against name)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		t, err := findWorkflowTemplateBySlug(client, args[0])
		if err != nil {
			return err
		}

		f := newFormatter()
		desc := derefStr(t.Description, "-")
		pairs := [][]string{
			{"ID", t.ID},
			{"Name", t.Name},
			{"Description", desc},
			{"Icon", derefStr(t.Icon, "-")},
			{"Color", derefStr(t.Color, "-")},
			{"Builtin", fmt.Sprintf("%v", t.IsBuiltin)},
			{"Stages", fmt.Sprintf("%d", countStages(t.TemplateJSON))},
			{"Template JSON", t.TemplateJSON},
			{"Created", t.CreatedAt},
			{"Updated", t.UpdatedAt},
		}
		return f.AutoDetail(t, pairs)
	},
}

var workflowCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a workflow template from a YAML manifest file",
	Long: `Create a workflow template by submitting the manifest in -f / --file.

The file MUST follow the SPEC-2 WorkflowTemplate envelope:

  apiVersion: crewship/v1
  kind: WorkflowTemplate
  metadata:
    name: Engineering Standard
    slug: engineering-standard
  spec:
    description: Default issue lifecycle
    icon: ":hammer_and_wrench:"
    color: "#3B82F6"
    stages:
      - { name: backlog,     type: open,      position: 1, color: "#9CA3AF" }
      - { name: in_progress, type: started,   position: 2, color: "#3B82F6" }
      - { name: done,        type: completed, position: 3, color: "#10B981" }

See docs/manifest/workflow_template.md for the full schema.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		path, _ := cmd.Flags().GetString("file")
		if path == "" {
			return fmt.Errorf("-f/--file is required")
		}

		body, err := loadWorkflowTemplateBody(path)
		if err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/workflow-templates", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created workflowTemplateItem
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Workflow template created: %s (%s)", created.Name, created.ID))
		return nil
	},
}

var workflowDeleteCmd = &cobra.Command{
	Use:     "delete <slug>",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete a workflow template by slug (matches against name)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete workflow template %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		t, err := findWorkflowTemplateBySlug(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/workflow-templates/" + t.ID)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Workflow template %s deleted.", t.Name))
		return nil
	},
}

// findWorkflowTemplateBySlug resolves a CLI <slug> argument to the matching
// template. The server has no slug column; the SPEC-2 convention is that
// metadata.slug equals the template name. We accept a hit on either the
// raw name or the kebab-cased name so users typing `engineering-standard`
// match a stored "Engineering Standard" without surprise.
func findWorkflowTemplateBySlug(client *cli.Client, slug string) (*workflowTemplateItem, error) {
	resp, err := client.Get("/api/v1/workflow-templates")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}

	var templates []workflowTemplateItem
	if err := cli.ReadJSON(resp, &templates); err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimSpace(slug))
	// Exact-name match wins: an operator who typed the literal name
	// expects that hit even if other templates happen to slugify to
	// the same value. After that, fall back to a slug match — but
	// detect collisions across the catalog so `workflow delete <slug>`
	// can't silently target the wrong row.
	for i := range templates {
		if strings.EqualFold(templates[i].Name, slug) {
			return &templates[i], nil
		}
	}
	var matches []*workflowTemplateItem
	for i := range templates {
		if slugify(templates[i].Name) == target {
			matches = append(matches, &templates[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no workflow template matches slug %q", slug)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, fmt.Sprintf("%q (id=%s)", m.Name, m.ID))
		}
		return nil, fmt.Errorf("ambiguous slug %q matches %d workflow templates: %s — use the exact name to disambiguate", slug, len(matches), strings.Join(names, ", "))
	}
}

// loadWorkflowTemplateBody reads a YAML manifest from disk and converts it
// into the JSON shape the POST handler expects. The handler validates the
// stage list itself, so we deliberately avoid duplicating that logic — we
// only enforce the kind/shape minimum needed to fail fast on obvious typos.
func loadWorkflowTemplateBody(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc workflowManifestDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != "" && doc.Kind != "WorkflowTemplate" {
		return nil, fmt.Errorf("expected kind: WorkflowTemplate, got %q", doc.Kind)
	}
	if doc.Metadata.Name == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}
	if len(doc.Spec.Stages) == 0 {
		return nil, fmt.Errorf("spec.stages must contain at least one stage")
	}

	stagesJSON, err := marshalStages(doc.Spec.Stages)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"name":          doc.Metadata.Name,
		"template_json": stagesJSON,
	}
	if doc.Spec.Description != "" {
		body["description"] = doc.Spec.Description
	}
	if doc.Spec.Icon != "" {
		body["icon"] = doc.Spec.Icon
	}
	if doc.Spec.Color != "" {
		body["color"] = doc.Spec.Color
	}
	return body, nil
}

// marshalStages converts the YAML-decoded stages back into a JSON string
// (the API's `template_json` field is a TEXT column holding a JSON-encoded
// list). Marshalling via the standard library keeps key order + escapes
// consistent with what the server's validator re-serialises, so manifest
// round-trips stay byte-stable.
func marshalStages(stages []workflowManifestStage) (string, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(stages); err != nil {
		return "", fmt.Errorf("encode stages: %w", err)
	}
	// json.Encoder.Encode appends a trailing newline; the server stores
	// the canonical (no-newline) form so we strip it for byte-stable diffs.
	return strings.TrimRight(buf.String(), "\n"), nil
}

// countStages reports the number of stages embedded in a stored
// template_json blob. Failure to parse falls back to 0 so a corrupt row
// doesn't crash `crewship workflow list` — the table view will simply show
// 0 stages and the user can investigate via `crewship workflow get`.
func countStages(raw string) int {
	var stages []workflowManifestStage
	if err := json.Unmarshal([]byte(raw), &stages); err != nil {
		return 0
	}
	return len(stages)
}

// slugify lowercases + replaces whitespace with hyphens so we can compare a
// user-provided slug like "engineering-standard" against a stored name like
// "Engineering Standard". Intentionally conservative — manifest writers who
// need non-ASCII names should match by exact name instead.
func slugify(s string) string {
	out := strings.ToLower(strings.TrimSpace(s))
	out = strings.ReplaceAll(out, "_", "-")
	out = strings.Join(strings.Fields(out), "-")
	return out
}

func init() {
	// NO -f shorthand: rootCmd already owns -f for the persistent
	// --format flag. Registering `-f` on a child for a different long
	// name ("file") triggers a cobra-level panic at FIRST invocation
	// of `crewship workflow create --help` — cobra merges persistent
	// flags into every child's flagset at lookup time, and the
	// shorthand collision aborts the process. Use the long form:
	// `crewship workflow create --file path.yaml`. Matches the
	// pattern in `crewship apply --file` which is also shorthand-free.
	workflowCreateCmd.Flags().String("file", "", "Path to a SPEC-2 YAML manifest (required)")
	workflowDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")

	workflowCmd.AddCommand(workflowListCmd)
	workflowCmd.AddCommand(workflowGetCmd)
	workflowCmd.AddCommand(workflowCreateCmd)
	workflowCmd.AddCommand(workflowDeleteCmd)
}
