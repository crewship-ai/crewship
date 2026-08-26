package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var intgToolsListCmd = &cobra.Command{
	Use:   "list <crew-slug> <integration-id>",
	Short: "List tool bindings for a crew-scoped integration",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/crews/" + crewID + "/integrations/" + args[1] + "/tools")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var tools []struct {
			ID          string  `json:"id"`
			ToolName    string  `json:"tool_name"`
			Description *string `json:"description"`
			Enabled     bool    `json:"enabled"`
			UpdatedAt   string  `json:"updated_at"`
		}
		if err := cli.ReadJSON(resp, &tools); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"TOOL", "ENABLED", "DESCRIPTION", "UPDATED"}
		var rows [][]string
		for _, t := range tools {
			desc := "-"
			if t.Description != nil && *t.Description != "" {
				desc = *t.Description
				if len(desc) > 50 {
					desc = desc[:47] + "..."
				}
			}
			rows = append(rows, []string{t.ToolName, yesNo(t.Enabled), desc, t.UpdatedAt})
		}
		return f.Auto(tools, headers, rows)
	},
}

// toggleCrewIntegrationTool is the shared body for `tools enable` and
// `tools disable`. Both PATCH the same row with a different enabled
// boolean, so the only thing the user-facing commands need to do is
// supply the value.
func toggleCrewIntegrationTool(crewSlug, integrationID, toolName string, enabled bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	crewID, err := resolveCrewID(client, crewSlug)
	if err != nil {
		return err
	}
	escapedToolName := url.PathEscape(toolName)
	resp, err := client.Patch(
		"/api/v1/crews/"+crewID+"/integrations/"+integrationID+"/tools/"+escapedToolName,
		map[string]interface{}{"enabled": enabled},
	)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("Tool %s on %s/%s %s.\n", toolName, crewSlug, integrationID, state)
	return nil
}

var intgToolsEnableCmd = &cobra.Command{
	Use:   "enable <crew-slug> <integration-id> <tool-name>",
	Short: "Enable a single tool on a crew-scoped integration",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return toggleCrewIntegrationTool(args[0], args[1], args[2], true)
	},
}

var intgToolsDisableCmd = &cobra.Command{
	Use:   "disable <crew-slug> <integration-id> <tool-name>",
	Short: "Disable a single tool on a crew-scoped integration",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return toggleCrewIntegrationTool(args[0], args[1], args[2], false)
	},
}

// discoveredTool is one entry of the refresh payload. It matches
// internal/api's refreshToolEntry: name plus an optional description.
//
// Description is a pointer with `omitempty` on purpose. The endpoint
// COALESCEs it, so an entry that carries no description leaves the stored
// one alone — which is what "I discovered this tool but no description for
// it" should mean. Encoding an empty string instead would overwrite a good
// description with a blank one.
type discoveredTool struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// toolsFileEnvelope lets --tools-file accept an MCP `tools/list` result
// verbatim ({"tools":[…]}) as well as a bare JSON array, so a probe's output
// can be piped straight in.
type toolsFileEnvelope struct {
	Tools []struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
	} `json:"tools"`
}

// readDiscoveredToolsFile loads --tools-file, where "-" means stdin.
func readDiscoveredToolsFile(path string) ([]discoveredTool, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
		if err != nil {
			return nil, cli.WithExitCode(fmt.Errorf("read --tools-file from stdin: %w", err), cli.ExitValidation)
		}
	} else {
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, cli.WithExitCode(fmt.Errorf("read --tools-file: %w", err), cli.ExitValidation)
		}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, cli.WithExitCode(fmt.Errorf(
			"--tools-file %s is empty; supply a JSON array of {\"name\",\"description\"} entries", path), cli.ExitValidation)
	}

	var env toolsFileEnvelope
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &env.Tools); err != nil {
			return nil, cli.WithExitCode(fmt.Errorf("--tools-file %s is not a JSON tool array: %w", path, err), cli.ExitValidation)
		}
	} else if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, cli.WithExitCode(fmt.Errorf(
			"--tools-file %s is neither a JSON array nor an MCP tools/list result: %w", path, err), cli.ExitValidation)
	}

	out := make([]discoveredTool, 0, len(env.Tools))
	for i, t := range env.Tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			return nil, cli.WithExitCode(fmt.Errorf(
				"--tools-file %s: entry %d has no \"name\"", path, i), cli.ExitValidation)
		}
		out = append(out, discoveredTool{Name: name, Description: t.Description})
	}
	return out, nil
}

