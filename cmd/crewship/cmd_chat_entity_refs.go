package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newChatRefsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "refs <chat-id>", Short: "Read a viewer-authorized page of durable chat entity cards", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--limit must be between 1 and 100")
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		query := url.Values{"limit": {fmt.Sprint(limit)}}
		cursor, _ := cmd.Flags().GetString("cursor")
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var page map[string]any
		if err = getJSON(client, "/api/v1/chats/"+url.PathEscape(args[0])+"/entity-refs?"+query.Encode(), &page); err != nil {
			return err
		}
		return emitJSONFiltered(cmd, page)
	}}
	cmd.Flags().Int("limit", 100, "Maximum number of cards (1–100)")
	cmd.Flags().String("cursor", "", "Opaque next_cursor from the preceding page")
	return cmd
}
func newOperationCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "operation", Short: "Inspect durable, authorized agent operations"}
	cmd.AddCommand(&cobra.Command{Use: "get <operation-id>", Short: "Read an operation without exposing internal authority or stored payloads", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var result map[string]any
		if err = getJSON(client, "/api/v1/operations/"+url.PathEscape(args[0]), &result); err != nil {
			return err
		}
		return emitJSONFiltered(cmd, result)
	}})
	return cmd
}
func init() { chatCmd.AddCommand(newChatRefsCommand()); rootCmd.AddCommand(newOperationCommand()) }
