package chatbridge

import (
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"path/filepath"
	"testing"
)

func TestRuntimePreparationInterruptedBeforeLaunch(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	state, err := bbolt.New(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	store := conversation.NewStore(dir, nil)
	for _, id := range []string{"wait", "ready"} {
		if err := store.Append(ctx, "chat", conversation.Message{ID: id, Role: conversation.RoleUser, Content: "hello"}); err != nil {
			t.Fatal(err)
		}
		if err := (runtimePreparation{Chat: "chat", Agent: "agent", Run: "run"}).persist(ctx, state, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.Delete(ctx, preparationBucket, "ready"); err != nil {
		t.Fatal(err)
	}
	// Simulate power loss after appending the notice but before clearing its receipt.
	if err := (runtimePreparation{Chat: "chat", Agent: "agent", Run: "run"}).interrupt(ctx, store, "wait"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RecoverRuntimePreparations(ctx, state, store, nil); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.ReadTranscript(ctx, "chat")
	if err != nil || len(history) != 3 || history[2].ID != "msg_preparation_wait" || history[2].Role != conversation.RoleSystem {
		t.Fatalf("history: %+v, %v", history, err)
	}
	remaining, err := state.List(ctx, preparationBucket)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining: %+v, %v", remaining, err)
	}
}
