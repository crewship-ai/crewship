package backup_test

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
)

// These are real same-installation snapshots taken before consent, not synthetic
// old schemas. Replacement must not discard newer audience proof while leaving
// the newer per-agent log outside the archived stores. No HTTP/model claim.
func TestPrivateArtifactTargetWorkspaceReplace(t *testing.T) {
	for _, kind := range []string{"consent", "created", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			f := newInstanceFixture(t)
			seedReplacementChat(t, f.db, f.paths.Output)
			bundle, err := backup.CreateBackup(t.Context(), f.db, backup.CreateOptions{
				Scope: backup.ScopeWorkspace, WorkspaceID: "ws_e2e_1", OutputDir: t.TempDir(),
				Actor:      backup.Actor{UserID: "u_admin", Role: "OWNER"},
				Recipients: []age.Recipient{f.identity.Recipient()}, ConversationRoot: f.paths.Output,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bundle.Manifest.Encryption.Enabled {
				t.Fatal("snapshot must be encrypted")
			}
			logPath, before := advanceReplacementTarget(t, f.db, f.paths.Output, filepath.Join(t.TempDir(), "logs"), kind)
			_, err = backup.RestoreBackup(t.Context(), f.db, backup.RestoreOptions{
				Path: bundle.Path, Identities: []age.Identity{f.identity}, Replace: true,
				Actor: backup.Actor{UserID: "u_admin", Role: "OWNER"}, ConversationRoot: f.paths.Output,
			})
			// Workspace restore reconciles the retained newer transcript into SQL.
			assertReplacementOutcome(t, f.db, logPath, before, kind, 2, err)
		})
	}
}

func replacementExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func seedReplacementChat(t *testing.T, db *sql.DB, output string) {
	t.Helper()
	replacementExec(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,mode,origin) VALUES('replace-chat','ws_e2e_1','a_alice','u_admin','private','CHAT','UI')`)
	appendReplacementMessage(t, db, output, "replace-original", conversation.RoleUser, "Earlier ordinary conversation")
	var marker sql.NullString
	if err := db.QueryRow(`SELECT private_artifact_author_user_id FROM chats WHERE id='replace-chat'`).Scan(&marker); err != nil || marker.Valid {
		t.Fatalf("earlier snapshot must be unsealed: marker=%v err=%v", marker.Valid, err)
	}
}

func appendReplacementMessage(t *testing.T, db *sql.DB, output, id string, role conversation.Role, content string) {
	t.Helper()
	store := conversation.NewStore(output, slog.New(slog.NewTextHandler(io.Discard, nil)), conversation.WithDB(db))
	defer store.Close()
	if err := store.Append(t.Context(), "replace-chat", conversation.Message{ID: id, AgentID: "a_alice", AuthorUserID: "u_admin", Role: role, Content: content, Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncSession(t.Context(), "replace-chat"); err != nil {
		t.Fatal(err)
	}
}

func advanceReplacementTarget(t *testing.T, db *sql.DB, output, logs, kind string) (string, []byte) {
	t.Helper()
	replacementExec(t, db, `UPDATE workspaces SET name='newer target state' WHERE id='ws_e2e_1'`)
	if kind != "ordinary" {
		replacementExec(t, db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('replace-original','ws_e2e_1','u_admin','replace-chat','replace-submission','2026-10-19T00:00:00Z')`)
		if kind == "created" {
			seedBackupDocument(t, db, "ws_e2e_1", "replace-document", "u_admin")
			replacementExec(t, db, `UPDATE pages SET private_audience=1 WHERE id='replace-document'`)
			replacementExec(t, db, `INSERT INTO document_creations(page_id,workspace_id,run_id,agent_id,human_user_id,source_chat_id,submission_id,mutation_id,request_digest,created_at) VALUES('replace-document','ws_e2e_1','replace-run','a_alice','u_admin','replace-chat','replace-submission','replace-mutation',?,'2026-10-19T00:00:00Z')`, strings.Repeat("a", 64))
		}
	}
	appendReplacementMessage(t, db, output, "replace-newer", conversation.RoleAssistant, "Newer output retained by the target")
	var marker sql.NullString
	if err := db.QueryRow(`SELECT private_artifact_author_user_id FROM chats WHERE id='replace-chat'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if kind == "ordinary" && marker.Valid || kind != "ordinary" && (!marker.Valid || marker.String != "u_admin") {
		t.Fatal("target classification fixture does not match requested case")
	}
	writer := logcollector.NewWriter(logs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := writer.Append("c_alpha", "alice", logcollector.LogEntry{Event: "tool_result", Content: "newer-target-output-canary"}); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	path := filepath.Join(logs, "crews", "c_alpha", "agents", "alice", "current.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		t.Fatalf("newer log fixture: %v", err)
	}
	return path, raw
}

func assertReplacementOutcome(t *testing.T, db *sql.DB, logPath string, before []byte, kind string, ordinaryMessages int, restoreErr error) {
	t.Helper()
	protected := kind != "ordinary"
	if protected {
		if restoreErr == nil || !strings.Contains(restoreErr.Error(), "private artifact") {
			t.Errorf("replacement must refuse protected target before mutation: %v", restoreErr)
		}
	} else if restoreErr != nil {
		t.Fatalf("ordinary replacement must remain supported: %v", restoreErr)
	}
	var name string
	var marker sql.NullString
	var messages int
	if err := db.QueryRow(`SELECT w.name,c.private_artifact_author_user_id,(SELECT count(*) FROM conversation_messages WHERE session_id=c.id) FROM chats c JOIN workspaces w ON w.id=c.workspace_id WHERE c.id='replace-chat'`).Scan(&name, &marker, &messages); err != nil {
		t.Fatal(err)
	}
	if protected && (name != "newer target state" || !marker.Valid || marker.String != "u_admin" || messages != 2) {
		t.Errorf("protected target changed: sentinel=%v marker=%v messages=%d", name == "newer target state", marker, messages)
	}
	if !protected && (name != "E2E Workspace" || marker.Valid || messages != ordinaryMessages) {
		t.Errorf("ordinary snapshot not restored: name=%q sealed=%v messages=%d", name, marker.Valid, messages)
	}
	after, err := os.ReadFile(logPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("newer per-agent log changed: %v", err)
	}
}
