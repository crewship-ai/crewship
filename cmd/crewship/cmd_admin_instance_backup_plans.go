//go:build !clionly

package main

// `crewship admin instance backups …` — plans, runs and the overview.
//
//	plans list                          GET    /api/v1/admin/instance/backups/plans
//	plans get <id>                      GET    /api/v1/admin/instance/backups/plans/{id}
//	plans create [flags]                POST   /api/v1/admin/instance/backups/plans
//	plans update <id> [flags]           PUT    /api/v1/admin/instance/backups/plans/{id}
//	plans delete <id>                   DELETE /api/v1/admin/instance/backups/plans/{id}
//	plans next <id> [--n 5]             GET    /api/v1/admin/instance/backups/plans/{id}/next
//	plans calendar <id> [--from --to]   GET    /api/v1/admin/instance/backups/plans/{id}/calendar
//	plans preview [--preset --contents --env-mode]  POST …/plans/preview-contents
//	runs [--plan --scope --ws --limit]  GET    /api/v1/admin/instance/backups/runs
//	run  [--plan | --scope …] [--wait]  POST   /api/v1/admin/instance/backups/run
//	run-status <id>                     GET    /api/v1/admin/instance/backups/run/{runId}
//	overview [--scope --ws]             GET    /api/v1/admin/instance/backups/overview

