package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

// validTriageMatchTypes is the closed enum the CreateRule/UpdateRule
// handlers enforce server-side. We mirror it here so a typo fails fast
// before a round-trip instead of surfacing as a generic 400.
var validTriageMatchTypes = map[string]bool{"contains": true, "regex": true, "exact": true}

var triageCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a triage rule",
	Long: `Create a triage rule that classifies pending issues by title.

A rule matches an issue title via --match-type (contains | regex | exact)
against --pattern, then applies the configured actions (route to a crew,
assign an agent, set priority/project, attach labels). Rules are evaluated
in position order by ` + "`crewship triage process`" + `; the first match wins.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		pattern, _ := cmd.Flags().GetString("pattern")
		matchType, _ := cmd.Flags().GetString("match-type")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if pattern == "" {
			return fmt.Errorf("--pattern is required")
		}
		if !validTriageMatchTypes[matchType] {
			return fmt.Errorf("--match-type must be one of: contains, regex, exact (got %q)", matchType)
		}

		body := map[string]any{
			"name":       name,
			"pattern":    pattern,
			"match_type": matchType,
		}
		// Optional action fields — only sent when explicitly set so the
		// server stores NULL for the rest.
		setStringFlag(cmd, body, "crew", "crew_id")
		setStringFlag(cmd, body, "assignee", "assignee_id")
		setStringFlag(cmd, body, "priority", "priority")
		setStringFlag(cmd, body, "project", "project_id")
		if err := setJSONFlag(cmd, body, "labels", "labels_json"); err != nil {
			return err
		}

		var out any
		if err := postJSON(client, "/api/v1/triage-rules", body, &out); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, out)
	},
}

var triageUpdateCmd = &cobra.Command{
	Use:   "update <rule-id>",
	Short: "Update a triage rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		flags := cmd.Flags()
		body := map[string]any{}
		setChangedString(flags, body, "name", "name")
		setChangedString(flags, body, "pattern", "pattern")
		if flags.Changed("match-type") {
			v, _ := flags.GetString("match-type")
			if !validTriageMatchTypes[v] {
				return fmt.Errorf("--match-type must be one of: contains, regex, exact (got %q)", v)
			}
			body["match_type"] = v
		}
		setChangedString(flags, body, "crew", "crew_id")
		setChangedString(flags, body, "assignee", "assignee_id")
		setChangedString(flags, body, "priority", "priority")
		setChangedString(flags, body, "project", "project_id")
		if err := setChangedJSON(flags, body, "labels", "labels_json"); err != nil {
			return err
		}
		if flags.Changed("position") {
			v, _ := flags.GetInt("position")
			body["position"] = v
		}
		if flags.Changed("enabled") {
			v, _ := flags.GetBool("enabled")
			body["enabled"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		var out any
		if err := patchJSON(client, "/api/v1/triage-rules/"+url.PathEscape(args[0]), body, &out); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, out)
	},
}

var triageDeleteCmd = &cobra.Command{
	Use:     "delete <rule-id>",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete a triage rule",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete triage rule %q?", args[0])); err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/triage-rules/"+url.PathEscape(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s[deleted triage rule %s]%s\n", cli.Dim, args[0], cli.Reset)
		return nil
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

var recurringCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a recurring-issue schedule",
	Long: `Create a recurring issue schedule — the server stamps out a new issue
for the given crew on the cadence described by --cron (standard 5-field
"minute hour day-of-month month day-of-week").

