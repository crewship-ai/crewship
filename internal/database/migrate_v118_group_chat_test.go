package database

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrationV118_GroupChat verifies the group-chat groundwork lands:
// chat_participants table + chats.visibility + conversation_messages.author_user_id.
func TestMigrationV118_GroupChat(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v118.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db.DB, newTestLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	colExists := func(table, col string) bool {
		rows, err := db.DB.Query("SELECT 1 FROM pragma_table_info(?) WHERE name = ?", table, col)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		defer rows.Close()
		return rows.Next()
	}

	tableExists := func(name string) bool {
		rows, err := db.DB.Query("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", name)
		if err != nil {
			t.Fatalf("sqlite_master: %v", err)
		}
		defer rows.Close()
		return rows.Next()
	}

	if !tableExists("chat_participants") {
		t.Error("chat_participants table missing")
	}
	for _, c := range []string{"chat_id", "user_id", "role", "joined_at"} {
		if !colExists("chat_participants", c) {
			t.Errorf("chat_participants.%s missing", c)
		}
	}
	if !colExists("chats", "visibility") {
		t.Error("chats.visibility missing")
	}
	if !colExists("conversation_messages", "author_user_id") {
		t.Error("conversation_messages.author_user_id missing")
	}
}
