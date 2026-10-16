package backup_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

func TestDocumentRequestsEncryptedHistoryRoundtrip(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	seedBackupDocument(t, source, ws, "request-doc", "u_admin")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := source.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE documents SET generation=7 WHERE page_id='request-doc'`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('doc-crew',?,'Docs','docs')`, ws)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('doc-agent',?,'doc-crew','Agent','doc-agent')`, ws)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('doc-chat',?,'doc-agent','Private')`, ws)
	exec(`INSERT INTO users(id,email,full_name) VALUES('doc-former-human','doc-former@example.test','Former member')`)
	const anchor = `{"id":"comment","block":"stable-block","quote":"retained quote","checkpoint":2,"start":"relative-start","end":"relative-end","spans":[{"start":"relative-start","end":"relative-first-end"},{"start":"relative-second-start","end":"relative-end"}]}`
	exec(`INSERT INTO document_comments(workspace_id,page_id,id,body,anchor_json,actor,rev,version,resolved) VALUES(?,'request-doc','comment','Retained discussion',?,'user:u_admin',2,3,1)`, ws, anchor)
	for _, status := range []string{"queued", "running", "succeeded", "failed"} {
		exec(`INSERT INTO document_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,receipt_json,status,run_id,result_revision,checkpoint_id,error,created_at,updated_at) VALUES(?,'request-doc','u_admin','doc-chat','doc-agent',2,?,'digest','Retained instructions','LIVE AUTHORITY',?,'source-live-run',2,'original-checkpoint','Historical failure','2026-10-15T16:00:00Z','2026-10-15T16:01:00Z')`, "doc-request-"+status, status, status)
	}
	exec(`UPDATE document_requests SET human_user_id='doc-former-human' WHERE status='succeeded'`)
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "document-request-history-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Manifest.Encryption.Enabled {
		t.Fatal("backup not encrypted")
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: "document-request-history-passphrase", Actor: actor}
			if fork {
				options.AsWorkspace = "document-request-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := target.QueryRow(`SELECT COUNT(*) FROM document_requests q JOIN pages p ON p.id=q.page_id JOIN chats c ON c.id=q.chat_id JOIN agents a ON a.id=q.agent_id JOIN document_checkpoints cp ON cp.page_id=q.page_id AND cp.workspace_id=p.workspace_id AND cp.id=q.checkpoint_id WHERE q.receipt_json='{}' AND q.run_id IS NULL AND q.instruction='Retained instructions' AND q.result_revision=2 AND q.human_user_id IN ('u_admin','doc-former-human') AND c.agent_id=a.id AND a.workspace_id=p.workspace_id`).Scan(&count); err != nil || count != 4 {
				t.Fatalf("restored attribution/history=%d: %v", count, err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM document_requests WHERE status IN ('queued','running')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("replayed work=%d: %v", count, err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM document_requests WHERE status='succeeded'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("completed result lost=%d: %v", count, err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM document_requests WHERE status='failed' AND error LIKE '%backup restore%'`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("live requests not suspended=%d: %v", count, err)
			}
			var restoredAnchor, body, actorName, page, chat, agent, commentID string
			var generation, epoch, version, resolved int
			if err := target.QueryRow(`SELECT c.id,c.anchor_json,c.body,c.actor,c.version,c.resolved,d.generation,d.epoch FROM document_comments c JOIN documents d ON d.workspace_id=c.workspace_id AND d.page_id=c.page_id`).Scan(&commentID, &restoredAnchor, &body, &actorName, &version, &resolved, &generation, &epoch); err != nil {
				t.Fatal(err)
			}
			var actualAnchor, expectedAnchor map[string]any
			if err := json.Unmarshal([]byte(restoredAnchor), &actualAnchor); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(anchor), &expectedAnchor); err != nil {
				t.Fatal(err)
			}
			expectedAnchor["id"] = commentID
			if !reflect.DeepEqual(actualAnchor, expectedAnchor) || body != "Retained discussion" || actorName != "user:u_admin" || version != 3 || resolved != 1 || generation != 8 || epoch != 42 {
				t.Fatalf("comment/relative anchor or generation changed: %q %q %q %d %d %d %d", restoredAnchor, body, actorName, version, resolved, generation, epoch)
			}
			if err := target.QueryRow(`SELECT page_id,chat_id,agent_id FROM document_requests LIMIT 1`).Scan(&page, &chat, &agent); err != nil {
				t.Fatal(err)
			}
			if fork && (page == "request-doc" || chat == "doc-chat" || agent == "doc-agent") {
				t.Fatal("fork kept source identities")
			}
			if !fork && (page != "request-doc" || chat != "doc-chat" || agent != "doc-agent") {
				t.Fatal("ordinary restore changed identities")
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("live principals restored=%d: %v", count, err)
			}
		})
	}
}

func TestDocumentRequestsInstanceRecoveryClearsOnlyDocumentAuthority(t *testing.T) {
	f := newInstanceFixture(t)
	seedBackupDocument(t, f.db, "ws_e2e_1", "instance-request-doc", "u_admin")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('instance-doc-crew','ws_e2e_1','Docs','instance-docs')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('instance-doc-agent','ws_e2e_1','instance-doc-crew','Agent','instance-doc-agent')`)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('instance-doc-chat','ws_e2e_1','instance-doc-agent','Private')`)
	for _, run := range []string{"document-live-run", "unrelated-html-run"} {
		exec(`INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) VALUES(?,'ws_e2e_1','instance-doc-agent','instance-doc-crew','u_admin','instance-doc-chat','message','member',1,'generation',1,'2099-01-01T00:00:00Z')`, run)
	}
	exec(`INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) VALUES('instance-request-doc','document-live-run','u_admin','2099-01-01T00:00:00Z','2026-10-15T16:00:00Z')`)
	exec(`INSERT INTO document_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,receipt_json,status,run_id,result_revision,created_at,updated_at) VALUES('instance-doc-request','instance-request-doc','u_admin','instance-doc-chat','instance-doc-agent',2,'submission','digest','Keep history','LIVE AUTHORITY','running','document-live-run',2,'2026-10-15T16:00:00Z','2026-10-15T16:00:00Z')`)
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "recovered-doc-request")
	if _, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}); err != nil {
		t.Fatal(err)
	}
	target, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var count int
	for _, query := range []string{`SELECT COUNT(*) FROM document_requests WHERE status='failed' AND run_id IS NULL AND receipt_json='{}' AND result_revision=2 AND instruction='Keep history'`, `SELECT COUNT(*) FROM artifact_run_principals WHERE run_id='unrelated-html-run'`} {
		if err := target.QueryRow(query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("history/baseline count=%d: %v", count, err)
		}
	}
	for _, query := range []string{`SELECT COUNT(*) FROM artifact_run_principals WHERE run_id='document-live-run'`, `SELECT COUNT(*) FROM page_artifact_run_delegations WHERE run_id='document-live-run'`} {
		if err := target.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("document live authority restored=%d: %v", count, err)
		}
	}
}
