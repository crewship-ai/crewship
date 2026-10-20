package main

// GET /api/v1/chats/{chatId}/artifact-context/preview chat artifact-preview

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newChatArtifactPreviewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "artifact-preview <chat-id>", Short: "Preview your personal Document's audience and confirmed revision without sending a change",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			page, _ := cmd.Flags().GetString("page-id")
			if err := validateEntityCLIIdentifier(args[0]); err != nil {
				return err
			}
			if err := validateEntityCLIIdentifier(page); err != nil {
				return fmt.Errorf("--page-id: %w", err)
			}
			client, err := requireAuthAndWorkspace()
			if err != nil {
				return err
			}
			// Do not follow another resource or retry with a changed target. The
			// response describes exactly the chat and immutable Page requested.
			httpClient := *client.HTTPClient
			httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
				return fmt.Errorf("Document context preview redirects are not accepted")
			}
			client.HTTPClient = &httpClient
			path := "/api/v1/chats/" + url.PathEscape(args[0]) + "/artifact-context/preview?page_id=" + url.QueryEscape(page)
			response, err := client.Get(path)
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		},
	}
	cmd.Flags().String("page-id", "", "Immutable ID of your saved personal Document; slugs are not resolved")
	_ = cmd.MarkFlagRequired("page-id")
	return cmd
}

func init() { chatCmd.AddCommand(newChatArtifactPreviewCommand()) }
