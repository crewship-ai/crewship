package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// issueCommentsCmd lists comments on an issue. Registered as `issue comments`.
var issueCommentsCmd = &cobra.Command{
	Use:   "comments <identifier>",
	Short: "List comments on an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)

		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/comments",
			issue.CrewID, url.PathEscape(identifier)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var comments []struct {
			ID         string `json:"id"`
			MissionID  string `json:"mission_id"`
			AuthorType string `json:"author_type"`
			AuthorID   string `json:"author_id"`
			AuthorName string `json:"author_name"`
			Body       string `json:"body"`
			CreatedAt  string `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &comments); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "AUTHOR", "TYPE", "BODY", "CREATED"}
		rows := make([][]string, 0, len(comments))
		for _, c := range comments {
			rows = append(rows, []string{
				truncateID(c.ID, 12),
				c.AuthorName,
				c.AuthorType,
				truncateStr(strings.ReplaceAll(c.Body, "\n", " "), 60),
				c.CreatedAt,
			})
		}
		return f.Auto(comments, headers, rows)
	},
}

// issueRelateCmd creates a relation between two issues.
var issueRelateCmd = &cobra.Command{
	Use:   "relate <identifier> <target-identifier>",
	Short: "Create a relation between two issues",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		relType, _ := cmd.Flags().GetString("type")
		// Normalize hyphen form to the API's underscore form.
		relType = strings.ReplaceAll(relType, "-", "_")
		valid := map[string]bool{
			"blocks":       true,
			"blocked_by":   true,
			"relates_to":   true,
			"duplicate_of": true,
		}
		if !valid[relType] {
			return fmt.Errorf("--type must be one of: blocks, blocked_by, relates_to, duplicate_of")
		}

		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)

		body := map[string]interface{}{
			"target_identifier": args[1],
			"relation_type":     relType,
		}
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s/relations",
				issue.CrewID, url.PathEscape(identifier)),
			body,
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Created %s relation: %s → %s", relType, identifier, args[1]))
		return nil
	},
}

// issueRelationsCmd lists relations for an issue.
var issueRelationsCmd = &cobra.Command{
	Use:     "relations <identifier>",
	Aliases: []string{"relates"},
	Short:   "List relations for an issue",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)

		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/relations",
			issue.CrewID, url.PathEscape(identifier)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var rels []struct {
			ID               string `json:"id"`
			SourceID         string `json:"source_id"`
			TargetID         string `json:"target_id"`
			RelationType     string `json:"relation_type"`
			TargetIdentifier string `json:"target_identifier"`
			TargetTitle      string `json:"target_title"`
			TargetStatus     string `json:"target_status"`
			CreatedAt        string `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &rels); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "TYPE", "TARGET", "TITLE", "STATUS"}
		rows := make([][]string, 0, len(rels))
		for _, r := range rels {
			rows = append(rows, []string{
				truncateID(r.ID, 12),
				r.RelationType,
				r.TargetIdentifier,
				truncateStr(r.TargetTitle, 40),
				r.TargetStatus,
			})
		}
		return f.Auto(rels, headers, rows)
	},
}

// issueUnrelateCmd deletes a relation by its ID.
var issueUnrelateCmd = &cobra.Command{
	Use:   "unrelate <relation-id>",
	Short: "Remove a relation between two issues",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/relations/" + args[0])
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Relation removed.")
		return nil
	},
}

// resolveRoutineID looks up a routine by slug and returns its pipeline ID.
// The issue routine_id binding stores pipeline UUIDs (not slugs) so renames
// don't break the link — see issue_handler_create.go:38.
func resolveRoutineID(client *cli.Client, slug string) (string, error) {
	ws := client.GetWorkspaceID()
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", ws, url.PathEscape(slug)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return "", fmt.Errorf("routine %q: %w", slug, err)
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &p); err != nil {
		return "", err
	}
	if p.ID == "" {
		return "", fmt.Errorf("routine %q has no id in response", slug)
	}
	return p.ID, nil
}

