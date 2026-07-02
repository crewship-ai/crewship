package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// feedbackCmd exposes the typed feedback-signal API (thumbs / edit /
// regenerate bound to a message or trace) over the CLI. The endpoints
// backed the chat UI only; agents recording eval signal on their own
// outputs now have a first-class command.

// feedbackRow mirrors the wire shape of /api/v1/feedback rows.
type feedbackRow struct {
	ID        string  `json:"id"`
	MessageID string  `json:"message_id"`
	ChatID    *string `json:"chat_id,omitempty"`
	TraceID   *string `json:"trace_id,omitempty"`
	Signal    string  `json:"signal"`
	Reason    *string `json:"reason,omitempty"`
	UserID    *string `json:"user_id,omitempty"`
	CreatedAt string  `json:"created_at"`
}

var feedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Record and inspect typed feedback signals on messages",
	Long: `Feedback is structured eval signal (helpful / not_helpful / inaccurate /
unsafe / edit / regenerate) bound to a message or trace — distinct from
chat reactions, which are social UI signal.

Examples:
  crewship feedback create --message <id> --signal not_helpful --reason "wrong file"
  crewship feedback list --message <id>
  crewship feedback list --trace <trace-id>
  crewship feedback delete --message <id> --signal not_helpful`,
}

var feedbackCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Record a feedback signal on a message",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		messageID, _ := cmd.Flags().GetString("message")
		signal, _ := cmd.Flags().GetString("signal")
		if messageID == "" || signal == "" {
			return fmt.Errorf("--message and --signal are required")
		}
		body := map[string]any{"message_id": messageID, "signal": signal}
		if v, _ := cmd.Flags().GetString("chat"); v != "" {
			body["chat_id"] = v
		}
		if v, _ := cmd.Flags().GetString("trace"); v != "" {
			body["trace_id"] = v
		}
		if v, _ := cmd.Flags().GetString("reason"); v != "" {
			body["reason"] = v
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/feedback", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// The server returns only the persisted row id; echo the inputs
		// back so machine output is self-describing.
		var created struct {
			ID string `json:"id"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		f := newFormatter()
		out := map[string]string{"id": created.ID, "message_id": messageID, "signal": signal}
		pairs := [][]string{
			{"ID", created.ID},
			{"Message", messageID},
			{"Signal", signal},
		}
		return f.AutoDetail(out, pairs)
	},
}

var feedbackListCmd = &cobra.Command{
	Use:   "list",
	Short: "List feedback signals for a message or trace",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		messageID, _ := cmd.Flags().GetString("message")
		traceID, _ := cmd.Flags().GetString("trace")
		if (messageID == "") == (traceID == "") {
			return fmt.Errorf("exactly one of --message or --trace is required")
		}
		q := url.Values{}
		if messageID != "" {
			q.Set("message_id", messageID)
		}
		if traceID != "" {
			q.Set("trace_id", traceID)
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/feedback?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// The server wraps rows in {"feedback": [...]}.
		var envelope struct {
			Feedback []feedbackRow `json:"feedback"`
		}
		if err := cli.ReadJSON(resp, &envelope); err != nil {
			return err
		}
		rows := envelope.Feedback
		if rows == nil {
			rows = []feedbackRow{} // "[]", never "null"
		}
		f := newFormatter()
		headers := []string{"ID", "MESSAGE", "SIGNAL", "REASON", "CREATED"}
		table := make([][]string, 0, len(rows))
		for _, r := range rows {
			reason := ""
			if r.Reason != nil {
				reason = *r.Reason
			}
			table = append(table, []string{r.ID, r.MessageID, r.Signal, reason, r.CreatedAt})
		}
		return f.Auto(rows, headers, table)
	},
}

var feedbackDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Remove a feedback signal from a message",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		messageID, _ := cmd.Flags().GetString("message")
		signal, _ := cmd.Flags().GetString("signal")
		if messageID == "" || signal == "" {
			return fmt.Errorf("--message and --signal are required")
		}
		q := url.Values{}
		q.Set("message_id", messageID)
		q.Set("signal", signal)
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/feedback?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess("Feedback removed.")
		return nil
	},
}

func init() {
	feedbackCreateCmd.Flags().String("message", "", "message id the signal binds to (required)")
	feedbackCreateCmd.Flags().String("signal", "", "signal type: helpful | not_helpful | inaccurate | unsafe | edit | regenerate (required)")
	feedbackCreateCmd.Flags().String("chat", "", "chat id (optional)")
	feedbackCreateCmd.Flags().String("trace", "", "trace id (optional)")
	feedbackCreateCmd.Flags().String("reason", "", "free-text reason (optional)")

	feedbackListCmd.Flags().String("message", "", "list signals for this message id")
	feedbackListCmd.Flags().String("trace", "", "list signals for this trace id")

	feedbackDeleteCmd.Flags().String("message", "", "message id (required)")
	feedbackDeleteCmd.Flags().String("signal", "", "signal type to remove (required)")

	feedbackCmd.AddCommand(feedbackCreateCmd)
	feedbackCmd.AddCommand(feedbackListCmd)
	feedbackCmd.AddCommand(feedbackDeleteCmd)
	rootCmd.AddCommand(feedbackCmd)
}
