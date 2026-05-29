package main

// Phase 0: tear down every workspace-scoped row before re-seeding.
// Extracted from cmd_seed_data.go for readability — independent of the
// per-entity seeders.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// nukeDecision is the pure confirmation policy for `seed --nuke`, extracted so
// the branch matrix is unit-testable without a real TTY/stdin. The wipe deletes
// ALL workspace contents, so the gate is strict:
//   - yes=true                  → proceed (CI / scripted resets)
//   - not interactive, no --yes → refuse (never wipe unattended)
//   - interactive               → typed input must equal the workspace slug,
//     and the slug must be known (non-empty)
func nukeDecision(yes, interactive bool, typed, wsSlug string) error {
	if yes {
		return nil
	}
	if !interactive {
		return errors.New("refusing to nuke in a non-interactive session without --yes")
	}
	if wsSlug == "" || strings.TrimSpace(typed) != wsSlug {
		return errors.New("aborted: workspace slug did not match")
	}
	return nil
}

// confirmNuke prints a blast-radius summary (what / where / how much) and gates
// the wipe behind a typed-slug confirmation. --yes bypasses the prompt for CI.
// Counts are best-effort — a failed lookup shows 0 but never weakens the gate.
func confirmNuke(cmd *cobra.Command, client *cli.Client, server string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	wsName, wsSlug := nukeWorkspaceIdentity(client)
	crews := nukeCount(client, "/api/v1/crews")
	agents := nukeCount(client, "/api/v1/agents")

	fmt.Fprintf(os.Stderr, "\n⚠️  NUKE permanently deletes ALL contents of workspace %q (%s)\n", wsName, wsSlug)
	fmt.Fprintf(os.Stderr, "    on %s — %d crew(s), %d agent(s), plus every issue, project,\n", server, crews, agents)
	fmt.Fprintln(os.Stderr, "    label, pipeline, schedule, webhook, and credential. This cannot be undone.")

	if yes {
		fmt.Fprintln(os.Stderr, "    --yes set; proceeding without prompt.")
		return nukeDecision(true, false, "", wsSlug)
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	if !interactive {
		return nukeDecision(false, false, "", wsSlug)
	}
	fmt.Fprintf(os.Stderr, "\nType the workspace slug %q to confirm the wipe: ", wsSlug)
	typed, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return nukeDecision(false, true, typed, wsSlug)
}

// nukeWorkspaceIdentity resolves the active workspace's (name, slug) for the
// confirmation summary. Returns ("the active workspace", "") when it can't be
// determined — nukeDecision then refuses an interactive confirm (empty slug).
//
// Fail-closed by design: we MUST NOT fall back to the first workspace in the
// list. The actual wipe is wsCtx-bound to client.WorkspaceID server-side; if
// we showed the operator a different workspace's slug to type, they'd confirm
// against the wrong identity and still wipe the active one. An empty slug
// forces the user to pass --yes explicitly (CI/scripts), which is the safer
// degradation than a misleading prompt.
func nukeWorkspaceIdentity(client *cli.Client) (name, slug string) {
	resp, err := client.Get("/api/v1/workspaces")
	if err != nil {
		return "the active workspace", ""
	}
	var wss []workspaceSummary
	if err := cli.ReadJSON(resp, &wss); err != nil || len(wss) == 0 {
		return "the active workspace", ""
	}
	n, s := findActiveWorkspace(wss, client.WorkspaceID)
	if s == "" {
		return "the active workspace", ""
	}
	return n, s
}

// workspaceSummary is the subset of the /workspaces list shape that the nuke
// confirmation gate needs. Named so findActiveWorkspace is unit-testable.
type workspaceSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// findActiveWorkspace returns the (name, slug) of the workspace whose id matches
// activeID, or ("", "") if no workspace matches. Fail-closed by design: a
// no-match must NOT fall back to wss[0], because the wipe is wsCtx-bound to
// activeID server-side — showing the operator a different workspace's slug to
// type would let them confirm under a false identity and still wipe the active
// one. The empty-slug return forces nukeDecision to refuse unless --yes is set.
func findActiveWorkspace(wss []workspaceSummary, activeID string) (name, slug string) {
	for _, w := range wss {
		if w.ID == activeID {
			return w.Name, w.Slug
		}
	}
	return "", ""
}

// nukeCount returns len() of a list endpoint, best-effort (0 on any error).
func nukeCount(client *cli.Client, path string) int {
	resp, err := client.Get(path)
	if err != nil {
		return 0
	}
	var items []json.RawMessage
	if err := cli.ReadJSON(resp, &items); err != nil {
		return 0
	}
	return len(items)
}

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
