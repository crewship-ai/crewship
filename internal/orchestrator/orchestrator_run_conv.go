package orchestrator

// Conversation history / context assembly helpers extracted from
// orchestrator_run.go. Pure file move; signatures and behavior unchanged.

import (
	"context"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/tokenutil"
)

// buildConversationContext reads messages from the session JSONL and formats them
// as a conversation transcript for the system prompt. Uses a token budget to
// dynamically size the window — short exchanges get more turns, long tool-heavy
// turns get fewer but always include the most recent messages.

func (o *Orchestrator) buildConversationContext(ctx context.Context, sessionID string, tokenBudget int) string {
	messages, err := o.convStore.Read(ctx, sessionID, 0, 0)
	if err != nil || len(messages) == 0 {
		return ""
	}

	// Skip the current user message (just appended by bridge before RunAgent call).
	if len(messages) > 0 && messages[len(messages)-1].Role == conversation.RoleUser {
		messages = messages[:len(messages)-1]
	}
	if len(messages) == 0 {
		return ""
	}

	charBudget := tokenutil.CharsForTokens(tokenBudget)

	// Iterate backward from newest, accumulate until budget exhausted.
	var selected []conversation.Message
	totalChars := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		msgLen := len(msg.Content) + len(msg.ToolSummary)
		if totalChars+msgLen > charBudget {
			// Try to fit a truncated version of this message.
			remaining := charBudget - totalChars
			if remaining > 200 {
				truncated := msg
				if len(truncated.Content) > remaining {
					truncated.Content = truncated.Content[:remaining-20] + "...(truncated)"
					truncated.ToolSummary = ""
				}
				selected = append(selected, truncated)
			}
			break
		}
		selected = append(selected, msg)
		totalChars += msgLen
	}
	if len(selected) == 0 {
		return ""
	}

	// Reverse to chronological order.
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}

	var b strings.Builder
	// Pre-size: header + per-message overhead + already-counted totalChars + trailer.
	// Avoids the Builder's geometric-growth reallocations over the loop.
	b.Grow(totalChars + len(selected)*16 + 256)

	b.WriteString("[CONVERSATION HISTORY - previous messages in this session]\n")
	for _, msg := range selected {
		// fmt.Fprintf streams directly into the Builder — the previous
		// b.WriteString(fmt.Sprintf(...)) allocated an intermediate string
		// per line that the Builder then copied into the same buffer.
		fmt.Fprintf(&b, "[%s]: %s\n", msg.Role, msg.Content)
		if msg.ToolSummary != "" {
			fmt.Fprintf(&b, "  %s\n", msg.ToolSummary)
		}
	}
	b.WriteString("[END CONVERSATION HISTORY]\n")
	b.WriteString("The user's new message follows. Continue the conversation naturally, referencing previous context when relevant.")
	return b.String()
}
