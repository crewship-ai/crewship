package main

// Demo-issue seeding extracted from cmd_seed_data.go. Independent of
// integrations and the per-entity seeders — runs last to populate
// realistic mission/issue data once crews + agents exist.

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	// Create labels
	fmt.Fprintln(os.Stderr, "Creating labels...")
	for _, l := range seeddata.Labels {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/labels", l)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ! Label %s: %v\n", l.Name, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			fmt.Fprintf(os.Stderr, "  + Label: %s\n", l.Name)
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
			ID string `json:"id"`
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

		body := map[string]interface{}{
			"title":    def.Title,
			"priority": def.Priority,
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
			ID         string  `json:"id"`
			Identifier *string `json:"identifier"`
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

	// Create relations between issues using stable seed keys (issue titles).
	// If a referenced issue failed to create, the relation is skipped instead
	// of being wired to the wrong target.
	fmt.Fprintln(os.Stderr, "Creating relations...")
	type relDef struct {
		sourceKey, targetKey, rtype string
	}
	rels := []relDef{
		{"Ping google.com 5 times and save results", "Check HTTP status of 5 popular websites", "blocks"},
		{"Ping google.com 5 times and save results", "Create a directory tree with sample files", "relates_to"},
		{"Trace DNS resolution for 3 domains", "Measure download speed with a 1MB test file", "relates_to"},
		{"Generate a CSV report with random data", "Create a directory tree with sample files", "blocked_by"},
	}
	for _, rd := range rels {
		if err := ctx.Err(); err != nil {
			return err
		}
		src, srcOK := issueByKey[rd.sourceKey]
		tgt, tgtOK := issueByKey[rd.targetKey]
		if !srcOK || !tgtOK {
			fmt.Fprintf(os.Stderr, "  ! relation skipped (missing endpoint): %s %s %s\n", rd.sourceKey, rd.rtype, rd.targetKey)
			continue
		}
		r, err := client.Post(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s/relations", src.CrewID, src.Identifier),
			map[string]string{"target_identifier": tgt.Identifier, "relation_type": rd.rtype},
		)
		if err == nil {
			if r.StatusCode < 400 {
				fmt.Fprintf(os.Stderr, "  + %s %s %s\n", src.Identifier, rd.rtype, tgt.Identifier)
			}
			r.Body.Close()
		}
	}

	fmt.Fprintf(os.Stderr, "  Seeded %d labels, %d projects, %d issues\n", len(seeddata.Labels), len(projectIDs), len(seeddata.Issues))
	return nil
}
