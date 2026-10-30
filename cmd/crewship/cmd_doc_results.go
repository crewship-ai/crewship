package main

// GET /api/v1/chats/{chatId}/document-results doc chat-results

import "github.com/spf13/cobra"

func newDocResultsCommand() *cobra.Command {
	return &cobra.Command{Use: "chat-results <chat-id>", Short: "List saved documents created from a chat you can access", Example: "  crewship doc chat-results <chat-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/chats/" + pagePathEscape(args[0]) + "/document-results")
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
}
