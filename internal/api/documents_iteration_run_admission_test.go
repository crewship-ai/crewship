package api

import (
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func newIterationRunAdmissionRig(t *testing.T) *documentRunAdmissionRig {
	t.Helper()
	f, _, _, intent := iterationFixture(t)
	// Keep the original Page and human turn but let the real HTTP admission own
	// its journal, immutable principal and previously unbound targeted intent.
	mustExec(t, f.rig.db, `DELETE FROM document_iteration_intents WHERE run_id=?`, f.a.RunID)
	mustExec(t, f.rig.db, `DELETE FROM journal_entries WHERE trace_id=?`, f.a.RunID)
	mustExec(t, f.rig.db, `DELETE FROM artifact_run_principals WHERE run_id=?`, f.a.RunID)
	if err := f.pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	handler := NewInternalHandler(f.rig.db, "synthetic-host-admission", newTestLogger())
	handler.SetJournal(writer)
	handler.documentCreationEnabled = true
	return &documentRunAdmissionRig{f: f, handler: handler, writer: writer, body: map[string]any{
		"id": f.a.RunID, "agent_id": f.a.AgentID, "workspace_id": f.a.WorkspaceID, "chat_id": f.a.ChatID,
		"trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": f.a.MessageID,
		"document_iteration_intent": intent,
	}}
}

func TestDocumentIterationHTTPAdmissionRetainsOriginalBinding(t *testing.T) {
	rig := newIterationRunAdmissionRig(t)
	rig.f.rig.db.SetMaxOpenConns(1)
	creationExpect(t, rig.call(t, nil, false), 201)
	var before string
	if err := rig.f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, rig.f.a.RunID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	creationExpect(t, rig.call(t, nil, false), 201)
	var after string
	if err := rig.f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, rig.f.a.RunID).Scan(&after); err != nil || after != before {
		t.Fatalf("retry renewed principal: %q %q %v", before, after, err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM journal_entries WHERE entry_type='run.started' AND trace_id=?`,
		`SELECT count(*) FROM artifact_run_principals WHERE run_id=?`,
		`SELECT count(*) FROM document_iteration_intents WHERE run_id=? AND expires_at=(SELECT expires_at FROM artifact_run_principals WHERE run_id=document_iteration_intents.run_id)`,
	} {
		var count int
		if err := rig.f.rig.db.QueryRow(query, rig.f.a.RunID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("original binding count=%d err=%v", count, err)
		}
	}
}

func TestDocumentIterationHTTPAdmissionRejectsUntrustedAndRollsBack(t *testing.T) {
	for _, name := range []string{"crew", "no receipt", "combined purpose", "binding fault", "stale revision", "no retained intent"} {
		t.Run(name, func(t *testing.T) {
			rig := newIterationRunAdmissionRig(t)
			if name == "no retained intent" {
				mustExec(t, rig.f.rig.db, `DELETE FROM document_iteration_intents`)
			}
			if name == "binding fault" {
				mustExec(t, rig.f.rig.db, `CREATE TRIGGER refuse_iteration_bind BEFORE UPDATE OF run_id ON document_iteration_intents BEGIN SELECT RAISE(ABORT,'fixture bind fault'); END`)
			}
			if name == "stale revision" {
				mustExec(t, rig.f.rig.db, `UPDATE documents SET head_rev=head_rev+1 WHERE page_id=(SELECT page_id FROM document_iteration_intents WHERE message_id=?)`, rig.f.a.MessageID)
			}
			response := rig.call(t, func(body map[string]any) {
				if name == "no receipt" {
					delete(body, "human_authority")
				}
				if name == "combined purpose" {
					body["document_creation_intent"] = map[string]any{"submission_id": "another", "drafting": true}
				}
			}, name == "crew")
			if response.Code < 400 {
				t.Fatalf("invalid targeted admission accepted: %d", response.Code)
			}
			for _, query := range []string{
				`SELECT count(*) FROM journal_entries WHERE entry_type='run.started' AND trace_id=?`,
				`SELECT count(*) FROM artifact_run_principals WHERE run_id=?`,
				`SELECT count(*) FROM document_iteration_intents WHERE run_id=?`,
			} {
				var count int
				if err := rig.f.rig.db.QueryRow(query, rig.f.a.RunID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial refused admission: count=%d err=%v", count, err)
				}
			}
		})
	}
}