// issueBindRoutineCmd binds a routine (by slug) to an issue. Thin wrapper
// over PATCH /crews/{crewId}/issues/{ident} that sets routine_id only.
// The corresponding UI surface is the RoutineBinder panel (PR #292).
var issueBindRoutineCmd = &cobra.Command{
	Use:   "bind-routine <identifier> <routine-slug>",
	Short: "Bind a routine to an issue (sets routine_id)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		routineID, err := resolveRoutineID(client, args[1])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s", issue.CrewID, url.PathEscape(identifier)),
			map[string]interface{}{"routine_id": routineID},
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Bound routine %s to issue %s.", args[1], identifier))
		return nil
	},
}

// issueUnbindRoutineCmd clears the routine binding on an issue.
// Server-side, empty routine_id is normalized to NULL — see
// issue_handler_update.go (SetNull on empty string).
var issueUnbindRoutineCmd = &cobra.Command{
	Use:   "unbind-routine <identifier>",
	Short: "Remove the routine binding from an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s", issue.CrewID, url.PathEscape(identifier)),
			map[string]interface{}{"routine_id": ""},
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Unbound routine from issue %s.", identifier))
		return nil
	},
}

// issueSubtasksCmd lists sub-issues (children with parent_issue_id ==
// this issue's id), the same surface the issue-detail SubIssues panel
// renders. Aliased as `subissues` to match the UI naming.
var issueSubtasksCmd = &cobra.Command{
	Use:     "subtasks <identifier>",
	Aliases: []string{"subissues", "sub-issues"},
	Short:   "List sub-issues of an issue",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/subtasks",
			issue.CrewID, url.PathEscape(identifier)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var subs []issueItem
		if err := cli.ReadJSON(resp, &subs); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE"}
		rows := make([][]string, 0, len(subs))
		for _, s := range subs {
			id := derefStr(s.Identifier, s.ID[:min(12, len(s.ID))])
			rows = append(rows, []string{
				id,
				truncateStr(s.Title, 50),
				s.Status,
				capitalizePriority(s.Priority),
				derefStr(s.AssigneeName, "-"),
			})
		}
		return f.Auto(subs, headers, rows)
	},
}

