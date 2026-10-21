package api

import (
	"testing"
)

func TestDocumentIterationHostRecorderUsesExistingRetainedAuthority(t *testing.T) {
	f, _, _, intent := iterationFixture(t)
	mustExec(t, f.rig.db, `DELETE FROM document_iteration_intents`)
	if err := f.pages.RecordDocumentIteration(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM document_iteration_intents WHERE message_id=? AND run_id IS NULL AND page_id=?`, f.a.MessageID, intent.PageID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("original retained intent absent %d %v", n, err)
	}
	if err := bindIterationFixture(t, f, intent); err != nil {
		t.Fatal(err)
	}
	scope, err := f.pages.documentIterationScope(t.Context(), &f.a, intent.PageID)
	if err != nil || scope.PageID != intent.PageID {
		t.Fatalf("host-issued purpose cannot draft same Page: %+v %v", scope, err)
	}
}