import (
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type backupPlanRow struct {
	ID                 string   `json:"id" yaml:"id"`
	Name               string   `json:"name" yaml:"name"`
	Preset             string   `json:"preset" yaml:"preset"`
	Scope              string   `json:"scope" yaml:"scope"`
	WorkspaceIDs       []string `json:"workspace_ids" yaml:"workspace_ids"`
	Contents           []string `json:"contents" yaml:"contents"`
	EnvMode            string   `json:"env_mode" yaml:"env_mode"`
	Cadence            string   `json:"cadence" yaml:"cadence"`
	TimeOfDay          string   `json:"time_of_day" yaml:"time_of_day"`
	Weekday            *int     `json:"weekday" yaml:"weekday"`
	Monthday           *int     `json:"monthday" yaml:"monthday"`
	CronExpr           *string  `json:"cron_expr" yaml:"cron_expr"`
	Timezone           string   `json:"timezone" yaml:"timezone"`
	EnvCadence         string   `json:"env_cadence" yaml:"env_cadence"`
	KeepMin            int      `json:"keep_min" yaml:"keep_min"`
	KeepDaily          int      `json:"keep_daily" yaml:"keep_daily"`
	KeepWeekly         int      `json:"keep_weekly" yaml:"keep_weekly"`
	KeepMonthly        int      `json:"keep_monthly" yaml:"keep_monthly"`
	Destinations       []string `json:"destinations" yaml:"destinations"`
	RecipientIDs       []string `json:"recipient_ids" yaml:"recipient_ids"`
	BusyWait           int      `json:"busy_wait_minutes" yaml:"busy_wait_minutes"`
	BusyRetry          int      `json:"busy_retry_minutes" yaml:"busy_retry_minutes"`
	HoldCap            int      `json:"hold_cap_minutes" yaml:"hold_cap_minutes"`
	StaleAlert         int      `json:"stale_alert_hours" yaml:"stale_alert_hours"`
	Enabled            bool     `json:"enabled" yaml:"enabled"`
	NextRunAt          *string  `json:"next_run_at" yaml:"next_run_at"`
	LastRunAt          *string  `json:"last_run_at" yaml:"last_run_at"`
	CountsAsProtection bool     `json:"counts_as_protection" yaml:"counts_as_protection"`
}

type backupPlanList struct {
	Data []backupPlanRow `json:"data" yaml:"data"`
}

type backupRunPhase struct {
	Name      string  `json:"name" yaml:"name"`
	StartedAt *string `json:"started_at" yaml:"started_at"`
	EndedAt   *string `json:"ended_at" yaml:"ended_at"`
	Status    string  `json:"status" yaml:"status"`
	Detail    *string `json:"detail" yaml:"detail"`
}

type backupRunIncomplete struct {
	Kind        string  `json:"kind" yaml:"kind"`
	Count       int     `json:"count" yaml:"count"`
	WorkspaceID *string `json:"workspace_id" yaml:"workspace_id"`
	Detail      string  `json:"detail" yaml:"detail"`
}

type backupRunRow struct {
	ID            string                `json:"id" yaml:"id"`
	PlanID        *string               `json:"plan_id" yaml:"plan_id"`
	PlanName      *string               `json:"plan_name" yaml:"plan_name"`
	Trigger       string                `json:"trigger" yaml:"trigger"`
	Note          *string               `json:"note" yaml:"note"`
	Scope         string                `json:"scope" yaml:"scope"`
	WorkspaceID   *string               `json:"workspace_id" yaml:"workspace_id"`
	WorkspaceName *string               `json:"workspace_name" yaml:"workspace_name"`
	Kind          string                `json:"kind" yaml:"kind"`
	Status        string                `json:"status" yaml:"status"`
	RetriedAt     *string               `json:"retried_at" yaml:"retried_at"`
	Phases        []backupRunPhase      `json:"phases" yaml:"phases"`
	BundlePath    *string               `json:"bundle_path" yaml:"bundle_path"`
	SizeBytes     *int64                `json:"size_bytes" yaml:"size_bytes"`
	Error         *string               `json:"error" yaml:"error"`
	Incomplete    []backupRunIncomplete `json:"incomplete" yaml:"incomplete"`
	StartedAt     string                `json:"started_at" yaml:"started_at"`
	EndedAt       *string               `json:"ended_at" yaml:"ended_at"`
	ProofLevel    int                   `json:"proof_level" yaml:"proof_level"`
	DrillResult   *string               `json:"drill_result" yaml:"drill_result"`
	Pinned        bool                  `json:"pinned" yaml:"pinned"`
	Recipients    []string              `json:"recipients" yaml:"recipients"`
	Phase         string                `json:"phase" yaml:"phase"`
	Environments  bool                  `json:"environments" yaml:"environments"`
	DueAt         *string               `json:"due_at" yaml:"due_at"`
	RetryOf       *string               `json:"retry_of" yaml:"retry_of"`
	Categories    []string              `json:"categories" yaml:"categories"`
	HoldMs        *int64                `json:"hold_ms" yaml:"hold_ms"`
}

type backupRunList struct {
	Data []backupRunRow `json:"data" yaml:"data"`
}

type backupNextRuns struct {
	Runs []struct {
		At           string `json:"at" yaml:"at"`
		Environments bool   `json:"environments" yaml:"environments"`
	} `json:"runs" yaml:"runs"`
}

type backupCalendar struct {
	Days []struct {
		Date    string `json:"date" yaml:"date"`
		Entries []struct {
			At     string `json:"at" yaml:"at"`
			Kind   string `json:"kind" yaml:"kind"`
			Status string `json:"status" yaml:"status"`
		} `json:"entries" yaml:"entries"`
	} `json:"days" yaml:"days"`
}

type backupPreview struct {
	Included []string `json:"included" yaml:"included"`
	Required []struct {
		Key     string   `json:"key" yaml:"key"`
		Because []string `json:"because" yaml:"because"`
	} `json:"required" yaml:"required"`
	Excluded []string `json:"excluded" yaml:"excluded"`
}

type backupRunStarted struct {
	ID     string   `json:"id" yaml:"id"`
	RunID  string   `json:"run_id" yaml:"run_id"`
	RunIDs []string `json:"run_ids" yaml:"run_ids"`
	Status string   `json:"status" yaml:"status"`
}

type backupStatusRow struct {
	Value      string  `json:"value" yaml:"value"`
	Detail     *string `json:"detail" yaml:"detail"`
	DetailTone *string `json:"detail_tone" yaml:"detail_tone"`
}

type backupOverview struct {
	Status struct {
		Label           string          `json:"label" yaml:"label"`
		Verdict         string          `json:"verdict" yaml:"verdict"`
		Summary         *string         `json:"summary" yaml:"summary"`
		Protects        backupStatusRow `json:"protects" yaml:"protects"`
		HowOften        backupStatusRow `json:"how_often" yaml:"how_often"`
		Where           backupStatusRow `json:"where" yaml:"where"`
		HowLong         backupStatusRow `json:"how_long" yaml:"how_long"`
		ReallyRestored  backupStatusRow `json:"really_restored" yaml:"really_restored"`
		OffsiteVerified bool            `json:"offsite_verified" yaml:"offsite_verified"`
	} `json:"status" yaml:"status"`
	NeedsAttention []struct {
		ID       string `json:"id" yaml:"id"`
		Severity string `json:"severity" yaml:"severity"`
		Title    string `json:"title" yaml:"title"`
		Detail   string `json:"detail" yaml:"detail"`
		Action   *struct {
			Kind        string  `json:"kind" yaml:"kind"`
			Label       string  `json:"label" yaml:"label"`
			WorkspaceID *string `json:"workspace_id" yaml:"workspace_id"`
			RunID       *string `json:"run_id" yaml:"run_id"`
		} `json:"action" yaml:"action"`
	} `json:"needs_attention" yaml:"needs_attention"`
	Nights []struct {
		Date   string  `json:"date" yaml:"date"`
		Status string  `json:"status" yaml:"status"`
		Proof  int     `json:"proof" yaml:"proof"`
		Detail *string `json:"detail" yaml:"detail"`
	} `json:"nights" yaml:"nights"`
	Space struct {
		BackupsBytes     int64 `json:"backups_bytes" yaml:"backups_bytes"`
		FreeBytes        int64 `json:"free_bytes" yaml:"free_bytes"`
		TotalBytes       int64 `json:"total_bytes" yaml:"total_bytes"`
		StagingNeedBytes int64 `json:"staging_need_bytes" yaml:"staging_need_bytes"`
		RestoreNeedBytes int64 `json:"restore_need_bytes" yaml:"restore_need_bytes"`
	} `json:"space" yaml:"space"`
	Workspaces []struct {
		WorkspaceID  string  `json:"workspace_id" yaml:"workspace_id"`
		Name         string  `json:"name" yaml:"name"`
		LastBackupAt *string `json:"last_backup_at" yaml:"last_backup_at"`
		Status       string  `json:"status" yaml:"status"`
		Plan         *string `json:"plan" yaml:"plan"`
		Proof        int     `json:"proof" yaml:"proof"`
	} `json:"workspaces,omitempty" yaml:"workspaces,omitempty"`
	InstanceSummary *string `json:"instance_summary" yaml:"instance_summary"`
}

const instanceBackupsAPI = "/api/v1/admin/instance/backups"

func backupStrOr(s *string, dflt string) string {
	if s == nil || *s == "" {
		return dflt
	}
	return *s
}

func planWhen(p backupPlanRow) string {
	switch p.Cadence {
	case "custom":
		return "cron " + backupStrOr(p.CronExpr, "?")
	case "weekly":
		wd := 0
		if p.Weekday != nil {
			wd = *p.Weekday
		}
		return fmt.Sprintf("weekly %s %s", []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}[wd%7], p.TimeOfDay)
	case "monthly":
		if p.Monthday != nil {
			return fmt.Sprintf("monthly day %d %s", *p.Monthday, p.TimeOfDay)
		}
		return "monthly 1st Sun " + p.TimeOfDay
	}
	return "daily " + p.TimeOfDay
}

