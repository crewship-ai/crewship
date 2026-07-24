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
	"github.com/crewship-ai/crewship/internal/pipeline"
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
	WakePipelineID        string                 `json:"wake_pipeline_id,omitempty"`
	WakePipelineSlug      string                 `json:"wake_pipeline_slug,omitempty"`
	WakeInputs            map[string]interface{} `json:"wake_inputs,omitempty"`
	WakeFailClosed        bool                   `json:"wake_fail_closed,omitempty"`
	WakeCheckCount        int                    `json:"wake_check_count,omitempty"`
	WakeFireCount         int                    `json:"wake_fire_count,omitempty"`
	LastWakeAt            *string                `json:"last_wake_at,omitempty"`
	LastWakeStatus        string                 `json:"last_wake_status,omitempty"`
	CatchupPolicy         string                 `json:"catchup_policy,omitempty"`
	LastMissedCount       int                    `json:"last_missed_count,omitempty"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
}

// routineCell renders the ROUTINE column for trigger lists: the target
// slug, suffixed with "@vN" when the trigger is pinned to a specific
// routine version (target_pipeline_version). Pinned triggers execute
// that immutable version instead of head, so the pin must be visible
// at a glance — "daily-digest@v3" reads as "fires v3, not whatever the
// routine looks like today".
func routineCell(slug string, pinnedVersion *int) string {
	if pinnedVersion == nil {
		return slug
	}
	return fmt.Sprintf("%s@v%d", slug, *pinnedVersion)
}

// wakeCell renders the WAKE column: the probe slug plus woke/checked
// telemetry once the gate has actually run ("cost-probe 3/96" = 96
// checks, 3 fires). "—" = ungated.
func wakeCell(s scheduleRow) string {
	if s.WakePipelineSlug == "" && s.WakePipelineID == "" {
		return "—"
	}
	label := s.WakePipelineSlug
	if label == "" {
		label = shortID(s.WakePipelineID)
	}
	if s.WakeCheckCount > 0 {
		return fmt.Sprintf("%s %d/%d", label, s.WakeFireCount, s.WakeCheckCount)
	}
	return label
}

var routineSchedulesCmd = &cobra.Command{
	Use:   "schedules",
	Short: "Manage cron triggers attached to routines",
	Long: `Schedules fire saved routines on a cron expression. Each schedule is
named, targets one routine by slug, and can be enabled/disabled
independently. The scheduler runs in-process and ticks every 30s, so
the minimum cron resolution is one minute.

A schedule may carry a WAKE GATE: an agentless probe routine that runs
first on every tick — free of LLM spend by the agentless guarantee —
and the main routine fires only when the probe's final output is
truthy (same falsy rule as step 'if:' conditions). The list view
shows woke/checked telemetry per gated schedule.

