package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestArtifactRequestUpdatesEncryptedHistoryRoundtrip(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := source.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('updates-crew',?,'Updates','updates')`, ws)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('updates-agent',?,'updates-crew','Agent','agent')`, ws)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('updates-chat',?,'updates-agent','Private')`, ws)
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('updates-page',?,'updates-page','Private','u_admin','{}')`, ws)
	exec(`INSERT INTO page_artifact_creations(page_id,run_id,agent_id,human_user_id,mutation_id,request_digest,created_at) VALUES('updates-page','updates-run','updates-agent','u_admin','create','digest','2026-10-13T00:00:00.000Z')`)
	exec(`INSERT INTO page_artifact_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,snapshot_json,receipt_json,status,run_id,created_at,updated_at,result_kind,notice_pending) VALUES('updates-request','updates-page','u_admin','updates-chat','updates-agent',1,'submission','digest','Private input','{}','LIVE AUTHORITY','succeeded','updates-run','2026-10-13T00:00:00.000Z','2026-10-13T00:00:00.000Z','reply',1)`)
	exec(`INSERT INTO page_artifact_request_updates(id,request_id,agent_id,run_id,on_behalf_of_user_id,mutation_id,fingerprint,operation,phase,message,body,created_at) VALUES('progress','updates-request','updates-agent','updates-run','u_admin','progress','digest','progress','preparing','Preparing safely','','2026-10-13T00:00:00.000Z'),('reply','updates-request','updates-agent','updates-run','u_admin','reply','digest','reply','','','Typed private reply','2026-10-13T00:01:00.000Z')`)
	// Historical attribution can outlive a different parent identity. Exercise
	// SET NULL itself independently of the parent's normal CASCADE behavior.
	exec(`INSERT INTO users(id,email,full_name) VALUES('updates-historical-human','updates-history@example.test','Historical')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('updates-historical-agent',?,'updates-crew','Historical','historical')`, ws)
	exec(`INSERT INTO page_artifact_request_updates(id,request_id,agent_id,run_id,on_behalf_of_user_id,mutation_id,fingerprint,operation,phase,message,created_at) VALUES('historical-progress','updates-request','updates-historical-agent','updates-run','updates-historical-human','historical','digest','progress','finalizing','Historical progress','2026-10-13T00:00:30.000Z')`)

	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "updates-encrypted-history-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Manifest.Encryption.Enabled {
		t.Fatal("not encrypted")
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: created.Path, Passphrase: "updates-encrypted-history-passphrase", Actor: actor}
			if fork {
				options.AsWorkspace = "updates-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_artifact_request_updates u JOIN page_artifact_requests q ON q.id=u.request_id JOIN page_artifact_creations c ON c.page_id=q.page_id WHERE q.run_id IS NULL AND u.run_id=c.run_id AND u.agent_id=q.agent_id AND u.on_behalf_of_user_id=q.human_user_id AND q.status='succeeded' AND q.result_kind='reply' AND q.receipt_json='{}' AND q.notice_pending=0 AND ((u.operation='reply' AND u.body='Typed private reply') OR (u.operation='progress' AND u.phase='preparing' AND u.message='Preparing safely'))`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("history/provenance count=%d err=%v", count, err)
			}
			var id, run string
			if err := target.QueryRow(`SELECT id,run_id FROM page_artifact_request_updates WHERE operation='reply'`).Scan(&id, &run); err != nil {
				t.Fatal(err)
			}
			if fork && (id == "reply" || run == "updates-run") {
				t.Fatal("fork identities unchanged")
			}
			if !fork && (id != "reply" || run != "updates-run") {
				t.Fatal("same restore identities changed")
			}
			for _, table := range []string{"artifact_run_principals", "artifact_creation_authorizations", "page_artifact_run_delegations"} {
				if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("restored authority %s count=%d err=%v", table, count, err)
				}
			}
			if _, err := target.Exec(`UPDATE page_artifact_request_updates SET body='rewritten' WHERE operation='reply'`); err == nil {
				t.Fatal("history mutable")
			}
			if _, err := target.Exec(`DELETE FROM agents WHERE id=(SELECT agent_id FROM page_artifact_request_updates WHERE mutation_id='historical')`); err != nil {
				t.Fatalf("historical agent FK null blocked: %v", err)
			}
			if _, err := target.Exec(`DELETE FROM users WHERE id=(SELECT on_behalf_of_user_id FROM page_artifact_request_updates WHERE mutation_id='historical')`); err != nil {
				t.Fatalf("historical human FK null blocked: %v", err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_artifact_request_updates WHERE mutation_id='historical' AND agent_id IS NULL AND on_behalf_of_user_id IS NULL AND message='Historical progress'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("nullable historical attribution=%d %v", count, err)
			}

			// Parent identity deletion follows existing request CASCADE policy; the
			// append-only guard must not obstruct the resulting history deletion.
			if _, err := target.Exec(`DELETE FROM agents WHERE id=(SELECT agent_id FROM page_artifact_requests LIMIT 1)`); err != nil {
				t.Fatalf("agent FK deletion blocked: %v", err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_artifact_request_updates`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("cascade history=%d %v", count, err)
			}
		})
	}
}