func printBackupPlan(cmd *cobra.Command, p backupPlanRow) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%s  %s (%s)\n", p.ID, p.Name, p.Preset)
	scope := p.Scope
	if p.Scope == "workspaces" {
		if len(p.WorkspaceIDs) == 0 {
			scope += ": every workspace"
		} else {
			scope += ": " + strings.Join(p.WorkspaceIDs, ", ")
		}
	}
	fmt.Fprintf(w, "  scope     %s\n", scope)
	if len(p.Contents) > 0 {
		fmt.Fprintf(w, "  contents  %s (dependencies added when it runs)\n", strings.Join(p.Contents, ", "))
	}
	fmt.Fprintf(w, "  when      %s %s · environments: %s (%s)\n", planWhen(p), p.Timezone, p.EnvMode, p.EnvCadence)
	fmt.Fprintf(w, "  keep      %d checked, %d daily, %d weekly, %d monthly\n", p.KeepMin, p.KeepDaily, p.KeepWeekly, p.KeepMonthly)
	fmt.Fprintf(w, "  busy      wait up to %d min, look again every %d min\n", p.BusyWait, p.BusyRetry)
	fmt.Fprintf(w, "  enabled   %v · next %s · last %s\n", p.Enabled, backupStrOr(p.NextRunAt, "—"), backupStrOr(p.LastRunAt, "never"))
	if !p.CountsAsProtection {
		fmt.Fprintln(w, "  note      a custom plan's backups do not count as protecting a workspace")
	}
}

