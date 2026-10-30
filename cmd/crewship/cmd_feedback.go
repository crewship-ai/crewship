package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// feedbackCmd exposes the typed feedback-signal API (thumbs / edit /
// regenerate bound to a message or trace) over the CLI. The endpoints
// backed the chat UI only; agents recording eval signal on their own
// outputs now have a first-class command.

// feedbackRow mirrors the wire shape of /api/v1/feedback rows.
type feedbackRow struct {
	ID          string         `json:"id" yaml:"id"`
	WorkspaceID string         `json:"workspace_id" yaml:"workspace_id"`
	MessageID   string         `json:"message_id" yaml:"message_id"`
	ChatID      *string        `json:"chat_id,omitempty" yaml:"chat_id,omitempty"`
	TraceID     *string        `json:"trace_id,omitempty" yaml:"trace_id,omitempty"`
	Signal      string         `json:"signal" yaml:"signal"`
	Reason      *string        `json:"reason,omitempty" yaml:"reason,omitempty"`
	UserID      *string        `json:"user_id,omitempty" yaml:"user_id,omitempty"`
	CreatedAt   string         `json:"created_at" yaml:"created_at"`
	Revision    int            `json:"revision" yaml:"revision"`
	Memory      feedbackMemory `json:"memory" yaml:"memory"`
}

