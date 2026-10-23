package database

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

func TestAssignmentWorkMigrationAdoptsOnlyUnexecutedDomains(t *testing.T) {
	db := migrationBoundaryDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateSkipping(ctx, db, logger, "assignment_work_admission"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','test','test')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','ws','test','test')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('agent','ws','crew','test','test')`,
		`INSERT INTO chats(id,workspace_id,agent_id,title,mode,status) VALUES('chat','ws','agent','test','MISSION','ACTIVE')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"PENDING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELLED"} {
		if _, err := db.Exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,lease_owner,exec_id,created_at) VALUES(?,'ws','chat','agent','agent','immutable task',?,CASE WHEN ?='RUNNING' THEN 'legacy-owner' END,CASE WHEN ?='RUNNING' THEN 'legacy-exec' END,'2026-10-12T13:00:00Z')`, status, status, status, status); err != nil {
			t.Fatal(err)
		}
	}
	// An already accepted domain keeps its original owner; upgrade must not
	// replace its input, burn an attempt or create another queue row.
	if _, err := db.Exec(`INSERT INTO work_items(id,workspace_id,source,domain_kind,domain_id,agent_id,crew_id,class,input_json,state,eligible_at,created_at,updated_at) VALUES('existing','ws','assignment','assignment','QUEUED','agent','crew','background','{"existing":true}','queued','2026-10-12T13:00:00Z','2026-10-12T13:00:00Z','2026-10-12T13:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, db, logger); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE source='assignment'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("owners=%d err=%v", count, err)
	}
	var domain, agent, crew, scope, state, raw string
	var generation, attempts int
	if err := db.QueryRow(`SELECT domain_id,agent_id,crew_id,authorized_scope,state,generation,attempts,input_json FROM work_items WHERE id='assignment:PENDING'`).Scan(&domain, &agent, &crew, &scope, &state, &generation, &attempts, &raw); err != nil {
		t.Fatal(err)
	}
	if domain != "PENDING" || agent != "agent" || crew != "crew" || scope != "assignment:PENDING" || state != "queued" || generation != 0 || attempts != 0 {
		t.Fatalf("changed acceptance identity: %s/%s/%s/%s/%s/%d/%d", domain, agent, crew, scope, state, generation, attempts)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"version": float64(1), "assignment_id": "PENDING", "workspace_id": "ws", "agent_id": "agent", "crew_id": "crew", "task": "immutable task", "author_agent_id": "agent"} {
		if input[key] != want {
			t.Fatalf("input %s=%v want=%v", key, input[key], want)
		}
	}
	if err := db.QueryRow(`SELECT input_json FROM work_items WHERE id='existing'`).Scan(&raw); err != nil || raw != `{"existing":true}` {
		t.Fatalf("existing input=%q err=%v", raw, err)
	}
	var status, owner, exec string
	if err := db.QueryRow(`SELECT status,lease_owner,exec_id FROM assignments WHERE id='RUNNING'`).Scan(&status, &owner, &exec); err != nil || status != "RUNNING" || owner != "legacy-owner" || exec != "legacy-exec" {
		t.Fatalf("legacy execution changed: %s/%s/%s err=%v", status, owner, exec, err)
	}
}

func TestAssignmentWorkMigrationRefusesAmbiguousExecution(t *testing.T) {
	db := migrationBoundaryDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateSkipping(ctx, db, logger, "assignment_work_admission"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','test','test')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','ws','test','test')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('agent','ws','crew','test','test')`,
		`INSERT INTO chats(id,workspace_id,agent_id,title,mode,status) VALUES('chat','ws','agent','test','MISSION','ACTIVE')`,
		`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status) VALUES('ambiguous','ws','chat','agent','agent','test','QUEUED')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`UPDATE assignments SET exec_id='old-exec',exec_container_id=NULL WHERE id='ambiguous'`,
		`UPDATE assignments SET exec_id=NULL,exec_container_id='old-container' WHERE id='ambiguous'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(ctx, db, logger); err == nil {
			t.Fatal("ambiguous execution was adopted as new queued work")
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE source='assignment'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed migration leaked admission: %d %v", count, err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE name='assignment_work_admission'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed migration was acknowledged: %d %v", count, err)
		}
	}
	// Model an operator-confirmed terminal reconciliation, rather than clearing
	// a locator and pretending the old execution never happened.
	if _, err := db.Exec(`UPDATE assignments SET status='FAILED',finished_at='2026-10-12T14:00:00Z',error_message='operator confirmed old execution ended' WHERE id='ambiguous'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("reconciled upgrade: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE domain_id='ambiguous'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("terminal domain relaunched: %d %v", count, err)
	}
}