var adminInstanceBackupsPlansCmd = &cobra.Command{
	Use:   "plans",
	Short: "Backup plans: what goes in, when, for how long",
	Long: `Backup plans run on the server's backup scheduler — never through an agent.

  crewship admin instance backups plans list
  crewship admin instance backups plans create --preset workspace --workspace lab --recipient age1…
  crewship admin instance backups plans create --preset custom --contents memory --cadence custom --cron "0 */6 * * *" --recipient age1…
  crewship admin instance backups plans next <id>
  crewship admin instance backups plans preview --preset custom --contents memory`,
}

var adminInstanceBackupsPlansListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every backup plan",
	Long:  `GET /api/v1/admin/instance/backups/plans.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out backupPlanList
		if err := getJSON(client, instanceBackupsAPI+"/plans", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPRESET\tSCOPE\tWHEN\tTIMEZONE\tENABLED\tNEXT")
			for _, p := range out.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%v\t%s\n", p.ID, p.Name, p.Preset, p.Scope, planWhen(p), p.Timezone, p.Enabled, backupStrOr(p.NextRunAt, "—"))
			}
			_ = tw.Flush()
		})
	},
}

var adminInstanceBackupsPlansGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one backup plan",
	Long:  `GET /api/v1/admin/instance/backups/plans/{id}.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out backupPlanRow
		if err := getJSON(client, instanceBackupsAPI+"/plans/"+url.PathEscape(args[0]), &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() { printBackupPlan(cmd, out) })
	},
}

// planBody collects the plan flags the user actually set. The server fills
// the rest from the preset's defaults (create) or keeps them (update).
func planBody(cmd *cobra.Command) map[string]any {
	f := cmd.Flags()
	body := map[string]any{}
	str := func(flag, key string) {
		if f.Changed(flag) {
			v, _ := f.GetString(flag)
			body[key] = v
		}
	}
	num := func(flag, key string) {
		if f.Changed(flag) {
			v, _ := f.GetInt(flag)
			body[key] = v
		}
	}
	list := func(flag, key string) {
		if f.Changed(flag) {
			v, _ := f.GetStringSlice(flag)
			if v == nil {
				v = []string{}
			}
			body[key] = v
		}
	}
	str("preset", "preset")
	str("name", "name")
	str("scope", "scope")
	list("workspace", "workspace_ids")
	list("contents", "contents")
	str("env-mode", "env_mode")
	str("cadence", "cadence")
	str("time", "time_of_day")
	str("timezone", "timezone")
	str("env-cadence", "env_cadence")
	num("keep-min", "keep_min")
	num("keep-daily", "keep_daily")
	num("keep-weekly", "keep_weekly")
	num("keep-monthly", "keep_monthly")
	list("recipient", "recipient_ids")
	list("destination", "destinations")
	num("busy-wait", "busy_wait_minutes")
	num("busy-retry", "busy_retry_minutes")
	num("hold-cap", "hold_cap_minutes")
	num("stale-alert", "stale_alert_hours")
	if f.Changed("weekday") {
		v, _ := f.GetInt("weekday")
		body["weekday"] = v
	}
	if f.Changed("monthday") {
		v, _ := f.GetInt("monthday")
		if v == 0 {
			body["monthday"] = nil // first Sunday
		} else {
			body["monthday"] = v
		}
	}
	if f.Changed("cron") {
		v, _ := f.GetString("cron")
		body["cron_expr"] = v
		if !f.Changed("cadence") {
			body["cadence"] = "custom"
		}
	}
	if f.Changed("disabled") {
		v, _ := f.GetBool("disabled")
		body["enabled"] = !v
	}
	return body
}

func addPlanFlags(c *cobra.Command) {
	f := c.Flags()
	f.String("preset", "", "complete | workspace | custom")
	f.String("name", "", "Plan name")
	f.String("scope", "", "instance | workspaces (complete recovery is always instance)")
	f.StringSlice("workspace", nil, "Workspace ids or slugs, comma-separated (default: every workspace)")
	f.StringSlice("contents", nil, "Custom plans: categories (agents,memory,chats,att,routines,journal,creds,pages,files)")
	f.String("env-mode", "", "files | complete")
	f.String("cadence", "", "daily | weekly | monthly | custom")
	f.String("time", "", "Time of day, HH:MM in the plan's timezone")
	f.Int("weekday", 0, "Weekly plans: 0 = Sunday … 6 = Saturday")
	f.Int("monthday", 0, "Monthly plans: day 1-31 (0 = the first Sunday)")
	f.String("cron", "", "Custom cadence: a 5-field cron expression (sets --cadence custom)")
	f.String("timezone", "", "IANA timezone, e.g. Europe/Prague")
	f.String("env-cadence", "", "every | weekly | monthly: which runs also take environments")
	f.Int("keep-min", 0, "Newest copies never aged out (at least 1)")
	f.Int("keep-daily", 0, "Daily copies kept")
	f.Int("keep-weekly", 0, "Weekly copies kept")
	f.Int("keep-monthly", 0, "Monthly copies kept")
	f.StringSlice("recipient", nil, "age public keys (age1…) or recipient ids that can open the bundles")
	f.StringSlice("destination", nil, "Destinations (default: local)")
	f.Int("busy-wait", 0, "Minutes a run waits for busy workspaces before it is skipped")
	f.Int("busy-retry", 0, "Minutes between looks while busy")
	f.Int("hold-cap", 0, "Longest quiet window, in minutes")
	f.Int("stale-alert", 0, "Hours without a backup before it needs attention")
	f.Bool("disabled", false, "Create or leave the plan switched off")
}

var adminInstanceBackupsPlansCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a backup plan",
	Long: `POST /api/v1/admin/instance/backups/plans. --preset picks the defaults
(complete: the whole instance, data daily 03:00 and environments weekly;
workspace: every category of the chosen workspaces, daily; custom: only the
categories in --contents). --recipient is required: every backup is
encrypted. Recorded in the instance audit log.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out backupPlanRow
		if err := postJSON(client, instanceBackupsAPI+"/plans", planBody(cmd), &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintln(cmd.OutOrStdout(), "Created backup plan:")
			printBackupPlan(cmd, out)
		})
	},
}

var adminInstanceBackupsPlansUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Change a backup plan",
	Long: `PUT /api/v1/admin/instance/backups/plans/{id}. Only the flags you pass
change; the next run time is recomputed. Recorded in the instance audit log.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out backupPlanRow
		if err := putJSON(client, instanceBackupsAPI+"/plans/"+url.PathEscape(args[0]), planBody(cmd), &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintln(cmd.OutOrStdout(), "Updated backup plan:")
			printBackupPlan(cmd, out)
		})
	},
}

var adminInstanceBackupsPlansDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a backup plan (its runs stay as history)",
	Long: `DELETE /api/v1/admin/instance/backups/plans/{id}. Bundles and run history
stay. Recorded in the instance audit log.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, instanceBackupsAPI+"/plans/"+url.PathEscape(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted backup plan %s.\n", args[0])
		return nil
	},
}

var adminInstanceBackupsPlansNextCmd = &cobra.Command{
	Use:   "next <id>",
	Short: "The plan's next run times, and which also take environments",
	Long:  `GET /api/v1/admin/instance/backups/plans/{id}/next?n=5.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		n, _ := cmd.Flags().GetInt("n")
		var out backupNextRuns
		if err := getJSON(client, instanceBackupsAPI+"/plans/"+url.PathEscape(args[0])+"/next"+queryString("n", fmt.Sprint(n)), &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			for _, r := range out.Runs {
				extra := ""
				if r.Environments {
					extra = "  + environments"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s\n", r.At, extra)
			}
		})
	},
}

var adminInstanceBackupsPlansCalendarCmd = &cobra.Command{
	Use:   "calendar <id>",
	Short: "Done, skipped, catch-up, failed and planned runs per day",
	Long: `GET /api/v1/admin/instance/backups/plans/{id}/calendar?from=&to=. Dates
are YYYY-MM-DD in the plan's timezone; the default is the last 7 and the
next 28 days.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		path := instanceBackupsAPI + "/plans/" + url.PathEscape(args[0]) + "/calendar" + queryString("from", from, "to", to)
		var out backupCalendar
		if err := getJSON(client, path, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			for _, d := range out.Days {
				if len(d.Entries) == 0 {
					continue
				}
				var parts []string
				for _, e := range d.Entries {
					parts = append(parts, fmt.Sprintf("%s %s %s", e.At[11:16], e.Kind, e.Status))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", d.Date, strings.Join(parts, " · "))
			}
		})
	},
}

var adminInstanceBackupsPlansPreviewCmd = &cobra.Command{
	Use:   "preview",
	Short: "What a plan's backups carry: included, required dependencies, excluded",
	Long:  `POST /api/v1/admin/instance/backups/plans/preview-contents.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		preset, _ := cmd.Flags().GetString("preset")
		contents, _ := cmd.Flags().GetStringSlice("contents")
		envMode, _ := cmd.Flags().GetString("env-mode")
		if contents == nil {
			contents = []string{}
		}
		var out backupPreview
		if err := postJSON(client, instanceBackupsAPI+"/plans/preview-contents",
			map[string]any{"preset": preset, "contents": contents, "env_mode": envMode}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "included  %s\n", strings.Join(out.Included, ", "))
			for _, r := range out.Required {
				fmt.Fprintf(w, "required  %s (needed by %s)\n", r.Key, strings.Join(r.Because, ", "))
			}
			fmt.Fprintf(w, "excluded  %s\n", strings.Join(out.Excluded, ", "))
		})
	},
}

