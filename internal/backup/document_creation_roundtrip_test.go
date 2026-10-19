package backup_test

import (
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
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
			for _, table := range []string{"document_creation_consents", "artifact_run_principals"} {
				var count int
				if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("restored live authority %s=%d %v", table, count, err)
				}
			}
		})
	}
}

func TestDocumentCreationInstanceRecoveryClearsAuthorityPreservesProvenance(t *testing.T) {
	f := newInstanceFixture(t)
	seedBackupDocument(t, f.db, "ws_e2e_1", "instance-created-doc", "u_admin")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('instance-create-crew','ws_e2e_1','Creator','instance-create')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('instance-create-agent','ws_e2e_1','instance-create-crew','Creator','instance-create-agent')`)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES('instance-create-chat','ws_e2e_1','instance-create-agent','u_admin')`)
	exec(`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('instance-create-message','instance-create-chat','instance-create-agent','user','Create a useful Doc','2026-10-16T10:00:00Z','u_admin')`)
	for _, run := range []string{"creation-live-run", "unrelated-html-run"} {
		exec(`INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) VALUES(?,'ws_e2e_1','instance-create-agent','instance-create-crew','u_admin','instance-create-chat','instance-create-message','fixture-member',1,'fixture-generation',1,'2099-01-01T00:00:00Z')`, run)
	}
	exec(`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,run_id,expires_at,created_at) VALUES('instance-create-message','ws_e2e_1','u_admin','instance-create-chat','instance-consent','creation-live-run','2099-01-01T00:00:00Z','2026-10-16T10:00:00Z')`)
	exec(`INSERT INTO document_creations(page_id,workspace_id,run_id,agent_id,human_user_id,source_chat_id,submission_id,mutation_id,request_digest,created_at) VALUES('instance-created-doc','ws_e2e_1','historical-created-run','instance-create-agent','u_admin','instance-create-chat','historical-submission','historical-mutation',?,'2026-10-16T09:00:00Z')`, strings.Repeat("a", 64))
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "recovered-document-creation")
	report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	for _, query := range []string{`SELECT COUNT(*) FROM document_creation_consents`, `SELECT COUNT(*) FROM artifact_run_principals`} {
		var count int
		if err := target.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Errorf("creation authority survived physical recovery: count%d %v (%s)", count, err, query)
		}
	}
	for _, query := range []string{`SELECT COUNT(*) FROM document_creations WHERE run_id='historical-created-run' AND mutation_id='historical-mutation'`} {
		var count int
		if err := target.QueryRow(query).Scan(&count); err != nil || count != 1 {
			t.Errorf("historical provenance changed: count%d %v", count, err)
		}
	}
	if !strings.Contains(strings.Join(report.RuntimeReset, "\n"), "document creation") {
		t.Error("creation authority reset not reported")
	}
}
