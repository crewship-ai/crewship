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
				ID string `json:"id"`
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

	// Create relations between issues using stable seed keys (issue titles).
	// If a referenced issue failed to create, the relation is skipped instead
	// of being wired to the wrong target.
	fmt.Fprintln(os.Stderr, "Creating relations...")
	type relDef struct {
		sourceKey, targetKey, rtype string
	}
	rels := []relDef{
		// site-replica: the hand-off order inside the engineering crew.
		{"Map the seznam.cz home page — sections, navigation and metadata", "Build the static replica from the content map", "blocks"},
		{"Extract the seznam.cz home page into a structured data model", "Build the static replica from the content map", "blocks"},
		{"Build the static replica from the content map", "Run the replica acceptance checks and report PASS or FAIL", "blocks"},
		{"Run the replica acceptance checks and report PASS or FAIL", "Replicate https://www.seznam.cz as a self-contained static page — delegate analysis, data, build and test", "blocks"},
		// docs-drift: the fact-check gates the fix list.
		{"Fact-check every docs-drift candidate against the file and line it cites", "Run the docs-drift audit on main and turn it into a fix list", "blocks"},
		// ci-watch: reconciliation and the stale investigation feed the handover.
		{"Run the CI probe against crewship-ai/crewship and reconcile it with GitHub", "Bring the nightly CI watch live and hand me the first real report", "relates_to"},
		{"Explain why a scheduled workflow went stale", "Bring the nightly CI watch live and hand me the first real report", "relates_to"},
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
