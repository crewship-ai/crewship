package api

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
)

// keeperStoreReader is the server adapter's ReadTail over a real store.
type keeperStoreReader struct {
	covKPConvReader
	store *conversation.Store
}

func (r keeperStoreReader) ReadTail(ctx context.Context, chat string, max int) ([]ConversationMessage, error) {
	msgs, err := r.store.ReadTail(ctx, chat, max)
	var out []ConversationMessage
	for _, m := range msgs {
		out = append(out, ConversationMessage{Role: string(m.Role), Content: m.Content})
	}
	return out, err
}

// #5009: this history is the Keeper judge's prompt, stored with the request
// in keeper_requests.ollama_prompt.
func TestKeeperHistoryLeavesOutNativeCraftInstruction(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, agentID, _ := seedKeeperFixture(t, db)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, status) VALUES ('keeper-instruction-chat', ?, ?, 'ACTIVE')`, agentID, wsID)
	store := conversation.NewStore(t.TempDir(), newTestLogger())
	defer store.Close()
	for _, m := range []conversation.Message{
		{ID: "marked", Role: conversation.RoleUser, Content: "Confidential merger instruction", NativeCraftInstruction: true},
		{ID: "ordinary", Role: conversation.RoleUser, Content: "Please fetch the report"},
	} {
		if err := store.Append(context.Background(), "keeper-instruction-chat", m); err != nil {
			t.Fatal(err)
		}
	}
	h := newKeeperHandler(t, db).WithConversations(keeperStoreReader{store: store})
	got := h.loadConversationHistory(context.Background(), agentID)
	if strings.Contains(got, "merger") || !strings.Contains(got, "Please fetch the report") {
		t.Fatalf("judge history = %q", got)
	}
}
