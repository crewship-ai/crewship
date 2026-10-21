package conversation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

func TestSyncSessionAfterWriterCacheClosesKeepsDurabilityBarrier(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root, logging.New("error", "json", nil))
	defer store.Close()
	if err := store.Append(t.Context(), "session", Message{ID: "trigger", Role: RoleUser}); err != nil {
		t.Fatal(err)
	}
	// Restore uses the same close-and-evict behavior for cached file handles.
	store.Close()
	if err := store.SyncSession(t.Context(), "session"); err != nil {
		t.Fatalf("existing transcript lost its admission durability barrier: %v", err)
	}
	messages, err := store.Read(t.Context(), "session", 0, 0)
	if err != nil || len(messages) != 1 || messages[0].ID != "trigger" {
		t.Fatalf("sync changed history: %+v %v", messages, err)
	}
	for _, id := range []string{"missing", "../outside"} {
		if err := store.SyncSession(t.Context(), id); err == nil {
			t.Fatalf("sync admitted missing/invalid history %q", id)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "conversations", "missing.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("sync created missing history: %v", err)
	}
}
