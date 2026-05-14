package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// recapCmd produces an LLM-generated summary of a chat session.
//
// Trending 2026 pattern (Claude Code `/recap`, OpenCode auto-compact):
// when you return to a long conversation, you don't want to scroll —
// you want the punch line, the open threads, and what the agent last
// did. This is that, dispatched through the default agent so it
// composes with the existing run/ask streaming + markdown rendering.
var recapCmd = &cobra.Command{
	Use:   "recap <chat-id>",
	Short: "AI-generated summary of a chat session",
	Long: `Summarise an existing chat session via the default agent.

Useful when picking up an older thread, sharing a session with a
teammate, or auditing what an agent actually did across many turns.

Examples:
  crewship recap c_abc123
  crewship recap c_abc123 --agent viktor
  crewship recap c_abc123 --bullets 5    # shorter`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		chatID := args[0]
		bullets, _ := cmd.Flags().GetInt("bullets")
		if bullets <= 0 {
			bullets = 8
		}

		// Pull the chat transcript. Limit=500 mirrors `crewship chat` —
		// long sessions get capped, which is fine for a recap (the most
		// recent 500 turns are what matters anyway).
		path := "/api/v1/chats/" + url.PathEscape(chatID) + "/messages?limit=500"
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := getJSON(client, path, &body); err != nil {
			return err
		}
		if len(body.Messages) == 0 {
			return fmt.Errorf("chat %s has no messages", chatID)
		}

		// Build a transcript block the recap agent can ingest. Tool calls
		// are summarised by name only — full payloads are noise for a
		// summary. Role labels are normalised to keep the prompt token
		// budget tight.
		var sb strings.Builder
		fmt.Fprintf(&sb, "Transcript (%d turns):\n\n", len(body.Messages))
		for _, m := range body.Messages {
			role, _ := m["role"].(string)
			content, _ := m["content"].(string)
			role = strings.ToLower(strings.TrimSpace(role))
			if role == "" {
				role = "unknown"
			}
			// Keep individual messages short — heuristic: 800 chars per turn.
			if len(content) > 800 {
				content = content[:800] + "…"
			}
			fmt.Fprintf(&sb, "[%s] %s\n\n", role, content)
		}

		systemPrompt := fmt.Sprintf(`You are summarising a Crewship agent session for a returning user.

Output a markdown summary with these sections, in this order:
1. **Final outcome** — one sentence
2. **Key decisions** — up to %d bullets
3. **Open threads** — anything unresolved, or "(none)"
4. **What to ask next** — one suggested follow-up prompt

Be terse. No preamble. No "Here is the summary:". Start with the outcome.

%s`, bullets, sb.String())

		// Re-use ask's RunE by re-setting flags. We don't want to fan out
		// or stream — recap should land as one block.
		_ = askCmd.Flags().Set("prompt", systemPrompt)
		_ = askCmd.Flags().Set("quiet", "true")
		if !cmd.Flags().Changed("agent") {
			// Fall through to default-agent resolution.
		} else {
			v, _ := cmd.Flags().GetString("agent")
			_ = askCmd.Flags().Set("agent", v)
		}

		// Format resolution is handled inside newFormatter (called by
		// the ask command's RunE); no explicit fallback needed here.

		if askCmd.RunE == nil {
			return fmt.Errorf("internal: ask command has no RunE")
		}
		if err := askCmd.RunE(askCmd, nil); err != nil {
			fmt.Fprintf(os.Stderr, "%s[recap]%s failed: %v\n", cli.Yellow, cli.Reset, err)
			return err
		}
		return nil
	},
}

func init() {
	recapCmd.Flags().String("agent", "", "Agent slug to use for the summary (defaults to default-agent)")
	recapCmd.Flags().Int("bullets", 8, "Max bullets in the 'Key decisions' section")
	rootCmd.AddCommand(recapCmd)
}
