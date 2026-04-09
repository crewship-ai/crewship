package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// ---------- types ----------

type issueItem struct {
	ID           string  `json:"id"`
	CrewID       string  `json:"crew_id"`
	CrewName     string  `json:"crew_name"`
	CrewSlug     string  `json:"crew_slug"`
	Number       *int    `json:"number"`
	Identifier   *string `json:"identifier"`
	Title        string  `json:"title"`
	Description  *string `json:"description"`
	Status       string  `json:"status"`
	Priority     string  `json:"priority"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	AssigneeName *string `json:"assignee_name"`
	DueDate      *string `json:"due_date"`
	MissionType  string  `json:"mission_type"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	Labels       []issueLabel `json:"labels"`
	CommentCount int     `json:"comment_count"`
}

type issueLabel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type issueComment struct {
	ID        string  `json:"id"`
	Body      string  `json:"body"`
	AuthorID  *string `json:"author_id"`
	Author    *string `json:"author_name"`
	CreatedAt string  `json:"created_at"`
}

type labelItem struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Color string  `json:"color"`
	Group *string `json:"group"`
}

// ---------- helpers ----------

func issueRelativeTime(iso string) string {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		// fallback: try RFC3339 without nanos
		t, err = time.Parse(time.RFC3339, iso)
		if err != nil {
			return iso
		}
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	default:
		months := int(d.Hours() / 24 / 30)
		if months < 12 {
			return fmt.Sprintf("%dmo ago", months)
		}
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func derefStr(s *string, fallback string) string {
	if s != nil && *s != "" {
		return *s
	}
	return fallback
}

func capitalizePriority(p string) string {
	if p == "" {
		return "-"
	}
	return strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
}

// fetchIssue retrieves a single issue by identifier for commands that need
// crew_id before making a mutation request.
func fetchIssue(client *cli.Client, identifier string) (*issueItem, error) {
	resp, err := client.Get("/api/v1/issues/" + url.PathEscape(identifier))
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var issue issueItem
	if err := cli.ReadJSON(resp, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// ---------- commands ----------

var issueCmd = &cobra.Command{
	Use:     "issue",
	Short:   "Manage issues",
	Aliases: []string{"issues"},
}

var issueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		flags := cmd.Flags()

		params := url.Values{}
		if v, _ := flags.GetString("status"); v != "" {
			params.Set("status", v)
		}
		if v, _ := flags.GetString("priority"); v != "" {
			params.Set("priority", v)
		}
		if v, _ := flags.GetString("crew"); v != "" {
			crewID, err := resolveCrewID(client, v)
			if err != nil {
				return err
			}
			params.Set("crew_id", crewID)
		}
		if v, _ := flags.GetString("assignee"); v != "" {
			params.Set("assignee_id", v)
		}
		if v, _ := flags.GetString("label"); v != "" {
			params.Set("label", v)
		}
		if v, _ := flags.GetString("search"); v != "" {
			params.Set("search", v)
		}
		if v, _ := flags.GetInt("limit"); v > 0 {
			params.Set("limit", fmt.Sprintf("%d", v))
		}

		path := "/api/v1/issues"
		if q := params.Encode(); q != "" {
			path += "?" + q
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var issues []issueItem
		if err := cli.ReadJSON(resp, &issues); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "CREW", "LABELS", "UPDATED"}
		var rows [][]string
		for _, iss := range issues {
			id := derefStr(iss.Identifier, iss.ID[:min(12, len(iss.ID))])
			title := truncateStr(iss.Title, 40)
			assignee := derefStr(iss.AssigneeName, "-")
			var labelNames []string
			for _, l := range iss.Labels {
				labelNames = append(labelNames, l.Name)
			}
			labels := truncateStr(strings.Join(labelNames, ", "), 20)
			updated := issueRelativeTime(iss.UpdatedAt)

			rows = append(rows, []string{
				id,
				title,
				iss.Status,
				capitalizePriority(iss.Priority),
				assignee,
				iss.CrewSlug,
				labels,
				updated,
			})
		}
		return f.Auto(issues, headers, rows)
	},
}

var issueGetCmd = &cobra.Command{
	Use:   "get <identifier>",
	Short: "Show issue details",
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

		f := newFormatter()

		var labelNames []string
		for _, l := range issue.Labels {
			labelNames = append(labelNames, l.Name)
		}

		pairs := [][]string{
			{"Identifier", derefStr(issue.Identifier, "-")},
			{"Title", issue.Title},
			{"Status", issue.Status},
			{"Priority", capitalizePriority(issue.Priority)},
			{"Crew", issue.CrewSlug},
			{"Assignee", derefStr(issue.AssigneeName, "-")},
			{"Assignee Type", derefStr(issue.AssigneeType, "-")},
			{"Description", derefStr(issue.Description, "-")},
			{"Due Date", derefStr(issue.DueDate, "-")},
			{"Mission Type", issue.MissionType},
			{"Labels", strings.Join(labelNames, ", ")},
			{"Comments", fmt.Sprintf("%d", issue.CommentCount)},
			{"Created", issueRelativeTime(issue.CreatedAt)},
			{"Updated", issueRelativeTime(issue.UpdatedAt)},
			{"ID", issue.ID},
		}

		if err := f.AutoDetail(issue, pairs); err != nil {
			return err
		}

		// Fetch and display comments
		commentsResp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/comments",
			issue.CrewID, url.PathEscape(derefStr(issue.Identifier, issue.ID))))
		if err != nil {
			return nil // non-fatal: issue displayed, comments failed
		}
		if err := cli.CheckError(commentsResp); err != nil {
			return nil
		}

		var comments []issueComment
		if err := cli.ReadJSON(commentsResp, &comments); err != nil {
			return nil
		}

		if len(comments) > 0 {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintf(os.Stderr, "  Comments:\n")
			for _, c := range comments {
				author := derefStr(c.Author, "unknown")
				ts := issueRelativeTime(c.CreatedAt)
				fmt.Fprintf(os.Stderr, "  @%s (%s): %s\n", author, ts, c.Body)
			}
		}

		return nil
	},
}

var issueCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new issue",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		crewSlug, _ := flags.GetString("crew")
		title, _ := flags.GetString("title")
		if crewSlug == "" {
			return fmt.Errorf("--crew is required")
		}
		if title == "" {
			return fmt.Errorf("--title is required")
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}

		body := map[string]interface{}{
			"title": title,
		}

		if v, _ := flags.GetString("description"); v != "" {
			body["description"] = v
		}
		if v, _ := flags.GetString("priority"); v != "" {
			body["priority"] = v
		}
		if v, _ := flags.GetString("assignee"); v != "" {
			agentID, err := resolveAgentID(client, v)
			if err != nil {
				return fmt.Errorf("cannot resolve assignee %q: %w", v, err)
			}
			body["assignee_id"] = agentID
			if atype, _ := flags.GetString("assignee-type"); atype != "" {
				body["assignee_type"] = atype
			} else {
				body["assignee_type"] = "agent"
			}
		}
		if v, _ := flags.GetString("labels"); v != "" {
			body["labels"] = strings.Split(v, ",")
		}
		if v, _ := flags.GetString("due-date"); v != "" {
			body["due_date"] = v
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/issues", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created issueItem
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		identifier := derefStr(created.Identifier, created.ID)
		cli.PrintSuccess(fmt.Sprintf("Created issue %s: %s", identifier, created.Title))
		return nil
	},
}

var issueUpdateCmd = &cobra.Command{
	Use:   "update <identifier>",
	Short: "Update an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		// Fetch issue to get crew_id
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("title") {
			v, _ := flags.GetString("title")
			body["title"] = v
		}
		if flags.Changed("description") {
			v, _ := flags.GetString("description")
			body["description"] = v
		}
		if flags.Changed("status") {
			v, _ := flags.GetString("status")
			body["status"] = v
		}
		if flags.Changed("priority") {
			v, _ := flags.GetString("priority")
			body["priority"] = v
		}
		if flags.Changed("assignee") {
			v, _ := flags.GetString("assignee")
			if v == "" {
				body["assignee_id"] = nil
				body["assignee_type"] = nil
			} else {
				// Resolve agent slug to ID
				agentID, err := resolveAgentID(client, v)
				if err != nil {
					return fmt.Errorf("cannot resolve assignee %q: %w", v, err)
				}
				body["assignee_id"] = agentID
				// Auto-set type to agent if not explicitly set
				if !flags.Changed("assignee-type") {
					body["assignee_type"] = "agent"
				}
			}
		}
		if flags.Changed("assignee-type") {
			v, _ := flags.GetString("assignee-type")
			if strings.TrimSpace(v) == "" {
				body["assignee_type"] = nil
			} else {
				body["assignee_type"] = v
			}
		}
		if flags.Changed("due-date") {
			v, _ := flags.GetString("due-date")
			body["due_date"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s", issue.CrewID, url.PathEscape(identifier)),
			body,
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Issue %s updated.", args[0]))
		return nil
	},
}

var issueDeleteCmd = &cobra.Command{
	Use:   "delete <identifier>",
	Short: "Delete an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete issue %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}

		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Delete(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s", issue.CrewID, url.PathEscape(identifier)),
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Issue %s deleted.", args[0]))
		return nil
	},
}

var issueCommentCmd = &cobra.Command{
	Use:   "comment <identifier> [message...]",
	Short: "Add a comment to an issue",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		// Comment body: --body flag takes precedence, then positional args
		body, _ := cmd.Flags().GetString("body")
		if body == "" && len(args) > 1 {
			body = strings.Join(args[1:], " ")
		}
		if body == "" {
			return fmt.Errorf("comment body is required (pass as arguments or use --body)")
		}

		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}

		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s/comments", issue.CrewID, url.PathEscape(identifier)),
			map[string]interface{}{"body": body},
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Comment added to %s.", args[0]))
		return nil
	},
}

var issueLabelsCmd = &cobra.Command{
	Use:   "labels",
	Short: "List workspace labels",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/labels")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var labels []labelItem
		if err := cli.ReadJSON(resp, &labels); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"NAME", "COLOR", "GROUP"}
		var rows [][]string
		for _, l := range labels {
			group := derefStr(l.Group, "-")
			rows = append(rows, []string{l.Name, l.Color, group})
		}
		return f.Auto(labels, headers, rows)
	},
}

