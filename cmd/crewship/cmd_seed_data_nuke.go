package main

// Phase 0: tear down every workspace-scoped row before re-seeding.
// Extracted from cmd_seed_data.go for readability — independent of the
// per-entity seeders.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

func seedNuke(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Nuking workspace contents...")

	var failures []string

	// Delete issues — paginate through the full result set. A single
	// limit=500 request would leave any issue past the first page behind
	// and block later project/crew deletion.
	const pageLimit = 500
	totalDeleted := 0
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/issues?limit=%d&offset=%d", pageLimit, offset))
		if err != nil {
			failures = append(failures, fmt.Sprintf("list issues (offset=%d): %v", offset, err))
			break
		}
		var issues []issueItem
		if err := cli.ReadJSON(resp, &issues); err != nil {
			failures = append(failures, fmt.Sprintf("decode issues (offset=%d): %v", offset, err))
			break
		}
		if len(issues) == 0 {
			break
		}
		deletedOnPage := 0
		for _, iss := range issues {
			if err := ctx.Err(); err != nil {
				return err
			}
			if iss.Identifier == nil {
				continue
			}
			// Transition through a valid status path from the issue's CURRENT
			// state to CANCELLED (only BACKLOG/CANCELLED can be deleted).
			// Using StatusPath("CANCELLED") would always start from BACKLOG,
			// which for an issue already in TODO/IN_PROGRESS/DONE would emit
			// a backward transition the server rejects (e.g. IN_PROGRESS→TODO
			// on its way to BACKLOG→CANCELLED), leaving the issue non-deletable.
			if iss.Status != "BACKLOG" && iss.Status != "CANCELLED" {
				path := seeddata.StatusPathFrom(iss.Status, "CANCELLED")
				if path == nil {
					failures = append(failures, fmt.Sprintf("no transition path %s→CANCELLED for %s", iss.Status, *iss.Identifier))
					fmt.Fprintf(os.Stderr, "  ! nuke: no transition path %s→CANCELLED for %s\n", iss.Status, *iss.Identifier)
					continue
				}
				for _, status := range path {
					r, err := client.Patch(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier), map[string]string{"status": status})
					if err != nil {
						failures = append(failures, fmt.Sprintf("transition %s→%s: %v", *iss.Identifier, status, err))
						fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: %v\n", *iss.Identifier, status, err)
						break
					}
					if r.StatusCode >= 300 {
						failures = append(failures, fmt.Sprintf("transition %s→%s: HTTP %d", *iss.Identifier, status, r.StatusCode))
						fmt.Fprintf(os.Stderr, "  ! nuke transition %s→%s: HTTP %d\n", *iss.Identifier, status, r.StatusCode)
						r.Body.Close()
						break
					}
					r.Body.Close()
				}
			}
			r, err := client.Delete(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier))
			if err != nil {
				failures = append(failures, fmt.Sprintf("delete issue %s: %v", *iss.Identifier, err))
				fmt.Fprintf(os.Stderr, "  ! delete issue %s: %v\n", *iss.Identifier, err)
				continue
			}
			if r.StatusCode >= 300 {
				failures = append(failures, fmt.Sprintf("delete issue %s: HTTP %d", *iss.Identifier, r.StatusCode))
				fmt.Fprintf(os.Stderr, "  ! delete issue %s: HTTP %d\n", *iss.Identifier, r.StatusCode)
				r.Body.Close()
				continue
			}
			r.Body.Close()
			totalDeleted++
			deletedOnPage++
		}
		// End conditions:
		// - Partial page (fewer than pageLimit rows) → nothing left to scan.
		// - Full page but zero deletions → every row is undeletable; advance
		//   offset past them so we don't re-fetch the same 500 rows forever.
		// - Full page with deletions → the rows we removed shifted the
		//   result set, so the next page starts at the same offset (0).
		if len(issues) < pageLimit {
			break
		}
		if deletedOnPage == 0 {
			fmt.Fprintf(os.Stderr, "  ! nuke: page at offset=%d had no deletable issues, advancing\n", offset)
			offset += pageLimit
		}
	}
	fmt.Fprintf(os.Stderr, "  Deleted %d issues\n", totalDeleted)

	// Delete projects
	if err := nukeList(ctx, client, "/api/v1/projects", "/api/v1/projects/"); err != nil {
		failures = append(failures, fmt.Sprintf("projects: %v", err))
	}

	// Delete labels
	if err := nukeList(ctx, client, "/api/v1/labels", "/api/v1/labels/"); err != nil {
		failures = append(failures, fmt.Sprintf("labels: %v", err))
	}

	// Delete agents (this also removes bindings, credential assignments, skill assignments)
	if err := nukeList(ctx, client, "/api/v1/agents", "/api/v1/agents/"); err != nil {
		failures = append(failures, fmt.Sprintf("agents: %v", err))
	}

	// Delete credentials
	if err := nukeList(ctx, client, "/api/v1/credentials", "/api/v1/credentials/"); err != nil {
		failures = append(failures, fmt.Sprintf("credentials: %v", err))
	}

	// Delete integrations
	if err := nukeCrewIntegrations(ctx, client); err != nil {
		failures = append(failures, fmt.Sprintf("integrations: %v", err))
	}

	// Delete routine triggers + routines. Order matters: webhooks and
	// schedules FK back to the pipeline row, so they go first.
	// The routine endpoints are workspace-scoped (no implicit-ws
	// variant), so we compose the URLs from the client's resolved ID.
	ws := client.GetWorkspaceID()
	if ws != "" {
		if err := nukeList(ctx, client,
			"/api/v1/workspaces/"+ws+"/pipeline-webhooks",
			"/api/v1/workspaces/"+ws+"/pipeline-webhooks/",
		); err != nil {
			failures = append(failures, fmt.Sprintf("pipeline-webhooks: %v", err))
		}
		if err := nukeList(ctx, client,
			"/api/v1/workspaces/"+ws+"/pipeline-schedules",
			"/api/v1/workspaces/"+ws+"/pipeline-schedules/",
		); err != nil {
			failures = append(failures, fmt.Sprintf("pipeline-schedules: %v", err))
		}
		// Pipelines key by slug, not ID — use nukeListBySlug.
		if err := nukeListBySlug(ctx, client,
			"/api/v1/workspaces/"+ws+"/pipelines",
			"/api/v1/workspaces/"+ws+"/pipelines/",
		); err != nil {
			failures = append(failures, fmt.Sprintf("pipelines: %v", err))
		}
	}

	// Delete crews
	if err := nukeList(ctx, client, "/api/v1/crews", "/api/v1/crews/"); err != nil {
		failures = append(failures, fmt.Sprintf("crews: %v", err))
	}

	if len(failures) > 0 {
		return fmt.Errorf("workspace cleanup had %d failures: %s", len(failures), strings.Join(failures, "; "))
	}

	cli.PrintSuccess("Workspace contents cleaned")
	return nil
}

