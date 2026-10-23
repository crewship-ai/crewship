package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollaborationCustodyAndGenericDeletionRefusal(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedCrew(t, "collab-custody", "Custody", "collab-custody")
	r.seedCrewMember(t, "collab-custody", r.userID)
	r.seedPage(t, "collab-page", "collab-page", "Private document", r.userID, "")
	mustExec(t, r.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES(?,?,'wordcraft','0','e30=')`, "collab-page", r.wsID)
	h := NewPageHandler(r.db, nil, newTestLogger())
	req := pagesRequest(t, "DELETE", "/", r.wsID, r.userID, "MEMBER", "")
	req.SetPathValue("slug", "collab-page")
	rec, err := h.loadPage(req.Context(), r.wsID, "collab-page")
	if err != nil || !rec.HasCollaboration {
		t.Fatal("collaboration was not classified", rec, err)
	}
	if _, hasSlug := pageWorkspaceInvalidation(rec, false)["slug"]; hasSlug {
		t.Fatal("workspace broadcast exposed collaboration title/slug")
	}
	response := httptest.NewRecorder()
	h.Delete(response, req)
	if response.Code != http.StatusForbidden && response.Code != http.StatusConflict {
		t.Fatalf("generic delete: %d %s", response.Code, response.Body.String())
	}
	var count int
	if err = r.db.QueryRow(`SELECT count(*) FROM mooring_collab_documents WHERE page_id='collab-page'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("generic deletion lost content", count, err)
	}
	tx, err := r.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = transferDepartingUserPagesTx(t.Context(), tx, r.wsID, r.userID)
	var conflict *ErrPagesNeedManualTransfer
	if !errors.As(err, &conflict) {
		t.Fatal("collaboration auto-transferred to a crew", err)
	}
}