type feedbackMemory struct {
	Status  string `json:"status" yaml:"status"`
	EntryID string `json:"entry_id,omitempty" yaml:"entry_id,omitempty"`
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
			ID       string         `json:"id" yaml:"id"`
			Revision int            `json:"revision" yaml:"revision"`
			Memory   feedbackMemory `json:"memory" yaml:"memory"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		f := newFormatter()
		out := map[string]any{"id": created.ID, "message_id": messageID, "signal": signal, "revision": created.Revision, "memory": created.Memory}
		pairs := [][]string{
			{"ID", created.ID},
			{"Message", messageID},
			{"Signal", signal},
			{"Memory", created.Memory.Status},
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
			Feedback []feedbackRow `json:"feedback" yaml:"feedback"`
		}
		if err := cli.ReadJSON(resp, &envelope); err != nil {
			return err
		}
		rows := envelope.Feedback
		if rows == nil {
			rows = []feedbackRow{} // "[]", never "null"
		}
		f := newFormatter()
		headers := []string{"ID", "WORKSPACE", "MESSAGE", "SIGNAL", "REASON", "MEMORY", "CREATED"}
		table := make([][]string, 0, len(rows))
		for _, r := range rows {
			reason := ""
			if r.Reason != nil {
				reason = *r.Reason
			}
			table = append(table, []string{r.ID, r.WorkspaceID, r.MessageID, r.Signal, reason, r.Memory.Status, r.CreatedAt})
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
		client := newAPIClient()
		q := url.Values{}
		q.Set("message_id", messageID)
		q.Set("signal", signal)
		q.Set("workspace_id", client.GetWorkspaceID())
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

// feedbackPreferenceCatalogueV1 is usermodel.FeedbackPreferenceOptionsV1's
// keys, restated rather than imported: the CLI-side commands stay out of the
// daemon packages, which the clionly build leaves out with cmd_start.go.
// TestFeedbackPreferenceCatalogueMatchesServer pins the two together.
var feedbackPreferenceCatalogueV1 = []string{
	"Next time, keep answers concise.",
	"Next time, start with the conclusion.",
	"Příště piš stručněji.",
	"Příště nejdřív ukaž závěr.",
}

// feedbackPreferenceOps are the three self-service preference commands over
// the agent peers API, each with its own help.
var feedbackPreferenceOps = []struct {
	use, short string
}{
	{"preferences", "List your explicit preferences recorded for one agent"},
	{"edit-preference", "Change the reason of one of your preferences (exact catalogue v1 text)"},
	{"delete-preference", "Withdraw one of your preferences for an agent"},
}

// feedbackSubjectID is the caller's own user id: the {userId} the peers
// routes only accept for their own subject.
func feedbackSubjectID(client *cli.Client) (string, error) {
	resp, err := client.Get("/api/v1/users/me/peer-consent")
	if err != nil {
		return "", err
	}
	if err = cli.CheckError(resp); err != nil {
		return "", err
	}
	var subject struct {
		UserID string `json:"user_id" yaml:"user_id"`
	}
	if err = cli.ReadJSON(resp, &subject); err != nil {
		return "", err
	}
	if subject.UserID == "" {
		return "", fmt.Errorf("server did not name the current user")
	}
	return subject.UserID, nil
}

// feedbackAgentFlag resolves --agent (slug or id) to the id the peers routes
// key on: the route takes an id, and a slug used to go out verbatim and 404.
func feedbackAgentFlag(cmd *cobra.Command, client *cli.Client) (string, error) {
	agent, _ := cmd.Flags().GetString("agent")
	if strings.TrimSpace(agent) == "" {
		return "", fmt.Errorf("--agent is required")
	}
	return resolveAgentID(client, agent)
}

func init() {
	for _, operation := range feedbackPreferenceOps {
		op := operation.use
		c := &cobra.Command{Use: op, Short: operation.short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := requireAuthAndWorkspace()
			if err != nil {
				return err
			}
			agent, err := feedbackAgentFlag(cmd, client)
			if err != nil {
				return err
			}
			path := "/api/v1/agents/" + url.PathEscape(agent) + "/peers"
			if op == "preferences" {
				resp, err := client.Get(path)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if err = cli.CheckError(resp); err != nil {
					return err
				}
				var out struct {
					Preferences []map[string]any `json:"preferences" yaml:"preferences"`
				}
				if err = cli.ReadJSON(resp, &out); err != nil {
					return err
				}
				rows := [][]string{}
				for _, p := range out.Preferences {
					rows = append(rows, []string{fmt.Sprint(p["id"]), fmt.Sprint(p["revision"]), fmt.Sprint(p["status"]), fmt.Sprint(p["value"]), fmt.Sprint(p["reason"])})
				}
				return newFormatter().Auto(out.Preferences, []string{"ID", "REVISION", "STATUS", "VALUE", "REASON"}, rows)
			}
			entry, _ := cmd.Flags().GetString("entry")
			revision, _ := cmd.Flags().GetInt("revision")
			if entry == "" || revision < 1 {
				return fmt.Errorf("--entry and --revision are required")
			}
			reason, _ := cmd.Flags().GetString("reason")
			if op == "edit-preference" && !slices.Contains(feedbackPreferenceCatalogueV1, reason) {
				return fmt.Errorf("--reason must be one of the catalogue v1 preferences: %q", feedbackPreferenceCatalogueV1)
			}
			userID, err := feedbackSubjectID(client)
			if err != nil {
				return err
			}
			entryPath := path + "/" + url.PathEscape(userID)
			var resp *http.Response
			if op == "delete-preference" {
				q := url.Values{"entry_id": {entry}, "revision": {fmt.Sprint(revision)}}
				resp, err = client.Delete(entryPath + "?" + q.Encode())
			} else {
				resp, err = client.Patch(entryPath, map[string]any{"entry_id": entry, "revision": revision, "reason": reason})
			}
			if err != nil {
				return err
			}
			if err = cli.CheckError(resp); err != nil {
				return err
			}
			raw, err := readServerDocument(resp)
			if err != nil {
				return err
			}
			// The route answers 204. A script asking for JSON gets what was
			// asked of it; the new revision is the server's to assign, so it
			// is read back with `feedback preferences`, not guessed here.
			result := map[string]any{"agent_id": agent, "entry_id": entry, "revision": revision}
			done := "Preference updated."
			if op == "delete-preference" {
				result["withdrawn"] = true
				done = "Preference withdrawn."
			} else {
				result["updated"] = true
				result["reason"] = reason
			}
			if handled, err := machineMutationResult(cmd, raw, result); handled {
				return err
			}
			cli.PrintSuccess(done)
			return nil
		}}
		c.Flags().String("agent", "", "agent slug or id (required)")
		if op != "preferences" {
			c.Flags().String("entry", "", "preference entry id (required)")
			c.Flags().Int("revision", 0, "expected entry revision (required)")
		}
		if op == "edit-preference" {
			c.Flags().String("reason", "", "exact catalogue v1 preference: "+strings.Join(quoteAll(feedbackPreferenceCatalogueV1), " | "))
		}
		feedbackCmd.AddCommand(c)
	}
	feedbackPeerCardCmd.Flags().String("agent", "", "agent slug or id (required)")
	feedbackCmd.AddCommand(feedbackPeerCardCmd)
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = fmt.Sprintf("%q", v)
	}
	return out
}

// feedbackPeerCardCmd reads the caller's own peer card on one agent: the
// Markdown the agent keeps about them. Self-only and audited server-side —
// every read lands in peer_card_audit.
var feedbackPeerCardCmd = &cobra.Command{
	Use:   "peer-card",
	Short: "Show the full peer card an agent keeps about you",
	Long: `Print the peer card one agent keeps about you: the Markdown it reads before
working with you. Only your own card is readable, and the server records every
read in the peer-card audit log.

Backed by GET /api/v1/agents/{agentId}/peers/{userId}; your user id is looked
up from the server. An agent with no card about you exits non-zero (404).

Examples:
  crewship feedback peer-card --agent alex
  crewship feedback peer-card --agent alex --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agent, err := feedbackAgentFlag(cmd, client)
		if err != nil {
			return err
		}
		userID, err := feedbackSubjectID(client)
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/agents/" + url.PathEscape(agent) + "/peers/" + url.PathEscape(userID))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		raw, err := readServerDocument(resp)
		if err != nil {
			return err
		}
		var card struct {
			Content string `json:"content" yaml:"content"`
		}
		if err := json.Unmarshal(raw, &card); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(cli.RawJSON(raw), func() {
			out := cmd.OutOrStdout()
			fmt.Fprint(out, card.Content)
			if !strings.HasSuffix(card.Content, "\n") {
				fmt.Fprintln(out)
			}
		})
	},
}
