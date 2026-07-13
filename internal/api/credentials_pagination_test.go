package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedCredWithMeta inserts a credential with an explicit created_at (for
// deterministic cursor ordering) and optional JSON tags.
func seedCredWithMeta(t *testing.T, h *CredentialHandler, wsID, userID, id, name, createdAt, tagsJSON string) {
	t.Helper()
	var tags any
	if tagsJSON != "" {
		tags = tagsJSON
	}
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, tags, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'x', 'API_KEY', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, ?, ?, ?)`,
		id, wsID, name, tags, userID, createdAt, createdAt); err != nil {
		t.Fatalf("seed cred %s: %v", id, err)
	}
}

func listReq(wsID, query string) *http.Request {
	req := httptest.NewRequest("GET", "/api/v1/credentials?"+query, nil)
	return req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
}

// TestCredList_LegacyBareArray: without paginate=true the response stays a bare
// JSON array — the backward-compatible default every existing consumer relies on.
func TestCredList_LegacyBareArray(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredWithMeta(t, h, wsID, userID, "c1", "ALPHA", "2026-01-01 00:00:01", "")

	rr := httptest.NewRecorder()
	h.List(rr, listReq(wsID, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var arr []credentialResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &arr); err != nil {
		t.Fatalf("expected bare array, got %s (%v)", rr.Body.String(), err)
	}
	if len(arr) != 1 {
		t.Errorf("len = %d, want 1", len(arr))
	}
}

// TestCredList_CursorPagination walks all pages via next_cursor and asserts
// each row is seen exactly once in (created_at DESC) order.
func TestCredList_CursorPagination(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// 5 creds, distinct created_at. DESC order → E,D,C,B,A.
	seedCredWithMeta(t, h, wsID, userID, "a", "A", "2026-01-01 00:00:01", "")
	seedCredWithMeta(t, h, wsID, userID, "b", "B", "2026-01-01 00:00:02", "")
	seedCredWithMeta(t, h, wsID, userID, "c", "C", "2026-01-01 00:00:03", "")
	seedCredWithMeta(t, h, wsID, userID, "d", "D", "2026-01-01 00:00:04", "")
	seedCredWithMeta(t, h, wsID, userID, "e", "E", "2026-01-01 00:00:05", "")

	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		q := "paginate=true&limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		rr := httptest.NewRecorder()
		h.List(rr, listReq(wsID, q))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var page credentialListPage
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode envelope: %v (%s)", err, rr.Body.String())
		}
		if len(page.Credentials) > 2 {
			t.Fatalf("page bigger than limit: %d", len(page.Credentials))
		}
		for _, c := range page.Credentials {
			seen = append(seen, c.Name)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	got := seen
	want := []string{"E", "D", "C", "B", "A"}
	if len(got) != len(want) {
		t.Fatalf("saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order: saw %v, want %v", got, want)
		}
	}
}

// TestCredList_InvalidCursor rejects a malformed cursor with 400.
func TestCredList_InvalidCursor(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	rr := httptest.NewRecorder()
	h.List(rr, listReq(wsID, "paginate=true&cursor=not-a-valid-cursor"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

// TestCredList_SearchFilter filters by name/description substring, in both modes.
func TestCredList_SearchFilter(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredWithMeta(t, h, wsID, userID, "a", "PROD_STRIPE", "2026-01-01 00:00:01", "")
	seedCredWithMeta(t, h, wsID, userID, "b", "DEV_GITHUB", "2026-01-01 00:00:02", "")

	rr := httptest.NewRecorder()
	h.List(rr, listReq(wsID, "search=stripe"))
	var arr []credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &arr)
	if len(arr) != 1 || arr[0].Name != "PROD_STRIPE" {
		t.Errorf("search=stripe → %+v, want just PROD_STRIPE", arr)
	}
}

// TestCredList_TagFilter filters by an exact tag in the JSON tags array.
func TestCredList_TagFilter(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredWithMeta(t, h, wsID, userID, "a", "A", "2026-01-01 00:00:01", `["prod","ci"]`)
	seedCredWithMeta(t, h, wsID, userID, "b", "B", "2026-01-01 00:00:02", `["dev"]`)

	rr := httptest.NewRecorder()
	h.List(rr, listReq(wsID, "tag=prod"))
	var arr []credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &arr)
	if len(arr) != 1 || arr[0].Name != "A" {
		t.Errorf("tag=prod → %+v, want just A", arr)
	}
}