Example cron values:
  "0 9 * * *"       every day at 09:00 UTC
  "0 9 * * 1"       every Monday at 09:00 UTC
  "*/30 * * * *"    every 30 minutes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		crew, _ := cmd.Flags().GetString("crew")
		title, _ := cmd.Flags().GetString("title")
		cronExpr, _ := cmd.Flags().GetString("cron")
		if crew == "" {
			return fmt.Errorf("--crew is required")
		}
		if title == "" {
			return fmt.Errorf("--title is required")
		}
		if cronExpr == "" {
			return fmt.Errorf("--cron is required")
		}

		body := map[string]any{
			"crew_id":         crew,
			"title":           title,
			"cron_expression": cronExpr,
		}
		setStringFlag(cmd, body, "description", "description")
		setStringFlag(cmd, body, "priority", "priority")
		setStringFlag(cmd, body, "project", "project_id")
		setStringFlag(cmd, body, "milestone", "milestone_id")
		setStringFlag(cmd, body, "assignee-type", "assignee_type")
		setStringFlag(cmd, body, "assignee", "assignee_id")
		if err := setJSONFlag(cmd, body, "labels", "labels_json"); err != nil {
			return err
		}

		var out any
		if err := postJSON(client, "/api/v1/recurring-issues", body, &out); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, out)
	},
}

