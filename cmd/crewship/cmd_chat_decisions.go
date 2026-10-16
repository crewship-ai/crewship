package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/spf13/cobra"
)

func newChatDecisionCommand() *cobra.Command {
	group := &cobra.Command{Use: "decision", Short: "Read and answer existing source-backed chat decisions"}
	for _, name := range []string{"list", "show", "answer"} {
		name := name
		use := name + " <chat-id>"
		argsCount := 1
		if name != "list" {
			use += " <source-type> <source-id>"
			argsCount = 3
		}
		cmd := &cobra.Command{Use: use, Short: map[string]string{"list": "Read authorized replacement decision snapshots", "show": "Read a decision and its current source version", "answer": "Answer the reviewed source version with a stable operation key"}[name], Args: cobra.ExactArgs(argsCount), RunE: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if err := validateEntityCLIIdentifier(arg); err != nil {
					return err
				}
			}
			client, err := requireAuthAndWorkspace()
			if err != nil {
				return err
			}
			path := "/api/v1/chats/" + url.PathEscape(args[0]) + "/decisions"
			if name != "list" {
				path += "/" + url.PathEscape(args[1]) + "/" + url.PathEscape(args[2])
			} else {
				limit, _ := cmd.Flags().GetInt("limit")
				if limit < 1 || limit > 100 {
					return fmt.Errorf("--limit must be between1 and100")
				}
				cursor, _ := cmd.Flags().GetString("cursor")
				params := url.Values{"limit": {fmt.Sprint(limit)}}
				if cursor != "" {
					params.Set("cursor", cursor)
				}
				path += "?" + params.Encode()
			}
			var result map[string]any
			if name == "answer" {
				version, _ := cmd.Flags().GetString("expected-version")
				key, _ := cmd.Flags().GetString("idempotency-key")
				text, _ := cmd.Flags().GetString("text")
				if strings.TrimSpace(key) == "" || len(key) > 200 || strings.TrimSpace(version) == "" || text == "" {
					return fmt.Errorf("--expected-version, --idempotency-key and --text are required")
				}
				raw, _ := json.Marshal(text)
				body := chatentities.DecisionAnswer{SourceID: args[2], ExpectedVersion: version, Action: "answer", Values: map[string]json.RawMessage{"answer": raw}}
				err = postJSON(client.WithHeader("Idempotency-Key", key), path+"/answer", body, &result)
			} else {
				err = getJSON(client, path, &result)
			}
			if err != nil {
				return err
			}
			return resolvedFormatter(cmd).Machine(result)
		}}
		if name == "list" {
			cmd.Flags().Int("limit", 100, "Maximum source cards per page (1–100)")
			cmd.Flags().String("cursor", "", "Opaque next_cursor from the preceding page")
		}
		if name == "answer" {
			cmd.Flags().String("expected-version", "", "Opaque reviewed source version")
			cmd.Flags().String("idempotency-key", "", "Stable operation key; reuse on uncertain retry")
			cmd.Flags().String("text", "", "Information answer (TEXT escalations only)")
		}
		group.AddCommand(cmd)
	}
	return group
}
func init() { chatCmd.AddCommand(newChatDecisionCommand()) }
