package backup_test

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestRestoreLegacyWorkAddsInvalidOriginsWithoutDiscardingRecords(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	for _, q := range []string{
		`UPDATE assignments SET status='PENDING',task='Preserve original task' WHERE id='origin-assignment'`,
		`UPDATE missions SET mission_type='orchestration',status='IN_PROGRESS' WHERE id='history-live'`,
		`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('legacy-human-parent','ws_e2e_1','a_alice','run.started','orchestrator','Historical human request',json_object('trigger_type','USER','chat_id','history-chat','human_authority',json((SELECT human_authority FROM execution_origins WHERE id='execution-root')),'trigger_message_id','parent-message'),'human-parent')`,
		`UPDATE pending_runs SET status='pending',chain_origin='human-parent' WHERE id='origin-pending'`,
		`UPDATE pipeline_runs SET status='queued',triggered_by_id='human-parent' WHERE id='origin-run'`,
		`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status) VALUES('current-autonomous','ws_e2e_1','history-chat','a_alice','a_bob','Current autonomous task','PENDING'),('legacy-terminal','ws_e2e_1','history-chat','a_alice','a_bob','Historical completed task','COMPLETED')`,
		`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,status,fire_at,chain_origin) VALUES('legacy-machine','ws_e2e_1','origin-pipeline','origin-pipeline','pending','2026-10-13T10:00:00Z','machine-parent')`,
	} {
		if _, e := source.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := runauthority.Resolve(t.Context(), source, "human-parent", "a_alice", workspace); e != nil {
		t.Fatalf("authentic source receipt invalid: %v", e)
	}
	dump, e := backup.DumpWorkspace(t.Context(), source, workspace)
	if e != nil {
		t.Fatal(e)
	}
	delete(dump.Tables, "execution_origins")
	for _, rows := range dump.Tables {
		for _, row := range rows {
			if row["id"] == "current-autonomous" {
				continue
			} // Explicit NULL in a current dump is meaningful.
			delete(row, "chat_origin_id")
		}
	}
	target := openMigratedDB(t)
	var before int
	if e = target.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='human-parent'`).Scan(&before); e != nil || before != 0 {
		t.Fatalf("target already had source authority: %d %v", before, e)
	}

	if e = backup.RestoreDump(t.Context(), target, dump); e != nil {
		t.Fatal(e)
	}
	var sentinel string
	if e = target.QueryRow(`SELECT id FROM execution_origins WHERE workspace_id=? AND root_run_id=?`, workspace, runauthority.LegacyUnverifiedDelegationRootRunID).Scan(&sentinel); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ table, id, status string }{{"assignments", "origin-assignment", "PENDING"}, {"missions", "history-live", "IN_PROGRESS"}, {"pending_runs", "origin-pending", "pending"}, {"pipeline_runs", "origin-run", "queued"}} {
		var origin, status string
		if e = target.QueryRow(`SELECT chat_origin_id,status FROM `+tc.table+` WHERE id=?`, tc.id).Scan(&origin, &status); e != nil || origin != sentinel || status != tc.status {
			t.Fatalf("%s changed: origin=%s status=%s err=%v", tc.id, origin, status, e)
		}
	}
	var task string
	if e = target.QueryRow(`SELECT task FROM assignments WHERE id='origin-assignment'`).Scan(&task); e != nil || task != "Preserve original task" {
		t.Fatal("legacy task discarded")
	}
	for _, tc := range []struct{ table, id string }{{"assignments", "current-autonomous"}, {"assignments", "legacy-terminal"}, {"pending_runs", "legacy-machine"}} {
		var autonomous bool
		if e = target.QueryRow(`SELECT chat_origin_id IS NULL FROM `+tc.table+` WHERE id=?`, tc.id).Scan(&autonomous); e != nil || !autonomous {
			t.Fatalf("unrelated standing authority changed for %s: %v", tc.id, e)
		}
	}
	if _, e = runauthority.ValidateOrigin(t.Context(), target, sentinel, "a_bob", workspace); !errors.Is(e, runauthority.ErrIdentityMismatch) {
		t.Fatalf("legacy imported work became executable: %v", e)
	}
	if e = backup.RestoreDump(t.Context(), target, dump); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = target.QueryRow(`SELECT COUNT(*) FROM execution_origins WHERE workspace_id=? AND root_run_id=?`, workspace, runauthority.LegacyUnverifiedDelegationRootRunID).Scan(&count); e != nil || count != 1 {
		t.Fatal("repeat import duplicated quarantine origin")
	}
	for _, rows := range dump.Tables {
		for _, row := range rows {
			if row["id"] == "origin-assignment" {
				if _, ok := row["chat_origin_id"]; ok {
					t.Fatal("restore mutated caller bundle")
				}
			}
		}
	}
}
