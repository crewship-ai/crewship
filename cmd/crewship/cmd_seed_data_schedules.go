package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
)

// seedSchedules — Phase 9b. Wires demo cron schedules onto existing
// pipelines so a fresh workspace immediately has cron-driven runs
// flowing into /activity.
//
// Why a separate phase instead of embedding the schedule in the
// routine seed: schedules belong to the pipeline_schedules table,
// not the pipeline DSL. Keeping them on a separate seed step also
// lets us extend the demo set later (e.g. one cron per crew) without
// touching routines.go.
//
// Why every 10 minutes: lower-frequency cron mirrors what real
// workspaces look like — minute-cadence schedules drown the rail
// with noise during demos and burn LLM/tool budgets if any of the
// agents on the path call out to a real model.

type demoSchedule struct {
	Name       string
	TargetSlug string
	CronExpr   string
	Inputs     map[string]interface{}
	Enabled    bool
}

var demoSchedules = []demoSchedule{
	{
		// Light, recurring activity on the rail — a deterministic recipe.
		Name:       "Demo: classify ticket (every 30m)",
		TargetSlug: "classify-ticket",
		CronExpr:   "*/30 * * * *",
		Enabled:    true,
	},
	{
		// Daily generative digest — the canonical scheduled-summary use case.
		Name:       "Demo: daily status digest (09:00)",
		TargetSlug: "daily-status-digest",
		CronExpr:   "0 9 * * *",
		Enabled:    true,
	},
	{
		// Consistency harness — fans out across the core deterministic
		// recipes every 6h so cross-tier drift shows up on the rail.
		Name:       "Demo: consistency sweep (every 6h)",
		TargetSlug: "consistency-sweep",
		CronExpr:   "0 */6 * * *",
		Enabled:    true,
	},
}

func seedSchedules(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return fmt.Errorf("seedSchedules: workspace_id not set on client")
	}
	fmt.Fprintln(os.Stderr, "Creating demo schedules...")

	endpoint := fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", wsID)

	// Idempotency: the create endpoint does NOT 409 on a duplicate name, so a
	// re-seed without --nuke would otherwise stack a second (third, …) copy of
	// every demo schedule. Pre-fetch existing schedules and skip any whose name
	// already exists. Keyed by name because that's what's stable across re-seeds
	// (the target slug can repeat across demo schedules).
	existingByName := map[string]bool{}
	if r, err := client.Get(endpoint); err == nil {
		var existing []struct {
			Name string `json:"name"`
		}
		if cli.ReadJSON(r, &existing) == nil {
			for _, e := range existing {
				existingByName[e.Name] = true
			}
		}
	}

	created := 0
	for _, s := range demoSchedules {
		if err := ctx.Err(); err != nil {
			return err
		}
		if existingByName[s.Name] {
			fmt.Fprintf(os.Stderr, "  = Schedule exists: %s\n", s.Name)
			continue
		}
		body := map[string]interface{}{
			"name":                 s.Name,
			"target_pipeline_slug": s.TargetSlug,
			"cron_expr":            s.CronExpr,
			"enabled":              s.Enabled,
		}
		if len(s.Inputs) > 0 {
			body["inputs"] = s.Inputs
		}
		r, err := client.Post(endpoint, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Schedule %s: %v\n", s.TargetSlug, err)
			continue
		}
		// 404 = target pipeline missing (e.g. routine seed skipped or
		// failed); 409 = duplicate (idempotent re-seed). Surface both
		// without aborting the whole seed phase.
		if r.StatusCode == http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "  ! Schedule %s: target pipeline not found — skipping\n", s.TargetSlug)
			r.Body.Close()
			continue
		}
		if r.StatusCode == http.StatusConflict {
			fmt.Fprintf(os.Stderr, "  = Schedule %s: already exists\n", s.TargetSlug)
			r.Body.Close()
			continue
		}
		if r.StatusCode >= 400 {
			fmt.Fprintf(os.Stderr, "  ! Schedule %s: HTTP %d\n", s.TargetSlug, r.StatusCode)
			r.Body.Close()
			continue
		}
		r.Body.Close()
		fmt.Fprintf(os.Stderr, "  + Schedule: %s (%s)\n", s.Name, s.CronExpr)
		created++
	}
	fmt.Fprintf(os.Stderr, "  Created %d/%d demo schedule(s)\n", created, len(demoSchedules))
	return nil
}
