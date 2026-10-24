package api

import (
	"encoding/json"
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

// Canonical SQL storage classification reaches authorized list/detail without
// disclosing checkpoint bytes or relabeling the document as the pilot engine.
func TestCollaborationPageMetadataUsesCanonicalStorage(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedPage(t, "metadata-collab", "metadata-collab", "Native", r.userID, "")
	mustExec(t, r.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('metadata-collab',?,'wordcraft','0','e30=')`, r.wsID)
	h := NewPageHandler(r.db, nil, newTestLogger())
	for _, index := range []bool{false, true} {
		req := pagesRequest(t, "GET", "/api/v1/pages", r.wsID, r.userID, "MEMBER", "")
		req.SetPathValue("slug", "metadata-collab")
		response := httptest.NewRecorder()
		if index {
			h.List(response, req)
		} else {
			h.Get(response, req)
		}
		if response.Code != http.StatusOK {
			t.Fatalf("index=%v: HTTP%d %s", index, response.Code, response.Body.String())
		}
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var find func(any) map[string]any
		find = func(v any) map[string]any {
			switch x := v.(type) {
			case map[string]any:
				if x["id"] == "metadata-collab" {
					return x
				}
				for _, child := range x {
					if found := find(child); found != nil {
						return found
					}
				}
			case []any:
				for _, child := range x {
					if found := find(child); found != nil {
						return found
					}
				}
			}
			return nil
		}
		page := find(body)
		if page == nil || page["has_collaboration"] != true || page["has_mooring"] == true || page["has_document"] == true {
			t.Fatalf("index=%v wrong classification: %v", index, page)
		}
		for _, key := range []string{"checkpoint_base64", "checkpoint", "state", "ticket", "origin"} {
			if _, ok := page[key]; ok {
				t.Fatalf("content/authority leaked in metadata: %s", key)
			}
		}
	}
}
