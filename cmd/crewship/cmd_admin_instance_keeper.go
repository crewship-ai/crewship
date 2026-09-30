//go:build !clionly

package main

// `crewship admin instance keeper …` — Admin › Security across workspaces.
//
//	governance                          GET /api/v1/admin/instance/keeper/governance
//	set (--workspace a,b | --all) …     PUT /api/v1/admin/instance/keeper/governance
//	requests [--workspace a,b]          GET /api/v1/admin/instance/keeper/requests
//	health                              GET /api/v1/admin/instance/keeper/health
//
// `set` for more than one workspace first asks the server what it would
// overwrite (dry_run), prints that workspace by workspace, and writes only
// after a yes — the same dialog the console shows.

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type instanceGovRow struct {
	WorkspaceID           string   `json:"workspace_id" yaml:"workspace_id"`
	WorkspaceName         string   `json:"workspace_name" yaml:"workspace_name"`
	WorkspaceSlug         string   `json:"workspace_slug" yaml:"workspace_slug"`
	Configured            bool     `json:"configured" yaml:"configured"`
	Enabled               bool     `json:"enabled" yaml:"enabled"`
	DenyNotifyMinRisk     int      `json:"deny_notify_min_risk" yaml:"deny_notify_min_risk"`
	WatchPresets          []string `json:"watch_presets" yaml:"watch_presets"`
	RequireSecondApprover bool     `json:"require_second_approver" yaml:"require_second_approver"`
	GovModelProvider      string   `json:"gov_model_provider" yaml:"gov_model_provider"`
	GovModelID            string   `json:"gov_model_id" yaml:"gov_model_id"`
	AutoLeaseSeconds      int      `json:"auto_lease_seconds" yaml:"auto_lease_seconds"`
	BehaviorSampleEvery   int      `json:"behavior_sample_every" yaml:"behavior_sample_every"`
}

type instanceGovList struct {
	Defaults   instanceGovRow   `json:"defaults" yaml:"defaults"`
	Workspaces []instanceGovRow `json:"workspaces" yaml:"workspaces"`
}