Examples:
  crewship routine schedules list
  crewship routine schedules list --slug summarize-text
  crewship routine schedules create --slug summarize-text \
      --name "daily-summary" --cron "0 9 * * *" --inputs '{"text":"…"}'
  crewship routine schedules create --slug summarize-text \
      --name "daily-summary" --when "every weekday at 9"   # NL→cron, confirms next 3 fires
  crewship routine schedules create --slug cost-report \
      --cron "*/15 * * * *" --wake-slug cost-spike-probe   # LLM only on spike
  crewship routine schedules create --slug daily-digest \
      --cron "0 8 * * *" --pin-version 3   # always fire v3, ignore later edits
  crewship routine schedules enable <schedule_id>
  crewship routine schedules disable <schedule_id>
  crewship routine schedules update <schedule_id> --cron "0 8 * * *"
  crewship routine schedules update <schedule_id> --no-wake   # drop the gate
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
		// Machine formats (global -f/--format, or the legacy --json bool)
		// pass the API rows through — full IDs included, so scripts can
		// feed them straight into `schedules now <id>` / `update <id>`.
		// The human table below truncates IDs via shortID.
		f := resolvedFormatter(cmd)
		if rows == nil {
			rows = []scheduleRow{} // "[]", never "null"
		}
		if f.Format == "json" {
			return f.JSON(rows)
		}
		switch f.Format {
		case "yaml":
			return f.YAML(rows)
		case "ndjson":
			return f.NDJSON(rows)
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
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tROUTINE\tCRON\tTZ\tENABLED\tWAKE\tNEXT\tMISSED")
		for _, s := range rows {
			next := "—"
			if s.NextRunAt != nil && *s.NextRunAt != "" {
				next = formatTimestamp(*s.NextRunAt)
			}
			enabled := "no"
			if s.Enabled {
				enabled = "yes"
			}
			// #1422 item 2: surface backlog occurrences dropped/collapsed
			// on the most recent tick. "—" when current (the overwhelming
			// common case) so an on-time schedule list stays uncluttered.
			missed := "—"
			if s.LastMissedCount > 0 {
				missed = fmt.Sprintf("%d (%s)", s.LastMissedCount, defaultIfBlankCLI(s.CatchupPolicy, "once"))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				shortID(s.ID), s.Name, routineCell(s.TargetPipelineSlug, s.TargetPipelineVersion), s.CronExpr, s.Timezone, enabled, wakeCell(s), next, missed)
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
		when, _ := cmd.Flags().GetString("when")
		timezone, _ := cmd.Flags().GetString("timezone")
		inputsJSON, _ := cmd.Flags().GetString("inputs")
		enabled, _ := cmd.Flags().GetBool("enabled")
		wakeSlug, _ := cmd.Flags().GetString("wake-slug")
		wakeInputsJSON, _ := cmd.Flags().GetString("wake-inputs")
		failClosed, _ := cmd.Flags().GetBool("fail-closed")
		catchup, _ := cmd.Flags().GetString("catchup")
		yes, _ := cmd.Flags().GetBool("yes")
		if slug == "" {
			return fmt.Errorf("--slug is required")
		}
		if cronExpr != "" && when != "" {
			return fmt.Errorf("--cron and --when are mutually exclusive — pass one or the other")
		}
		if cronExpr == "" && when == "" {
			return fmt.Errorf("--cron or --when is required")
		}
		// #1422 item 1: NL→cron. --when is parsed to a cron expression,
		// then echoed back with its next 3 fire times so the operator can
		// confirm the derived schedule actually means what they intended
		// before it's saved — the whole point of exposing this instead of
		// silently trusting the guess.
		if when != "" {
			derived, perr := pipeline.ParseNaturalCron(when)
			if perr != nil {
				return perr
			}
			cronExpr = derived
			previewTZ := timezone
			if previewTZ == "" {
				previewTZ = "UTC"
			}
			occs, oerr := pipeline.NextOccurrences(cronExpr, previewTZ, 3, time.Now())
			fmt.Printf("Parsed %q as cron %q (%s).\n", when, cronExpr, previewTZ)
			if oerr == nil {
				fmt.Println("Next 3 fire times:")
				for _, o := range occs {
					fmt.Printf("  - %s\n", o.Format("2006-01-02 15:04 MST"))
				}
			}
			if !yes {
				fmt.Print("Create this schedule? [y/N]: ")
				var input string
				_, _ = fmt.Scanln(&input)
				if strings.ToLower(strings.TrimSpace(input)) != "y" && strings.ToLower(strings.TrimSpace(input)) != "yes" {
					return fmt.Errorf("aborted")
				}
			}
		}
		if wakeInputsJSON != "" && wakeSlug == "" {
			return fmt.Errorf("--wake-inputs requires --wake-slug")
		}
		if failClosed && wakeSlug == "" {
			return fmt.Errorf("--fail-closed requires --wake-slug (it governs the wake gate's probe-failure handling)")
		}
		if name == "" {
			name = fmt.Sprintf("%s schedule", slug)
		}
		// Default to UTC server-side rather than guessing server's
		// time.Local from the CLI host — running the CLI from a
		// different timezone than the server would otherwise silently
		// pin schedules to the operator's local time. Users who want
		// host-local must pass an IANA zone explicitly (e.g.
		// --timezone Europe/Prague). `date +%Z` returns abbreviations
		// like CEST/PST that the server's tz database rejects, so
		// don't suggest it as a shortcut.
		inputs := map[string]interface{}{}
		if inputsJSON != "" {
			if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
				return fmt.Errorf("--inputs must be valid JSON: %w", err)
			}
		}
		body := map[string]interface{}{
			"name":                 name,
			"target_pipeline_slug": slug,
			"cron_expr":            cronExpr,
			"timezone":             timezone,
			"inputs":               inputs,
			"enabled":              enabled,
		}
		if catchup != "" {
			body["catchup_policy"] = catchup
		}
		if cmd.Flags().Changed("pin-version") {
			pin, _ := cmd.Flags().GetInt("pin-version")
			if pin < 1 {
				return fmt.Errorf("--pin-version must be a positive routine version number")
			}
			body["target_pipeline_version"] = pin
		}
		if wakeSlug != "" {
			body["wake_pipeline_slug"] = wakeSlug
			if wakeInputsJSON != "" {
				var wakeInputs map[string]interface{}
				if err := json.Unmarshal([]byte(wakeInputsJSON), &wakeInputs); err != nil {
					return fmt.Errorf("--wake-inputs must be valid JSON: %w", err)
				}
				body["wake_inputs"] = wakeInputs
			}
			if failClosed {
				body["wake_fail_closed"] = true
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
			body,
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out scheduleRow
		if derr := json.NewDecoder(resp.Body).Decode(&out); derr != nil {
			return fmt.Errorf("decode created schedule: %w", derr)
		}
		// #1219: `create` is the only place a schedule id comes into
		// existence, so a script that can't parse this reply has no way
		// to reach `schedules now <id>` at all. Pass the API row through
		// verbatim for machine formats — full id included.
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Printf("Schedule created: %s (%s @ %s)\n", out.Name, out.CronExpr, out.Timezone)
			fmt.Printf("  ID:     %s\n", out.ID)
			if out.TargetPipelineVersion != nil {
				fmt.Printf("  Pinned: v%d (fires execute this version, not head)\n", *out.TargetPipelineVersion)
			}
			if out.WakePipelineSlug != "" {
				fmt.Printf("  Wake:   %s (routine fires only when the probe's output is truthy)\n", out.WakePipelineSlug)
				if out.WakeFailClosed {
					fmt.Printf("  Policy: fail-closed (a probe error/timeout HOLDS the run instead of firing)\n")
				}
			}
			if out.NextRunAt != nil {
				fmt.Printf("  Next:   %s\n", formatTimestamp(*out.NextRunAt))
			}
		})
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
		if v, _ := cmd.Flags().GetString("catchup"); v != "" {
			body["catchup_policy"] = v
		}
		unpin, _ := cmd.Flags().GetBool("unpin")
		if cmd.Flags().Changed("pin-version") {
			if unpin {
				return fmt.Errorf("--pin-version and --unpin are mutually exclusive")
			}
			pin, _ := cmd.Flags().GetInt("pin-version")
			if pin < 1 {
				return fmt.Errorf("--pin-version must be a positive routine version number")
			}
			body["target_pipeline_version"] = pin
		}
		if unpin {
			// Explicit null clears the pin (absent field keeps it).
			body["target_pipeline_version"] = nil
		}
		noWake, _ := cmd.Flags().GetBool("no-wake")
		wakeSlug, _ := cmd.Flags().GetString("wake-slug")
		if noWake && wakeSlug != "" {
			return fmt.Errorf("--no-wake and --wake-slug are mutually exclusive")
		}
		if noWake {
			// Explicit empty slug = clear the gate (the API treats an
			// absent field as "keep existing").
			body["wake_pipeline_slug"] = ""
		}
		if wakeSlug != "" {
			body["wake_pipeline_slug"] = wakeSlug
		}
		if v, _ := cmd.Flags().GetString("wake-inputs"); v != "" {
			if noWake {
				return fmt.Errorf("--no-wake and --wake-inputs are mutually exclusive")
			}
			var wakeInputs map[string]interface{}
			if err := json.Unmarshal([]byte(v), &wakeInputs); err != nil {
				return fmt.Errorf("--wake-inputs must be valid JSON: %w", err)
			}
			body["wake_inputs"] = wakeInputs
		}
		if cmd.Flags().Changed("fail-closed") {
			if noWake {
				return fmt.Errorf("--no-wake and --fail-closed are mutually exclusive")
			}
			fc, _ := cmd.Flags().GetBool("fail-closed")
			body["wake_fail_closed"] = fc
		}
		if len(body) == 0 {
			return fmt.Errorf("at least one of --cron / --timezone / --name / --enabled / --inputs / --pin-version / --unpin / --wake-slug / --wake-inputs / --no-wake / --fail-closed / --catchup required")
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
		return resolvedFormatter(cmd).AutoHuman(
			scheduleActionResult{ID: args[0], Updated: true},
			func() { fmt.Printf("Schedule %s updated.\n", args[0]) },
		)
	},
}