var recurringUpdateCmd = &cobra.Command{
	Use:   "update <recurring-id>",
	Short: "Update a recurring-issue schedule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		flags := cmd.Flags()
		body := map[string]any{}
		setChangedString(flags, body, "crew", "crew_id")
		setChangedString(flags, body, "title", "title")
		setChangedString(flags, body, "description", "description")
		setChangedString(flags, body, "priority", "priority")
		setChangedString(flags, body, "project", "project_id")
		setChangedString(flags, body, "milestone", "milestone_id")
		setChangedString(flags, body, "assignee-type", "assignee_type")
		setChangedString(flags, body, "assignee", "assignee_id")
		if err := setChangedJSON(flags, body, "labels", "labels_json"); err != nil {
			return err
		}
		setChangedString(flags, body, "cron", "cron_expression")
		if flags.Changed("enabled") {
			v, _ := flags.GetBool("enabled")
			body["enabled"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		var out any
		if err := patchJSON(client, "/api/v1/recurring-issues/"+url.PathEscape(args[0]), body, &out); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, out)
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

var savedViewCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a saved view",
	Long: `Create a saved view — a named, server-stored query bookmark. --filters
is the same filter JSON the web UI persists; --sort is optional sort JSON.
Both are validated as JSON client-side before the request is sent.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		filters, _ := cmd.Flags().GetString("filters")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if filters == "" {
			return fmt.Errorf("--filters is required")
		}
		if !json.Valid([]byte(filters)) {
			return fmt.Errorf("--filters must be valid JSON")
		}

		body := map[string]any{
			"name":         name,
			"filters_json": filters,
		}
		if sort, _ := cmd.Flags().GetString("sort"); sort != "" {
			if !json.Valid([]byte(sort)) {
				return fmt.Errorf("--sort must be valid JSON")
			}
			body["sort_json"] = sort
		}
		setStringFlag(cmd, body, "view-type", "view_type")
		if cmd.Flags().Changed("shared") {
			v, _ := cmd.Flags().GetBool("shared")
			body["shared"] = v
		}

		var out any
		if err := postJSON(client, "/api/v1/saved-views", body, &out); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, out)
	},
}

var savedViewUpdateCmd = &cobra.Command{
	Use:   "update <view-id>",
	Short: "Update a saved view (owner only)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		flags := cmd.Flags()
		body := map[string]any{}
		setChangedString(flags, body, "name", "name")
		if flags.Changed("filters") {
			v, _ := flags.GetString("filters")
			if !json.Valid([]byte(v)) {
				return fmt.Errorf("--filters must be valid JSON")
			}
			body["filters_json"] = v
		}
		if flags.Changed("sort") {
			v, _ := flags.GetString("sort")
			if v != "" && !json.Valid([]byte(v)) {
				return fmt.Errorf("--sort must be valid JSON")
			}
			body["sort_json"] = v
		}
		setChangedString(flags, body, "view-type", "view_type")
		if flags.Changed("default") {
			v, _ := flags.GetBool("default")
			body["is_default"] = v
		}
		if flags.Changed("shared") {
			v, _ := flags.GetBool("shared")
			body["shared"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		var out any
		if err := patchJSON(client, "/api/v1/saved-views/"+url.PathEscape(args[0]), body, &out); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, out)
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

// setStringFlag copies a non-empty string flag into the request body under
// `key`. Used by the create verbs, where an unset optional flag should be
// omitted entirely (so the server applies its own default / NULL).
func setStringFlag(cmd *cobra.Command, body map[string]any, flag, key string) {
	if v, _ := cmd.Flags().GetString(flag); v != "" {
		body[key] = v
	}
}

// setChangedString copies a string flag into the body under `key` only when
// the user explicitly set it (flags.Changed). Used by the update verbs so an
// empty `--crew ""` is sent (the handler treats "" as "clear to NULL"),
// while an untouched flag is omitted entirely.
func setChangedString(flags *pflag.FlagSet, body map[string]any, flag, key string) {
	if flags.Changed(flag) {
		v, _ := flags.GetString(flag)
		body[key] = v
	}
}

// setJSONFlag is setStringFlag for fields that must carry valid JSON (e.g.
// labels_json). A malformed value is rejected locally with a precise error
// rather than forwarded to the server.
func setJSONFlag(cmd *cobra.Command, body map[string]any, flag, key string) error {
	v, _ := cmd.Flags().GetString(flag)
	if v == "" {
		return nil
	}
	if !json.Valid([]byte(v)) {
		return fmt.Errorf("--%s must be valid JSON", flag)
	}
	body[key] = v
	return nil
}

// setChangedJSON is setChangedString with JSON validation for the update verbs.
// An explicit empty string still passes through (the handler clears it to
// NULL); a non-empty value must parse as JSON.
func setChangedJSON(flags *pflag.FlagSet, body map[string]any, flag, key string) error {
	if !flags.Changed(flag) {
		return nil
	}
	v, _ := flags.GetString(flag)
	if v != "" && !json.Valid([]byte(v)) {
		return fmt.Errorf("--%s must be valid JSON", flag)
	}
	body[key] = v
	return nil
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
	triageCreateCmd.Flags().String("name", "", "Rule name (required)")
	triageCreateCmd.Flags().String("pattern", "", "Title pattern to match (required)")
	triageCreateCmd.Flags().String("match-type", "", "Match type: contains | regex | exact (required)")
	triageCreateCmd.Flags().String("crew", "", "Route matched issues to this crew ID")
	triageCreateCmd.Flags().String("assignee", "", "Assign matched issues to this agent ID")
	triageCreateCmd.Flags().String("priority", "", "Set priority on matched issues")
	triageCreateCmd.Flags().String("project", "", "Set project ID on matched issues")
	triageCreateCmd.Flags().String("labels", "", "Labels JSON to attach to matched issues")

	triageUpdateCmd.Flags().String("name", "", "New rule name")
	triageUpdateCmd.Flags().String("pattern", "", "New title pattern")
	triageUpdateCmd.Flags().String("match-type", "", "New match type: contains | regex | exact")
	triageUpdateCmd.Flags().String("crew", "", "New crew ID (empty string clears it)")
	triageUpdateCmd.Flags().String("assignee", "", "New assignee agent ID (empty string clears it)")
	triageUpdateCmd.Flags().String("priority", "", "New priority")
	triageUpdateCmd.Flags().String("project", "", "New project ID (empty string clears it)")
	triageUpdateCmd.Flags().String("labels", "", "New labels JSON")
	triageUpdateCmd.Flags().Int("position", 0, "New ordering position")
	triageUpdateCmd.Flags().Bool("enabled", false, "Enable or disable the rule")

	triageDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")

	triageCmd.AddCommand(triageListCmd)
	triageCmd.AddCommand(triageProcessCmd)
	triageCmd.AddCommand(triageCreateCmd)
	triageCmd.AddCommand(triageUpdateCmd)
	triageCmd.AddCommand(triageDeleteCmd)
	jqExprFlag(triageListCmd)
	jqExprFlag(triageCreateCmd)
	jqExprFlag(triageUpdateCmd)

	// recurring
	recurringCreateCmd.Flags().String("crew", "", "Crew ID to stamp issues for (required)")
	recurringCreateCmd.Flags().String("title", "", "Issue title (required)")
	recurringCreateCmd.Flags().String("cron", "", "5-field cron expression (required)")
	recurringCreateCmd.Flags().String("description", "", "Issue description")
	recurringCreateCmd.Flags().String("priority", "", "Issue priority")
	recurringCreateCmd.Flags().String("project", "", "Project ID")
	recurringCreateCmd.Flags().String("milestone", "", "Milestone ID")
	recurringCreateCmd.Flags().String("assignee-type", "", "Assignee type (agent|user)")
	recurringCreateCmd.Flags().String("assignee", "", "Assignee ID")
	recurringCreateCmd.Flags().String("labels", "", "Labels JSON")

	recurringUpdateCmd.Flags().String("crew", "", "New crew ID")
	recurringUpdateCmd.Flags().String("title", "", "New issue title")
	recurringUpdateCmd.Flags().String("description", "", "New issue description")
	recurringUpdateCmd.Flags().String("priority", "", "New issue priority")
	recurringUpdateCmd.Flags().String("project", "", "New project ID (empty string clears it)")
	recurringUpdateCmd.Flags().String("milestone", "", "New milestone ID (empty string clears it)")
	recurringUpdateCmd.Flags().String("assignee-type", "", "New assignee type (agent|user)")
	recurringUpdateCmd.Flags().String("assignee", "", "New assignee ID")
	recurringUpdateCmd.Flags().String("labels", "", "New labels JSON")
	recurringUpdateCmd.Flags().String("cron", "", "New 5-field cron expression (recomputes next run)")
	recurringUpdateCmd.Flags().Bool("enabled", false, "Enable or disable the schedule")

	recurringCmd.AddCommand(recurringListCmd)
	recurringCmd.AddCommand(recurringCreateCmd)
	recurringCmd.AddCommand(recurringUpdateCmd)
	recurringCmd.AddCommand(recurringDeleteCmd)
	jqExprFlag(recurringListCmd)
	jqExprFlag(recurringCreateCmd)
	jqExprFlag(recurringUpdateCmd)

	// saved-view
	savedViewCreateCmd.Flags().String("name", "", "View name (required)")
	savedViewCreateCmd.Flags().String("filters", "", "Filter JSON (required)")
	savedViewCreateCmd.Flags().String("sort", "", "Sort JSON")
	savedViewCreateCmd.Flags().String("view-type", "", "View type (default: list)")
	savedViewCreateCmd.Flags().Bool("shared", false, "Share the view with the workspace")

	savedViewUpdateCmd.Flags().String("name", "", "New view name")
	savedViewUpdateCmd.Flags().String("filters", "", "New filter JSON")
	savedViewUpdateCmd.Flags().String("sort", "", "New sort JSON")
	savedViewUpdateCmd.Flags().String("view-type", "", "New view type")
	savedViewUpdateCmd.Flags().Bool("default", false, "Mark as the default view")
	savedViewUpdateCmd.Flags().Bool("shared", false, "Share or unshare the view")

	savedViewCmd.AddCommand(savedViewListCmd)
	savedViewCmd.AddCommand(savedViewCreateCmd)
	savedViewCmd.AddCommand(savedViewUpdateCmd)
	savedViewCmd.AddCommand(savedViewDeleteCmd)
	jqExprFlag(savedViewListCmd)
	jqExprFlag(savedViewCreateCmd)
	jqExprFlag(savedViewUpdateCmd)

	// mcp-calls
	mcpCallsCmd.Flags().Int("limit", 50, "Max calls to return")
	jqExprFlag(mcpCallsCmd)

	// metrics
	metricsCmd.Flags().String("series", "", "Fetch timeseries for this metric instead of mission summary")
	metricsCmd.Flags().String("range", "24h", "Window for --series (1h, 24h, 7d)")
	jqExprFlag(metricsCmd)

	_ = time.Now // keep import for potential future timestamp formatting
}
