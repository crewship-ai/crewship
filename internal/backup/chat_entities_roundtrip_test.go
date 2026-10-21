package backup_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/chatentities"
)

func seedChatEntityHistory(t *testing.T, db *sql.DB) string {
	t.Helper()
	ws := seedWorkspace(t, db)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES('history-chat',?,'a_alice','u_admin')`, ws)
	exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,identifier,status) VALUES('history-live',?,'c_alpha','a_alice','history-live-trace','Live','HIS-1','BACKLOG'),('history-gone',?,'c_alpha','a_alice','history-gone-trace','Gone','HIS-2','BACKLOG')`, ws, ws)
	tx, e := db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	for _, entity := range []string{"history-live", "history-gone"} {
		s := chatentities.OperationSpec{ID: "operation-" + entity, WorkspaceID: ws, Key: entity, RequestHash: "hash", ActorType: "agent", ActorID: "a_alice", RunID: "history-run", HumanAuthority: json.RawMessage(`{"historical_receipt":"source-only"}`), Provenance: "verified", Kind: "issue", Verb: "created"}
		o, _, e := chatentities.BeginOperation(t.Context(), tx, s)
		if e != nil {
			t.Fatal(e)
		}
		if e = chatentities.CompleteOperation(t.Context(), tx, o.ID, chatentities.ResultSpec{EntityID: entity, Revision: 1, AggregateRevision: 1, Result: json.RawMessage(`{"historical_result":true}`)}); e != nil {
			t.Fatal(e)
		}
		if _, e = chatentities.AppendRef(t.Context(), tx, chatentities.RefSpec{EventID: "event-" + entity, ChatID: "history-chat", OperationID: o.ID, RunID: "history-run", TriggerMessageID: "historical-message"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = tx.Exec(`DELETE FROM missions WHERE id='history-gone'`); e != nil {
		t.Fatal(e)
	}
	s := chatentities.OperationSpec{ID: "operation-delete", WorkspaceID: ws, Key: "delete", RequestHash: "delete-hash", ActorType: "human", ActorID: "u_admin", Provenance: "human", Kind: "issue", Verb: "deleted"}
	o, _, e := chatentities.BeginOperation(t.Context(), tx, s)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, _ := json.Marshal(chatentities.DeletionAccessSnapshot{Version: 1, WorkspaceID: ws, Visibility: "trusted_workspace"})
	if e = chatentities.CompleteOperation(t.Context(), tx, o.ID, chatentities.ResultSpec{EntityID: "history-gone", Revision: 1, AggregateRevision: 1, AccessSnapshot: snapshot, Result: json.RawMessage(`{"deleted":true}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e = chatentities.AppendDeletionRefs(t.Context(), tx, o.ID, ws, "history-gone"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	return ws
}
func TestChatEntityHistoryBackupRestoreIntoPopulatedTarget(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedChatEntityHistory(t, source)
	dump, e := backup.DumpWorkspace(t.Context(), source, ws)
	if e != nil {
		t.Fatal(e)
	}
	if len(dump.Tables["agent_operations"]) != 3 || len(dump.Tables["chat_entity_refs"]) != 3 {
		t.Fatal("durable operation history omitted")
	}
	// Serialized table rows need not arrive in chronological order.
	refs := dump.Tables["chat_entity_refs"]
	refs[0], refs[2] = refs[2], refs[0]
	target := openMigratedDB(t)
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('other-workspace','Other','other-workspace')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('other-crew','other-workspace','Other','other')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('other-agent','other-workspace','other-crew','Other','other')`,
		`INSERT INTO chats(id,workspace_id,agent_id) VALUES('other-chat','other-workspace','other-agent')`,
		`INSERT INTO agent_operations(id,workspace_id,operation_key,request_hash,actor_type,actor_id,provenance,kind,verb,entity_id,status,created_at) VALUES('other-op','other-workspace','other-key','hash','human','other-human','human','issue','deleted','other-issue','completed','now')`,
		`INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES('other-event','other-chat','other-op','other-run','other-message','now')`,
	} {
		if _, e = target.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	if e = backup.RestoreDump(t.Context(), target, dump); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = target.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs WHERE chat_id='history-chat'`).Scan(&count); e != nil || count != 3 {
		t.Fatalf("lost refs %d %v", count, e)
	}
	var lastVerb string
	if e = target.QueryRow(`SELECT op.verb FROM chat_entity_refs r JOIN agent_operations op ON op.id=r.operation_id WHERE r.chat_id='history-chat' ORDER BY r.chat_seq DESC LIMIT 1`).Scan(&lastVerb); e != nil || lastVerb != "deleted" {
		t.Fatalf("chronology changed %s %v", lastVerb, e)
	}
	var minSeq int
	if e = target.QueryRow(`SELECT MIN(chat_seq) FROM chat_entity_refs WHERE chat_id='history-chat'`).Scan(&minSeq); e != nil || minSeq <= 1 {
		t.Fatalf("sequence collision %d %v", minSeq, e)
	}
	op, e := chatentities.GetOperation(t.Context(), target, ws, "operation-delete")
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := chatentities.ParseDeletionAccessSnapshot(op.AccessSnapshot)
	if e != nil || snapshot.WorkspaceID != ws || op.ActorID != "u_admin" {
		t.Fatalf("deletion provenance lost %+v %v", op, e)
	}
	if e = backup.RestoreDump(t.Context(), target, dump); e != nil {
		t.Fatal(e)
	}
	if e = target.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs`).Scan(&count); e != nil || count != 4 {
		t.Fatalf("repeat restore duplicated history %d %v", count, e)
	}
}
func TestChatEntityHistoryForkPreservesReferencesWithoutAuthority(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedChatEntityHistory(t, source)
	dump, e := backup.DumpWorkspace(t.Context(), source, ws)
	if e != nil {
		t.Fatal(e)
	}
	if e = backup.RemapIDs(t.Context(), source, dump); e != nil {
		t.Fatal(e)
	}
	// RestoreBackup --as-workspace supplies a distinct workspace slug.
	dump.Tables["workspaces"][0]["slug"] = "forked-history"
	// Same-instance fork used to collide with both chat_seq and event_id.
	if e = backup.RestoreDump(t.Context(), source, dump); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = source.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs`).Scan(&count); e != nil || count != 6 {
		t.Fatalf("fork lost history %d %v", count, e)
	}
	forkWorkspace, _ := dump.Tables["workspaces"][0]["id"].(string)
	if forkWorkspace == "" || forkWorkspace == ws {
		t.Fatal("workspace did not fork")
	}
	for _, row := range dump.Tables["agent_operations"] {
		id, _ := row["id"].(string)
		o, e := chatentities.GetOperation(t.Context(), source, forkWorkspace, id)
		if e != nil {
			t.Fatal(e)
		}
		if string(o.HumanAuthority) != "{}" {
			t.Fatal("fork imported execution authority")
		}
		if o.ActorType == "agent" && o.ActorID == "a_alice" {
			t.Fatal("fork points at original agent")
		}
		if o.Verb == "deleted" {
			snapshot, e := chatentities.ParseDeletionAccessSnapshot(o.AccessSnapshot)
			if e != nil || snapshot.WorkspaceID != forkWorkspace {
				t.Fatal("deletion boundary did not fork")
			}
		}
		if o.Verb != "deleted" && o.EntityID != "history-gone" {
			var issueWorkspace string
			if e = source.QueryRow(`SELECT workspace_id FROM missions WHERE id=?`, o.EntityID).Scan(&issueWorkspace); e != nil || issueWorkspace != forkWorkspace {
				t.Fatalf("issue did not fork %s %v", issueWorkspace, e)
			}
		}
	}
}
