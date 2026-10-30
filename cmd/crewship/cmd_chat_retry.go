package main

// `crewship chat retry` and `crewship chat edit` — the web chat's Regenerate
// and Edit-and-resend (hooks/use-chat.ts regenerateLastTurn, editAndResend).
//
// Neither rewrites history on the server: both are a new send on the same
// session, which is exactly what the browser does (it trims its own view, the
// stored transcript keeps every turn). What they add over `run --chat` is the
// browser's bookkeeping:
//
//   - retry resends the LAST user message as typed — the server-appended Page
//     snapshot stripped off and the Page asked for again by slug, so the
//     server re-checks access and takes a fresh snapshot instead of the old
//     one being duplicated into the text. An ask-form envelope is not carried
//     over: its submission id belonged to the original send.
//   - edit sends replacement text with no metadata at all: an envelope that
//     described the original answers would be false about edited text.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// pageSuffixRE matches the block chatbridge.pageContextBlock appends, anchored
// at the end of the message. Same expression as use-chat.ts regenerateLastTurn.
var pageSuffixRE = regexp.MustCompile(`^\n\n\[Page context — untrusted reference; snapshot ([^\]\n]+)\]\nPage: [^\n]*\nSlug: ([^\n]*)\nPage ID: ([^\n]*)\n\[/Page context\]$`)

// retryContent derives what regenerateLastTurn resends from a stored user
// message: its text without a matching, server-attributed Page suffix, and the
// Page slug to request again (empty when the message had none).
func retryContent(m map[string]any) (content, pageSlug string) {
	content, _ = m["content"].(string)
	page := eventMeta(eventMeta(m["metadata"])["page_context"])
	pageSlug = metaStr(page, "slug")
	idx := strings.LastIndex(content, "\n\n[Page context — untrusted reference; snapshot ")
	if idx < 0 || page == nil {
		return content, pageSlug
	}
	match := pageSuffixRE.FindStringSubmatch(content[idx:])
	if match != nil && match[1] == metaStr(page, "snapshot_at") && match[2] == metaStr(page, "slug") && match[3] == metaStr(page, "page_id") {
		content = content[:idx]
	}
	return content, pageSlug
}

// lastUserMessage returns the newest turn with explicit caller attribution.
// An absent author cannot safely establish ownership, including in shared chats.
func lastUserMessage(messages []map[string]any, caller string) map[string]any {
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if r := strings.ToLower(role); r == "user" || r == "human" {
			author, _ := messages[i]["author_user_id"].(string)
			if caller == "" || author != caller {
				continue
			}
			return messages[i]
		}
	}
	return nil
}

// resendToChat streams one new send on an existing chat, the way `run --chat`
// does. agentID is taken from the transcript when it carries one.
func resendToChat(cmd *cobra.Command, client *cli.Client, chatID, agentID, content string, metadata map[string]any) error {
	restricted, err := restrictedChatProfile(client, chatID)
	if err != nil {
		return err
	}
	if restricted {
		return fmt.Errorf("restricted history is immutable; send a new message with `crewship run --chat %s`", chatID)
	}
	if override, _ := cmd.Flags().GetString("agent"); override != "" || agentID == "" {
		if agentID, err = resolveChatAgent(cmd, client, chatID); err != nil {
			return err
		}
	}
	wsToken, err := cli.WSTokenFromServer(client)
	if err != nil {
		return fmt.Errorf("get WS token: %w", err)
	}
	quiet, _ := cmd.Flags().GetBool("quiet")
	noStream, _ := cmd.Flags().GetBool("no-stream")
	md := resolveMarkdownFromCmd(cmd)
	runMessageExtras = metadata
	defer func() { runMessageExtras = nil }()
	if noStream {
		return runNoStream(streamServerURL(), wsToken, agentID, chatID, content, quiet, md, nil, 0)
	}
	return runStream(streamServerURL(), wsToken, agentID, agentID, chatID, content, quiet, md, nil, 0)
}

func transcriptAgentID(messages []map[string]any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if id, _ := messages[i]["agent_id"].(string); id != "" {
			return id
		}
	}
	return ""
}

var chatRetryCmd = &cobra.Command{
	Use:     "retry <chat-id>",
	Aliases: []string{"regenerate"},
	Short:   "Resend the last message of a chat for a fresh answer (Regenerate)",
	Long: `Send the chat's last user message again and stream the new answer — the
web chat's Regenerate.

The earlier answer stays in the stored transcript; this is a new turn. A Page
the message was sent with is requested again by slug, so the server re-checks
access and attaches a fresh snapshot instead of repeating the old one.

Examples:
  crewship chat retry c_abc123
  crewship chat retry c_abc123 --no-stream`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		chatID := args[0]
		messages, err := fetchChatMessages(client, chatID)
		if err != nil {
			return err
		}
		var me struct {
			UserID string `json:"user_id" yaml:"user_id"`
		}
		if err := getJSON(client, "/api/v1/auth/cli-token/validate", &me); err != nil {
			return err
		}
		if me.UserID == "" {
			return fmt.Errorf("could not determine your user id for retry")
		}
		last := lastUserMessage(messages, me.UserID)
		if last == nil {
			return fmt.Errorf("chat %s has no message of yours to retry", chatID)
		}
		content, slug := retryContent(last)
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("the last message of chat %s has no text to resend", chatID)
		}
		var metadata map[string]any
		if slug != "" {
			metadata = map[string]any{"page_context": map[string]any{"slug": slug}}
		}
		return resendToChat(cmd, client, chatID, transcriptAgentID(messages), content, metadata)
	},
}

var chatEditCmd = &cobra.Command{
	Use:   "edit <chat-id>",
	Short: "Send a corrected version of your message and stream the new answer (Edit and resend)",
	Long: `Send replacement text for a message on a chat and stream the new answer —
the web chat's Edit and resend.

The stored transcript keeps the original turn; the edit is a new send. It
carries no form envelope or Page: an envelope describing the original answers
would be false about edited text.

Examples:
  crewship chat edit c_abc123 -m "use the staging database, not prod"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		message, _ := cmd.Flags().GetString("message")
		message = strings.TrimSpace(message)
		if message == "" {
			return fmt.Errorf("--message is required")
		}
		chatID := args[0]
		messages, err := fetchChatMessages(client, chatID)
		if err != nil {
			return err
		}
		return resendToChat(cmd, client, chatID, transcriptAgentID(messages), message, nil)
	},
}

func init() {
	for _, c := range []*cobra.Command{chatRetryCmd, chatEditCmd} {
		c.Flags().Bool("no-stream", false, "Wait for completion and print only the final text")
		c.Flags().BoolP("quiet", "q", false, "Only output text, no meta info")
		c.Flags().Bool("markdown", false, "Force markdown ANSI styling")
		c.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling")
		c.Flags().String("agent", "", "Override the auto-resolved agent slug or ID")
		chatCmd.AddCommand(c)
	}
	chatEditCmd.Flags().StringP("message", "m", "", "Replacement message text (required)")
}
