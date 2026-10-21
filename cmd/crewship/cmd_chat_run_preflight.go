package main

import (
	"net/url"

	"github.com/spf13/cobra"
)

func newChatRunPreflightCommand() *cobra.Command {
	return &cobra.Command{Use: "run-preflight <chat-id>", Short: "Read current payer, budgets and predicted gate for the configured chat agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateEntityCLIIdentifier(args[0]); err != nil {
			return err
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out map[string]any
		if err = getJSON(client, "/api/v1/chats/"+url.PathEscape(args[0])+"/run-preflight", &out); err != nil {
			return err
		}
		if filter, _ := cmd.Flags().GetString("filter"); filter != "" {
			return emitJSONFiltered(cmd, out)
		}
		return resolvedFormatter(cmd).Machine(out)
	}}
}
func init() { chatCmd.AddCommand(newChatRunPreflightCommand()) }
