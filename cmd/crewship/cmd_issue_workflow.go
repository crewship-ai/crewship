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
		identifier := derefStr(iss.Identifier, iss.ID)
		escaped := url.PathEscape(identifier)

		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/stop", iss.CrewID, escaped), nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Stopped %s", identifier))
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
