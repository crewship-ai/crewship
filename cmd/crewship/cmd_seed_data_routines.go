package main

// Routine seeding — populates a fresh workspace with 5 starter
// routines so the /routines page isn't empty on first boot. Mirrors
// seedIssues' pattern: independent function, takes the crew/agent ID
// maps from earlier phases, idempotent (409 conflict = skip).
//
// Routines are saved via the workspace-scoped /pipelines/save
// endpoint. The admin user the seeder runs as has OWNER role, so we set
// both OWNER/ADMIN-only escape hatches:
//   - skip_test_gate=true — there's no live LLM during seed and the
//     definitions are hand-curated, so the test gate would just block us.
//   - skip_governance_gate=true — the maker-checker risk gate would
//     otherwise land every routine with an http/code step or
//     credentials_required as 'proposed' (awaiting approval), leaving a
//     freshly-seeded workspace full of un-runnable routines.
// Real users authoring through the UI still hit both gates.

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
	starterStats, err := seedRoutineSlice(ctx, client, wsID, crewIDs, "Routine", seeddata.Routines)
	if err != nil {
		return err
	}

	// Eval scenarios are seeded as a separate batch so their failure
	// surface is reported independently — a regression in the eval
	// suite shouldn't be misread as a starter-routine regression and
	// vice versa. Both batches use the same /pipelines/save endpoint
	// and the same per-routine error handling; only the log prefix
	// differs.
	fmt.Fprintln(os.Stderr, "Creating eval scenarios...")
	evalStats, err := seedRoutineSlice(ctx, client, wsID, crewIDs, "Eval", seeddata.EvalScenarios)
	if err != nil {
		return err
	}

	_ = starterStats
	_ = evalStats
	return nil
}

// seedRoutineSlice POSTs each routine in the slice to /pipelines/save and
// reports per-batch totals. Returns an error only when every eligible
// routine in the batch failed — same regression-surface heuristic the
// original seedRoutines used, scoped per-batch so a starter-routine
// regression doesn't mask an eval-scenario one (or vice versa).
//
// `kind` is a short label used in log lines ("Routine", "Eval"); it
// has no behavioural impact and exists purely to make `crewship seed`
// output disambiguate the two batches at a glance.
type routineBatchStats struct {
	eligible int
	ok       int
	conflict int
	failed   int
}

func seedRoutineSlice(ctx context.Context, client *cli.Client, wsID string, crewIDs map[string]string, kind string, defs []seeddata.RoutineDef) (routineBatchStats, error) {
	var stats routineBatchStats
	for _, r := range defs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		crewID, exists := crewIDs[r.CrewSlug]
		if !exists {
			fmt.Fprintf(os.Stderr, "  ! %s %s: skipped (crew %q not seeded)\n", kind, r.Slug, r.CrewSlug)
			continue
		}
		stats.eligible++
		body := map[string]interface{}{
			"slug":                 r.Slug,
			"name":                 r.Name,
			"description":          r.Description,
			"definition":           r.Definition,
			"author_crew_id":       crewID,
			"skip_test_gate":       true, // OWNER can skip; seed has no live LLM
			"last_test_run_passed": true,
			// skip_governance_gate: the seeder runs as OWNER and these
			// definitions are hand-curated and trusted. Without this the
			// maker-checker risk gate (http/code steps, credentials_required,
			// unmet integrations) lands most starter routines as 'proposed' —
			// so a freshly-seeded workspace is full of un-runnable "awaiting
			// approval" routines whose scheduled runs then fail the
			// active-status gate. Force them live. Real users authoring through
			// the UI still hit the gate.
			"skip_governance_gate": true,
		}
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/save", wsID)
		resp, err := client.Post(path, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s %s: %v\n", kind, r.Slug, err)
			stats.failed++
			continue
		}
		switch {
		case resp.StatusCode == http.StatusCreated:
			fmt.Fprintf(os.Stderr, "  + %s: %s (crew=%s)\n", kind, r.Slug, r.CrewSlug)
			stats.ok++
		case resp.StatusCode == http.StatusConflict:
			fmt.Fprintf(os.Stderr, "  = %s exists: %s\n", kind, r.Slug)
			stats.conflict++
		default:
			// 5xx / 422 — surface the status so a misshapen DSL is
			// debuggable. Individual failures are tolerated (mirrors
			// seedIssues), but if EVERY eligible routine in the
			// batch fails we likely hit a server-side regression
			// and should surface that to the operator.
			fmt.Fprintf(os.Stderr, "  ! %s %s: HTTP %d\n", kind, r.Slug, resp.StatusCode)
			stats.failed++
		}
		_ = resp.Body.Close()
	}
	if stats.eligible > 0 && stats.ok == 0 && stats.conflict == 0 && stats.failed == stats.eligible {
		return stats, fmt.Errorf("seedRoutineSlice[%s]: all %d eligible routines failed (likely server-side regression)", kind, stats.failed)
	}
	return stats, nil
}
