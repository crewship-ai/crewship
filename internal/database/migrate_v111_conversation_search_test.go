package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV111_ConversationSearchSchema verifies the v111 migration
// creates conversation_messages, its FTS5 shadow, and the three sync
// triggers, and that the contentless 'delete' DELETE/UPDATE trigger form
// keeps the FTS index consistent through insert → update → delete without
// tripping SQLite error 267 ("database disk image is malformed"), which
// the plain-DELETE trigger form would.
func TestMigrateV111_ConversationSearchSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v111.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Base table + FTS shadow + triggers all present.
	objects := map[string]string{
		"conversation_messages":     "table",
		"conversation_messages_fts": "table",
		"conversation_messages_ai":  "trigger",
		"conversation_messages_ad":  "trigger",
		"conversation_messages_au":  "trigger",
	}
	for name, kind := range objects {
		var got string
		err := db.QueryRow(
			`SELECT type FROM sqlite_master WHERE name = ?`, name).Scan(&got)
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		if got != kind {
			t.Errorf("%s is a %q, want %q", name, got, kind)
		}
	}

	// INSERT trigger: a row must become FTS-searchable.
	if _, err := db.Exec(
		`INSERT INTO conversation_messages (id, session_id, agent_id, role, content, tool_summary, ts)
		 VALUES ('m1','s1','agentA','user','deploy the staging pipeline','', '2026-06-01T00:00:00.000Z')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM conversation_messages_fts WHERE conversation_messages_fts MATCH 'deploy'`,
	).Scan(&n); err != nil {
		t.Fatalf("match after insert: %v", err)
	}
	if n != 1 {
		t.Fatalf("after insert: FTS match count = %d, want 1", n)
	}

	// UPDATE trigger: old terms drop out, new terms come in. The
	// contentless 'delete' leg must run before the re-insert; the wrong
	// trigger form corrupts the index here.
	if _, err := db.Exec(
		`UPDATE conversation_messages SET content = 'rollback the production release' WHERE id = 'm1'`,
	); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := db.QueryRow(
		`SELECT count(*) FROM conversation_messages_fts WHERE conversation_messages_fts MATCH 'deploy'`,
	).Scan(&n); err != nil {
		t.Fatalf("match old term after update: %v", err)
	}
	if n != 0 {
		t.Errorf("after update: stale term 'deploy' still matches %d rows, want 0", n)
	}
	if err := db.QueryRow(
		`SELECT count(*) FROM conversation_messages_fts WHERE conversation_messages_fts MATCH 'rollback'`,
	).Scan(&n); err != nil {
		t.Fatalf("match new term after update: %v", err)
	}
	if n != 1 {
		t.Errorf("after update: new term 'rollback' matches %d rows, want 1", n)
	}

	// DELETE trigger: the contentless 'delete' form must remove the row
	// from the index cleanly.
	if _, err := db.Exec(`DELETE FROM conversation_messages WHERE id = 'm1'`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.QueryRow(
		`SELECT count(*) FROM conversation_messages_fts WHERE conversation_messages_fts MATCH 'rollback'`,
	).Scan(&n); err != nil {
		t.Fatalf("match after delete: %v", err)
	}
	if n != 0 {
		t.Errorf("after delete: FTS still matches %d rows, want 0", n)
	}

	// Integrity check — the corruption the verbatim trigger form guards
	// against would surface here as a non-'ok' result or an error.
	var integrity string
	if err := db.QueryRow(
		`INSERT INTO conversation_messages_fts(conversation_messages_fts) VALUES('integrity-check')`,
	).Scan(&integrity); err != nil {
		// integrity-check returns no rows on success; the Exec form is
		// the right call. Re-run as Exec to assert it doesn't error.
		if _, eerr := db.Exec(
			`INSERT INTO conversation_messages_fts(conversation_messages_fts) VALUES('integrity-check')`,
		); eerr != nil {
			t.Fatalf("FTS integrity-check failed (index corrupted): %v", eerr)
		}
	}
}
