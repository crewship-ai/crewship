package main

// Issue collaboration + status transition commands: comment, labels,
// start, stop, review. Extracted from cmd_issue.go.

import (
	"fmt"
	"net/http"
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
		// The 201 body is the created comment (api.commentResponse). Decoding
		// it into issueComment — the same struct `issue comments` renders —
		// means a caller reads the new comment's id back in exactly the shape
		// the list command already emits.
		var created issueComment
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		return resolvedFormatter(cmd).AutoHuman(created, func() {
			cli.PrintSuccess(fmt.Sprintf("Comment added to %s.", args[0]))
		})
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
		result, err := readIssueTransition(resp, identifier)
		if err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(result, func() {
			cli.PrintSuccess(fmt.Sprintf("Started %s — agent dispatched", identifier))
		})
	},
}

// issueTransitionResult is what /start, /stop and /review answer with. The
// three endpoints return slightly different objects — start has a status,
// stop adds runs_stopped/hard, review reports the action — so the union is
// carried in one struct with omitempty rather than three near-identical ones.
// The status is the field a caller reads: it is the only place the CLI says
// what the transition actually produced, as opposed to what was requested.
type issueTransitionResult struct {
	Identifier  string `json:"identifier" yaml:"identifier"`
	Status      string `json:"status,omitempty" yaml:"status,omitempty"`
	Action      string `json:"action,omitempty" yaml:"action,omitempty"`
	RunsStopped *int   `json:"runs_stopped,omitempty" yaml:"runs_stopped,omitempty"`
	Hard        *bool  `json:"hard,omitempty" yaml:"hard,omitempty"`
}

// readIssueTransition decodes one of those bodies, backfilling the identifier
// the CLI already knows — `/review` does not echo one, and a result that
// cannot say which issue it is about is not usable in a pipeline that acts on
// several.
func readIssueTransition(resp *http.Response, identifier string) (issueTransitionResult, error) {
	var out issueTransitionResult
	if err := cli.ReadJSON(resp, &out); err != nil {
		return out, err
	}
	if out.Identifier == "" {
		out.Identifier = identifier
	}
	return out, nil
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
		result, err := readIssueTransition(resp, identifier)
		if err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(result, func() {
			if hard {
				cli.PrintSuccess(fmt.Sprintf("Hard stop requested for %s — the running agent process is being terminated (TERM, then KILL after a grace period)", identifier))
				return
			}
			cli.PrintSuccess(fmt.Sprintf("Stop requested for %s — the current step will finish; no further step will start", identifier))
		})
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
		result, err := readIssueTransition(resp, identifier)
		if err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(result, func() {
			if action == "approve" {
				cli.PrintSuccess(fmt.Sprintf("Approved %s", identifier))
			} else {
				cli.PrintSuccess(fmt.Sprintf("Changes requested on %s", identifier))
			}
		})
	},
}

// ---------- init ----------
