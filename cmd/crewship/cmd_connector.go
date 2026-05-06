// `crewship connector` — the catalog-side counterpart to
// `crewship integration` (which manages installed instances).
//
// This subcommand is split into two halves:
//
//   - Pure-local (validate, lint): parse YAML, run
//     internal/connectors.{ParseManifest,Validate}. No network. Useful
//     for CI gates against community-contributed manifests and for
//     editing the catalog locally.
//   - API-bound (list, show, test): GET /api/v1/connectors{...} and
//     POST /api/v1/connectors/{id}/verify against a running Crewship
//     server. Same flags as `crewship credential` / `crewship skill`.
//
// Naming: chose `connector` (singular, with `connectors` and `cn`
// aliases) so it doesn't collide with `integration`/`mcp` which
// already names the installed-instance domain.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/connectors"
	"github.com/spf13/cobra"
)

// -------------------------------------------------------------------
// Root + registration
// -------------------------------------------------------------------

var connectorCmd = &cobra.Command{
	Use:     "connector",
	Aliases: []string{"connectors", "cn"},
	Short:   "Manage and validate connector manifests (the install catalog)",
	Long: `Connector manifests describe how Crewship installs a third-party
integration: which auth mode, which fields the user fills in, which
MCP server runs once credentials are in hand.

Commands:
  validate <file>     Validate one manifest file
  lint     [dir]      Validate every *.yaml in a directory
  list                List the catalog (requires auth)
  show     <id>       Show one manifest from the catalog
  test     <id>       Run pre-install verify against the catalog manifest

The validate + lint commands are local — no server needed.`,
}

// -------------------------------------------------------------------
// validate <file>
// -------------------------------------------------------------------

var connectorValidateCmd = &cobra.Command{
	Use:   "validate <file>",
	Short: "Parse and validate a single connector manifest YAML",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		strict, _ := cmd.Flags().GetBool("strict")
		return runValidateOne(args[0], strict)
	},
}

// runValidateOne is exported (lowercase, package-internal) so lint can
// reuse the per-file path without re-implementing argument plumbing.
func runValidateOne(path string, strict bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	m, err := connectors.ParseManifest(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("validate %s: %w", path, err)
	}
	// Strict-mode warnings — expand as the schema grows. For now we
	// surface "missing brand color" and "missing description" as
	// warnings; both are silently allowed by Validate but make the
	// catalog UI render a featureless tile.
	if strict {
		if m.Brand.Color == "" {
			return fmt.Errorf("strict %s: brand.color is empty", path)
		}
		if m.Description == "" {
			return fmt.Errorf("strict %s: description is empty", path)
		}
	}
	cli.PrintSuccess(fmt.Sprintf("ok %s (id=%s, mode=%s)", path, m.ID, m.AuthMode))
	return nil
}

// -------------------------------------------------------------------
// lint [dir]
// -------------------------------------------------------------------

var connectorLintCmd = &cobra.Command{
	Use:   "lint [dir]",
	Short: "Validate every *.yaml manifest in a directory",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		strict, _ := cmd.Flags().GetBool("strict")
		recursive, _ := cmd.Flags().GetBool("recursive")
		return runLintDir(dir, strict, recursive)
	},
}

func runLintDir(dir string, strict, recursive bool) error {
	var failures []string
	walked := 0

	walk := filepath.Walk
	visit := func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if !recursive && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".yaml" {
			return nil
		}
		walked++
		if err := runValidateOne(path, strict); err != nil {
			failures = append(failures, err.Error())
		}
		return nil
	}
	if err := walk(dir, visit); err != nil {
		return err
	}
	if walked == 0 {
		// Empty dir is not a hard error — the user might be using lint
		// in a layout where some flavor dirs are empty.
		cli.PrintWarning(fmt.Sprintf("no .yaml manifests found under %s", dir))
		return nil
	}
	if len(failures) > 0 {
		for _, f := range failures {
			cli.PrintError(f)
		}
		return errors.New("connector lint: one or more manifests failed validation")
	}
	cli.PrintSuccess(fmt.Sprintf("lint clean — %d manifest(s) under %s", walked, dir))
	return nil
}

// -------------------------------------------------------------------
// list  — GET /api/v1/connectors
// -------------------------------------------------------------------

var connectorListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the connector catalog from the server",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/connectors")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var items []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Category    string `json:"category"`
			AuthMode    string `json:"auth_mode"`
			BrandLogo   string `json:"brand_logo"`
			BrandColor  string `json:"brand_color"`
		}
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"ID", "NAME", "CATEGORY", "AUTH", "DESCRIPTION"}
		var rows [][]string
		for _, it := range items {
			rows = append(rows, []string{it.ID, it.Name, it.Category, it.AuthMode, it.Description})
		}
		// Auto picks json/yaml/table based on the global --format flag.
		return f.Auto(items, headers, rows)
	},
}

// -------------------------------------------------------------------
// show <id>  — GET /api/v1/connectors/{id}
// -------------------------------------------------------------------

var connectorShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show one manifest from the catalog",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/connectors/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// Stream raw JSON to stdout — the manifest can be large and
		// nested; pretty-printing in a table loses fidelity. Users
		// who want columns should use `connector list`.
		var raw map[string]any
		if err := cli.ReadJSON(resp, &raw); err != nil {
			return err
		}
		return printJSON(raw)
	},
}

// -------------------------------------------------------------------
// test <id>  — POST /api/v1/connectors/{id}/verify with --field
// -------------------------------------------------------------------

var connectorTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Run the manifest's pre-install verify check (requires --field flags)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		fieldsRaw, _ := cmd.Flags().GetStringArray("field")
		fields := map[string]string{}
		for _, kv := range fieldsRaw {
			k, v, ok := splitKV(kv)
			if !ok {
				return fmt.Errorf("invalid --field %q (want key=value)", kv)
			}
			fields[k] = v
		}
		body := map[string]any{"fields": fields}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/connectors/"+args[0]+"/verify", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if !out.OK {
			return fmt.Errorf("verify failed: %s", out.Message)
		}
		cli.PrintSuccess("verify ok")
		return nil
	},
}

// -------------------------------------------------------------------
// init — flags + tree wiring
// -------------------------------------------------------------------

func init() {
	connectorValidateCmd.Flags().Bool("strict", false, "Fail on warning-tier issues (empty brand.color, empty description)")
	connectorLintCmd.Flags().Bool("strict", false, "Pass --strict to each per-file validate")
	connectorLintCmd.Flags().Bool("recursive", false, "Walk subdirectories instead of only the given dir")
	connectorTestCmd.Flags().StringArray("field", nil, "key=value pair for a manifest field; repeat for multiple fields")

	connectorCmd.AddCommand(connectorValidateCmd)
	connectorCmd.AddCommand(connectorLintCmd)
	connectorCmd.AddCommand(connectorListCmd)
	connectorCmd.AddCommand(connectorShowCmd)
	connectorCmd.AddCommand(connectorTestCmd)
}

// -------------------------------------------------------------------
// Tiny helpers — kept here so the file is self-contained.
// (The CLI has scattered helpers elsewhere; keeping these local
// avoids a dependency on whichever utility package they live in.)
// -------------------------------------------------------------------

// splitKV parses "key=value" into ("key","value",true). Empty key or
// missing "=" returns ok=false.
func splitKV(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			if i == 0 {
				return "", "", false
			}
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// printJSON pretty-prints v to stdout. The cli package doesn't ship
// a PrintJSONIndent helper, so we inline the marshal + write.
func printJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(out))
	return err
}
