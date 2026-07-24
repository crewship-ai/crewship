package main

// `crewship digest enable` — a convenience wrapper around the
// workspace-digest routine template (#1422 item 4): a token-zero ops
// digest (run counts, cost, top failures over a trailing window) that
// posts to the workspace inbox and fans out to Slack/email/etc via the
// existing #1412 notification-preference matrix.
//
// The routine composes three deterministic pipeline primitives —
// query(pipeline_runs) -> transform(extract summary_md) -> notify — and
// is already seeded into every fresh workspace (seeddata.Routines). This
// command exists for a workspace that predates the template, or that
// nuked its seed data: it creates the routine from the same definition
// the seeder uses (seeddata.WorkspaceDigestDefinition — single source of
// truth) if missing, then wires a schedule if one doesn't already exist.
// Both steps are idempotent: re-running `digest enable` is a no-op once
// the routine and a schedule both exist.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

// digestRoutineSlug is fixed — the workspace-digest routine template is
// singular per workspace, unlike ordinary routines which are user-named.
const digestRoutineSlug = "workspace-digest"

// defaultDigestCron fires once a day — enough cadence for an ops digest
// without spamming the inbox. Override with --cron/--when.
const defaultDigestCron = "0 8 * * *"

var digestCmd = &cobra.Command{
	Use:   "digest",
	Short: "Workspace digest routine — a token-zero ops summary of runs/costs/failures",
	Long: `The workspace-digest routine (query pipeline_runs -> transform -> notify,
zero LLM spend) is seeded into every fresh workspace. 'digest enable'
creates it if missing (for a workspace that predates the template) and
wires a daily schedule if one doesn't already exist. Both steps are
idempotent.`,
}

var digestEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Ensure the workspace-digest routine exists and is scheduled",
	Long: `Ensures the workspace-digest routine exists in this workspace (creating it
from the built-in template if missing — requires --crew the first time)
and that a schedule fires it. Composes the existing #1412 notification
system (notify step -> inbox -> your notification channel preferences)
with the query/transform/notify pipeline primitives; run 'crewship notify
prefs' / 'crewship notifychannel' separately to route the digest to
Slack/email.

Examples:
  crewship digest enable --crew ops
  crewship digest enable --crew ops --cron "0 9 * * 1-5"   # weekdays 9am
  crewship digest enable --crew ops --when "every day at 9am"
`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		crewSlug, _ := cmd.Flags().GetString("crew")
		cronExpr, _ := cmd.Flags().GetString("cron")
		when, _ := cmd.Flags().GetString("when")
		yes, _ := cmd.Flags().GetBool("yes")
		if cronExpr != "" && when != "" {
			return fmt.Errorf("--cron and --when are mutually exclusive — pass one or the other")
		}

		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()

		result := digestEnableResult{}

		exists, err := digestRoutineExists(client, ws)
		if err != nil {
			return err
		}
		if !exists {
			if crewSlug == "" {
				return fmt.Errorf("workspace-digest routine does not exist yet — --crew <slug> is required to create it (the crew that will own it)")
			}
			if err := createDigestRoutine(client, ws, crewSlug); err != nil {
				return fmt.Errorf("create workspace-digest routine: %w", err)
			}
			result.RoutineCreated = true
			fmt.Println("Created routine: workspace-digest")
		} else {
			fmt.Println("Routine workspace-digest already exists.")
		}

		if when != "" {
			derived, perr := pipeline.ParseNaturalCron(when)
			if perr != nil {
				return perr
			}
			cronExpr = derived
			occs, oerr := pipeline.NextOccurrences(cronExpr, "UTC", 3, time.Now())
			fmt.Printf("Parsed %q as cron %q (UTC).\n", when, cronExpr)
			if oerr == nil {
				fmt.Println("Next 3 fire times:")
				for _, o := range occs {
					fmt.Printf("  - %s\n", o.Format("2006-01-02 15:04 MST"))
				}
			}
			if !yes {
				fmt.Print("Schedule the digest with this cadence? [y/N]: ")
				var input string
				_, _ = fmt.Scanln(&input)
				if strings.ToLower(strings.TrimSpace(input)) != "y" && strings.ToLower(strings.TrimSpace(input)) != "yes" {
					return fmt.Errorf("aborted")
				}
			}
		}
		if cronExpr == "" {
			cronExpr = defaultDigestCron
		}

		scheduleID, alreadyScheduled, err := ensureDigestSchedule(client, ws, cronExpr)
		if err != nil {
			return fmt.Errorf("ensure schedule: %w", err)
		}
		result.CronExpr = cronExpr
		result.ScheduleID = scheduleID
		if alreadyScheduled {
			fmt.Printf("A schedule already targets workspace-digest (id=%s) — leaving its cadence as-is. Use `crewship routine schedules update %s --cron '...'` to change it.\n", scheduleID, scheduleID)
		} else {
			result.ScheduleCreated = true
			fmt.Printf("Scheduled workspace-digest: %s UTC (id=%s)\n", cronExpr, scheduleID)
		}
		fmt.Println("Configure delivery to Slack/email: `crewship notifychannel add ...` + `crewship notify prefs set`.")
		return resolvedFormatter(cmd).AutoHuman(result, func() {})
	},
}

