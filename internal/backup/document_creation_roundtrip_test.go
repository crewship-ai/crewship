package backup_test

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// The bytes are deliberately opaque fixtures: this proves encrypted transport
// and provenance remapping, not validation/materialization of Yjs content.
func TestDocumentCreationEncryptedProvenanceFork(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	seedBackupDocument(t, source, workspace, "created-document", "u_admin")
	if _, err := source.Exec(`INSERT INTO document_creations(page_id,workspace_id,run_id,human_user_id,submission_id,mutation_id,request_digest,created_at) VALUES('created-document',?,'creation-origin-run','u_admin','origin-submission','origin-mutation',?,'2026-10-16T09:00:00Z')`, workspace, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	// A real nonportable consent exists in the source and must not be restored.
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO crews(id,workspace_id,name,slug) VALUES('create-crew',?,'Creator','create-crew')`, []any{workspace}},
		{`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('create-agent',?,'create-crew','Creator','create-agent')`, []any{workspace}},
		{`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES('create-chat',?,'create-agent','u_admin')`, []any{workspace}},
		{`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('create-message','create-chat','create-agent','user','Create a document','2026-10-16T09:00:00Z','u_admin')`, nil},
		{`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,run_id,expires_at,created_at) VALUES('create-message',?,'u_admin','create-chat','source-consent','creation-origin-run','2099-01-01T00:00:00Z','2026-10-16T09:00:00Z')`, []any{workspace}},
		{`UPDATE document_creations SET agent_id='create-agent',source_chat_id='create-chat'`, nil},
	} {
		if _, err := source.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: "synthetic-create-backup", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Manifest.Encryption.Enabled {
		t.Fatal("expected encrypted bundle")
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: "synthetic-create-backup", Actor: actor}
			if fork {
				options.AsWorkspace = "creation-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			assertBackupDocument(t, target, 1)
			var restoredWorkspace, page, run, submission, mutation, digest string
			if err := target.QueryRow(`SELECT c.workspace_id,c.page_id,c.run_id,c.submission_id,c.mutation_id,c.request_digest FROM document_creations c JOIN documents d ON d.workspace_id=c.workspace_id AND d.page_id=c.page_id JOIN pages p ON p.id=c.page_id AND p.workspace_id=c.workspace_id`).Scan(&restoredWorkspace, &page, &run, &submission, &mutation, &digest); err != nil {
				t.Fatal(err)
			}
			if submission != "origin-submission" || mutation != "origin-mutation" || digest != strings.Repeat("a", 64) {
				t.Fatal("historical authoring identity changed")
			}
			if fork {
				if restoredWorkspace == workspace || page == "created-document" || run == "creation-origin-run" || run == "" {
					t.Fatal("fork failed to remap scope/run")
				}
			} else if restoredWorkspace != workspace || page != "created-document" || run != "creation-origin-run" {
				t.Fatal("same restore changed historical identity")
			}
			for _, table := range []string{"document_creation_consents", "artifact_run_principals", "page_artifact_run_delegations"} {
				var count int
				if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("restored live authority %s=%d %v", table, count, err)
				}
			}
		})
	}
}
