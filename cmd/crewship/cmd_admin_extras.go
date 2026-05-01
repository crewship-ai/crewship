package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// "Admin extras": thin REST wrappers for endpoints that already exist
// server-side but lacked CLI surfaces. Grouped together because each is
// a 30-line wrapper sharing the exact same fetch+formatter+jq pattern,
// and a file per command would dilute the codebase.
//
// Commands added here:
//   crewship triage rules / process
//   crewship recurring  (issues)
//   crewship saved-view (list)
//   crewship mcp-calls  (audit)
//   crewship metrics    (mission + timeseries)
//   crewship workspace invite / invitations

// ----- crewship triage -----

var triageCmd = &cobra.Command{
	Use:   "triage",
	Short: "Manage triage rules + run the triage processor",
}

var triageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List triage rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var body any
		if err := getJSON(client, "/api/v1/triage-rules", &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

var triageProcessCmd = &cobra.Command{
	Use:   "process",
	Short: "Run the triage processor (apply all enabled rules to pending issues)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var body any
		if err := postJSON(client, "/api/v1/triage/process", map[string]any{}, &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

// ----- crewship recurring -----

var recurringCmd = &cobra.Command{
	Use:   "recurring",
	Short: "Manage recurring issues (scheduled issue creation)",
}

var recurringListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recurring-issue schedules",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var body any
		if err := getJSON(client, "/api/v1/recurring-issues", &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

var recurringDeleteCmd = &cobra.Command{
	Use:   "delete <recurring-id>",
	Short: "Delete a recurring-issue schedule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/recurring-issues/"+url.PathEscape(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s[deleted recurring %s]%s\n", cli.Dim, args[0], cli.Reset)
		return nil
	},
}

// ----- crewship saved-view -----

var savedViewCmd = &cobra.Command{
	Use:     "saved-view",
	Aliases: []string{"saved-views", "view"},
	Short:   "Manage saved views (query bookmarks)",
}

var savedViewListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved views",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var body any
		if err := getJSON(client, "/api/v1/saved-views", &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

var savedViewDeleteCmd = &cobra.Command{
	Use:   "delete <view-id>",
	Short: "Delete a saved view",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/saved-views/"+url.PathEscape(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s[deleted view %s]%s\n", cli.Dim, args[0], cli.Reset)
		return nil
	},
}

// ----- crewship mcp-calls -----

var mcpCallsCmd = &cobra.Command{
	Use:   "mcp-calls",
	Short: "Audit MCP tool calls across the workspace",
	Long: `List recent MCP tool invocations. Each row shows the tool name,
agent that called it, arguments, and outcome — useful for tracking which
external integrations are doing real work.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		path := "/api/v1/mcp-tool-calls" + queryString("limit", fmt.Sprintf("%d", limit))
		var body any
		if err := getJSON(client, path, &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

// ----- crewship metrics -----

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Mission performance metrics + timeseries",
	Long: `Fetch mission metrics (success rate, p50/p99 duration) or a metric
timeseries. Defaults to mission metrics; pass --series <name> for a
timeseries window.

Examples:
  crewship metrics                       # mission summary
  crewship metrics --series active_runs  # timeseries
  crewship metrics --series active_runs --range 24h
  crewship metrics --format json | jq`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		series, _ := cmd.Flags().GetString("series")
		rng, _ := cmd.Flags().GetString("range")

		var path string
		if series != "" {
			path = "/api/v1/metrics/timeseries" + queryString("metric", series, "range", rng)
		} else {
			path = "/api/v1/mission-metrics"
		}
		var body any
		if err := getJSON(client, path, &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

// emitFormattedJSON honours --filter/--format on commands that return raw
// JSON-shaped data without a custom table view. Centralised so every
// admin-extra subcommand has the same UX surface (json/yaml/jq/quiet)
// without each duplicating 8 lines of boilerplate.
func emitFormattedJSON(cmd *cobra.Command, body any) error {
	jq, _ := cmd.Flags().GetString("filter")
	if jq != "" {
		return emitJSONFiltered(cmd, body)
	}
	f := newFormatter()
	switch f.Format {
	case "yaml":
		return f.YAML(body)
	case "quiet":
		return nil
	default:
		// Default to indented JSON — the data here is structured records
		// without a single canonical "table" form, so JSON is the most
		// useful pretty default. Users wanting a table can pipe through jq.
		data, err := json.MarshalIndent(body, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
}

func init() {
	// triage
	triageCmd.AddCommand(triageListCmd)
	triageCmd.AddCommand(triageProcessCmd)
	jqExprFlag(triageListCmd)

	// recurring
	recurringCmd.AddCommand(recurringListCmd)
	recurringCmd.AddCommand(recurringDeleteCmd)
	jqExprFlag(recurringListCmd)

	// saved-view
	savedViewCmd.AddCommand(savedViewListCmd)
	savedViewCmd.AddCommand(savedViewDeleteCmd)
	jqExprFlag(savedViewListCmd)

	// mcp-calls
	mcpCallsCmd.Flags().Int("limit", 50, "Max calls to return")
	jqExprFlag(mcpCallsCmd)

	// metrics
	metricsCmd.Flags().String("series", "", "Fetch timeseries for this metric instead of mission summary")
	metricsCmd.Flags().String("range", "24h", "Window for --series (1h, 24h, 7d)")
	jqExprFlag(metricsCmd)

	_ = time.Now // keep import for potential future timestamp formatting
}