func phaseLine(phases []backupRunPhase) string {
	var parts []string
	for _, p := range phases {
		parts = append(parts, p.Name+":"+p.Status)
	}
	return strings.Join(parts, " ")
}

var adminInstanceBackupsRunsCmd = &cobra.Command{
	Use:   "runs",
	Short: "Backup run history with phases and proof",
	Long: `GET /api/v1/admin/instance/backups/runs. Newest first: trigger (schedule,
manual, catchup), status (running, done, incomplete, failed, interrupted,
skipped), phases, and the bundle's proof level. --ws takes workspace slugs
or ids.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		plan, _ := cmd.Flags().GetString("plan")
		scope, _ := cmd.Flags().GetString("scope")
		ws, _ := cmd.Flags().GetStringSlice("ws")
		limit, _ := cmd.Flags().GetInt("limit")
		path := instanceBackupsAPI + "/runs" + queryString("plan", plan, "scope", scope, "ws", strings.Join(ws, ","), "limit", fmt.Sprint(limit))
		var out backupRunList
		if err := getJSON(client, path, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "STARTED\tPLAN\tTRIGGER\tWORKSPACE\tSTATUS\tPROOF\tPHASES\tERROR")
			for _, r := range out.Data {
				ws := backupStrOr(r.WorkspaceName, backupStrOr(r.WorkspaceID, "instance"))
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.StartedAt, backupStrOr(r.PlanName, "—"), r.Trigger, ws, r.Status,
					proofLabel(r.ProofLevel), phaseLine(r.Phases), backupStrOr(r.Error, ""))
			}
			_ = tw.Flush()
		})
	},
}

var adminInstanceBackupsRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Start a backup now: a plan, the whole instance, or selected workspaces",
	Long: `POST /api/v1/admin/instance/backups/run. The run executes in the server's
backup service (never through an agent) and the command answers at once with
its id; --wait follows it until it ends and prints what it wrote.

With --plan, runs that plan now. Without it, --scope says what to back up:
instance writes one instance bundle (the whole database, every file store
and every crew container, copied in one quiet window: running work is
drained first and writes are held for the copy only); workspaces writes one
bundle per workspace (--workspace narrows it, default every one), with
--preset and --contents choosing what goes in.

Every backup is encrypted: --recipient (an age1… public key or a recipient
id, repeatable) or --passphrase-file. Without either, the recipients of the
plan that covers the scope are used. The passphrase is held in the server's
memory until the run starts and never stored.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		f := cmd.Flags()
		body := map[string]any{}
		for flag, key := range map[string]string{"plan": "plan_id", "scope": "scope", "preset": "preset", "env-mode": "env_mode", "note": "note"} {
			if v, _ := f.GetString(flag); v != "" {
				body[key] = v
			}
		}
		for flag, key := range map[string]string{"workspace": "workspace_ids", "contents": "contents", "recipient": "recipient_ids"} {
			if v, _ := f.GetStringSlice(flag); len(v) > 0 {
				body[key] = v
			}
		}
		if passFile, _ := f.GetString("passphrase-file"); passFile != "" {
			if _, ok := body["recipient_ids"]; ok {
				return fmt.Errorf("give --recipient or --passphrase-file, not both")
			}
			p, err := readPassphrase(passFile, false)
			if err != nil {
				return err
			}
			body["passphrase"] = p
		}
		var out backupRunStarted
		if err := postJSON(client, instanceBackupsAPI+"/run", body, &out); err != nil {
			return err
		}
		if wait, _ := f.GetBool("wait"); !wait {
			return resolvedFormatter(cmd).AutoHuman(out, func() {
				fmt.Fprintf(cmd.OutOrStdout(), "Started %s. It runs on the server; follow it with: crewship admin instance backups run-status %s\n",
					strings.Join(out.RunIDs, ", "), out.ID)
			})
		}
		timeout, _ := f.GetDuration("timeout")
		deadline := time.Now().Add(timeout)
		runs := make([]backupRunRow, 0, len(out.RunIDs))
		for _, id := range out.RunIDs {
			for {
				var st backupRunRow
				if err := getJSON(client, instanceBackupsAPI+"/run/"+id, &st); err != nil {
					return err
				}
				if st.Status != "running" {
					runs = append(runs, st)
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("run %s is still running after %s; follow it with: crewship admin instance backups run-status %s", id, timeout, id)
				}
				time.Sleep(2 * time.Second)
			}
		}
		if err := resolvedFormatter(cmd).AutoHuman(runs, func() {
			for _, r := range runs {
				printRunRow(cmd, r)
			}
		}); err != nil {
			return err
		}
		return runsFailed(runs)
	},
}

