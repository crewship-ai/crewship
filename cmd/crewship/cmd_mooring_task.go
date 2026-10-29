package main

// POST /api/v1/pages/{slug}/mooring/tasks mooring task

import (
	"errors"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/spf13/cobra"
)

func newMooringTaskCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "task <page-slug|@ID>", Short: "Run an original stored chat message in an existing document", Long: "Use an original stored message authored by the current human. The agent must already have document write access and workspace policy must allow it. Acceptance queues work; it is not a Saved receipt.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := humanWorkbookClient()
		if err != nil {
			return err
		}
		return runMooringTask(cmd, args[0], client)
	}}
	cmd.Flags().String("chat", "", "Original chat ID")
	cmd.Flags().String("message", "", "Original stored message ID")
	return cmd
}
func runMooringTask(cmd *cobra.Command, page string, client *cli.Client) error {
	chat, _ := cmd.Flags().GetString("chat")
	message, _ := cmd.Flags().GetString("message")
	if !mooring.ValidWireIdentifier(chat) || !mooring.ValidWireIdentifier(message) {
		return errors.New("--chat and --message must identify an original stored message")
	}
	response, err := client.Post("/api/v1/pages/"+pagePathEscape(page)+"/mooring/tasks", map[string]string{"chat_id": chat, "message_id": message})
	if err != nil {
		return err
	}
	return printPageArtifactResponse(cmd, response)
}
