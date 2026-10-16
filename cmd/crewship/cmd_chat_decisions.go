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

			var result map[string]any
			if name == "list" {
				limit, _ := cmd.Flags().GetInt("limit")
				if limit < 1 || limit > 100 {
					return fmt.Errorf("--limit must be between1 and100")
				}
				cursor, _ := cmd.Flags().GetString("cursor")
				params := url.Values{"limit": {fmt.Sprint(limit)}}
				if cursor != "" {
					params.Set("cursor", cursor)
				}
				err = getJSON(client, chatDecisionListPath(args[0])+"?"+params.Encode(), &result)
			} else if name == "answer" {
				version, _ := cmd.Flags().GetString("expected-version")
				key, _ := cmd.Flags().GetString("idempotency-key")
				text, _ := cmd.Flags().GetString("text")
				action, _ := cmd.Flags().GetString("action")
				actionID, _ := cmd.Flags().GetString("action-id")
				valuesJSON, _ := cmd.Flags().GetString("values-json")
				if strings.TrimSpace(key) == "" || len(key) > 200 || strings.TrimSpace(version) == "" {
					return fmt.Errorf("--expected-version and --idempotency-key are required")
				}
				body := chatentities.DecisionAnswer{SourceID: args[2], ExpectedVersion: version, Values: map[string]json.RawMessage{}}
				switch args[1] {
				case "escalation":
					if text == "" || action != "" || actionID != "" || cmd.Flags().Changed("values-json") {
						return fmt.Errorf("TEXT escalation requires only --text")
					}
					raw, _ := json.Marshal(text)
					body.Action = "answer"
					body.Values["answer"] = raw
				case "waitpoint":
					if actionID == "" || text != "" || action != "" {
						return fmt.Errorf("waitpoint requires --action-id and optional --values-json")
					}
					if err = json.Unmarshal([]byte(valuesJSON), &body.Values); err != nil || body.Values == nil {
						return fmt.Errorf("--values-json must be a JSON object of typed field values")
					}
					body.Action = "answer"
					body.ActionID = actionID
				case "run_approval":
					if (action != "approve" && action != "reject") || text != "" || actionID != "" || cmd.Flags().Changed("values-json") {
						return fmt.Errorf("whole-run approval requires only --action approve or reject")
					}
					body.Action = action
				default:
					return fmt.Errorf("unsupported decision source type")
				}
				err = postJSON(client.WithHeader("Idempotency-Key", key), chatDecisionSourcePath(args[0], args[1], args[2])+"/answer", body, &result)
			} else {
				err = getJSON(client, chatDecisionSourcePath(args[0], args[1], args[2]), &result)
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
			cmd.Flags().String("action", "", "Whole-run decision: approve or reject (run_approval only)")
			cmd.Flags().String("action-id", "", "Declared frozen workflow action (waitpoint only)")
			cmd.Flags().String("values-json", "{}", "JSON object of typed workflow field values (waitpoint only)")
		}
		group.AddCommand(cmd)
	}
	return group
}
func init() { chatCmd.AddCommand(newChatDecisionCommand()) }

func chatDecisionListPath(chat string) string {
	return "/api/v1/chats/" + url.PathEscape(chat) + "/decisions"
}
func chatDecisionSourcePath(chat, source, id string) string {
	return "/api/v1/chats/" + url.PathEscape(chat) + "/decisions/" + url.PathEscape(source) + "/" + url.PathEscape(id)
}