func nukeList(ctx context.Context, client *cli.Client, listPath, deletePrefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := client.Get(listPath)
	if err != nil {
		return fmt.Errorf("GET %s: %w", listPath, err)
	}
	var items []struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return fmt.Errorf("decode %s: %w", listPath, err)
	}
	var failures []string
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := client.Delete(deletePrefix + item.ID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("DELETE %s%s: %v", deletePrefix, item.ID, err))
			continue
		}
		if r.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("DELETE %s%s: HTTP %d", deletePrefix, item.ID, r.StatusCode))
		}
		r.Body.Close()
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d delete failures: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// nukeListBySlug is nukeList for endpoints that key on slug rather
// than id (pipelines).
func nukeListBySlug(ctx context.Context, client *cli.Client, listPath, deletePrefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := client.Get(listPath)
	if err != nil {
		return fmt.Errorf("GET %s: %w", listPath, err)
	}
	var items []struct {
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return fmt.Errorf("decode %s: %w", listPath, err)
	}
	var failures []string
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Slug == "" {
			// A row with no slug can't be addressed by the slug-based
			// delete path, so it would survive the nuke. Surface it as a
			// failure instead of silently leaving it behind.
			failures = append(failures, fmt.Sprintf("DELETE %s<empty-slug>: row has no slug — remove it manually", deletePrefix))
			continue
		}
		r, err := client.Delete(deletePrefix + item.Slug)
		if err != nil {
			failures = append(failures, fmt.Sprintf("DELETE %s%s: %v", deletePrefix, item.Slug, err))
			continue
		}
		if r.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("DELETE %s%s: HTTP %d", deletePrefix, item.Slug, r.StatusCode))
		}
		r.Body.Close()
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d delete failures: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func nukeCrewIntegrations(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := client.Get("/api/v1/integrations/crews")
	if err != nil {
		return fmt.Errorf("GET /api/v1/integrations/crews: %w", err)
	}
	var items []struct {
		ID     string `json:"id"`
		CrewID string `json:"crew_id"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return fmt.Errorf("decode integrations: %w", err)
	}
	var failures []string
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := client.Delete(fmt.Sprintf("/api/v1/crews/%s/integrations/%s", item.CrewID, item.ID))
		if err != nil {
			failures = append(failures, fmt.Sprintf("DELETE crew %s integration %s: %v", item.CrewID, item.ID, err))
			continue
		}
		if r.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("DELETE crew %s integration %s: HTTP %d", item.CrewID, item.ID, r.StatusCode))
		}
		r.Body.Close()
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d delete failures: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 2: Crews
// ════════════════════════════════════════════════════════════════════════════
