package backup_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestArtifactFeedbackBackupPreservesThreadsWithoutRuntimeAuthority(t *testing.T) {
	source := openDurableArtifactBackupDB(t)
	ws := seedWorkspace(t, source)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := source.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('feedback-owner-member',?,'u_admin','OWNER')`, ws)
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('feedback-page',?,'feedback','Feedback','u_admin','{}')`, ws)
	store := artifacts.NewVersionStore(source, artifacts.VersionOptions{Enabled: true})
	auth := func(context.Context, *sql.Tx) (string, error) { return "u_admin", nil }
	if _, err := store.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: ws, PageID: "feedback-page"}, artifacts.VersionRequest{EntryPoint: "index.html", Files: map[string]artifacts.VersionFile{"index.html": {Content: []byte("Retained content"), MediaType: "text/html"}}}, auth); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('feedback-crew',?,'Feedback','feedback')`, ws)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('feedback-agent',?,'feedback-crew','Agent','feedback')`, ws)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('feedback-chat',?,'feedback-agent','u_admin','private')`, ws)
	exec(`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('feedback-message','feedback-chat','feedback-agent','user','Original request',datetime('now'),'u_admin')`)
	exec(`INSERT INTO artifact_project_comments(id,workspace_id,page_id,version,path,selector,quote,text,author,created_at) VALUES('feedback-comment',?,'feedback-page',1,'index.html','#hero','Retained','Change this','u_admin',datetime('now'))`, ws)
	exec(`INSERT INTO artifact_project_comment_tasks(id,comment_id,workspace_id,page_id,base_version,created_at) VALUES('feedback-task','feedback-comment',?,'feedback-page',1,datetime('now'))`, ws)
	exec(`INSERT INTO artifact_project_comment_replies(id,comment_id,version,text,author,created_at) VALUES('feedback-reply','feedback-comment',1,'Need clarification','u_admin',datetime('now'))`)
	exec(`INSERT INTO artifact_project_origins(page_id,workspace_id,human_user_id,chat_id,agent_id,message_id,created_at) VALUES('feedback-page',?,'u_admin','feedback-chat','feedback-agent','feedback-message',datetime('now'))`, ws)
	exec(`INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT 'feedback-run',?,'feedback-agent','feedback-crew','u_admin','feedback-chat','feedback-message',m.id,m.access_revision,c.authority_generation,c.authority_revision,datetime('now','+1 hour') FROM workspace_members m JOIN chats c ON c.id='feedback-chat' WHERE m.workspace_id=? AND m.user_id='u_admin'`, ws, ws)
	exec(`INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,run_id,created_at) VALUES('feedback-message',?,'u_admin','feedback-chat','feedback-submission','feedback-run',datetime('now'))`, ws)
	exec(`INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,source_kind,expires_at,result_page_id,created_at) VALUES('feedback-run',?,'u_admin','feedback-chat','feedback-message','chat',datetime('now','+1 hour'),'feedback-page',datetime('now'))`, ws)
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "feedback-portable-test-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openDurableArtifactBackupDB(t)
			opts := backup.RestoreOptions{Path: bundle.Path, Passphrase: "feedback-portable-test-passphrase", Actor: actor}
			if fork {
				opts.AsWorkspace = "feedback-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
				t.Fatal(err)
			}
			var threads int
			err := target.QueryRow(`SELECT count(*) FROM artifact_project_comments c JOIN artifact_project_comment_replies r ON r.comment_id=c.id JOIN artifact_project_comment_tasks t ON t.comment_id=c.id AND t.page_id=c.page_id AND t.workspace_id=c.workspace_id JOIN artifact_project_origins o ON o.page_id=c.page_id AND o.workspace_id=c.workspace_id JOIN pages p ON p.id=c.page_id AND p.workspace_id=c.workspace_id WHERE c.quote='Retained' AND c.text='Change this' AND r.text='Need clarification'`).Scan(&threads)
			if err != nil || threads != 1 {
				t.Fatalf("feedback graph lost: %d: %v", threads, err)
			}
			for _, table := range []string{"artifact_run_principals", "artifact_project_run_consents", "artifact_project_message_intents"} {
				var n int
				if err := target.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
					t.Errorf("runtime authority restored in %s: %d: %v", table, n, err)
				}
			}
			rows, err := target.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() {
				t.Fatal("feedback restore left broken foreign keys")
			}
		})
	}
}