// scheduleActionResult is the machine-readable acknowledgement shared by
// the schedule mutations (now / update / enable / disable / delete).
//
// #1219: these all used to print prose only, so scripting the family meant
// scraping "Schedule <id> fired (out-of-cycle)." — the exact gap the global
// -f/--format flag exists to close. ID is always present so a caller can
// correlate the reply with the request; the action fields are omitempty so
// each command emits only its own verb rather than a union of false values.
type scheduleActionResult struct {
	ID      string `json:"id" yaml:"id"`
	Fired   bool   `json:"fired,omitempty" yaml:"fired,omitempty"`
	Updated bool   `json:"updated,omitempty" yaml:"updated,omitempty"`
	Deleted bool   `json:"deleted,omitempty" yaml:"deleted,omitempty"`
	// Enabled is a pointer: enable/disable must be able to report the
	// meaningful value `false` without omitempty swallowing it.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

var routineSchedulesEnableCmd = &cobra.Command{
	Use:   "enable <schedule_id>",
	Short: "Enable a schedule (next tick will fire)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setScheduleEnabled(cmd, args[0], true) },
}

var routineSchedulesDisableCmd = &cobra.Command{
	Use:   "disable <schedule_id>",
	Short: "Disable a schedule (skipped on tick until re-enabled)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setScheduleEnabled(cmd, args[0], false) },
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
		// #1219: force-fire is the one schedule operation a test
		// harness drives in a loop, so its acknowledgement has to be
		// machine-readable — otherwise the only way to know it worked
		// is to scrape prose.
		return resolvedFormatter(cmd).AutoHuman(
			scheduleActionResult{ID: args[0], Fired: true},
			func() { fmt.Printf("Schedule %s fired (out-of-cycle).\n", args[0]) },
		)
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
		return resolvedFormatter(cmd).AutoHuman(
			scheduleActionResult{ID: args[0], Deleted: true},
			func() { fmt.Printf("Schedule %s deleted.\n", args[0]) },
		)
	},
}