// printRunRow is the human form of one run: status, what it wrote, how long
// writes were held, what is missing, and why it failed.
func printRunRow(cmd *cobra.Command, r backupRunRow) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Run %s (%s, %s): %s\n", r.ID, backupStrOr(r.WorkspaceName, backupStrOr(r.WorkspaceID, r.Scope)), r.Trigger, r.Status)
	if line := phaseLine(r.Phases); line != "" {
		fmt.Fprintf(out, "  phases  %s\n", line)
	}
	if r.HoldMs != nil && *r.HoldMs > 0 {
		fmt.Fprintf(out, "  Writes held for %s for the consistent copy.\n", time.Duration(*r.HoldMs)*time.Millisecond)
	}
	if r.BundlePath != nil {
		size := int64(0)
		if r.SizeBytes != nil {
			size = *r.SizeBytes
		}
		fmt.Fprintf(out, "  %s  %d bytes  %s\n", *r.BundlePath, size, proofLabel(r.ProofLevel))
	}
	for _, it := range r.Incomplete {
		fmt.Fprintf(out, "  missing: %s ×%d — %s\n", it.Kind, it.Count, it.Detail)
	}
	if r.Error != nil && *r.Error != "" {
		fmt.Fprintf(out, "  Error: %s\n", *r.Error)
	}
}

// runsFailed: a run that failed, was interrupted or was skipped wrote no
// usable bundle, so the command exits non-zero.
func runsFailed(runs []backupRunRow) error {
	for _, r := range runs {
		switch r.Status {
		case "failed", "interrupted", "skipped":
			return fmt.Errorf("backup run %s %s", r.ID, r.Status)
		}
	}
	return nil
}

