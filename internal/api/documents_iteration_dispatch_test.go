package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

// Exercise the production draft dispatcher, rather than calling the isolated
// iteration helper directly. A follow-up run has no creation consent of its own.
func TestDocumentIterationProductionDraftDispatch(t *testing.T) {
	f, _, _, intent := iterationFixture(t)
	scope, err := f.pages.documentDraftScope(t.Context(), &f.a, intent.PageID)
	if err != nil || scope.PageID != intent.PageID || scope.Rev != intent.Rev || scope.Generation != intent.Generation {
		t.Fatalf("production draft tools rejected the original targeted follow-up: scope=%+v err=%v", scope, err)
	}
	key := documents.Key{WorkspaceID: f.a.WorkspaceID, PageID: intent.PageID}
	actor, err := f.pages.authorizeDocumentDraft(t.Context(), &f.a, key, "state")
	if err != nil || actor != "agent:"+f.a.AgentID+":"+f.a.RunID {
		t.Fatalf("production authority lost the original human-bound run: actor=%q err=%v", actor, err)
	}
	mustExec(t, f.rig.db, `UPDATE document_iteration_intents SET revoked_at='2026-10-20T00:00:00Z' WHERE run_id=?`, f.a.RunID)
	if _, err := f.pages.documentDraftScope(t.Context(), &f.a, intent.PageID); err == nil {
		t.Fatal("revoked targeted run fell back to creation authority")
	}
	if _, err := f.pages.authorizeDocumentDraft(t.Context(), &f.a, key, "state"); err == nil {
		t.Fatal("revoked targeted run retained production draft access")
	}
}
