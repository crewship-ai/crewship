package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// ---------- types ----------

type issueItem struct {
	ID           string       `json:"id"`
	CrewID       string       `json:"crew_id"`
	CrewName     string       `json:"crew_name"`
	CrewSlug     string       `json:"crew_slug"`
	Number       *int         `json:"number"`
	Identifier   *string      `json:"identifier"`
	Title        string       `json:"title"`
	Description  *string      `json:"description"`
	Status       string       `json:"status"`
	Priority     string       `json:"priority"`
	AssigneeType *string      `json:"assignee_type"`
	AssigneeID   *string      `json:"assignee_id"`
	AssigneeName *string      `json:"assignee_name"`
	DueDate      *string      `json:"due_date"`
	MissionType  string       `json:"mission_type"`
	CreatedAt    string       `json:"created_at"`
	UpdatedAt    string       `json:"updated_at"`
	Labels       []issueLabel `json:"labels"`
	CommentCount int          `json:"comment_count"`
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

		// Description is rendered separately via glamour (see below) so it
		// doesn't get truncated inside the tabwriter column alignment.
		pairs := [][]string{
			{"Identifier", derefStr(issue.Identifier, "-")},
			{"Title", issue.Title},
			{"Status", issue.Status},
			{"Priority", capitalizePriority(issue.Priority)},
			{"Crew", issue.CrewSlug},
			{"Assignee", derefStr(issue.AssigneeName, "-")},
			{"Assignee Type", derefStr(issue.AssigneeType, "-")},
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

		// Render description as markdown (glamour) below the metadata table,
		// but ONLY for human-facing formats. JSON/YAML/quiet already serialize
		// the description in the struct, so an extra styled rendering would
		// pollute machine output.
		desc := derefStr(issue.Description, "")
		if desc != "" && (f.Format == "" || f.Format == "table") {
			fmt.Fprintln(f.Writer)
			fmt.Fprintf(f.Writer, "%sDescription:%s\n", cli.Bold, cli.Reset)
			f.Markdown(desc)
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

		// Comments section is human-facing only. JSON/YAML/quiet formats
		// consume the main issue struct (already serialized by AutoDetail
		// above) and would break if we appended styled text afterwards.
		if len(comments) > 0 && (f.Format == "" || f.Format == "table") {
			fmt.Fprintln(f.Writer)
			fmt.Fprintf(f.Writer, "%sComments:%s\n", cli.Bold, cli.Reset)
			for _, c := range comments {
				author := derefStr(c.Author, "unknown")
				ts := issueRelativeTime(c.CreatedAt)
				fmt.Fprintf(f.Writer, "\n%s@%s%s %s(%s)%s\n", cli.Bold, author, cli.Reset, cli.Dim, ts, cli.Reset)
				// Render comment body as markdown.
				f.Markdown(c.Body)
			}
		}

		return nil
	},
}

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