type instanceGovChange struct {
	WorkspaceID   string `json:"workspace_id" yaml:"workspace_id"`
	WorkspaceName string `json:"workspace_name" yaml:"workspace_name"`
	WorkspaceSlug string `json:"workspace_slug" yaml:"workspace_slug"`
	Changes       []struct {
		Field  string `json:"field" yaml:"field"`
		Before any    `json:"before" yaml:"before"`
		After  any    `json:"after" yaml:"after"`
	} `json:"changes" yaml:"changes"`
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

type instanceGovPutResult struct {
	Applied         bool                `json:"applied" yaml:"applied"`
	Changed         int                 `json:"changed" yaml:"changed"`
	DefaultsUpdated bool                `json:"defaults_updated" yaml:"defaults_updated"`
	Workspaces      []instanceGovChange `json:"workspaces" yaml:"workspaces"`
	PreviewID       string              `json:"preview_id" yaml:"preview_id"`
}

var adminInstanceKeeperCmd = &cobra.Command{
	Use:   "keeper",
	Short: "The Keeper of every workspace: settings, decisions and health",
	Long: `Admin › Security from the terminal, across every workspace on the server —
including workspaces you are not a member of. Settings are saved for one,
several or all workspaces; saving for several overwrites what each has, so
'set' shows the changes first and asks. Saving for all also becomes what a
workspace created later starts with.

  crewship admin instance keeper governance
  crewship admin instance keeper set --workspace coolify --watchdog on --sample-every 10
  crewship admin instance keeper set --all --watchdog on --dry-run
  crewship admin instance keeper requests --workspace coolify,sandbox --decision DENY`,
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

var adminInstanceKeeperGovernanceCmd = &cobra.Command{
	Use:   "governance",
	Short: "Show what each workspace has switched on",
	Long: `GET /api/v1/admin/instance/keeper/governance. One row per workspace: the
watchdog, how often it samples, the DENY alert threshold, four-eyes approval,
the credential lease and the judge. "default" means the workspace has never
been set and follows the instance defaults (the last save for all).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out instanceGovList
		if err := getJSON(client, "/api/v1/admin/instance/keeper/governance", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WORKSPACE\tWATCHDOG\tSAMPLE\tDENY ALERT ≥\tFOUR-EYES\tLEASE\tJUDGE\tSOURCE")
			for _, w := range out.Workspaces {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", w.WorkspaceSlug, onOff(w.Enabled), sampleLabel(w.BehaviorSampleEvery),
					w.DenyNotifyMinRisk, onOff(w.RequireSecondApprover), leaseLabel(w.AutoLeaseSeconds), judgeLabel(w), sourceLabel(w.Configured))
			}
			_ = tw.Flush()
		})
	},
}

func sampleLabel(n int) string {
	if n <= 0 {
		return "1 in 5"
	}
	return fmt.Sprintf("1 in %d", n)
}

func leaseLabel(s int) string {
	if s <= 0 {
		return "standing"
	}
	return (time.Duration(s) * time.Second).String()
}

func judgeLabel(w instanceGovRow) string {
	if w.GovModelProvider == "" {
		return "instance"
	}
	return w.GovModelProvider + ":" + w.GovModelID
}

func sourceLabel(configured bool) string {
	if configured {
		return "own"
	}
	return "default"
}

var adminInstanceKeeperSetCmd = &cobra.Command{
	Use:   "set (--workspace <slug>[,<slug>…] | --all) [settings]",
	Short: "Set the watchdog for one, several or all workspaces",
	Long: `PUT /api/v1/admin/instance/keeper/governance. Only the settings you pass
change. For one workspace it saves straight away. For several, or --all, it
first shows every workspace whose settings would be overwritten and asks;
--yes skips the question, --dry-run only shows. The save is all or nothing:
if one workspace refuses the values, none is changed. --all also sets what a
workspace created later starts with.

--contact and --gov-credential belong to one workspace and are refused with
more than one.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		f := cmd.Flags()
		all, _ := f.GetBool("all")
		wsList, _ := f.GetStringSlice("workspace")
		dry, _ := f.GetBool("dry-run")
		if all == (len(wsList) > 0) {
			return cli.WithExitCode(errors.New("choose --workspace <slug>[,<slug>…] or --all"), cli.ExitValidation)
		}
		set, err := governancePatchFromFlags(cmd)
		if err != nil {
			return err
		}
		if len(set) == 0 {
			return cli.WithExitCode(errors.New("nothing to change — pass at least one setting, e.g. --watchdog on"), cli.ExitValidation)
		}
		body := map[string]any{"set": set}
		if all {
			body["all"] = true
		} else {
			body["workspaces"] = wsList
		}

		bulk := all || len(wsList) > 1
		if dry || bulk {
			body["dry_run"] = true
			var preview instanceGovPutResult
			if err := putJSON(client, "/api/v1/admin/instance/keeper/governance", body, &preview); err != nil {
				return err
			}
			if dry {
				return resolvedFormatter(cmd).AutoHuman(preview, func() { printGovChanges(cmd, preview, "would change") })
			}
			printGovChanges(cmd, preview, "will change")
			if preview.Changed == 0 && !preview.DefaultsUpdated {
				cli.PrintSuccess("Nothing to overwrite: every selected workspace already has these settings.")
				return nil
			}
			if err := confirmAction(cmd, fmt.Sprintf("Overwrite the watchdog settings of %d workspace(s)?", preview.Changed)); err != nil {
				return cli.WithExitCode(err, cli.ExitValidation)
			}
			delete(body, "dry_run")
			// Save exactly what was shown: a workspace created or a value
			// changed since the preview makes the server answer 409.
			body["expect_preview"] = preview.PreviewID
		}
		var out instanceGovPutResult
		if err := putJSON(client, "/api/v1/admin/instance/keeper/governance", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			for _, w := range out.Workspaces {
				for _, warn := range w.Warnings {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning (%s): %s\n", w.WorkspaceSlug, warn)
				}
			}
			msg := fmt.Sprintf("Saved in %d workspace(s).", out.Changed)
			if out.DefaultsUpdated {
				msg += " New workspaces will start with these settings."
			}
			cli.PrintSuccess(msg)
		})
	},
}

func printGovChanges(cmd *cobra.Command, r instanceGovPutResult, verb string) {
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "%d workspace(s) %s:\n", r.Changed, verb)
	for _, ws := range r.Workspaces {
		if len(ws.Changes) == 0 {
			fmt.Fprintf(w, "  %-24s no change\n", ws.WorkspaceSlug)
			continue
		}
		parts := make([]string, 0, len(ws.Changes))
		for _, c := range ws.Changes {
			parts = append(parts, fmt.Sprintf("%s %s → %s", c.Field, govValue(c.Field, c.Before), govValue(c.Field, c.After)))
		}
		fmt.Fprintf(w, "  %-24s %s\n", ws.WorkspaceSlug, strings.Join(parts, ", "))
	}
	if r.DefaultsUpdated {
		fmt.Fprintln(w, "  New workspaces will start with these settings.")
	}
}

// govValue prints a changed value the way the settings read: the sampling
// cadence's 0 is "never set", which runs on the built-in default, not "never".
func govValue(field string, v any) string {
	if field == "behavior_sample_every" {
		if n, ok := v.(float64); ok && n == 0 {
			return "default (1 in 5)"
		}
	}
	return fmt.Sprint(v)
}

