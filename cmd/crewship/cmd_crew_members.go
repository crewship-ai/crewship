package main

// Crew member management commands. Extracted from cmd_crew.go.

import (
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewMemberCmd = &cobra.Command{
	Use:     "member",
	Aliases: []string{"members"},
	Short:   "Manage crew members",
}

var crewMemberListCmd = &cobra.Command{
	Use:   "list <crew-slug-or-id>",
	Short: "List crew members",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/crews/" + crewID + "/members")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var members []struct {
			ID   string `json:"id"`
			User struct {
				ID       string `json:"id"`
				Email    string `json:"email"`
				FullName string `json:"full_name"`
			} `json:"user"`
			JoinedAt string `json:"joined_at"`
		}
		if err := cli.ReadJSON(resp, &members); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "USER", "EMAIL", "JOINED"}
		var rows [][]string
		for _, m := range members {
			rows = append(rows, []string{m.ID, m.User.FullName, m.User.Email, m.JoinedAt})
		}
		return f.Auto(members, headers, rows)
	},
}

var crewMemberAddCmd = &cobra.Command{
	Use:   "add <crew-slug-or-id> <user-id>",
	Short: "Add a user to a crew",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/members", map[string]string{
			"user_id": args[1],
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Member added to crew.")
		return nil
	},
}

var crewMemberRemoveCmd = &cobra.Command{
	Use:   "remove <crew-slug-or-id> <member-id>",
	Short: "Remove a member from a crew",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/crews/" + crewID + "/members/" + args[1])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Member removed from crew.")
		return nil
	},
}