var issueStartCmd = &cobra.Command{
	Use:   "start <identifier>",
	Short: "Start an issue — dispatch to assigned agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		iss, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		if iss.Identifier == nil {
			return fmt.Errorf("issue has no identifier")
		}

		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/start", iss.CrewID, *iss.Identifier), nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Started %s — agent dispatched", *iss.Identifier))
		return nil
	},
}

var issueStopCmd = &cobra.Command{
	Use:   "stop <identifier>",
	Short: "Stop an issue — cancel running tasks",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		iss, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		if iss.Identifier == nil {
			return fmt.Errorf("issue has no identifier")
		}

		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/stop", iss.CrewID, *iss.Identifier), nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Stopped %s", *iss.Identifier))
		return nil
	},
}

var issueReviewCmd = &cobra.Command{
	Use:   "review <identifier>",
	Short: "Review an issue — approve or request changes",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		iss, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		if iss.Identifier == nil {
			return fmt.Errorf("issue has no identifier")
		}

		action, _ := cmd.Flags().GetString("action")
		if action == "" {
			return fmt.Errorf("--action is required (approve or request_changes)")
		}
		if action != "approve" && action != "request_changes" {
			return fmt.Errorf("--action must be 'approve' or 'request_changes'")
		}

		body := map[string]interface{}{"action": action}
		if comment, _ := cmd.Flags().GetString("comment"); comment != "" {
			body["comment"] = comment
		}
		if reassign, _ := cmd.Flags().GetString("reassign"); reassign != "" {
			body["reassign_to"] = reassign
		}

		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/review", iss.CrewID, *iss.Identifier), body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		if action == "approve" {
			cli.PrintSuccess(fmt.Sprintf("Approved %s", *iss.Identifier))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Changes requested on %s", *iss.Identifier))
		}
		return nil
	},
}

// ---------- init ----------

func init() {
	// issue list flags
	issueListCmd.Flags().String("status", "", "Filter by status (BACKLOG, TODO, IN_PROGRESS, REVIEW, DONE, FAILED, CANCELLED, DUPLICATE)")
	issueListCmd.Flags().String("priority", "", "Filter by priority (none, low, medium, high, urgent)")
	issueListCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	issueListCmd.Flags().String("assignee", "", "Filter by assignee ID")
	issueListCmd.Flags().String("label", "", "Filter by label name")
	issueListCmd.Flags().String("search", "", "Search issues by title or description")
	issueListCmd.Flags().Int("limit", 50, "Maximum number of issues to return")

	// issue create flags
	issueCreateCmd.Flags().String("crew", "", "Crew slug or ID (required)")
	issueCreateCmd.Flags().String("title", "", "Issue title (required)")
	issueCreateCmd.Flags().String("description", "", "Issue description")
	issueCreateCmd.Flags().String("priority", "none", "Priority: none, low, medium, high, urgent")
	issueCreateCmd.Flags().String("assignee", "", "Assignee agent slug")
	issueCreateCmd.Flags().String("assignee-type", "agent", "Assignee type: agent or user")
	issueCreateCmd.Flags().String("labels", "", "Comma-separated label IDs")
	issueCreateCmd.Flags().String("due-date", "", "Due date (ISO 8601)")

	// issue update flags
	issueUpdateCmd.Flags().String("title", "", "New title")
	issueUpdateCmd.Flags().String("description", "", "New description")
	issueUpdateCmd.Flags().String("status", "", "New status: BACKLOG, TODO, IN_PROGRESS, REVIEW, DONE, FAILED, CANCELLED, DUPLICATE")
	issueUpdateCmd.Flags().String("priority", "", "New priority: none, low, medium, high, urgent")
	issueUpdateCmd.Flags().String("assignee", "", "Assignee agent slug")
	issueUpdateCmd.Flags().String("assignee-type", "", "Assignee type: agent or user")
	issueUpdateCmd.Flags().String("due-date", "", "Due date (ISO 8601)")

	// issue review flags
	issueReviewCmd.Flags().String("action", "", "Review action: approve or request_changes (required)")
	issueReviewCmd.Flags().String("comment", "", "Review comment")
	issueReviewCmd.Flags().String("reassign", "", "Agent slug to reassign to (for request_changes)")

	// issue delete flags
	issueDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	// issue comment flags
	issueCommentCmd.Flags().String("body", "", "Comment body (alternative to positional args)")

	// register subcommands
	issueCmd.AddCommand(issueListCmd)
	issueCmd.AddCommand(issueGetCmd)
	issueCmd.AddCommand(issueCreateCmd)
	issueCmd.AddCommand(issueUpdateCmd)
	issueCmd.AddCommand(issueDeleteCmd)
	issueCmd.AddCommand(issueCommentCmd)
	issueCmd.AddCommand(issueLabelsCmd)
	issueCmd.AddCommand(issueStartCmd)
	issueCmd.AddCommand(issueStopCmd)
	issueCmd.AddCommand(issueReviewCmd)
}
