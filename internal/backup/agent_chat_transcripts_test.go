package backup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/conversation"
)

func TestAgentChatTranscriptBackupRestoreAndFork(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	if _, err := db.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,message_count,title) VALUES('transcript-chat',?,'a_alice','u_admin',2,'Backup transcript fixture')`, ws); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := conversation.NewStore(root, logger)
	messages := []conversation.Message{
		{ID: "trigger-message", AgentID: "a_alice", Role: conversation.RoleUser, AuthorUserID: "u_admin", Content: "Make an issue", Timestamp: time.Now().UTC()},
		{ID: "reply-message", AgentID: "a_alice", Role: conversation.RoleAssistant, Content: "Created", Parts: []conversation.Part{{Type: "tool_call", ToolName: "create_issue", ToolID: "call-1", Content: "structured tool data"}, {Type: "text", Content: "Created"}}, Timestamp: time.Now().UTC()},
	}
	for _, msg := range messages {
		if err := store.Append(ctx, "transcript-chat", msg); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	// Add an additive field from the chat-entity contract; a backup must not
	// discard fields this older main's Message struct does not yet decode.
	sourcePath := filepath.Join(root, "conversations", "transcript-chat.jsonl")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var body map[string]any
	if err := json.Unmarshal(lines[1], &body); err != nil {
		t.Fatal(err)
	}
	body["run_id"] = "durable-run"
	lines[1], err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(bytes.Join(lines, []byte("\n")), '\n')
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conversations", "foreign-chat.jsonl"), []byte("other tenant\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Scope: backup.ScopeWorkspace, WorkspaceID: ws, ConversationRoot: root, LockTranscripts: store.LockTranscripts, OutputDir: t.TempDir(), Passphrase: "transcript-test-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			targetRoot := filepath.Join(t.TempDir(), "storage")
			opts := backup.RestoreOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Path: created.Path, Passphrase: "transcript-test-passphrase", ConversationRoot: targetRoot}
			if fork {
				opts.AsWorkspace = "forked-transcript"
			}
			res, err := backup.RestoreBackup(ctx, target, opts)
			if err != nil {
				t.Fatal(err)
			}
			var chat, agent string
			if err := target.QueryRow(`SELECT id,agent_id FROM chats WHERE workspace_id=? AND title='Backup transcript fixture'`, res.RestoredWorkspaceID).Scan(&chat, &agent); err != nil {
				t.Fatal(err)
			}
			reader := conversation.NewStore(targetRoot, logger)
			defer reader.Close()
			got, err := reader.Read(ctx, chat, 0, 0)
			if err != nil || len(got) != 2 {
				t.Fatalf("history after restore: %d messages, %v", len(got), err)
			}
			if got[0].ID != "trigger-message" || got[0].AuthorUserID != "u_admin" || got[1].ChatID != chat || got[1].AgentID != agent || !reflect.DeepEqual(got[1].Parts, messages[1].Parts) {
				t.Fatalf("transcript identity/parts changed: %+v", got)
			}
			landed, err := os.ReadFile(filepath.Join(targetRoot, "conversations", chat+".jsonl"))
			if err != nil || !bytes.Contains(landed, []byte(`"run_id":"durable-run"`)) {
				t.Fatalf("additive run identity lost: %v", err)
			}
			if _, err := os.Stat(filepath.Join(targetRoot, "conversations", "foreign-chat.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("unscoped transcript copied: %v", err)
			}
			if !fork {
				if _, err := backup.RestoreBackup(ctx, target, opts); err != nil && !strings.Contains(err.Error(), "inserted zero rows") {
					t.Fatal(err)
				}
				again, err := os.ReadFile(filepath.Join(targetRoot, "conversations", chat+".jsonl"))
				if err != nil || !bytes.Equal(again, landed) {
					t.Fatalf("retry changed transcript: %v", err)
				}
				if err := reader.Append(ctx, chat, conversation.Message{ID: "later-message", Role: conversation.RoleUser, Content: "After the backup"}); err != nil {
					t.Fatal(err)
				}
				opts.DryRun = true
				if _, err := backup.RestoreBackup(ctx, target, opts); err != nil {
					t.Fatalf("dry-run of an existing chat must preserve its newer history: %v", err)
				}
				current, err := reader.Read(ctx, chat, 0, 0)
				if err != nil || len(current) != 3 {
					t.Fatalf("dry-run changed live history: %+v %v", current, err)
				}
				opts.DryRun = false
				opts.Replace = true
				opts.LockTranscripts = reader.LockTranscripts
				if _, err := backup.RestoreBackup(ctx, target, opts); err != nil {
					t.Fatal(err)
				}
				if err := reader.Append(ctx, chat, conversation.Message{ID: "after-restore", Role: conversation.RoleUser, Content: "New append descriptor"}); err != nil {
					t.Fatal(err)
				}
				got, err = reader.Read(ctx, chat, 0, 0)
				if err != nil || len(got) != 3 || got[2].ID != "after-restore" {
					t.Fatalf("replacement lost a later append: %+v %v", got, err)
				}
			}
		})
	}
}
