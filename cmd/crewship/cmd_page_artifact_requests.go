package main

// GET  /api/v1/pages/{slug}/artifact/requests                     page artifact requests list
// POST /api/v1/pages/{slug}/artifact/requests                     page artifact requests send
// POST /api/v1/pages/{slug}/artifact/requests/{requestId}/cancel  page artifact requests cancel

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newPageArtifactRequestsCommand() *cobra.Command {
	group := &cobra.Command{Use: "requests", Short: "Explicitly ask an agent to update an artifact and inspect the result"}
	for _, verb := range []string{"list", "send", "cancel"} {
		verb := verb
		use, argc := verb+" <slug>", 1
		if verb == "cancel" {
			use, argc = verb+" <slug> <request-id>", 2
		}
		cmd := &cobra.Command{Use: use, Args: cobra.ExactArgs(argc), Short: map[string]string{
			"list":   "List durable artifact requests and published results",
			"send":   "Send an explicit revision-bound instruction to an existing chat's agent",
			"cancel": "Cancel this request's authority to publish an artifact update",
		}[verb], RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if verb == "send" {
				chat, _ := cmd.Flags().GetString("chat")
				instruction, _ := cmd.Flags().GetString("instruction")
				base, _ := cmd.Flags().GetInt64("base-rev")
				if strings.TrimSpace(chat) == "" {
					return fmt.Errorf("--chat is required")
				}
				if !cmd.Flags().Changed("base-rev") || base < 1 {
					return fmt.Errorf("--base-rev is required and must be positive")
				}
				if strings.TrimSpace(instruction) == "" || !utf8.ValidString(instruction) || len(instruction) > 4096 {
					return fmt.Errorf("--instruction must be nonempty UTF-8 text of at most 4 KiB")
				}
				submission, _ := cmd.Flags().GetString("submission-id")
				if cmd.Flags().Changed("submission-id") && strings.TrimSpace(submission) == "" {
					return fmt.Errorf("--submission-id must not be empty")
				}
				if submission == "" {
					submission = uuid.NewString()
				}
				retryOf, _ := cmd.Flags().GetString("retry-of")
				if cmd.Flags().Changed("retry-of") && (strings.TrimSpace(retryOf) == "" || len(retryOf) > 128) {
					return fmt.Errorf("--retry-of must identify a prior failed or cancelled request (at most 128 bytes)")
				}
				body = map[string]any{"chat_id": chat, "instruction": instruction, "base_rev": base, "submission_id": submission}
				if retryOf != "" {
					body["retry_of_request_id"] = retryOf
				}
				thread, _ := cmd.Flags().GetString("thread")
				if thread != "" {
					body["thread_id"] = thread
				}
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			var response *http.Response
			switch verb {
			case "list":
				response, err = client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact/requests")
			case "send":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/requests", body)
			case "cancel":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/requests/"+pagePathEscape(args[1])+"/cancel", body)
			}
			if err != nil {
				if verb == "send" {
					return fmt.Errorf("%w; retry the same instruction and source with --submission-id %q", err, body["submission_id"])
				}
				return err
			}
			if err := printPageArtifactResponse(cmd, response); err != nil {
				if verb == "send" {
					return fmt.Errorf("%w; retry the same instruction and source with --submission-id %q", err, body["submission_id"])
				}
				return err
			}
			return nil
		}}
		if verb == "send" {
			cmd.Flags().String("chat", "", "Required existing human-accessible chat selecting the target agent")
			cmd.Flags().String("instruction", "", "Explicit instruction; sending may incur agent usage costs")
			cmd.Flags().Int64("base-rev", 0, "Required immutable source revision; stale publication is rejected")
			cmd.Flags().String("retry-of", "", "Prior failed/cancelled request; starts new work with a fresh snapshot and may duplicate an unconfirmed run")
			cmd.Flags().String("thread", "", "Optional discussion thread to snapshot with this request")
			cmd.Flags().String("submission-id", "", "Stable ID for retrying this exact submission; generated when omitted")
		}
		group.AddCommand(cmd)
	}
	return group
}