// digestEnableResult is the machine-readable summary for `digest enable`
// (--format json/yaml/ndjson).
type digestEnableResult struct {
	RoutineCreated  bool   `json:"routine_created"`
	ScheduleCreated bool   `json:"schedule_created"`
	ScheduleID      string `json:"schedule_id,omitempty"`
	CronExpr        string `json:"cron_expr,omitempty"`
}

// digestRoutineExists checks GET /pipelines/workspace-digest.
func digestRoutineExists(client *cli.Client, ws string) (bool, error) {
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", ws, digestRoutineSlug))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err := cli.CheckError(resp); err != nil {
		return false, err
	}
	return true, nil
}

// createDigestRoutine saves seeddata.WorkspaceDigestDefinition into the
// workspace, mirroring `routine save`'s test_run -> save_token -> save
// flow (see cmd_pipeline.go) rather than any privileged skip-gate path —
// the digest template is deterministic and agentless, so it passes the
// real server-side test-run gate like any other routine save.
func createDigestRoutine(client *cli.Client, ws, crewSlug string) error {
	crewID, err := resolveCrewID(client, crewSlug)
	if err != nil {
		return fmt.Errorf("resolve --crew: %w", err)
	}
	definitionRaw, err := json.Marshal(seeddata.WorkspaceDigestDefinition)
	if err != nil {
		return fmt.Errorf("marshal digest definition: %w", err)
	}

	fmt.Println("Validating workspace-digest routine (server-side dry-run)...")
	testResp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/test_run", ws), map[string]any{
		"definition":     json.RawMessage(definitionRaw),
		"author_crew_id": crewID,
	})
	if err != nil {
		return err
	}
	defer testResp.Body.Close()
	if err := cli.CheckError(testResp); err != nil {
		return err
	}
	var testResult struct {
		Status    string `json:"status"`
		SaveToken string `json:"save_token"`
		Error     string `json:"error_message"`
	}
	if err := json.NewDecoder(testResp.Body).Decode(&testResult); err != nil {
		return fmt.Errorf("decode test_run response: %w", err)
	}
	if testResult.Status != "DRY_RUN_OK" && testResult.Status != "COMPLETED" {
		return fmt.Errorf("routine failed validation (status=%s): %s", testResult.Status, testResult.Error)
	}

	saveBody := map[string]any{
		"slug":           digestRoutineSlug,
		"name":           "Workspace digest",
		"description":    "Token-zero ops digest: run counts, cost, and top failures over a trailing window.",
		"definition":     json.RawMessage(definitionRaw),
		"author_crew_id": crewID,
	}
	if testResult.SaveToken != "" {
		saveBody["save_token"] = testResult.SaveToken
	}
	saveResp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/save", ws), saveBody)
	if err != nil {
		return err
	}
	defer saveResp.Body.Close()
	return cli.CheckError(saveResp)
}

// ensureDigestSchedule returns (scheduleID, alreadyExisted, error). It
// lists schedules and looks for one already targeting workspace-digest
// before creating a new one, so `digest enable` run twice doesn't stack
// duplicate schedules.
func ensureDigestSchedule(client *cli.Client, ws, cronExpr string) (string, bool, error) {
	listResp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", ws))
	if err != nil {
		return "", false, err
	}
	defer listResp.Body.Close()
	if err := cli.CheckError(listResp); err != nil {
		return "", false, err
	}
	var rows []scheduleRow
	if err := json.NewDecoder(listResp.Body).Decode(&rows); err != nil {
		return "", false, fmt.Errorf("decode schedules: %w", err)
	}
	for _, r := range rows {
		if r.TargetPipelineSlug == digestRoutineSlug {
			return r.ID, true, nil
		}
	}

	createResp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", ws), map[string]any{
		"name":                 "workspace-digest schedule",
		"target_pipeline_slug": digestRoutineSlug,
		"cron_expr":            cronExpr,
		"timezone":             "UTC",
		"enabled":              true,
	})
	if err != nil {
		return "", false, err
	}
	defer createResp.Body.Close()
	if err := cli.CheckError(createResp); err != nil {
		return "", false, err
	}
	var created scheduleRow
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		return "", false, fmt.Errorf("decode created schedule: %w", err)
	}
	return created.ID, false, nil
}

func init() {
	digestEnableCmd.Flags().String("crew", "", "crew slug/id that will own the routine if it doesn't exist yet (required only the first time)")
	digestEnableCmd.Flags().String("cron", "", "cron expression for the digest schedule (default: '"+defaultDigestCron+"' — daily at 08:00 UTC)")
	digestEnableCmd.Flags().String("when", "", `natural-language schedule phrase (e.g. "every day at 9am") — same parser as 'routine schedules create --when'; mutually exclusive with --cron`)
	digestEnableCmd.Flags().Bool("yes", false, "skip the --when confirmation prompt")

	digestCmd.AddCommand(digestEnableCmd)
	rootCmd.AddCommand(digestCmd)
}
