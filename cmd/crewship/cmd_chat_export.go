package main

// `crewship chat <id> --parts` and `crewship chat export`.
//
// A message's `content` is the flattened text of its turn; `parts` is the
// ordered structure the web chat renders — thinking, each tool call and its
// result, images — and `author_user_id` names which human wrote a line in a
// group chat (internal/conversation/store.go Message). The plain transcript
// prints only content, so a turn that did its work in tools reads as a short
// answer with nothing behind it. --parts prints the structure; export writes
// the same Markdown the chat's Export dialog downloads.

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// chatMessagePart is one conversation.Part as the messages endpoint sends it.
type chatMessagePart struct {
	Type     string
	Content  string
	ToolName string
}

func chatMessageParts(m map[string]any) []chatMessagePart {
	raw, _ := m["parts"].([]any)
	parts := make([]chatMessagePart, 0, len(raw))
	for _, item := range raw {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mp := chatMessagePart{}
		mp.Type, _ = p["type"].(string)
		mp.Content, _ = p["content"].(string)
		mp.ToolName, _ = p["tool_name"].(string)
		parts = append(parts, mp)
	}
	if len(parts) == 0 {
		// A message written before the parts model: its content is its one
		// text part (conversation.Message.NormalizedParts).
		if c, _ := m["content"].(string); c != "" {
			parts = append(parts, chatMessagePart{Type: "text", Content: c})
		}
	}
	return parts
}

// printChatParts renders one message's parts for `chat <id> --parts`. Text is
// rendered (markdown for the assistant), everything else is one dim line.
func printChatParts(m map[string]any, md *cli.MarkdownRenderer, assistant bool) {
	for _, p := range chatMessageParts(m) {
		switch p.Type {
		case "text":
			out := p.Content
			if md != nil && assistant {
				out = md.Render(p.Content)
			} else {
				out = sanitizeTerminal(out)
			}
			fmt.Print(out)
			if !strings.HasSuffix(out, "\n") {
				fmt.Println()
			}
		case "thinking":
			fmt.Printf("%s[thinking]%s %s\n", cli.Gray, cli.Reset, truncate(sanitizeTerminal(p.Content), 200))
		case "tool_call":
			name := p.ToolName
			if name == "" {
				name = "tool"
			}
			fmt.Printf("%s[tool %s]%s %s\n", cli.Cyan, sanitizeTerminal(name), cli.Reset, truncate(sanitizeTerminal(p.Content), 200))
		case "tool_result":
			fmt.Printf("%s[result]%s %s\n", cli.Gray, cli.Reset, truncate(sanitizeTerminal(p.Content), 200))
		case "image":
			fmt.Printf("%s[image]%s %d bytes\n", cli.Dim, cli.Reset, len(p.Content))
		default:
			fmt.Printf("%s[%s]%s %s\n", cli.Dim, sanitizeTerminal(p.Type), cli.Reset, truncate(sanitizeTerminal(p.Content), 200))
		}
	}
}

// chatTranscriptMarkdown mirrors components/features/chat/export/
// export-dialog.tsx turnsToMarkdown, so a CLI export and a browser download of
// the same chat read the same: a title, an export stamp, then "## You" and
// "## <agent>" sections with thinking and tool calls quoted.
func chatTranscriptMarkdown(messages []map[string]any, agentName string, now time.Time) string {
	title := "Conversation"
	if agentName != "" {
		title = "Conversation with " + agentName
	}
	assistantHeading := agentName
	if assistantHeading == "" {
		assistantHeading = "Assistant"
	}
	out := []string{"# " + title, "Exported " + now.UTC().Format("2006-01-02T15:04:05.000Z"), ""}
	for _, m := range messages {
		role, _ := m["role"].(string)
		switch strings.ToLower(role) {
		case "user", "human":
			out = append(out, "## You")
			text := ""
			for _, p := range chatMessageParts(m) {
				if p.Type == "text" {
					text = p.Content
					break
				}
			}
			out = append(out, text, "")
		case "assistant", "model":
			out = append(out, "## "+assistantHeading)
			for _, p := range chatMessageParts(m) {
				switch p.Type {
				case "text":
					out = append(out, p.Content)
				case "thinking":
					out = append(out, "> _Thinking:_ "+p.Content)
				case "tool_call":
					name := p.ToolName
					if name == "" {
						name = p.Content
					}
					out = append(out, "> _Tool:_ `"+name+"`")
				}
			}
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

// fetchChatMessages is the one read both the transcript and the export use.
func fetchChatMessages(client *cli.Client, chatID string) ([]map[string]any, error) {
	var body struct {
		Messages []map[string]any `json:"messages" yaml:"messages"`
	}
	if err := getJSON(client, "/api/v1/chats/"+url.PathEscape(chatID)+"/messages?limit=500", &body); err != nil {
		return nil, err
	}
	return body.Messages, nil
}

// chatAgentName names the agent for an export heading. The messages carry
// their agent id, so no chat-ownership walk is needed; any failure falls back
// to the dialog's own default heading rather than failing the export.
func chatAgentName(client *cli.Client, messages []map[string]any) string {
	for _, m := range messages {
		id, _ := m["agent_id"].(string)
		if id == "" {
			continue
		}
		var agent struct {
			Name string `json:"name" yaml:"name"`
			Slug string `json:"slug" yaml:"slug"`
		}
		if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(id), &agent); err != nil {
			return ""
		}
		if agent.Name != "" {
			return agent.Name
		}
		return agent.Slug
	}
	return ""
}

var chatExportCmd = &cobra.Command{
	Use:   "export <chat-id>",
	Short: "Export a chat transcript as Markdown (the chat's Export dialog)",
	Long: `Write a chat session's transcript as Markdown — the same document the web
chat's Export dialog downloads: your messages under "## You", the agent's under
its name, with thinking and tool calls quoted.

Without -o the Markdown goes to stdout. With -o it is written atomically, so an
interrupted export never leaves a half-written file.

Examples:
  crewship chat export c_abc123 -o incident.md
  crewship chat export c_abc123 | less`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		messages, err := fetchChatMessages(client, args[0])
		if err != nil {
			return err
		}
		md := chatTranscriptMarkdown(messages, chatAgentName(client, messages), time.Now())
		path, _ := cmd.Flags().GetString("output")
		if path == "" || path == "-" {
			_, err := fmt.Fprintln(os.Stdout, md)
			return err
		}
		f, err := cli.NewAtomicFile(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()
		if _, err := f.WriteString(md + "\n"); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		if err := f.Commit(); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		cli.PrintSuccess(fmt.Sprintf("Exported %d messages to %s.", len(messages), path))
		return nil
	},
}

func init() {
	chatExportCmd.Flags().StringP("output", "o", "", "Write the Markdown to this file (default: stdout)")
	chatCmd.AddCommand(chatExportCmd)
}
