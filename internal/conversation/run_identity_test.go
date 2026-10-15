package conversation

import "testing"

func TestRunIdentityMirrorsToMigratedDatabase(t *testing.T) {
	store := newSearchStore(t)
	if err := store.Append(t.Context(), "chat", Message{ID: "message", RunID: "run", Role: RoleAssistant, Content: "answer"}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := store.db.QueryRowContext(t.Context(), `SELECT run_id FROM conversation_messages WHERE id='message'`).Scan(&got); err != nil || got != "run" {
		t.Fatalf("mirror run identity: %q %v", got, err)
	}
}
