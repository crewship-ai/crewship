package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newNotificationHandler(t *testing.T) (*NotificationHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewNotificationHandler(db, nil, logger), userID, wsID
}

func TestNotification_List_Unauthenticated(t *testing.T) {
	h, _, _ := newNotificationHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestNotification_List_Authenticated(t *testing.T) {
	h, userID, wsID := newNotificationHandler(t)
	CreateNotification(h.db, nil, wsID, userID, "user", userID, "created", "issue", "iss-1", "Test issue")
	CreateNotification(h.db, nil, wsID, userID, "user", userID, "created", "issue", "iss-2", "Other")

	for _, q := range []string{"", "?read=false", "?read=true"} {
		t.Run(q, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/"+q, nil)
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestNotification_MarkRead(t *testing.T) {
	h, userID, wsID := newNotificationHandler(t)
	CreateNotification(h.db, nil, wsID, userID, "user", userID, "x", "issue", "i", "T")
	var nid string
	h.db.QueryRow(`SELECT id FROM notifications`).Scan(&nid)

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", nid)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.MarkRead(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}

	// Mark read again -> still 200 (already marked)
	rr2 := httptest.NewRecorder()
	h.MarkRead(rr2, req)
	if rr2.Code != http.StatusOK {
		t.Errorf("re-read status = %d", rr2.Code)
	}
}

func TestNotification_MarkRead_NotFound(t *testing.T) {
	h, userID, _ := newNotificationHandler(t)

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", "missing")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.MarkRead(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestNotification_MarkRead_Unauthenticated(t *testing.T) {
	h, _, _ := newNotificationHandler(t)
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", "x")
	rr := httptest.NewRecorder()
	h.MarkRead(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestNotification_MarkAllRead(t *testing.T) {
	h, userID, wsID := newNotificationHandler(t)
	CreateNotification(h.db, nil, wsID, userID, "user", userID, "x", "issue", "1", "A")
	CreateNotification(h.db, nil, wsID, userID, "user", userID, "x", "issue", "2", "B")

	req := httptest.NewRequest("POST", "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.MarkAllRead(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp map[string]int64
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["updated"] != 2 {
		t.Errorf("updated = %d, want 2", resp["updated"])
	}

	// Unauthenticated
	req2 := httptest.NewRequest("POST", "/", nil)
	rr2 := httptest.NewRecorder()
	h.MarkAllRead(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("unauth status = %d", rr2.Code)
	}
}

func TestNotification_Delete(t *testing.T) {
	h, userID, wsID := newNotificationHandler(t)
	CreateNotification(h.db, nil, wsID, userID, "user", userID, "x", "issue", "1", "T")
	var nid string
	h.db.QueryRow(`SELECT id FROM notifications`).Scan(&nid)

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("id", nid)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d", rr.Code)
	}

	// Delete again -> 404
	rr2 := httptest.NewRecorder()
	h.Delete(rr2, req)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("re-delete = %d", rr2.Code)
	}

	// Unauthenticated
	req3 := httptest.NewRequest("DELETE", "/", nil)
	rr3 := httptest.NewRecorder()
	h.Delete(rr3, req3)
	if rr3.Code != http.StatusUnauthorized {
		t.Errorf("status = %d", rr3.Code)
	}
}

func TestNotification_Count(t *testing.T) {
	h, userID, wsID := newNotificationHandler(t)
	CreateNotification(h.db, nil, wsID, userID, "user", userID, "x", "issue", "1", "T")

	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp map[string]int
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["unread"] != 1 {
		t.Errorf("unread = %d, want 1", resp["unread"])
	}

	// Unauthenticated
	req2 := httptest.NewRequest("GET", "/", nil)
	rr2 := httptest.NewRecorder()
	h.Count(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("unauth status = %d", rr2.Code)
	}
}

// ── Saved View ────────────────────────────────────────────────────────

func newSavedViewHandler(t *testing.T) (*SavedViewHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewSavedViewHandler(db, logger), userID, wsID
}

func TestSavedView_CRUD(t *testing.T) {
	h, userID, wsID := newSavedViewHandler(t)

	// Create
	body := bytes.NewBufferString(`{"name":"My View","filters_json":"{}","view_type":"list","shared":false}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
	}
	var sv savedViewResponse
	json.Unmarshal(rr.Body.Bytes(), &sv)

	// List
	req2 := httptest.NewRequest("GET", "/", nil)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("list: %d", rr2.Code)
	}

	// Update
	body3 := bytes.NewBufferString(`{"name":"Updated","filters_json":"{\"x\":1}","view_type":"board","is_default":true,"shared":true,"sort_json":"{}"}`)
	req3 := httptest.NewRequest("PATCH", "/", body3)
	req3.SetPathValue("viewId", sv.ID)
	req3 = req3.WithContext(withWorkspace(withUser(req3.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr3 := httptest.NewRecorder()
	h.Update(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("update: %d body=%s", rr3.Code, rr3.Body.String())
	}

	// Delete
	req4 := httptest.NewRequest("DELETE", "/", nil)
	req4.SetPathValue("viewId", sv.ID)
	req4 = req4.WithContext(withWorkspace(withUser(req4.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr4 := httptest.NewRecorder()
	h.Delete(rr4, req4)
	if rr4.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rr4.Code)
	}
}

func TestSavedView_Validations(t *testing.T) {
	h, userID, wsID := newSavedViewHandler(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"no name", `{"filters_json":"{}"}`, 400},
		{"no filters", `{"name":"x"}`, 400},
		{"bad json", `{`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(tc.body))
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestSavedView_List_Unauthenticated(t *testing.T) {
	h, _, _ := newSavedViewHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestSavedView_Create_Forbidden(t *testing.T) {
	h, userID, wsID := newSavedViewHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestSavedView_Create_Unauthenticated(t *testing.T) {
	h, _, wsID := newSavedViewHandler(t)
	// Has role but no user
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"x","filters_json":"{}"}`))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestSavedView_Update_NotOwner(t *testing.T) {
	h, userID, wsID := newSavedViewHandler(t)
	// Insert a view owned by other user
	otherID := "other-user"
	if _, err := h.db.Exec(`INSERT INTO users(id,email,full_name) VALUES (?, 'o@x', 'O')`, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO saved_views(id,workspace_id,user_id,name,filters_json,view_type,created_at) VALUES ('v1',?,?,'V','{}','list',datetime('now'))`, wsID, otherID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"hijack"}`))
	req.SetPathValue("viewId", "v1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestSavedView_Update_NotFound(t *testing.T) {
	h, userID, wsID := newSavedViewHandler(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("viewId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestSavedView_Update_NoFields(t *testing.T) {
	h, userID, wsID := newSavedViewHandler(t)
	if _, err := h.db.Exec(`INSERT INTO saved_views(id,workspace_id,user_id,name,filters_json,view_type,created_at) VALUES ('v1',?,?,'V','{}','list',datetime('now'))`, wsID, userID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{}`))
	req.SetPathValue("viewId", "v1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSavedView_Delete_NotOwner(t *testing.T) {
	h, userID, wsID := newSavedViewHandler(t)
	otherID := "other2"
	h.db.Exec(`INSERT INTO users(id,email,full_name) VALUES (?, 'o2@x', 'O2')`, otherID)
	h.db.Exec(`INSERT INTO saved_views(id,workspace_id,user_id,name,filters_json,view_type,created_at) VALUES ('v2',?,?,'V','{}','list',datetime('now'))`, wsID, otherID)

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("viewId", "v2")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestSavedView_Delete_NotFound(t *testing.T) {
	h, userID, _ := newSavedViewHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("viewId", "missing")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestSavedView_Delete_Unauthenticated(t *testing.T) {
	h, _, _ := newSavedViewHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d", rr.Code)
	}
}
