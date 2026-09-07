package backup

import (
	"context"
	"testing"
)

func TestIssueWorkRestorePreservesHumanHoldAndDoesNotOverwriteExistingWork(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDBCov(t)
	ws := "work-ws"
	for _, stmt := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('u_admin','admin@e2e.test','Admin')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('work-ws','Work','work')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('member','work-ws','u_admin','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('c_alpha','work-ws','Alpha','alpha')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('a_alice','work-ws','c_alpha','Alice','alice')`,
	} {
		if _, err := source.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.Exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,mission_type) VALUES('held-issue',?,'c_alpha','a_alice','held-trace','Held work','TODO','issue')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('work-chat',?,'a_alice','Work')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO assignments(id,workspace_id,mission_id,chat_id,assigned_by_id,assigned_to_id,task,status,issue_brief_revision) VALUES('old-result',?,'held-issue','work-chat','a_alice','a_alice','Report','COMPLETED',1),('still-stopping',?,'held-issue','work-chat','a_alice','a_alice','Review','RUNNING',2)`, ws, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`UPDATE issue_work SET mode='human',worker_user_id='u_admin',revision=7,note='Verified report; waiting for client',brief_revision=2 WHERE mission_id='held-issue'`); err != nil {
		t.Fatal(err)
	}
	dump, err := DumpWorkspace(ctx, source, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["issue_work"]) != 1 {
		t.Fatal("hold missing from backup")
	}
	target := openMigratedDBCov(t)
	// Global users are normally imported by the restore runner's identity phase.
	if _, err := target.Exec(`INSERT OR IGNORE INTO users(id,email,full_name) VALUES('u_admin','admin@e2e.test','Admin')`); err != nil {
		t.Fatal(err)
	}
	if err := RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	var oldBrief int
	var stopped string
	if err := target.QueryRow(`SELECT issue_brief_revision FROM assignments WHERE id='old-result'`).Scan(&oldBrief); err != nil || oldBrief != 1 {
		t.Fatalf("lost historical brief snapshot: %d %v", oldBrief, err)
	}
	if err := target.QueryRow(`SELECT status FROM assignments WHERE id='still-stopping'`).Scan(&stopped); err != nil || stopped != "CANCELLED" {
		t.Fatalf("restored live work on human hold: %s %v", stopped, err)
	}
	var mode, note string
	var rev, brief int
	if err := target.QueryRow(`SELECT mode,revision,note,brief_revision FROM issue_work WHERE mission_id='held-issue'`).Scan(&mode, &rev, &note, &brief); err != nil {
		t.Fatal(err)
	}
	if mode != "human" || rev != 7 || brief != 2 || note != "Verified report; waiting for client" {
		t.Fatalf("restore lost hold: %s %d %d %s", mode, rev, brief, note)
	}
	if _, err := target.Exec(`UPDATE issue_work SET revision=8,note='Newer work' WHERE mission_id='held-issue'`); err != nil {
		t.Fatal(err)
	}
	if err := RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(`SELECT revision,note FROM issue_work WHERE mission_id='held-issue'`).Scan(&rev, &note); err != nil {
		t.Fatal(err)
	}
	if rev != 8 || note != "Newer work" {
		t.Fatal("restore overwrote an existing target's work")
	}
}