// issueActivityCmd dumps the mission_activity timeline. The server
// caps the result at 50 rows DESC; we surface that as-is so users
// can pipe into jq for deeper analysis.
var issueActivityCmd = &cobra.Command{
	Use:   "activity <identifier>",
	Short: "Show the activity timeline for an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/activity",
			issue.CrewID, url.PathEscape(identifier)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var events []struct {
			ID        string  `json:"id"`
			ActorType string  `json:"actor_type"`
			ActorName *string `json:"actor_name"`
			Action    string  `json:"action"`
			Details   *string `json:"details"`
			CreatedAt string  `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &events); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"WHEN", "ACTOR", "ACTION", "DETAILS"}
		rows := make([][]string, 0, len(events))
		for _, e := range events {
			rows = append(rows, []string{
				issueRelativeTime(e.CreatedAt),
				fmt.Sprintf("%s/%s", e.ActorType, derefStr(e.ActorName, "-")),
				e.Action,
				truncateStr(derefStr(e.Details, ""), 60),
			})
		}
		return f.Auto(events, headers, rows)
	},
}

// issueRunsCmd lists the pipeline runs triggered by an issue. CLI parity
// for GET /api/v1/crews/{crewId}/issues/{identifier}/runs — the data the
// dashboard's issue "Runs" tab shows.
var issueRunsCmd = &cobra.Command{
	Use:   "runs <identifier>",
	Short: "List runs triggered by an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/runs",
			issue.CrewID, url.PathEscape(identifier)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var runs []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			AgentName     string `json:"agent_name"`
			Task          string `json:"task"`
			StartedAt     string `json:"started_at"`
			DurationMs    int64  `json:"duration_ms"`
			ResultSummary string `json:"result_summary"`
			ErrorMessage  string `json:"error_message"`
		}
		if err := cli.ReadJSON(resp, &runs); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"AGENT", "TASK", "STATUS", "STARTED", "DURATION", "RESULT"}
		rows := make([][]string, 0, len(runs))
		for _, run := range runs {
			result := run.ErrorMessage
			if result == "" {
				result = run.ResultSummary
			}
			rows = append(rows, []string{
				run.AgentName,
				truncateStr(run.Task, 28),
				run.Status,
				issueRelativeTime(run.StartedAt),
				fmt.Sprintf("%dms", run.DurationMs),
				// result/error is verbatim agent output — strip ANSI / control
				// bytes before printing so a failed run can't inject escapes.
				truncateStr(strings.ReplaceAll(sanitizeTerminal(result), "\n", " "), 50),
			})
		}
		return f.Auto(runs, headers, rows)
	},
}

// issueChangesCmd shows the base-branch git diff of the crew working an
// issue. CLI parity for GET /api/v1/crews/{crewId}/git-diff — the data the
// dashboard's issue "Changes" tab renders.
var issueChangesCmd = &cobra.Command{
	Use:   "changes <identifier>",
	Short: "Show the git diff produced by work on an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/git-diff", issue.CrewID))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var diff struct {
			IsRepo bool `json:"is_repo"`
			Files  []struct {
				Path      string `json:"path"`
				Status    string `json:"status"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
			} `json:"files"`
			Diff      string `json:"diff"`
			Truncated bool   `json:"truncated"`
		}
		if err := cli.ReadJSON(resp, &diff); err != nil {
			return err
		}
		if !diff.IsRepo {
			fmt.Println("No git repository in this crew's workspace — nothing to diff.")
			return nil
		}
		if len(diff.Files) == 0 {
			fmt.Println("No changes against the base branch.")
			return nil
		}
		patch, _ := cmd.Flags().GetBool("patch")
		if patch {
			fmt.Println(diff.Diff)
			if diff.Truncated {
				fmt.Println("\n… (diff truncated)")
			}
			return nil
		}
		f := newFormatter()
		headers := []string{"STATUS", "FILE", "+", "−"}
		rows := make([][]string, 0, len(diff.Files))
		for _, fl := range diff.Files {
			rows = append(rows, []string{
				fl.Status,
				fl.Path,
				fmt.Sprintf("%d", fl.Additions),
				fmt.Sprintf("%d", fl.Deletions),
			})
		}
		return f.Auto(diff.Files, headers, rows)
	},
}

// issueBulkCmd applies the same patch to many issues in one round-trip
// via PATCH /api/v1/issues/bulk. The server hard-caps at 100 IDs; we
// enforce that client-side so the user gets a friendly error instead
// of an opaque 400.
var issueBulkCmd = &cobra.Command{
	Use:   "bulk",
	Short: "Bulk-update many issues at once (max 100 IDs)",
	Long: `Apply the same field updates to a list of issues. Pass IDs (CUIDs)
via --ids as a comma-separated list. All field flags are optional; only
the ones you set are sent. Status/priority/assignee/project/labels are
supported. Labels REPLACE the existing label set on each issue.

Server limit: 100 issues per call.

Examples:
  crewship issue bulk update --ids ms_a,ms_b,ms_c --status DONE
  crewship issue bulk update --ids ms_a,ms_b --priority high --assignee agent_xyz
  crewship issue bulk update --ids ms_a,ms_b --labels lbl_bug,lbl_p0
`,
}

var issueBulkUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Bulk-update many issues (PATCH /api/v1/issues/bulk)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		idsRaw, _ := cmd.Flags().GetString("ids")
		if strings.TrimSpace(idsRaw) == "" {
			return fmt.Errorf("--ids is required (comma-separated issue IDs)")
		}
		ids := strings.Split(idsRaw, ",")
		clean := ids[:0]
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" {
				clean = append(clean, id)
			}
		}
		if len(clean) == 0 {
			return fmt.Errorf("--ids is empty after trimming")
		}
		if len(clean) > 100 {
			return fmt.Errorf("--ids has %d entries; server caps bulk update at 100 per call", len(clean))
		}

		updates := map[string]interface{}{}
		flags := cmd.Flags()
		if flags.Changed("status") {
			v, _ := flags.GetString("status")
			updates["status"] = v
		}
		if flags.Changed("priority") {
			v, _ := flags.GetString("priority")
			updates["priority"] = v
		}
		if flags.Changed("assignee") {
			v, _ := flags.GetString("assignee")
			// The bulk endpoint takes assignee_id as a raw string; we
			// don't resolve a slug here because callers operating in
			// bulk tend to script with IDs. Empty string clears.
			updates["assignee_id"] = v
			if v != "" {
				atype, _ := flags.GetString("assignee-type")
				if atype == "" {
					atype = "agent"
				}
				updates["assignee_type"] = atype
			} else {
				updates["assignee_type"] = ""
			}
		}
		if flags.Changed("project") {
			v, _ := flags.GetString("project")
			updates["project_id"] = v // "" = unlink
		}
		if flags.Changed("labels") {
			v, _ := flags.GetString("labels")
			var labels []string
			if v != "" {
				for _, l := range strings.Split(v, ",") {
					if t := strings.TrimSpace(l); t != "" {
						labels = append(labels, t)
					}
				}
			}
			if labels == nil {
				labels = []string{} // explicit clear
			}
			updates["labels"] = labels
		}
		if len(updates) == 0 {
			return fmt.Errorf("at least one of --status / --priority / --assignee / --project / --labels is required")
		}

		client := newAPIClient()
		resp, err := client.Patch("/api/v1/issues/bulk", map[string]interface{}{
			"ids":     clean,
			"updates": updates,
		})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Updated int `json:"updated"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Bulk update applied to %d/%d issue(s).", out.Updated, len(clean)))
		return nil
	},
}

func init() {
	issueRelateCmd.Flags().String("type", "relates_to",
		"Relation type: blocks, blocked_by, relates_to (alias: relates-to), duplicate_of (alias: duplicate-of)")

	issueCmd.AddCommand(issueCommentsCmd)
	issueCmd.AddCommand(issueRunsCmd)
	issueChangesCmd.Flags().Bool("patch", false, "Print the raw unified diff instead of the file summary")
	issueCmd.AddCommand(issueChangesCmd)
	issueCmd.AddCommand(issueRelateCmd)
	issueCmd.AddCommand(issueRelationsCmd)
	issueCmd.AddCommand(issueUnrelateCmd)

	// New PR #292-parity subcommands.
	issueCmd.AddCommand(issueBindRoutineCmd)
	issueCmd.AddCommand(issueUnbindRoutineCmd)
	issueCmd.AddCommand(issueSubtasksCmd)
	issueCmd.AddCommand(issueActivityCmd)

	issueBulkUpdateCmd.Flags().String("ids", "", "Comma-separated issue IDs (max 100; REQUIRED)")
	issueBulkUpdateCmd.Flags().String("status", "", "New status for all listed issues")
	issueBulkUpdateCmd.Flags().String("priority", "", "New priority")
	issueBulkUpdateCmd.Flags().String("assignee", "", "Assignee ID (empty string clears)")
	issueBulkUpdateCmd.Flags().String("assignee-type", "agent", "Assignee type when --assignee is set")
	issueBulkUpdateCmd.Flags().String("project", "", "Project ID (empty string unlinks)")
	issueBulkUpdateCmd.Flags().String("labels", "", "Comma-separated label IDs (REPLACES existing labels; empty string clears)")
	issueBulkCmd.AddCommand(issueBulkUpdateCmd)
	issueCmd.AddCommand(issueBulkCmd)
}