// governancePatchFromFlags builds the partial update from the flags that were
// passed; an unset flag leaves that setting alone.
func governancePatchFromFlags(cmd *cobra.Command) (map[string]any, error) {
	f := cmd.Flags()
	set := map[string]any{}
	toggle := func(flag, field string) error {
		if !f.Changed(flag) {
			return nil
		}
		v, _ := f.GetString(flag)
		switch strings.ToLower(v) {
		case "on", "true", "yes":
			set[field] = true
		case "off", "false", "no":
			set[field] = false
		default:
			return cli.WithExitCode(fmt.Errorf("--%s wants on or off, not %q", flag, v), cli.ExitValidation)
		}
		return nil
	}
	if err := toggle("watchdog", "enabled"); err != nil {
		return nil, err
	}
	if err := toggle("second-approver", "require_second_approver"); err != nil {
		return nil, err
	}
	if f.Changed("sample-every") {
		v, _ := f.GetInt("sample-every")
		set["behavior_sample_every"] = v
	}
	if f.Changed("deny-alert-risk") {
		v, _ := f.GetInt("deny-alert-risk")
		set["deny_notify_min_risk"] = v
	}
	if f.Changed("lease") {
		d, _ := f.GetDuration("lease")
		set["auto_lease_seconds"] = int(d.Seconds())
	}
	if f.Changed("presets") {
		v, _ := f.GetStringSlice("presets")
		set["watch_presets"] = v
	}
	if f.Changed("gov-provider") {
		v, _ := f.GetString("gov-provider")
		set["gov_model_provider"] = v
	}
	if f.Changed("gov-model") {
		v, _ := f.GetString("gov-model")
		set["gov_model_id"] = v
	}
	if f.Changed("gov-credential") {
		v, _ := f.GetString("gov-credential")
		set["gov_model_credential_id"] = v
	}
	if f.Changed("contact") {
		v, _ := f.GetString("contact")
		set["security_contact_user_id"] = v
	}
	return set, nil
}

type instanceKeeperRequestRow struct {
	ID             string  `json:"id" yaml:"id"`
	WorkspaceID    string  `json:"workspace_id" yaml:"workspace_id"`
	WorkspaceName  string  `json:"workspace_name" yaml:"workspace_name"`
	AgentName      string  `json:"agent_name" yaml:"agent_name"`
	CredentialName string  `json:"credential_name" yaml:"credential_name"`
	RequestType    string  `json:"request_type" yaml:"request_type"`
	Decision       *string `json:"decision" yaml:"decision"`
	RiskScore      *int    `json:"risk_score" yaml:"risk_score"`
	CreatedAt      string  `json:"created_at" yaml:"created_at"`
}

type instanceKeeperRequestPage struct {
	Items  []instanceKeeperRequestRow `json:"items" yaml:"items"`
	Total  int                        `json:"total" yaml:"total"`
	Counts struct {
		Allow    int `json:"allow" yaml:"allow"`
		Deny     int `json:"deny" yaml:"deny"`
		Escalate int `json:"escalate" yaml:"escalate"`
		Pending  int `json:"pending" yaml:"pending"`
	} `json:"counts" yaml:"counts"`
}

