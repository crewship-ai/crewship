package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
)

// BenchmarkBuildConversationContext exercises the history-rendering step
// that every RunAgent call pays for when the chat has prior messages.
// 20 messages is a realistic mid-session load; the Sprintf-in-loop pattern
// creates 2N intermediate strings that the fix streams directly into the
// Builder.
func BenchmarkBuildConversationContext(b *testing.B) {
	const sessionID = "sess-bench"
	const nMessages = 20

	dir := b.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	store := conversation.NewStore(dir, logger)
	b.Cleanup(func() { store.Close() })

	baseContent := "The agent processed the request and produced a summary of the changes " +
		"along with a brief justification that the user should review before moving on."
	ctx := context.Background()
	for i := 0; i < nMessages; i++ {
		role := conversation.RoleUser
		if i%2 == 1 {
			role = conversation.RoleAssistant
		}
		msg := conversation.Message{
			ID:        "m-bench",
			Role:      role,
			Content:   baseContent,
			Timestamp: time.Unix(int64(i), 0).UTC(),
		}
		if role == conversation.RoleAssistant && i%4 == 1 {
			msg.ToolSummary = "ran tests: 42 passed"
		}
		if err := store.Append(ctx, sessionID, msg); err != nil {
			b.Fatalf("append %d: %v", i, err)
		}
	}

	o := &Orchestrator{convStore: store, logger: logger}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = o.buildConversationContext(ctx, sessionID, 8000)
	}
}
