package main

// GET /api/v1/chats/{chatId}/links chat artifact-links

import (
	"net/url"

	"github.com/spf13/cobra"
)

func newChatArtifactLinksCommand() *cobra.Command {
	return &cobra.Command{
		Use: "artifact-links <chat-id>", Short: "List saved native Documents linked to a chat, opening their latest content",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateEntityCLIIdentifier(args[0]); err != nil {
				return err
			}
			client, err := requireAuthAndWorkspace()
			if err != nil {
				return err
			}
			response, err := client.Get("/api/v1/chats/" + url.PathEscape(args[0]) + "/links")
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		},
	}
}

func init() { chatCmd.AddCommand(newChatArtifactLinksCommand()) }
