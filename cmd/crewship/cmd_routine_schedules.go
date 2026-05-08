package main

// Routine schedule (cron) subcommands. Full CRUD parity with the API:
// list / create / update / delete / enable / disable / now (force-fire).
//
// Schedules are triggers that auto-fire saved routines on a cron
// expression. The scheduler runs in-process (single-instance, no
// leader election yet — see PIPELINES.md §17.5 caveat).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type scheduleRow struct {
	ID                    string                 `json:"id"`
	WorkspaceID           string                 `json:"workspace_id"`
	Name                  string                 `json:"name"`
	TargetPipelineID      string                 `json:"target_pipeline_id"`
	TargetPipelineSlug    string                 `json:"target_pipeline_slug,omitempty"`
	TargetPipelineVersion *int                   `json:"target_pipeline_version,omitempty"`
	CronExpr              string                 `json:"cron_expr"`
	Timezone              string                 `json:"timezone"`
	Inputs                map[string]interface{} `json:"inputs"`
	Enabled               bool                   `json:"enabled"`
	LastRunAt             *string                `json:"last_run_at,omitempty"`
	LastStatus            *string                `json:"last_status,omitempty"`
	LastRunID             *string                `json:"last_run_id,omitempty"`
	NextRunAt             *string                `json:"next_run_at,omitempty"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
}

var routineSchedulesCmd = &cobra.Command{
	Use:   "schedules",
	Short: "Manage cron triggers attached to routines",
	Long: `Schedules fire saved routines on a cron expression. Each schedule is
named, targets one routine by slug, and can be enabled/disabled
independently. The scheduler runs in-process and ticks every 30s, so
the minimum cron resolution is one minute.

Examples:
  crewship routine schedules list
  crewship routine schedules list --slug summarize-text
  crewship routine schedules create --slug summarize-text \
      --name "daily-summary" --cron "0 9 * * *" --inputs '{"text":"…"}'
  crewship routine schedules enable <schedule_id>
  crewship routine schedules disable <schedule_id>
  crewship routine schedules update <schedule_id> --cron "0 8 * * *"
  crewship routine schedules now <schedule_id>     # force-fire ad-hoc
  crewship routine schedules delete <schedule_id>