var adminInstanceBackupsRunStatusCmd = &cobra.Command{
	Use:   "run-status <id>",
	Short: "Show one backup run: status, phases, the bundle it wrote",
	Long: `GET /api/v1/admin/instance/backups/run/{runId}. The same fields as a row
of 'backups runs'. Exits non-zero when the run failed, was interrupted or was
skipped.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var st backupRunRow
		if err := getJSON(client, instanceBackupsAPI+"/run/"+url.PathEscape(args[0]), &st); err != nil {
			return err
		}
		if err := resolvedFormatter(cmd).AutoHuman(st, func() { printRunRow(cmd, st) }); err != nil {
			return err
		}
		return runsFailed([]backupRunRow{st})
	},
}

var adminInstanceBackupsOverviewCmd = &cobra.Command{
	Use:   "overview",
	Short: "Whether a restore would work: verdict, gaps, the last 14 nights, space",
	Long: `GET /api/v1/admin/instance/backups/overview?scope=instance|workspaces&ws=.
The verdict is the weakest proof among the latest full backups of the
selection (custom backups never count): verified, partial, failed,
contents_checked, checksum_only or none.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		scope, _ := cmd.Flags().GetString("scope")
		ws, _ := cmd.Flags().GetStringSlice("ws")
		var out backupOverview
		if err := getJSON(client, instanceBackupsAPI+"/overview"+queryString("scope", scope, "ws", strings.Join(ws, ",")), &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			st := out.Status
			fmt.Fprintf(w, "%s: %s\n", st.Label, st.Verdict)
			if st.Summary != nil {
				fmt.Fprintf(w, "  %s\n", *st.Summary)
			}
			for _, row := range []struct {
				k string
				v backupStatusRow
			}{{"protects", st.Protects}, {"how often", st.HowOften}, {"where", st.Where}, {"how long", st.HowLong}, {"restored", st.ReallyRestored}} {
				fmt.Fprintf(w, "  %-10s %s", row.k, row.v.Value)
				if row.v.Detail != nil {
					fmt.Fprintf(w, " (%s)", *row.v.Detail)
				}
				fmt.Fprintln(w)
			}
			var marks []string
			for _, n := range out.Nights {
				marks = append(marks, n.Date[8:]+":"+n.Status)
			}
			fmt.Fprintf(w, "  nights     %s\n", strings.Join(marks, " "))
			fmt.Fprintf(w, "  space      backups %d B · free %d of %d B · staging needs %d B · restore needs %d B\n",
				out.Space.BackupsBytes, out.Space.FreeBytes, out.Space.TotalBytes, out.Space.StagingNeedBytes, out.Space.RestoreNeedBytes)
			if len(out.NeedsAttention) > 0 {
				fmt.Fprintln(w, "Needs attention:")
				for _, a := range out.NeedsAttention {
					fmt.Fprintf(w, "  [%s] %s — %s\n", a.Severity, a.Title, a.Detail)
				}
			}
			for _, row := range out.Workspaces {
				fmt.Fprintf(w, "  %-20s %-4s last %s · plan %s · %s\n", row.Name, row.Status, backupStrOr(row.LastBackupAt, "never"), backupStrOr(row.Plan, "none"), proofLabel(row.Proof))
			}
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{adminInstanceBackupsPlansCreateCmd, adminInstanceBackupsPlansUpdateCmd} {
		addPlanFlags(c)
	}
	adminInstanceBackupsPlansNextCmd.Flags().Int("n", 5, "How many run times (1-50)")
	adminInstanceBackupsPlansCalendarCmd.Flags().String("from", "", "First date, YYYY-MM-DD (default: 7 days ago)")
	adminInstanceBackupsPlansCalendarCmd.Flags().String("to", "", "Last date, YYYY-MM-DD (default: 28 days ahead)")
	pv := adminInstanceBackupsPlansPreviewCmd.Flags()
	pv.String("preset", "custom", "complete | workspace | custom")
	pv.StringSlice("contents", nil, "Categories a custom plan chooses")
	pv.String("env-mode", "files", "files | complete")

	rf := adminInstanceBackupsRunsCmd.Flags()
	rf.String("plan", "", "Only this plan's runs")
	rf.String("scope", "", "instance | workspaces")
	rf.StringSlice("ws", nil, "Workspace slugs or ids, comma-separated")
	rf.Int("limit", 100, "Rows to return (max 500)")

	run := adminInstanceBackupsRunCmd.Flags()
	run.String("plan", "", "Run this plan now")
	run.String("scope", "", "instance | workspaces")
	run.StringSlice("workspace", nil, "With --scope workspaces: workspace slugs or ids (default: every workspace)")
	run.String("preset", "", "complete | workspace | custom")
	run.StringSlice("contents", nil, "Custom run: categories")
	run.String("env-mode", "", "files | complete")
	run.StringSlice("recipient", nil, "Encrypt to these age public keys (age1…) or recipient ids; repeatable (default: the covering plan's)")
	run.String("passphrase-file", "", "Encrypt with the passphrase in this file instead of recipients")
	run.String("note", "", "A note on the run, e.g. \"before upgrade\"")
	run.Bool("wait", false, "Wait for the run(s) to finish and print what they wrote")
	run.Duration("timeout", 3*time.Hour, "With --wait: how long to wait")

	of := adminInstanceBackupsOverviewCmd.Flags()
	of.String("scope", "instance", "instance | workspaces")
	of.StringSlice("ws", nil, "Workspace slugs or ids, comma-separated (default: all)")

	adminInstanceBackupsPlansCmd.AddCommand(adminInstanceBackupsPlansListCmd, adminInstanceBackupsPlansGetCmd,
		adminInstanceBackupsPlansCreateCmd, adminInstanceBackupsPlansUpdateCmd, adminInstanceBackupsPlansDeleteCmd,
		adminInstanceBackupsPlansNextCmd, adminInstanceBackupsPlansCalendarCmd, adminInstanceBackupsPlansPreviewCmd)
	adminInstanceBackupsCmd.AddCommand(adminInstanceBackupsPlansCmd, adminInstanceBackupsRunsCmd,
		adminInstanceBackupsRunCmd, adminInstanceBackupsRunStatusCmd, adminInstanceBackupsOverviewCmd)
}
