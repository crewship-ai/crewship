package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var proposalCmd = &cobra.Command{
	Use:   "proposal",
	Short: "Manage mission proposals",
}

var proposalListCmd = &cobra.Command{
	Use:   "list",
	Short: "List mission proposals",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		path := "/api/v1/mission-proposals"
		if status, _ := cmd.Flags().GetString("status"); status != "" {
			path += "?status=" + status
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var proposals []struct {
			ID           string  `json:"id"`
			Title        string  `json:"title"`
			Status       string  `json:"status"`
			ProposerName *string `json:"proposer_name"`
			ProposerSlug *string `json:"proposer_slug"`
			CreatedAt    string  `json:"created_at"`
			Missions     []struct {
				Title string `json:"title"`
			} `json:"missions"`
		}
		if err := cli.ReadJSON(resp, &proposals); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "TITLE", "STATUS", "PROPOSED BY", "MISSIONS", "CREATED"}
		var rows [][]string
		for _, p := range proposals {
			proposer := "-"
			if p.ProposerSlug != nil && *p.ProposerSlug != "" {
				proposer = *p.ProposerSlug
			} else if p.ProposerName != nil && *p.ProposerName != "" {
				proposer = *p.ProposerName
			}
			title := p.Title
			if len(title) > 40 {
				title = title[:37] + "..."
			}
			rows = append(rows, []string{
				p.ID[:min(12, len(p.ID))],
				title,
				p.Status,
				proposer,
				fmt.Sprintf("%d", len(p.Missions)),
				p.CreatedAt,
			})
		}
		return f.Auto(proposals, headers, rows)
	},
}

var proposalGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show proposal details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/mission-proposals/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var p struct {
			ID           string  `json:"id"`
			Title        string  `json:"title"`
			Status       string  `json:"status"`
			Description  *string `json:"description"`
			Plan         *string `json:"plan"`
			ProposerName *string `json:"proposer_name"`
			ProposerSlug *string `json:"proposer_slug"`
			ReviewedAt   *string `json:"reviewed_at"`
			ReviewNotes  *string `json:"review_notes"`
			CreatedAt    string  `json:"created_at"`
			Missions     []struct {
				Title string `json:"title"`
				Tasks []struct {
					Title string `json:"title"`
				} `json:"tasks"`
			} `json:"missions"`
		}
		if err := cli.ReadJSON(resp, &p); err != nil {
			return err
		}

		f := newFormatter()
		proposer := "-"
		if p.ProposerSlug != nil {
			proposer = *p.ProposerSlug
		}
		desc := "-"
		if p.Description != nil {
			desc = *p.Description
		}
		reviewedAt := "-"
		if p.ReviewedAt != nil {
			reviewedAt = *p.ReviewedAt
		}
		notes := "-"
		if p.ReviewNotes != nil {
			notes = *p.ReviewNotes
		}

		pairs := [][]string{
			{"Title", p.Title},
			{"ID", p.ID},
			{"Status", p.Status},
			{"Proposed by", proposer},
			{"Description", desc},
			{"Review notes", notes},
			{"Reviewed at", reviewedAt},
			{"Created", p.CreatedAt},
		}
		f.AutoDetail(p, pairs)

		if len(p.Missions) > 0 && f.Format == "table" {
			fmt.Printf("\n%sMISSIONS (%d):%s\n", cli.Bold, len(p.Missions), cli.Reset)
			for i, m := range p.Missions {
				fmt.Printf("  %d. %s (%d tasks)\n", i+1, m.Title, len(m.Tasks))
			}
		}

		return nil
	},
}

var proposalApproveCmd = &cobra.Command{
	Use:   "approve <id>",
	Short: "Approve a mission proposal (creates the missions)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		notes, _ := cmd.Flags().GetString("notes")
		body := map[string]interface{}{}
		if notes != "" {
			body["review_notes"] = notes
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/mission-proposals/"+args[0]+"/approve", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			ProposalID string   `json:"proposal_id"`
			Status     string   `json:"status"`
			MissionIDs []string `json:"mission_ids"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Proposal approved — %d mission(s) created: %v", len(result.MissionIDs), result.MissionIDs))
		return nil
	},
}

var proposalRejectCmd = &cobra.Command{
	Use:   "reject <id>",
	Short: "Reject a mission proposal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		notes, _ := cmd.Flags().GetString("notes")
		body := map[string]interface{}{}
		if notes != "" {
			body["review_notes"] = notes
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/mission-proposals/"+args[0]+"/reject", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Proposal rejected.")
		return nil
	},
}

var proposalDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a mission proposal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete proposal %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/mission-proposals/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Proposal deleted.")
		return nil
	},
}

func init() {
	proposalListCmd.Flags().String("status", "", "Filter by status: PENDING|APPROVED|REJECTED")

	proposalApproveCmd.Flags().String("notes", "", "Review notes")
	proposalRejectCmd.Flags().String("notes", "", "Review notes")
	proposalDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	proposalCmd.AddCommand(proposalListCmd)
	proposalCmd.AddCommand(proposalGetCmd)
	proposalCmd.AddCommand(proposalApproveCmd)
	proposalCmd.AddCommand(proposalRejectCmd)
	proposalCmd.AddCommand(proposalDeleteCmd)
}
