package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// chatCmd shows the full message log for a chat. Useful when you want
// to read what an agent answered without scrolling the live stream — or
// for archival / sharing of past conversations.
//
// Output is a sequence of "role · timestamp" headers followed by message
// bodies, optionally piped through the streaming markdown renderer so
// the on-disk markdown of past responses lands as styled output. JSON
// path returns the raw message list for scripting.
var chatCmd = &cobra.Command{
	Use:   "chat <chat-id>",
	Short: "Show the full message history of a chat session",
	Long: `Print every message in a chat session — user prompts, assistant
responses, tool calls — as a rendered markdown transcript.

Examples:
  crewship chat c_abc123
  crewship chat c_abc123 --no-markdown    # plain text
  crewship chat c_abc123 --format json | jq '.[] | {role, content}'
  crewship chat c_abc123 --since 24h      # filter by time (client-side)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		chatID := args[0]

		path := "/api/v1/chats/" + url.PathEscape(chatID) + "/messages?limit=500"
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := getJSON(client, path, &body); err != nil {
			return err
		}

		// Apply optional --since filter client-side. Server has no since
		// param on this endpoint and adding it server-side is out of scope
		// for the CLI — filtering N≤500 entries in Go is cheap.
		if since, _ := cmd.Flags().GetString("since"); since != "" {
			t, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			filtered := body.Messages[:0]
			for _, m := range body.Messages {
				ts, _ := m["created_at"].(string)
				if mt, err := time.Parse(time.RFC3339, ts); err != nil || mt.After(t) {
					filtered = append(filtered, m)
				}
			}
			body.Messages = filtered
		}

		jq, _ := cmd.Flags().GetString("filter")
		if jq != "" {
			return emitJSONFiltered(cmd, body.Messages)
		}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(body.Messages)
		case "yaml":
			return f.YAML(body.Messages)
		}

		// Pretty path: use markdown renderer for assistant messages, plain
		// for everything else, with role/timestamp headers.
		md := resolveMarkdownFromCmd(cmd)
		printChatTranscript(body.Messages, md)
		return nil
	},
}

// printChatTranscript renders messages with role-aware styling. Assistant
// messages go through the markdown renderer; user/system stay plain.
func printChatTranscript(messages []map[string]any, md *cli.MarkdownRenderer) {
	if len(messages) == 0 {
		fmt.Printf("%sNo messages.%s\n", cli.Dim, cli.Reset)
		return
	}
	for i, m := range messages {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		ts, _ := m["created_at"].(string)
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			ts = t.Format("2006-01-02 15:04:05")
		}
		if i > 0 {
			fmt.Println()
		}

		color := cli.Cyan
		switch strings.ToLower(role) {
		case "user", "human":
			color = cli.Yellow
		case "assistant", "model":
			color = cli.Green
		case "system":
			color = cli.Dim
		}
		fmt.Printf("%s═══ %s%s%s · %s %s═══%s\n",
			color, cli.Bold, role, cli.Reset+color, ts, color, cli.Reset)

		out := content
		if md != nil && (role == "assistant" || role == "model") {
			out = md.Render(content)
		}
		fmt.Print(out)
		if !strings.HasSuffix(out, "\n") {
			fmt.Println()
		}
	}
}

func init() {
	chatCmd.Flags().String("since", "", "Only show messages newer than this (1h, 24h, RFC3339)")
	chatCmd.Flags().Bool("markdown", false, "Force markdown ANSI styling")
	chatCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling")
	jqExprFlag(chatCmd)
}