var adminInstanceKeeperRequestsCmd = &cobra.Command{
	Use:   "requests",
	Short: "List Keeper decisions across workspaces, newest first",
	Long: `GET /api/v1/admin/instance/keeper/requests. Credential requests and the
background reviews of every workspace, each with its workspace. --workspace
narrows it (comma-separated slugs), --type to one kind (access, execute,
behavior, skill_review, memory_health, negative_learning), --decision to
ALLOW, DENY, ESCALATE or PENDING.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		f := cmd.Flags()
		q := url.Values{}
		if ws, _ := f.GetStringSlice("workspace"); len(ws) > 0 {
			q.Set("workspace", strings.Join(ws, ","))
		}
		if t, _ := f.GetString("type"); t != "" {
			q.Set("request_type", t)
		}
		if d, _ := f.GetString("decision"); d != "" {
			q.Set("decision", strings.ToUpper(d))
		}
		limit, _ := f.GetInt("limit")
		offset, _ := f.GetInt("offset")
		q.Set("limit", fmt.Sprint(limit))
		q.Set("offset", fmt.Sprint(offset))
		var page instanceKeeperRequestPage
		if err := getJSON(client, "/api/v1/admin/instance/keeper/requests?"+q.Encode(), &page); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(page, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tWORKSPACE\tAGENT\tKIND\tSECRET\tDECISION\tRISK")
			for _, r := range page.Items {
				d, risk := "PENDING", "-"
				if r.Decision != nil {
					d = *r.Decision
				}
				if r.RiskScore != nil {
					risk = fmt.Sprint(*r.RiskScore)
				}
				secret := r.CredentialName
				if secret == "" {
					secret = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.CreatedAt, r.WorkspaceName, r.AgentName, r.RequestType, secret, d, risk)
			}
			_ = tw.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "%d of %d · %d allowed · %d denied · %d escalated · %d pending\n",
				len(page.Items), page.Total, page.Counts.Allow, page.Counts.Deny, page.Counts.Escalate, page.Counts.Pending)
		})
	},
}

type instanceKeeperHealthRow struct {
	WorkspaceName    string  `json:"workspace_name" yaml:"workspace_name"`
	WorkspaceSlug    string  `json:"workspace_slug" yaml:"workspace_slug"`
	Samples          int     `json:"samples" yaml:"samples"`
	MinSamples       int     `json:"min_samples" yaml:"min_samples"`
	ProgressedRate   float64 `json:"progressed_rate" yaml:"progressed_rate"`
	JudgeFailureRate float64 `json:"judge_failure_rate" yaml:"judge_failure_rate"`
	P95LatencyMS     int64   `json:"p95_latency_ms" yaml:"p95_latency_ms"`
	Alarm            *struct {
		Kind    string `json:"kind" yaml:"kind"`
		Summary string `json:"summary" yaml:"summary"`
	} `json:"alarm,omitempty" yaml:"alarm,omitempty"`
}

var adminInstanceKeeperHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show every workspace's recent Keeper decision window",
	Long: `GET /api/v1/admin/instance/keeper/health. Per workspace: how many decisions
the window holds, how many let work go on, how often the judge failed, the
p95 latency, and any standing alarm. Below the sample minimum there is too
little to judge, which is not the same as healthy.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out struct {
			Workspaces []instanceKeeperHealthRow `json:"workspaces" yaml:"workspaces"`
		}
		if err := getJSON(client, "/api/v1/admin/instance/keeper/health", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WORKSPACE\tSAMPLES\tPROGRESSED\tJUDGE FAILED\tP95\tALARM")
			for _, h := range out.Workspaces {
				alarm := "-"
				if h.Alarm != nil {
					alarm = h.Alarm.Summary
				} else if h.Samples < h.MinSamples {
					alarm = "too few samples"
				}
				fmt.Fprintf(tw, "%s\t%d\t%.0f%%\t%.0f%%\t%dms\t%s\n", h.WorkspaceSlug, h.Samples,
					h.ProgressedRate*100, h.JudgeFailureRate*100, h.P95LatencyMS, alarm)
			}
			_ = tw.Flush()
		})
	},
}

func init() {
	s := adminInstanceKeeperSetCmd.Flags()
	s.StringSlice("workspace", nil, "Workspace slugs, comma-separated")
	s.Bool("all", false, "Every workspace, and what new workspaces start with")
	s.Bool("dry-run", false, "Show what would change, write nothing")
	s.BoolP("yes", "y", false, "Skip the confirmation for several workspaces")
	s.String("watchdog", "", "on | off")
	s.Int("sample-every", 0, "Review every Nth tool call (1–100)")
	s.Int("deny-alert-risk", 0, "Risk (1–10) from which a DENY also reaches the inbox")
	s.String("second-approver", "", "on | off — four-eyes approval of credential escalations")
	s.Duration("lease", 0, "Approved access lasts this long (0 = standing, min 1m, max 720h)")
	s.StringSlice("presets", nil, "Watch presets, comma-separated")
	s.String("gov-provider", "", "Judge provider: ollama | anthropic | openai_compat, empty = the instance judge")
	s.String("gov-model", "", "Judge model id")
	s.String("gov-credential", "", "Vault credential id for the judge (one workspace only)")
	s.String("contact", "", "Security contact user id (one workspace only)")

	r := adminInstanceKeeperRequestsCmd.Flags()
	r.StringSlice("workspace", nil, "Workspace slugs, comma-separated (default: all)")
	r.String("type", "", "access | execute | behavior | skill_review | memory_health | negative_learning")
	r.String("decision", "", "ALLOW | DENY | ESCALATE | PENDING")
	r.Int("limit", 50, "Rows to return (max 1000)")
	r.Int("offset", 0, "Rows to skip")

	adminInstanceKeeperCmd.AddCommand(adminInstanceKeeperGovernanceCmd, adminInstanceKeeperSetCmd,
		adminInstanceKeeperRequestsCmd, adminInstanceKeeperHealthCmd)
	adminInstanceCmd.AddCommand(adminInstanceKeeperCmd)
}
