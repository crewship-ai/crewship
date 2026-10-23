package api

import (
	"context"
	"testing"
)

func TestFreshReviewCollaborationSurvivesStaleLegacyDelete(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedPage(t, "fresh-race-page", "fresh-race-page", "Document", r.userID, "")
	mustExec(t, r.db, `UPDATE pages SET private_audience=1 WHERE id='fresh-race-page'`)
	mustExec(t, r.db, `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,snapshot) VALUES(?,'fresh-race-page','legacy',0,'{}')`, r.wsID)
	h := NewPageHandler(r.db, nil, newTestLogger())
	request := pagesRequest(t, "DELETE", "/", r.wsID, r.userID, "MEMBER", "")
	token := mintTokenFor(t, r.db, r.userID, "fresh-collab-delete-review")
	request.Header.Set("Authorization", "Bearer "+token)
	request = request.WithContext(context.WithValue(request.Context(), ctxAuthKind, AuthKindCLIToken))
	rec, err := h.loadPage(request.Context(), r.wsID, "fresh-race-page")
	if err != nil || !rec.HasMooring || rec.HasCollaboration {
		t.Fatal("initial legacy classification", rec, err)
	}
	// A second writer changes the attached type after the public DELETE's
	// loadPage and before its legacy-content branch begins its transaction.
	mustExec(t, r.db, `DELETE FROM mooring_documents WHERE page_id='fresh-race-page'`)
	mustExec(t, r.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('fresh-race-page',?,'wordcraft','0','e30=')`, r.wsID)
	refusal, err := h.deleteWorkbookPage(request, r.wsID, rec, UserFromContext(request.Context()))
	var retained int
	if queryErr := r.db.QueryRow(`SELECT count(*) FROM mooring_collab_documents WHERE page_id='fresh-race-page'`).Scan(&retained); queryErr != nil {
		t.Fatal(queryErr)
	}
	if retained != 1 {
		t.Fatalf("stale legacy delete cascaded new collaboration content: refusal=%q err=%v retained=%d", refusal, err, retained)
	}
}