`,
}

var routineSchedulesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List schedules in this workspace (optionally filtered by routine)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		slugFilter, _ := cmd.Flags().GetString("slug")
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []scheduleRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if slugFilter != "" {
			out := rows[:0]
			for _, r := range rows {
				if r.TargetPipelineSlug == slugFilter {
					out = append(out, r)
				}
			}
			rows = out
		}
		if len(rows) == 0 {
			if slugFilter != "" {
				fmt.Printf("No schedules for routine %q.\n", slugFilter)
			} else {
				fmt.Println("No schedules in this workspace.")
			}
			fmt.Println("Create one: crewship routine schedules create --slug <routine> --cron '0 9 * * *'")
			return nil
		}
		jsonOut, _ := cmd.Flags().GetBool("json")
		if jsonOut {
			b, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tROUTINE\tCRON\tTZ\tENABLED\tNEXT")
		for _, s := range rows {
			next := "—"
			if s.NextRunAt != nil && *s.NextRunAt != "" {
				next = formatTimestamp(*s.NextRunAt)
			}
			enabled := "no"
			if s.Enabled {
				enabled = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				shortID(s.ID), s.Name, s.TargetPipelineSlug, s.CronExpr, s.Timezone, enabled, next)
		}
		return w.Flush()
	},
}

var routineSchedulesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new schedule",
	RunE: func(cmd *cobra.Command, _ []string) error {
		slug, _ := cmd.Flags().GetString("slug")
		name, _ := cmd.Flags().GetString("name")
		cronExpr, _ := cmd.Flags().GetString("cron")
		timezone, _ := cmd.Flags().GetString("timezone")
		inputsJSON, _ := cmd.Flags().GetString("inputs")
		enabled, _ := cmd.Flags().GetBool("enabled")
		if slug == "" || cronExpr == "" {
			return fmt.Errorf("--slug and --cron are required")
		}
		if name == "" {
			name = fmt.Sprintf("%s schedule", slug)
		}
		// Default to UTC server-side rather than guessing server's
		// time.Local from the CLI host — running the CLI from a
		// different timezone than the server would otherwise silently
		// pin schedules to the operator's local time. Users who want
		// host-local can pass --timezone $(date +%Z) explicitly.
		inputs := map[string]interface{}{}
		if inputsJSON != "" {
			if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
				return fmt.Errorf("--inputs must be valid JSON: %w", err)
			}
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", ws),
			map[string]interface{}{
				"name":                 name,
				"target_pipeline_slug": slug,
				"cron_expr":            cronExpr,
				"timezone":             timezone,
				"inputs":               inputs,
				"enabled":              enabled,
			},
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out scheduleRow
		_ = json.NewDecoder(resp.Body).Decode(&out)
		fmt.Printf("Schedule created: %s (%s @ %s)\n", out.Name, out.CronExpr, out.Timezone)
		fmt.Printf("  ID:     %s\n", out.ID)
		if out.NextRunAt != nil {
			fmt.Printf("  Next:   %s\n", formatTimestamp(*out.NextRunAt))
		}
		return nil
	},
}

var routineSchedulesUpdateCmd = &cobra.Command{
	Use:   "update <schedule_id>",
	Short: "Update an existing schedule (cron expr, timezone, enabled state, inputs)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]interface{}{}
		if v, _ := cmd.Flags().GetString("cron"); v != "" {
			body["cron_expr"] = v
		}
		if v, _ := cmd.Flags().GetString("timezone"); v != "" {
			body["timezone"] = v
		}
		if v, _ := cmd.Flags().GetString("name"); v != "" {
			body["name"] = v
		}
		if cmd.Flags().Changed("enabled") {
			v, _ := cmd.Flags().GetBool("enabled")
			body["enabled"] = v
		}
		if v, _ := cmd.Flags().GetString("inputs"); v != "" {
			var inputs map[string]interface{}
			if err := json.Unmarshal([]byte(v), &inputs); err != nil {
				return fmt.Errorf("--inputs must be valid JSON: %w", err)
			}
			body["inputs"] = inputs
		}
		if len(body) == 0 {
			return fmt.Errorf("at least one of --cron / --timezone / --name / --enabled / --inputs required")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules/%s", ws, args[0]),
			body,
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Schedule %s updated.\n", args[0])
		return nil
	},
}

var routineSchedulesEnableCmd = &cobra.Command{
	Use:   "enable <schedule_id>",
	Short: "Enable a schedule (next tick will fire)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setScheduleEnabled(args[0], true) },
}

var routineSchedulesDisableCmd = &cobra.Command{
	Use:   "disable <schedule_id>",
	Short: "Disable a schedule (skipped on tick until re-enabled)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setScheduleEnabled(args[0], false) },
}

var routineSchedulesNowCmd = &cobra.Command{
	Use:   "now <schedule_id>",
	Short: "Force-fire a schedule immediately (out-of-cycle invoke for testing)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules/%s/run", ws, args[0]),
			map[string]interface{}{},
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// 404 means the /run endpoint isn't yet wired on the
		// server. Surface as a non-zero error so CI scripts that
		// chained `routine schedules now <id> && next-step` don't
		// silently swallow the missing capability and assume the
		// schedule fired.
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("force-fire endpoint unavailable on this server; toggle disable/enable to fire on next 30s tick instead")
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Schedule %s fired (out-of-cycle).\n", args[0])
		return nil
	},
}

var routineSchedulesDeleteCmd = &cobra.Command{
	Use:   "delete <schedule_id>",
	Short: "Delete a schedule (cannot be undone)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			fmt.Printf("Delete schedule %s? Use --yes to skip this prompt.\n", args[0])
			fmt.Print("Type 'yes' to confirm: ")
			var input string
			_, _ = fmt.Scanln(&input)
			if strings.ToLower(strings.TrimSpace(input)) != "yes" {
				return fmt.Errorf("aborted")
			}
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Delete(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules/%s", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Schedule %s deleted.\n", args[0])
		return nil
	},
}

func setScheduleEnabled(scheduleID string, enabled bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	ws := client.GetWorkspaceID()
	resp, err := client.Patch(
		fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules/%s", ws, scheduleID),
		map[string]interface{}{"enabled": enabled},
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("Schedule %s %s.\n", scheduleID, state)
	return nil
}

func formatTimestamp(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}

func shortID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:14] + "…"
}

func init() {
	routineSchedulesListCmd.Flags().String("slug", "", "filter to schedules targeting this routine slug")
	routineSchedulesListCmd.Flags().Bool("json", false, "output as JSON for scripting")

	routineSchedulesCreateCmd.Flags().String("slug", "", "target routine slug (REQUIRED)")
	routineSchedulesCreateCmd.Flags().String("name", "", "human-readable schedule name (default: '<slug> schedule')")
	routineSchedulesCreateCmd.Flags().String("cron", "", "5-field cron expression — e.g. '0 9 * * *' (REQUIRED)")
	routineSchedulesCreateCmd.Flags().String("timezone", "", "IANA timezone (default: UTC; pass --timezone $(date +%Z) for host-local)")
	routineSchedulesCreateCmd.Flags().String("inputs", "", "JSON object passed as inputs on each tick (e.g. '{\"text\":\"…\"}')")
	routineSchedulesCreateCmd.Flags().Bool("enabled", true, "create the schedule already enabled (default true)")

	routineSchedulesUpdateCmd.Flags().String("name", "", "new schedule name")
	routineSchedulesUpdateCmd.Flags().String("cron", "", "new cron expression")
	routineSchedulesUpdateCmd.Flags().String("timezone", "", "new IANA timezone")
	routineSchedulesUpdateCmd.Flags().Bool("enabled", false, "set enabled state — explicit flag presence required (--enabled / --enabled=false)")
	routineSchedulesUpdateCmd.Flags().String("inputs", "", "replace inputs JSON object")

	routineSchedulesDeleteCmd.Flags().Bool("yes", false, "skip the interactive confirmation prompt")

	routineSchedulesCmd.AddCommand(routineSchedulesListCmd)
	routineSchedulesCmd.AddCommand(routineSchedulesCreateCmd)
	routineSchedulesCmd.AddCommand(routineSchedulesUpdateCmd)
	routineSchedulesCmd.AddCommand(routineSchedulesEnableCmd)
	routineSchedulesCmd.AddCommand(routineSchedulesDisableCmd)
	routineSchedulesCmd.AddCommand(routineSchedulesNowCmd)
	routineSchedulesCmd.AddCommand(routineSchedulesDeleteCmd)

	pipelineCmd.AddCommand(routineSchedulesCmd)
}
