package main

// Routine seeding — populates a fresh workspace with 5 starter
// routines so the /routines page isn't empty on first boot. Mirrors
// seedIssues' pattern: independent function, takes the crew/agent ID
// maps from earlier phases, idempotent (409 conflict = skip).
//
// Routines are saved via the workspace-scoped /pipelines/save
// endpoint (added in this PR alongside the UI authoring flow). The
// admin user the seeder runs as has OWNER role, so we set
// skip_test_gate=true — there's no live LLM available during seed
// and the definitions are hand-curated, so the gate would just block
// us. Real users authoring through the UI still hit the gate.

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

func seedRoutines(ctx context.Context, client *cli.Client, crewIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return fmt.Errorf("seedRoutines: workspace_id not set on client")
	}

	fmt.Fprintln(os.Stderr, "Creating routines...")
	for _, r := range seeddata.Routines {
		if err := ctx.Err(); err != nil {
			return err
		}
		crewID, ok := crewIDs[r.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! Routine %s: skipped (crew %q not seeded)\n", r.Slug, r.CrewSlug)
			continue
		}
		body := map[string]interface{}{
			"slug":                 r.Slug,
			"name":                 r.Name,
			"description":          r.Description,
			"definition":           r.Definition,
			"author_crew_id":       crewID,
			"skip_test_gate":       true, // OWNER can skip; seed has no live LLM
			"last_test_run_passed": true,
		}
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/save", wsID)
		resp, err := client.Post(path, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Routine %s: %v\n", r.Slug, err)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusCreated:
			fmt.Fprintf(os.Stderr, "  + Routine: %s (crew=%s)\n", r.Slug, r.CrewSlug)
		case resp.StatusCode == http.StatusConflict:
			fmt.Fprintf(os.Stderr, "  = Routine exists: %s\n", r.Slug)
		default:
			// 5xx / 422 — surface the body so a misshapen DSL is
			// debuggable. We deliberately don't fail-fast: a
			// single broken routine shouldn't block the rest of
			// the seed, mirroring how seedIssues tolerates
			// individual failures.
			fmt.Fprintf(os.Stderr, "  ! Routine %s: HTTP %d\n", r.Slug, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	return nil
}
