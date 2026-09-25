package main

// Demo-issue seeding extracted from cmd_seed_data.go. Independent of
// integrations and the per-entity seeders — runs last to populate
// realistic mission/issue data once crews + agents exist.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

func seedIssues(ctx context.Context, client *cli.Client, crewIDs, agentIDs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Create labels — keeping their IDs, because an issue names labels by
	// catalogue name and the bulk endpoint attaches them by ID.
	fmt.Fprintln(os.Stderr, "Creating labels...")
	labelIDs := map[string]string{} // name → id
	for _, l := range seeddata.Labels {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/labels", l)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Label %s: %v\n", l.Name, err)
			continue
		}
		if resp.StatusCode < 400 {
			var created struct {
				ID string `json:"id" yaml:"id"`
			}
			if cli.ReadJSON(resp, &created) == nil && created.ID != "" {
				labelIDs[l.Name] = created.ID
			}
			fmt.Fprintf(os.Stderr, "  + Label: %s\n", l.Name)
			continue
		}
		resp.Body.Close()
		// A re-seed: the label exists. Its ID still matters for attachment.
		if id, rerr := resolveByName(client, "/api/v1/labels", l.Name); rerr == nil && id != "" {
			labelIDs[l.Name] = id
		}
	}

	// Create projects
	fmt.Fprintln(os.Stderr, "Creating projects...")
	projectIDs := map[string]string{} // name → id
	for _, p := range seeddata.Projects {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/projects", map[string]interface{}{
			"name":     p.Name,
			"color":    p.Color,
			"icon":     p.Icon,
			"status":   p.Status,
			"priority": p.Priority,
		})
		if err != nil {
			return fmt.Errorf("project %s: %w", p.Name, err)
		}
		// 409 Conflict → resolve existing.
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			existingID, err := resolveByName(client, "/api/v1/projects", p.Name)
			if err == nil && existingID != "" {
				projectIDs[p.Name] = existingID
				fmt.Fprintf(os.Stderr, "  = Project exists: %s\n", p.Name)
			} else {
				return fmt.Errorf("project %s: conflict but existing record could not be resolved", p.Name)
			}
			continue
		}
		// Any other non-2xx is a real failure.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("project %s: HTTP %d: %s", p.Name, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var created struct {
			ID string `json:"id" yaml:"id"`
		}
		if cli.ReadJSON(resp, &created) == nil {
			projectIDs[p.Name] = created.ID
			fmt.Fprintf(os.Stderr, "  + Project: %s\n", p.Name)
		}
	}

	// Create issues — track identifiers and crew IDs for relations.
	// Keyed by stable seed key (def.Title) so relations don't break when
	// individual creations fail and shift positional indexes.
	fmt.Fprintln(os.Stderr, "Creating issues...")
	type createdIssue struct {
		Identifier string
		CrewID     string
	}
	issueByKey := map[string]createdIssue{}

	for _, def := range seeddata.Issues {
		if err := ctx.Err(); err != nil {
			return err
		}
		crewID, ok := crewIDs[def.CrewSlug]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ! Crew %q not found, skipping: %s\n", def.CrewSlug, def.Title)
			continue
		}

		// Reuse the same seeded case on a second install; do not reset its state.
		if def.StorySlug != "" {
			r, err := client.Get("/api/v1/issues?q=" + url.QueryEscape(def.Title) + "&limit=100")
			if err != nil {
				return err
			}
			if err := cli.CheckError(r); err != nil {
				return err
			}
			var existing []issueItem
			if err := cli.ReadJSON(r, &existing); err != nil {
				return err
			}
			found := false
			for _, item := range existing {
				if item.Title == def.Title && item.CrewID == crewID && item.Identifier != nil {
					issueByKey[def.Title] = createdIssue{Identifier: *item.Identifier, CrewID: crewID}
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		body := map[string]interface{}{
			"title":    def.Title,
			"priority": def.Priority,
		}
		if def.RoutineSlug != "" {
			path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", client.GetWorkspaceID(), def.RoutineSlug)
			resp, err := client.Get(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s: resolve routine %s: %v\n", def.Title, def.RoutineSlug, err)
				continue
			}
			if err := cli.CheckError(resp); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s: resolve routine %s: %v\n", def.Title, def.RoutineSlug, err)
				continue
			}
			var routine struct {
				ID string `json:"id" yaml:"id"`
			}
			if err := cli.ReadJSON(resp, &routine); err != nil || routine.ID == "" {
				fmt.Fprintf(os.Stderr, "  ! %s: routine %s has no usable ID\n", def.Title, def.RoutineSlug)
				continue
			}
			body["routine_id"] = routine.ID
		}
		if def.Description != "" {
			body["description"] = def.Description
		}
		if def.Project != "" {
			if pid, ok := projectIDs[def.Project]; ok {
				body["project_id"] = pid
			}
		}
		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues", crewID), body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", def.Title, err)
			continue
		}
		if err := cli.CheckError(resp); err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", def.Title, err)
			continue
		}
		var created struct {
			ID         string  `json:"id" yaml:"id"`
			Identifier *string `json:"identifier" yaml:"identifier"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			continue
		}
		ident := ""
		if created.Identifier != nil {
			ident = *created.Identifier
		}
		if ident != "" {
			issueByKey[def.Title] = createdIssue{Identifier: ident, CrewID: crewID}
		}
		fmt.Fprintf(os.Stderr, "  + %s: %s (%s)\n", ident, truncate(def.Title, 50), def.Priority)

		// Labels: named in the fixture, attached by ID through the bulk
		// endpoint (the only route that writes mission_labels). A name the
		// catalogue does not carry is reported — the board would otherwise
		// show an issue without the chip the fixture promised.
		if len(def.Labels) > 0 && created.ID != "" {
			if err := attachIssueLabels(client, created.ID, def.Labels, labelIDs); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %s labels: %v\n", ident, err)
			}
		}

		// Transition to target state
		if def.TargetState != "" && def.TargetState != "BACKLOG" && ident != "" {
			for _, status := range seeddata.StatusPath(def.TargetState) {
				r, err := client.Patch(
					fmt.Sprintf("/api/v1/crews/%s/issues/%s", crewID, ident),
					map[string]string{"status": status},
				)
				if err != nil {
					break
				}
				r.Body.Close()
				if r.StatusCode >= 400 {
					break
				}
			}
		}

		// Assign agent via PATCH
		if def.Assignee != "" && ident != "" {
			aid, ok := agentIDs[def.Assignee]
			if ok {
				r, err := client.Patch(
					fmt.Sprintf("/api/v1/crews/%s/issues/%s", crewID, ident),
					map[string]string{"assignee_type": "agent", "assignee_id": aid},
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "    ! assign %s→%s: %v\n", ident, def.Assignee, err)
				} else {
					if r.StatusCode >= 400 {
						fmt.Fprintf(os.Stderr, "    ! assign %s→%s: HTTP %d\n", ident, def.Assignee, r.StatusCode)
					}
					r.Body.Close()
				}
			} else {
				fmt.Fprintf(os.Stderr, "    ! agent %q not in agentIDs\n", def.Assignee)
			}
		}

		// Add comment
		if def.Comment != "" && ident != "" {
			r, err := client.Post(
				fmt.Sprintf("/api/v1/crews/%s/issues/%s/comments", crewID, ident),
				map[string]string{"body": def.Comment},
			)
			if err == nil {
				r.Body.Close()
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	// Bind stable story slugs to the actual generated Issue identifiers.
	for crewSlug, crewID := range crewIDs {
		bindings := map[string]map[string]string{}
		for _, story := range seeddata.Stories {
			if story.Crew != crewSlug {
				continue
			}
			issue, ok := issueByKey[story.IssueTitle]
			if !ok {
				return fmt.Errorf("story %s has no seeded Issue", story.Slug)
			}
			bindings[story.Slug] = map[string]string{"identifier": issue.Identifier}
		}
		if len(bindings) > 0 {
			b, err := json.Marshal(bindings)
			if err != nil {
				return err
			}
			if err := putBytes(ctx, client, crewFileSavePath(crewID, "shared/demo/business/bindings.json"), bytes.NewReader(b)); err != nil {
				return fmt.Errorf("story bindings: %w", err)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "  Seeded %d labels, %d projects, %d issues\n", len(seeddata.Labels), len(projectIDs), len(seeddata.Issues))
	return nil
}

// attachIssueLabels resolves label names to IDs and attaches them in one
// bulk update. Unknown names are an error rather than a skip: the fixture
// and the catalogue live in the same file and must agree.
func attachIssueLabels(client *cli.Client, issueID string, names []string, labelIDs map[string]string) error {
	ids := make([]string, 0, len(names))
	for _, n := range names {
		id, ok := labelIDs[n]
		if !ok {
			return fmt.Errorf("label %q is not in the seeded catalogue", n)
		}
		ids = append(ids, id)
	}
	resp, err := client.Patch("/api/v1/issues/bulk", map[string]interface{}{
		"ids":     []string{issueID},
		"updates": map[string]interface{}{"labels": ids},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}
