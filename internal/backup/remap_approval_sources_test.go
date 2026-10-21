package backup_test

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestApprovalForkKeepsPrivateReferencesWithoutReanchoring(t *testing.T) {
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	// seedWorkspace includes a raw pre-chain journal fixture. Start this
	// synthetic source with real writer entries before testing chain integrity.
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE workspace_id=?`, ws); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('approval-fork-member','ws_e2e_1','u_admin','OWNER')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('approval-fork-chat','ws_e2e_1','a_alice','u_admin','private')`,
		`INSERT INTO approvals_queue(id,workspace_id,agent_id,requested_by,kind,reason,payload,status,answer_operation_key,answer_operation_hash) VALUES('approval-live','ws_e2e_1','a_alice','a_alice','custom','Private source','{"tool":"agent_run","gate_mode":"sync","correlation":{"trigger_type":"human_chat","chat_id":"approval-fork-chat","run_id":"approval-original-run","trigger_message_id":"approval-original-trigger"}}','pending','original-key','original-hash')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	a, err := chataudience.CaptureTrusted(t.Context(), db, "approval-fork-chat", "u_admin", nil)
	if err != nil || a == nil {
		t.Fatalf("source audience %+v %v", a, err)
	}
	writer := journal.NewWriter(db, nil, journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	for _, entry := range []journal.Entry{
		{WorkspaceID: ws, AgentID: "a_alice", TraceID: "approval-original-run", Type: journal.EntryRunStarted, ActorType: journal.ActorOrchestrator, Summary: "Original host proof", Payload: map[string]any{"trigger_type": "USER", "chat_id": "approval-fork-chat", "trigger_message_id": "approval-original-trigger", "human_authority": a}},
		{WorkspaceID: ws, Type: journal.EntryApprovalRequest, ActorType: journal.ActorAgent, Summary: "Private live source", Payload: map[string]any{"approval_id": "approval-live", "unrelated_id": "approval-live"}, Refs: map[string]any{"approval_id": "approval-live"}},
		{WorkspaceID: ws, Type: journal.EntryApprovalRequest, ActorType: journal.ActorAgent, Summary: "Private deleted source", Payload: map[string]any{"approval_id": "approval-deleted"}, Refs: map[string]any{"approval_id": "approval-deleted"}},
		{WorkspaceID: ws, Type: journal.EntryApprovalSourceRetired, ActorType: journal.ActorSystem, Summary: "Private approval source retired", Payload: map[string]any{}, Refs: map[string]any{"approval_id": "approval-deleted"}},
	} {
		if _, err = writer.Emit(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), db, ws)
	if err != nil {
		t.Fatal(err)
	}
	var originalProof, originalPayload string
	if err = db.QueryRow(`SELECT payload FROM journal_entries WHERE entry_type='run.started' AND workspace_id=?`, ws).Scan(&originalProof); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT payload FROM approvals_queue WHERE id='approval-live'`).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}
	if err = backup.RemapIDs(t.Context(), db, dump); err != nil {
		t.Fatal(err)
	}
	fork := dump.Tables["workspaces"][0]["id"].(string)
	dump.Tables["workspaces"][0]["slug"] = "approval-fork-copy"
	if err = backup.RestoreDump(t.Context(), db, dump); err != nil {
		t.Fatal(err)
	}
	var id, payload, key, hash string
	if err = db.QueryRow(`SELECT id,payload,answer_operation_key,answer_operation_hash FROM approvals_queue WHERE workspace_id=?`, fork).Scan(&id, &payload, &key, &hash); err != nil || id == "approval-live" || payload != originalPayload || key != "original-key" || hash != "original-hash" {
		t.Fatalf("fork source identity/proof/receipt %q %v", id, err)
	}
	var refsRaw, payloadRaw, proofRaw string
	if err = db.QueryRow(`SELECT refs,payload FROM journal_entries WHERE workspace_id=? AND summary='Private live source'`, fork).Scan(&refsRaw, &payloadRaw); err != nil {
		t.Fatal(err)
	}
	var refs, projection map[string]any
	if json.Unmarshal([]byte(refsRaw), &refs) != nil || json.Unmarshal([]byte(payloadRaw), &projection) != nil || refs["approval_id"] != id || projection["approval_id"] != id || projection["unrelated_id"] != "approval-live" {
		t.Fatalf("fork lost typed source binding refs=%s payload=%s expected=%s", refsRaw, payloadRaw, id)
	}
	if err = db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='run.started'`, fork).Scan(&proofRaw); err != nil || proofRaw != originalProof {
		t.Fatalf("fork rewrote original authority %v", err)
	}
	var supported, visible bool
	if err = db.QueryRow(`SELECT (`+chataudience.VerifiedRunApprovalSourceSQL+`),(`+chataudience.VerifiedRunApprovalReadSQL+`) FROM approvals_queue aq WHERE aq.workspace_id=?`, append(chataudience.VerifiedRunApprovalReadArgs("u_admin"), fork)...).Scan(&supported, &visible); err != nil || !supported || visible {
		t.Fatalf("fork manufactured authority or lost classification %v/%v %v", supported, visible, err)
	}
	retired, chat, err := chataudience.RunApprovalJournalSource(t.Context(), db, fork, "approval-deleted")
	if err != nil || !retired || chat != "" {
		t.Fatalf("fork declassified deleted source %v %q %v", retired, chat, err)
	}
	var unchanged int
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND json_extract(refs,'$.approval_id')='approval-deleted'`, fork).Scan(&unchanged); err != nil || unchanged != 2 {
		t.Fatalf("deleted source references diverged %d %v", unchanged, err)
	}
	assertNoFKViolations(t, db, "approval fork")

	// The normal fork runner re-signs mutated bundle rows before restoring;
	// changing the bundle must never mutate the live append-only source chain.
	actor := backup.Actor{UserID: "u_admin", Role: "ADMIN"}
	created, err := backup.CreateBackup(t.Context(), db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Actor: actor, Passphrase: "synthetic-approval-fork-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: created.Path, Passphrase: "synthetic-approval-fork-passphrase", AsWorkspace: "approval-canonical-copy", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	canonical := restored.RestoredWorkspaceID
	if err = db.QueryRow(`SELECT id FROM approvals_queue WHERE workspace_id=?`, canonical).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT refs FROM journal_entries WHERE workspace_id=? AND summary='Private live source'`, canonical).Scan(&refsRaw); err != nil || json.Unmarshal([]byte(refsRaw), &refs) != nil || refs["approval_id"] != id {
		t.Fatalf("canonical fork source linkage %s %v", refsRaw, err)
	}
	for _, workspace := range []string{ws, canonical} {
		verified, err := journal.VerifyChain(t.Context(), db, workspace)
		if err != nil || !verified.OK {
			t.Fatalf("live source/canonical fork chain %s %+v %v", workspace, verified, err)
		}
	}
	if err = db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='run.started'`, canonical).Scan(&proofRaw); err != nil || proofRaw != originalProof {
		t.Fatalf("canonical fork reanchored original proof %v", err)
	}
}
