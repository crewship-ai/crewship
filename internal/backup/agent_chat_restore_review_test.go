package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestTranscriptRestoreDoesNotOverwriteOtherWorkspaceOrExistingChat(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	sourceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(sourceRoot, "conversations"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"existing-chat", "new-chat"} {
		if _, err := source.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,message_count) VALUES(?,?,'a_alice','u_admin',1)`, id, ws); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceRoot, "conversations", id+".jsonl"), []byte(`{"session_id":"`+id+`","id":"backup-message"}`+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Scope: backup.ScopeWorkspace, WorkspaceID: ws, ConversationRoot: sourceRoot, OutputDir: t.TempDir(), NoEncrypt: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("foreign collision with replace", func(t *testing.T) {
		target := openMigratedDB(t)
		seedWorkspace(t, target)
		for _, query := range []string{
			`INSERT INTO workspaces(id,name,slug) VALUES('foreign-ws','Foreign','foreign-ws')`,
			`INSERT INTO crews(id,workspace_id,name,slug) VALUES('foreign-crew','foreign-ws','Foreign','foreign-crew')`,
			`INSERT INTO agents(id,workspace_id,crew_id,name,slug,status) VALUES('foreign-agent','foreign-ws','foreign-crew','Foreign','foreign-agent','IDLE')`,
			`INSERT INTO chats(id,workspace_id,agent_id,created_by,message_count) VALUES('existing-chat','foreign-ws','foreign-agent','u_admin',1)`,
		} {
			if _, err := target.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "conversations"), 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "conversations", "existing-chat.jsonl")
		original := `{"session_id":"existing-chat","content":"foreign-private"}` + "\n"
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Path: created.Path, ConversationRoot: root, Replace: true})
		if err == nil || !strings.Contains(err.Error(), "collides with another workspace") {
			t.Fatalf("foreign collision accepted: %v", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != original {
			t.Fatal("foreign history changed")
		}
		var count int
		if err := target.QueryRow(`SELECT COUNT(*) FROM chats WHERE id='existing-chat' AND workspace_id='foreign-ws'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("foreign DB changed: %d %v", count, err)
		}
	})
	t.Run("partial restore preserves newer chat", func(t *testing.T) {
		target := openMigratedDB(t)
		seedWorkspace(t, target)
		if _, err := target.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,message_count) VALUES('existing-chat',?,'a_alice','u_admin',2)`, ws); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "conversations"), 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "conversations", "existing-chat.jsonl")
		original := `{"session_id":"existing-chat","id":"newer-live-message"}` + "\n"
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Path: created.Path, ConversationRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != original {
			t.Fatal("partial restore overwrote newer history")
		}
		if _, err := os.Stat(filepath.Join(root, "conversations", "new-chat.jsonl")); err != nil {
			t.Fatalf("new chat not restored: %v", err)
		}
	})
	t.Run("dry run writes nothing and never fences", func(t *testing.T) {
		target := openMigratedDB(t)
		root := filepath.Join(t.TempDir(), "not-created")
		opts := backup.RestoreOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Path: created.Path, ConversationRoot: root, DryRun: true}
		opts.LockTranscripts = failTranscriptFence(t)
		if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote storage: %v", err)
		}
		var count int
		if err := target.QueryRow(`SELECT COUNT(*) FROM chats`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("dry run wrote DB: %d %v", count, err)
		}
	})
}

func failTranscriptFence(t *testing.T) func(context.Context) (func(), error) {
	t.Helper()
	return func(context.Context) (func(), error) {
		t.Error("restore acquired transcript fence in a read-only or transcript-free phase")
		return func() {}, nil
	}
}

func TestTranscriptFreeAndFilesOnlyRestoreNeverFence(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,message_count) VALUES('legacy-chat',?,'a_alice','u_admin',1)`, ws); err != nil {
		t.Fatal(err)
	}
	// A bundle without transcript metadata exercises the older additive format.
	created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), NoEncrypt: true, DockerOps: &recordingOps{}, CrewContainerName: func(id, _ string) string { return "synthetic-" + id }})
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDB(t)
	opts := backup.RestoreOptions{Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"}, Path: created.Path, AsWorkspace: "legacy-transcript-fork", LockTranscripts: failTranscriptFence(t)}
	result, err := backup.RestoreBackup(t.Context(), target, opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range result.Incomplete {
		if item.Kind == "agent_chat_transcript_missing" && item.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy missing transcript not reported")
	}
	opts.AsWorkspace = ""
	opts.FilesOnly = true
	opts.DockerOps = &recordingOps{}
	opts.ContainerFor = func(id, _ string) string { return "synthetic-" + id }
	opts.ResumeWorkspaceID = result.RestoredWorkspaceID
	if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
		t.Fatal(err)
	}
}
