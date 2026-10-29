package main

// GET /api/v1/chats/{chatId}/document-access chat document-access

import (
	"net/url"

	"github.com/spf13/cobra"
)

func newChatDocumentAccessCommand() *cobra.Command {
	return &cobra.Command{Use: "document-access <chat-id>", Short: "Read current document grants and verified task permissions visible in a chat", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateEntityCLIIdentifier(args[0]); err != nil {
			return err
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var result map[string]any
		if err = getJSON(client, "/api/v1/chats/"+url.PathEscape(args[0])+"/document-access", &result); err != nil {
			return err
		}
		if filter, _ := cmd.Flags().GetString("filter"); filter != "" {
			return emitJSONFiltered(cmd, result)
		}
		return resolvedFormatter(cmd).Machine(result)
	}}
}

func init() { chatCmd.AddCommand(newChatDocumentAccessCommand()) }
