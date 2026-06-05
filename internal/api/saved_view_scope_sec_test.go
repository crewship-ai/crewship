package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecSavedView_UpdateCrossWorkspace verifies that an Update request scoped
// to a different workspace than the one owning the view cannot mutate it, even
// when issued by the view's legitimate owner. Without an `AND workspace_id = ?`
// clause the owner-only check would pass and the row would be updated against a
// foreign workspace context. Expect 404 and an unchanged row.
func TestSecSavedView_UpdateCrossWorkspace(t *testing.T) {
	h, userID, wsA := newSavedViewHandler(t)

	// Second workspace the same user also belongs to.
	wsB := "ws-b"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('mb', ?, ?, 'OWNER')`, wsB, userID); err != nil {
		t.Fatal(err)
	}

	// View lives in workspace A.
	if _, err := h.db.Exec(`INSERT INTO saved_views(id,workspace_id,user_id,name,filters_json,view_type,created_at) VALUES ('v1',?,?,'Original','{}','list',datetime('now'))`, wsA, userID); err != nil {
		t.Fatal(err)
	}

	// Owner attempts to update it while scoped to workspace B.
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"Hijacked"}`))
	req.SetPathValue("viewId", "v1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsB, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace update status = %d, want 404", rr.Code)
	}

	var name string
	if err := h.db.QueryRow(`SELECT name FROM saved_views WHERE id = 'v1'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Original" {
		t.Errorf("row name = %q, want unchanged %q", name, "Original")
	}
}

// TestSecSavedView_DeleteCrossWorkspace is the Delete analogue: deleting a view
// while scoped to a foreign workspace must 404 and leave the row intact.
func TestSecSavedView_DeleteCrossWorkspace(t *testing.T) {
	h, userID, wsA := newSavedViewHandler(t)

	wsB := "ws-b"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('mb', ?, ?, 'OWNER')`, wsB, userID); err != nil {
		t.Fatal(err)
	}

	if _, err := h.db.Exec(`INSERT INTO saved_views(id,workspace_id,user_id,name,filters_json,view_type,created_at) VALUES ('v1',?,?,'Original','{}','list',datetime('now'))`, wsA, userID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("viewId", "v1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsB, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace delete status = %d, want 404", rr.Code)
	}

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM saved_views WHERE id = 'v1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (unchanged)", count)
	}
}

// TestSecSavedView_SameWorkspaceStillWorks confirms the positive path: with a
// matching workspace context the owner can still update and delete.
func TestSecSavedView_SameWorkspaceStillWorks(t *testing.T) {
	h, userID, wsA := newSavedViewHandler(t)

	if _, err := h.db.Exec(`INSERT INTO saved_views(id,workspace_id,user_id,name,filters_json,view_type,created_at) VALUES ('v1',?,?,'Original','{}','list',datetime('now'))`, wsA, userID); err != nil {
		t.Fatal(err)
	}

	// Update in the owning workspace.
	ureq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"Renamed"}`))
	ureq.SetPathValue("viewId", "v1")
	ureq = ureq.WithContext(withWorkspace(withUser(ureq.Context(), &AuthUser{ID: userID}), wsA, "OWNER"))
	urr := httptest.NewRecorder()
	h.Update(urr, ureq)
	if urr.Code != http.StatusOK {
		t.Fatalf("same-workspace update status = %d body=%s", urr.Code, urr.Body.String())
	}

	var name string
	if err := h.db.QueryRow(`SELECT name FROM saved_views WHERE id = 'v1'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Renamed" {
		t.Errorf("row name = %q, want %q", name, "Renamed")
	}

	// Delete in the owning workspace.
	dreq := httptest.NewRequest("DELETE", "/", nil)
	dreq.SetPathValue("viewId", "v1")
	dreq = dreq.WithContext(withWorkspace(withUser(dreq.Context(), &AuthUser{ID: userID}), wsA, "OWNER"))
	drr := httptest.NewRecorder()
	h.Delete(drr, dreq)
	if drr.Code != http.StatusNoContent {
		t.Fatalf("same-workspace delete status = %d body=%s", drr.Code, drr.Body.String())
	}
}