// collectDiscoveredTools assembles the refresh payload from --tools-file and
// --tool. The file is read first so an explicit --tool can correct a single
// entry of a captured probe result; duplicates collapse to one entry, last
// value winning, keeping first-seen order.
//
// Supplying neither flag is an error rather than an empty payload: the
// endpoint no-ops an empty list, so the command would print its success line
// having refreshed nothing — the failure mode of #1884. An explicitly empty
// --tools-file ("[]") is still accepted, because a probe that genuinely found
// no tools is a deliberate no-op, not a forgotten argument.
func collectDiscoveredTools(cmd *cobra.Command) ([]discoveredTool, error) {
	flags := cmd.Flags()
	toolsFile, _ := flags.GetString("tools-file")
	toolPairs, _ := flags.GetStringArray("tool")

	if toolsFile == "" && len(toolPairs) == 0 {
		return nil, cli.WithExitCode(errors.New(
			"no tools to refresh: pass --tool NAME[=DESCRIPTION] (repeatable) or --tools-file PATH ('-' for stdin). "+
				"Refreshing with an empty list is a server-side no-op, so it would report success without changing anything"),
			cli.ExitValidation)
	}

	var tools []discoveredTool
	if toolsFile != "" {
		fromFile, err := readDiscoveredToolsFile(toolsFile)
		if err != nil {
			return nil, err
		}
		tools = fromFile
	}
	for _, pair := range toolPairs {
		name, desc, hasDesc := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, cli.WithExitCode(fmt.Errorf(
				"--tool %q must be NAME or NAME=DESCRIPTION", pair), cli.ExitValidation)
		}
		entry := discoveredTool{Name: name}
		if hasDesc {
			entry.Description = &desc
		}
		tools = append(tools, entry)
	}

	// Collapse duplicates: last mention wins, first position kept.
	index := make(map[string]int, len(tools))
	deduped := make([]discoveredTool, 0, len(tools))
	for _, t := range tools {
		if at, seen := index[t.Name]; seen {
			deduped[at] = t
			continue
		}
		index[t.Name] = len(deduped)
		deduped = append(deduped, t)
	}
	return deduped, nil
}

var intgToolsRefreshCmd = &cobra.Command{
	Use:   "refresh <crew-slug> <integration-id>",
	Short: "Reconcile tool bindings with a discovered MCP tool list",
	Long: `Push a discovered tool list to the server, which upserts
mcp_tool_bindings rows: new tools default to enabled, existing ones keep
their toggle state and their stored description when the entry does not
carry one. Tools omitted from the payload are left in place — a refresh
never auto-revokes.

Supply them with either flag, or both:

  --tool NAME[=DESCRIPTION]   repeatable; the description is everything
                              after the first '=' and may be omitted
  --tools-file PATH           a JSON array of {"name","description"}
                              objects, or an MCP tools/list result
                              ({"tools":[…]}) verbatim. '-' reads stdin.

When both are given the file is read first and --tool overrides any entry
with the same name.

At least one tool is required. An empty list is a no-op on the server, so
refreshing with nothing would report success while changing nothing; pass
--tools-file with an explicit "[]" if that is really what you mean.`,
	Example: `  # From an MCP probe capture
  crewship integration tools refresh backend intg_123 --tools-file tools.json

  # Straight off a pipe
  mcp-probe list-tools | crewship integration tools refresh backend intg_123 --tools-file -

  # Ad hoc
  crewship integration tools refresh backend intg_123 \
    --tool search="Full-text search over the issue tracker" --tool create_issue`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		// Validate the payload before any round-trip: a typo should be a
		// local error, not a 400 after two requests.
		tools, err := collectDiscoveredTools(cmd)
		if err != nil {
			return err
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		body := map[string]interface{}{"tools": tools}
		resp, err := client.Post(
			"/api/v1/crews/"+crewID+"/integrations/"+args[1]+"/tools/refresh",
			body,
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			return fmt.Errorf("read refresh response: %w", err)
		}
		f := resolvedFormatter(cmd)
		// An empty body is a valid 2xx from this endpoint (see the TODO
		// above), and it used to answer a sentence on stdout regardless of
		// format. Give the machine formats the same envelope shape they get
		// when the server does return a document.
		if len(data) == 0 {
			return f.AutoHuman(map[string]any{
				"crew":        args[0],
				"integration": args[1],
				"refreshed":   true,
			}, func() {
				fmt.Printf("Tool bindings refresh requested for %s/%s.\n", args[0], args[1])
			})
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			return fmt.Errorf("decode refresh response: %w", err)
		}
		// Machine, not JSON: the server's document has always been this
		// command's output, so JSON stays the default, but `-f yaml` and
		// `-f ndjson` are no longer ignored.
		return f.Machine(result)
	},
}

func registerIntegrationToolsSubcommands() {
	integrationToolsCmd.AddCommand(intgToolsListCmd)
	integrationToolsCmd.AddCommand(intgToolsEnableCmd)
	integrationToolsCmd.AddCommand(intgToolsDisableCmd)
	integrationToolsCmd.AddCommand(intgToolsRefreshCmd)
}

func registerIntegrationToolsFlags() {
	// StringArray, not StringSlice: a tool description routinely contains
	// commas, and comma-splitting would shred it into bogus entries.
	intgToolsRefreshCmd.Flags().StringArray("tool", nil,
		"Discovered tool as NAME or NAME=DESCRIPTION (repeatable; description is everything after the first '=')")
	intgToolsRefreshCmd.Flags().String("tools-file", "",
		"Read discovered tools from a JSON array of {\"name\",\"description\"} objects, or an MCP tools/list result; '-' reads stdin")
}
