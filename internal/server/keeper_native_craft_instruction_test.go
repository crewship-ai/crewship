package server

import (
	"context"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
)

// #5009: Keeper builds its judge prompt from this adapter, and the prompt is
// stored in keeper_requests.ollama_prompt, which the Keeper audit API returns.
func TestKeeperConversationAdapterLeavesOutNativeCraftInstruction(t *testing.T) {
	store := conversation.NewStore(t.TempDir(), slog.Default())
	defer store.Close()
	ctx := context.Background()
	for _, m := range []conversation.Message{
		{ID: "marked", Role: conversation.RoleUser, Content: "Confidential merger instruction", NativeCraftInstruction: true},
		{ID: "ordinary", Role: conversation.RoleUser, Content: "Please fetch the report"},
	} {
		if err := store.Append(ctx, "chat", m); err != nil {
			t.Fatal(err)
		}
	}
	adapter := &convStoreAdapter{store: store}
	read, err := adapter.Read(ctx, "chat", 0, 0)
	if err != nil || len(read) != 1 || read[0].Content != "Please fetch the report" {
		t.Errorf("Read = %+v, %v", read, err)
	}
	tail, err := adapter.ReadTail(ctx, "chat", 20)
	if err != nil || len(tail) != 1 || tail[0].Content != "Please fetch the report" {
		t.Errorf("ReadTail = %+v, %v", tail, err)
	}
}
