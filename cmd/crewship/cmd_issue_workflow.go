package main

// Issue collaboration + status transition commands: comment, labels,
// start, stop, review. Extracted from cmd_issue.go.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

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
		mentionSlugs, _ := cmd.Flags().GetStringArray("mention")
		if body == "" && len(mentionSlugs) == 0 {
			return fmt.Errorf("comment body is required (pass as arguments, --body, or --mention)")
		}

		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}

		// The server only recognises a mention written as the Markdown link
		// [@<slug>](crewship:agent/<agentId>) — internal/mentions parses the
		// comment as CommonMark and reads only real link nodes, so a bare
		// "@slug" typed into the body is never a mention, just text. Resolve
		// each --mention slug to its agent id (resolveAgentID: the same
		// slug-or-CUID scan every other agent-slug flag uses, so an unknown
		// slug fails with the same "Did you mean" / "Available" hints) and
		// prepend one link per mention, space-separated, before the user's
		// own text.
		if len(mentionSlugs) > 0 {
			links := make([]string, 0, len(mentionSlugs))
			for _, raw := range mentionSlugs {
				slug := strings.TrimPrefix(raw, "@")
				agentID, err := resolveAgentID(client, slug)
				if err != nil {
					return err
				}
				links = append(links, fmt.Sprintf("[@%s](crewship:agent/%s)", slug, agentID))
			}
			if body == "" {
				body = strings.Join(links, " ")
			} else {
				body = strings.Join(links, " ") + " " + body
			}
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
		identifier := derefStr(iss.Identifier, iss.ID)
		escaped := url.PathEscape(identifier)

		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/start", iss.CrewID, escaped), nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Started %s — agent dispatched", identifier))
		return nil
	},
}

var issueStopCmd = &cobra.Command{
	Use:   "stop <identifier>",
	Short: "Stop an issue — cooperative cancel; --hard also terminates the running process",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		hard, _ := cmd.Flags().GetBool("hard")

		client := newAPIClient()
		iss, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		identifier := derefStr(iss.Identifier, iss.ID)
		escaped := url.PathEscape(identifier)

		path := fmt.Sprintf("/api/v1/crews/%s/issues/%s/stop", iss.CrewID, escaped)
		if hard {
			path += "?hard=true"
		}
		resp, err := client.Post(path, nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		if hard {
			cli.PrintSuccess(fmt.Sprintf("Hard stop requested for %s — the running agent process is being terminated (TERM, then KILL after a grace period)", identifier))
			return nil
		}
		cli.PrintSuccess(fmt.Sprintf("Stop requested for %s — the current step will finish; no further step will start", identifier))
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
		identifier := derefStr(iss.Identifier, iss.ID)
		escaped := url.PathEscape(identifier)

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

		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/review", iss.CrewID, escaped), body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		if action == "approve" {
			cli.PrintSuccess(fmt.Sprintf("Approved %s", identifier))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Changes requested on %s", identifier))
		}
		return nil
	},
}

// ---------- init ----------
