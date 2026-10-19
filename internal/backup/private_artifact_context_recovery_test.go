package backup_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/database"
)

// Actual encrypted instance creation/recovery, real SQL cascades and retained
// JSONL. This is recovery/classification coverage, not HTTP deletion, a model
// run or a deployed privacy acceptance test.
func TestPrivateArtifactContextPhysicalRecoveryRetainsSeal(t *testing.T) {
	for _, created := range []bool{false, true} {
		t.Run(map[bool]string{false: "consent only", true: "created then last Page deleted"}[created], func(t *testing.T) {
			f := newInstanceFixture(t)
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := f.db.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct{ user, role string }{{"u_admin", "MEMBER"}, {"context-admin", "ADMIN"}, {"context-owner", "OWNER"}} {
				if tc.user != "u_admin" {
					exec(`INSERT INTO users(id,email) VALUES(?,?)`, tc.user, tc.user+"@example.test")
				}
				exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES(?,'ws_e2e_1',?,?,'trusted')`, "member-"+tc.user, tc.user, tc.role)
			}
			exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,mode,origin) VALUES('context-recovery-chat','ws_e2e_1','a_alice','u_admin','private','CHAT','UI')`)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			store := conversation.NewStore(f.paths.Output, logger, conversation.WithDB(f.db))
			if err := store.Append(t.Context(), "context-recovery-chat", conversation.Message{ID: "context-original-message", AgentID: "a_alice", Role: conversation.RoleUser, AuthorUserID: "u_admin", Content: "Create a private Document", Timestamp: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			const marker = "private-recovery-output-marker"
			var parts conversation.PartAccumulator
			if created {
				parts.Add("tool_result", `{"title":"`+marker+`","url":"/pages?ref=context-document"}`, map[string]any{"tool_name": "doc_create"})
			} else {
				// A failed create can already have persisted private authoring input.
				parts.Add("tool_call", `{"name":"`+marker+`","slug":"private-draft"}`, map[string]any{"tool_name": "doc_create"})
				parts.Add("tool_result", `{"error":"creation did not complete"}`, map[string]any{"tool_name": "doc_create"})
			}
			if err := store.Append(t.Context(), "context-recovery-chat", conversation.Message{ID: "context-output-message", AgentID: "a_alice", Role: conversation.RoleAssistant, Parts: parts.Parts(), Timestamp: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if err := store.SyncSession(t.Context(), "context-recovery-chat"); err != nil {
				t.Fatal(err)
			}
			store.Close()
			exec(`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('context-original-message','ws_e2e_1','u_admin','context-recovery-chat','context-submission','2026-10-19T00:00:00Z')`)
			if created {
				seedBackupDocument(t, f.db, "ws_e2e_1", "context-document", "u_admin")
				exec(`INSERT INTO document_creations(page_id,workspace_id,run_id,agent_id,human_user_id,source_chat_id,submission_id,mutation_id,request_digest,created_at) VALUES('context-document','ws_e2e_1','context-original-run','a_alice','u_admin','context-recovery-chat','context-submission','context-mutation',?,'2026-10-19T00:00:00Z')`, strings.Repeat("a", 64))
			}
			bundle := f.create(t, true)
			if !bundle.Manifest.Encryption.Enabled {
				t.Fatal("fixture must use actual encrypted recovery")
			}
			dir := filepath.Join(t.TempDir(), "recovered-private-context")
			report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"})
			if err != nil {
				t.Fatal(err)
			}
			target, err := database.Open("file:" + report.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = target.Close() })
			count := func(q string) int {
				t.Helper()
				var n int
				if err := target.QueryRow(q).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if n := count(`SELECT count(*) FROM document_creation_consents`); n != 0 {
				t.Fatalf("runtime authority restored: %d", n)
			}
			if n := count(`SELECT count(*) FROM chats WHERE id='context-recovery-chat'`); n != 1 {
				t.Fatalf("source chat not retained: %d", n)
			}
			if n := count(`SELECT count(*) FROM conversation_messages WHERE session_id='context-recovery-chat'`); n != 2 {
				t.Fatalf("SQL transcript not retained: %d", n)
			}
			recoveredStore := conversation.NewStore(backup.RecoveredStoreDir(dir, backup.StoreOutput), logger)
			t.Cleanup(recoveredStore.Close)
			messages, err := recoveredStore.Read(t.Context(), "context-recovery-chat", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(messages)
			if err != nil || len(messages) != 2 || !strings.Contains(string(raw), marker) {
				t.Fatalf("private JSONL copy not retained: messages=%d err=%v", len(messages), err)
			}
			if created {
				if ok, err := chataudience.CanReadArtifactConversation(t.Context(), target.DB, "context-recovery-chat", "context-admin", "ws_e2e_1", true); err != nil || ok {
					t.Fatalf("fallback baseline before deletion: %v %v", ok, err)
				}
				// Exercise actual schema cascades, explicitly not an HTTP delete claim.
				if _, err := target.Exec(`DELETE FROM pages WHERE id='context-document'`); err != nil {
					t.Fatal(err)
				}
				if count(`SELECT count(*) FROM documents WHERE page_id='context-document'`)+count(`SELECT count(*) FROM document_creations WHERE page_id='context-document'`) != 0 {
					t.Fatal("last result did not cascade")
				}
			}
			if ok, err := chataudience.CanReadArtifactConversation(t.Context(), target.DB, "context-recovery-chat", "context-admin", "ws_e2e_1", true); err != nil || ok {
				t.Errorf("ADMIN gained retained private output after recovery/lifecycle: allowed=%v err=%v", ok, err)
			}
			for _, user := range []string{"u_admin", "context-owner"} {
				if ok, err := chataudience.CanReadArtifactConversation(t.Context(), target.DB, "context-recovery-chat", user, "ws_e2e_1", true); err != nil || !ok {
					t.Errorf("authorized %s read: %v %v", user, ok, err)
				}
			}
			if n := count(`SELECT count(*) FROM audit_logs WHERE user_id='context-owner' AND entity_type='chat' AND entity_id='context-recovery-chat' AND action='artifact.private_owner_accessed' AND json_extract(metadata,'$.owner_user_id')='u_admin' AND json_extract(metadata,'$.output_source')='conversation'`); n == 0 {
				t.Error("foreign OWNER recovery read lacks required source-context audit")
			}
		})
	}
}
