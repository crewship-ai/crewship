package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestCreateBackupTruncatedTranscriptIsIncompleteNotFatal(t *testing.T) {
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	if _, err := db.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,message_count,title) VALUES('damaged-chat',?,'a_alice','u_admin',3,'Damaged transcript')`, ws); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	dir := filepath.Join(storage, "conversations")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	valid := "{\"session_id\":\"damaged-chat\", \"id\":\"valid-message\",\"run_id\":\"preserved-run\",\"additive_field\":{\"n\":9007199254740993}}\n"
	raw := valid + "{\"session_id\":\"foreign-session\",\"content\":\"FOREIGN_SECRET\"}\n" + "{\"session_id\":\"damaged-chat\",\"content\":"
	if err := os.WriteFile(filepath.Join(dir, "damaged-chat.jsonl"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	// Inserting a chat when the short file fence is acquired proves the database
	// snapshot already completed: the late row must not enter this bundle.
	locked := false
	lock := func(ctx context.Context) (func(), error) {
		locked = true
		_, err := db.ExecContext(ctx, `INSERT INTO chats(id,workspace_id,agent_id,created_by,message_count,title) VALUES('after-dump-chat',?,'a_alice','u_admin',1,'Not in snapshot')`, ws)
		return func() {}, err
	}
	result, err := backup.CreateBackup(t.Context(), db, backup.CreateOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Scope: backup.ScopeWorkspace, WorkspaceID: ws, ConversationRoot: storage, LockTranscripts: lock, OutputDir: t.TempDir(), NoEncrypt: true})
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("file pinning was not fenced")
	}
	found := false
	for _, item := range result.Manifest.Contents.Incomplete {
		if item.Kind == "agent_chat_transcript_invalid_lines" {
			found = true
			if item.Count != 2 || item.Workspace != ws || !strings.Contains(item.Detail, "damaged-chat") {
				t.Fatalf("incomplete %+v", item)
			}
		}
		if item.Kind == "agent_chat_transcript_missing" {
			t.Fatalf("DB dump happened after transcript lock: %+v", item)
		}
	}
	if !found {
		t.Fatal("corrupt transcript was not reported incomplete")
	}
	target := openMigratedDB(t)
	targetRoot := t.TempDir()
	if _, err = backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Path: result.Path, ConversationRoot: targetRoot}); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(targetRoot, "conversations", "damaged-chat.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != valid {
		t.Fatalf("valid raw fields changed or foreign data leaked: %s", restored)
	}
	var late int
	if err = target.QueryRow(`SELECT COUNT(*) FROM chats WHERE id='after-dump-chat'`).Scan(&late); err != nil || late != 0 {
		t.Fatalf("late DB row included: %d %v", late, err)
	}
}