// setScheduleEnabled backs both `enable` and `disable`.
//
// It takes cmd purely so it can resolve the global -f/--format flag
// (#1219); the HTTP call is identical either way.
func setScheduleEnabled(cmd *cobra.Command, scheduleID string, enabled bool) error {
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
	return resolvedFormatter(cmd).AutoHuman(
		scheduleActionResult{ID: scheduleID, Enabled: &enabled},
		func() { fmt.Printf("Schedule %s %s.\n", scheduleID, state) },
	)
}

// defaultIfBlankCLI returns fallback when s is empty — used for display
// only (e.g. an older server that hasn't populated catchup_policy yet).
func defaultIfBlankCLI(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func formatTimestamp(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}

// shortID renders a schedule ID for the human table. #1199: schedule IDs
// are "psched_" + cuid, ~28 chars — the old 16-char cutoff (truncating to
// 14) sliced well into the ID on every real schedule, and `schedules now
// <id>` requires the exact ID with no prefix-matching fallback, so the
// truncated value from `list` was never directly usable. 40 chars covers
// the realistic ID length with room to spare; only pathologically long
// IDs get cut, and always with a visible "…" marker.
func shortID(id string) string {
	if len(id) <= 40 {
		return id
	}
	return id[:38] + "…"
}

func init() {
	routineSchedulesListCmd.Flags().String("slug", "", "filter to schedules targeting this routine slug")
	routineSchedulesListCmd.Flags().Bool("json", false, "Deprecated alias for --format json")

	routineSchedulesCreateCmd.Flags().String("slug", "", "target routine slug (REQUIRED)")
	routineSchedulesCreateCmd.Flags().String("name", "", "human-readable schedule name (default: '<slug> schedule')")
	routineSchedulesCreateCmd.Flags().String("cron", "", "5-field cron expression — e.g. '0 9 * * *' (required unless --when is given)")
	routineSchedulesCreateCmd.Flags().String("when", "", `natural-language schedule phrase — e.g. "every weekday at 9", "every day at 9am", "every monday at 14:00", "every hour", "every 15 minutes" (parsed to cron, previewed with its next 3 fire times, and confirmed before saving; mutually exclusive with --cron)`)
	routineSchedulesCreateCmd.Flags().Bool("yes", false, "skip the --when confirmation prompt")
	routineSchedulesCreateCmd.Flags().String("timezone", "", "IANA timezone (default: UTC; for host-local pass an IANA zone like Europe/Prague — `date +%Z` returns abbreviations the server rejects)")
	routineSchedulesCreateCmd.Flags().String("inputs", "", "JSON object passed as inputs on each tick (e.g. '{\"text\":\"…\"}')")
	routineSchedulesCreateCmd.Flags().Bool("enabled", true, "create the schedule already enabled (default true)")
	routineSchedulesCreateCmd.Flags().String("wake-slug", "", "wake gate: agentless probe routine evaluated before each fire — the main routine runs only when the probe's output is truthy")
	routineSchedulesCreateCmd.Flags().String("wake-inputs", "", "JSON object passed to the wake probe on each tick (requires --wake-slug)")
	routineSchedulesCreateCmd.Flags().Bool("fail-closed", false, "wake gate: HOLD the run when the probe errors/times out/returns non-COMPLETED instead of failing open (requires --wake-slug; default off — a broken probe fires the main run and records ERROR)")
	routineSchedulesCreateCmd.Flags().Int("pin-version", 0, "pin the schedule to a specific routine version — every fire executes that immutable version instead of head (see 'crewship routine versions <slug>'); if the version is later deleted the fire FAILS with an inbox alert rather than silently running head")
	routineSchedulesCreateCmd.Flags().String("catchup", "", "missed-run catch-up policy when the schedule falls overdue by more than one occurrence: 'skip' (fire nothing for the backlog), 'once' (default — fire once for the backlog, unchanged behaviour), or 'all' (fire once per missed occurrence, oldest first, capped). Ignored by wake-gated schedules, which always behave like 'once'.")

	routineSchedulesUpdateCmd.Flags().String("name", "", "new schedule name")
	routineSchedulesUpdateCmd.Flags().String("cron", "", "new cron expression")
	routineSchedulesUpdateCmd.Flags().String("timezone", "", "new IANA timezone")
	routineSchedulesUpdateCmd.Flags().Bool("enabled", false, "set enabled state — explicit flag presence required (--enabled / --enabled=false)")
	routineSchedulesUpdateCmd.Flags().String("inputs", "", "replace inputs JSON object")
	routineSchedulesUpdateCmd.Flags().String("wake-slug", "", "set/replace the wake gate's agentless probe routine")
	routineSchedulesUpdateCmd.Flags().String("wake-inputs", "", "replace the wake probe's inputs JSON object")
	routineSchedulesUpdateCmd.Flags().Bool("no-wake", false, "remove the wake gate (schedule fires on every tick again)")
	routineSchedulesUpdateCmd.Flags().Bool("fail-closed", false, "set the wake gate's probe-failure policy: --fail-closed HOLDS the run on a probe error/timeout; --fail-closed=false restores fail-open (fire anyway, record ERROR). Absent = keep existing")
	routineSchedulesUpdateCmd.Flags().Int("pin-version", 0, "pin (or re-pin) the schedule to a specific routine version; fires execute that immutable version instead of head")
	routineSchedulesUpdateCmd.Flags().Bool("unpin", false, "remove the version pin (fires track head again); updates that mention neither --pin-version nor --unpin keep the existing pin")
	routineSchedulesUpdateCmd.Flags().String("catchup", "", "set the missed-run catch-up policy: 'skip' / 'once' / 'all' (see 'schedules create --help'). Absent = keep existing")

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
