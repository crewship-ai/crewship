package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

func newChatProposalCommand() *cobra.Command {
	group := &cobra.Command{Use: "proposal", Short: "Review durable agent issue proposals"}
	for _, action := range []string{"list", "show", "edit", "confirm", "discard"} {
		action := action
		use := action + " <chat-id> <proposal-id>"
		n := 2
		if action == "list" {
			use = action + " <chat-id>"
			n = 1
		}
		cmd := &cobra.Command{Use: use, Short: action + " a chat proposal", Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
			for _, id := range args {
				if err := validateEntityCLIIdentifier(id); err != nil {
					return err
				}
			}
			client, err := requireAuthAndWorkspace()
			if err != nil {
				return err
			}
			path := "/api/v1/chats/" + url.PathEscape(args[0]) + "/proposals"
			if len(args) > 1 {
				path += "/" + url.PathEscape(args[1])
			}
			method := "GET"
			var body any
			if action == "list" {
				cursor, _ := cmd.Flags().GetString("cursor")
				if cursor != "" {
					path += "?cursor=" + url.QueryEscape(cursor)
				}
			}
			if action == "edit" || action == "confirm" || action == "discard" {
				key, _ := cmd.Flags().GetString("idempotency-key")
				if key == "" || len(key) > 200 {
					return fmt.Errorf("--idempotency-key is required (maximum 200 bytes); reuse it for retries")
				}
				version, _ := cmd.Flags().GetInt64("version")
				if version < 1 || version > 1<<53-1 {
					return fmt.Errorf("--version must be the reviewed proposal version")
				}
				client = client.WithHeader("Idempotency-Key", key)
				method = "POST"
				path += "/" + action
				switch action {
				case "confirm":
					body = chatentities.ProposalConfirm{ProposalID: args[1], ReviewedVersion: version}
				case "discard":
					body = chatentities.ProposalDiscard{ExpectedVersion: version}
				case "edit":
					file, _ := cmd.Flags().GetString("file")
					if file == "" {
						return fmt.Errorf("--file is required")
					}
					raw, e := os.ReadFile(file)
					if e != nil {
						return e
					}
					var edit chatentities.ProposalCreate
					if e = json.Unmarshal(raw, &edit); e != nil {
						return e
					}
					body = chatentities.ProposalEdit{ExpectedVersion: version, Title: edit.Title, Items: edit.Items}
					method = "PATCH"
					path = path[:len(path)-len("/edit")]
				}
			}
			resp, err := client.Do(method, path, body)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			var result map[string]any
			if err = cli.ReadJSON(resp, &result); err != nil {
				return err
			}
			return resolvedFormatter(cmd).Machine(result)
		}}
		if action == "list" {
			cmd.Flags().String("cursor", "", "Opaque next_cursor from the preceding page")
		}
		if action == "edit" || action == "confirm" || action == "discard" {
			cmd.Flags().String("idempotency-key", "", "Stable operation key; retain after an unknown outcome")
			cmd.Flags().Int64("version", 0, "Version explicitly reviewed by the human")
		}
		if action == "edit" {
			cmd.Flags().String("file", "", "JSON file containing title and complete issue items")
		}
		group.AddCommand(cmd)
	}
	return group
}
func init() { chatCmd.AddCommand(newChatProposalCommand()) }
