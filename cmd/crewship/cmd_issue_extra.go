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
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

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
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess("Relation removed.")
		return nil
	},
}

func init() {
	issueRelateCmd.Flags().String("type", "relates_to",
		"Relation type: blocks, blocked_by, relates_to (alias: relates-to), duplicate_of (alias: duplicate-of)")

	issueCmd.AddCommand(issueCommentsCmd)
	issueCmd.AddCommand(issueRelateCmd)
	issueCmd.AddCommand(issueRelationsCmd)
	issueCmd.AddCommand(issueUnrelateCmd)
}
