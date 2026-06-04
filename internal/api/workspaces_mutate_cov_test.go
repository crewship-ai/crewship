package api

// Coverage for workspaces_mutate.go (Create + Update) and the remaining
// branches of workspaces.go. Drives the WorkspaceHandler directly through
// httptest recorders, asserting status codes, DB state, and 500 fault
// injection via db.Close().
//
// NOTE: WorkspaceHandler has no Delete or Settings mutation methods, so
// those are not exercised here. The license/member-limit branches
// (SetLicense + enforcement in workspaces_membership.go) are SKIPPED —
// they require a real *license.License and live outside these two files.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// covWsMHandler builds a WorkspaceHandler with a fresh test DB plus a seeded
// user + workspace, returning the handler, db, userID and workspaceID.
func covWsMHandler(t *testing.T) (*WorkspaceHandler, userWS) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkspaceHandler(db, newTestLogger())
	return h, userWS{userID: userID, wsID: wsID}
}

type userWS struct {
	userID string
	wsID   string
}

// covWsMSlug fetches the current slug for a workspace row.
func covWsMSlug(t *testing.T, h *WorkspaceHandler, wsID string) string {
	t.Helper()
	var slug string
	if err := h.db.QueryRow("SELECT slug FROM workspaces WHERE id = ?", wsID).Scan(&slug); err != nil {
		t.Fatalf("read slug: %v", err)
	}
	return slug
}

// covWsMName fetches the current name for a workspace row.
func covWsMName(t *testing.T, h *WorkspaceHandler, wsID string) string {
	t.Helper()
	var name string
	if err := h.db.QueryRow("SELECT name FROM workspaces WHERE id = ?", wsID).Scan(&name); err != nil {
		t.Fatalf("read name: %v", err)
	}
	return name
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestCovWsMCreate_Unauthenticated(t *testing.T) {
	h, _ := covWsMHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/workspaces", strings.NewReader(`{"name":"X","slug":"x"}`))
	rr := httptest.NewRecorder()
	h.Create(rr, req) // no user in context
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestCovWsMCreate_InvalidJSON(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/workspaces", strings.NewReader(`{not json`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestCovWsMCreate_BadName(t *testing.T) {
	h, uw := covWsMHandler(t)
	for _, body := range []string{
		`{"name":"","slug":"validslug"}`,
		`{"name":"x","slug":"validslug"}`,
		`{"name":"` + strings.Repeat("a", 101) + `","slug":"validslug"}`,
	} {
		req := httptest.NewRequest("POST", "/api/v1/workspaces", strings.NewReader(body))
		req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %q: want 400, got %d", body, rr.Code)
		}
	}
}

func TestCovWsMCreate_BadSlug(t *testing.T) {
	h, uw := covWsMHandler(t)
	for _, body := range []string{
		`{"name":"Valid","slug":""}`,
		`{"name":"Valid","slug":"x"}`,
		`{"name":"Valid","slug":"` + strings.Repeat("s", 51) + `"}`,
	} {
		req := httptest.NewRequest("POST", "/api/v1/workspaces", strings.NewReader(body))
		req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %q: want 400, got %d", body, rr.Code)
		}
	}
}

func TestCovWsMCreate_BadLanguage(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/workspaces",
		strings.NewReader(`{"name":"Valid","slug":"validslug","preferred_language":"Klingon"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestCovWsMCreate_DuplicateSlug(t *testing.T) {
	h, uw := covWsMHandler(t)
	// Seeded workspace already uses slug "test".
	req := httptest.NewRequest("POST", "/api/v1/workspaces",
		strings.NewReader(`{"name":"Another","slug":"test"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rr.Code)
	}
}

func TestCovWsMCreate_Happy(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/workspaces",
		strings.NewReader(`{"name":"Brand New","slug":"brand-new","preferred_language":"cs"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// DB state: workspace row exists with canonical language + OWNER member.
	var (
		name, slug, lang string
		role             string
	)
	if err := h.db.QueryRow(
		"SELECT name, slug, preferred_language FROM workspaces WHERE slug = 'brand-new'").
		Scan(&name, &slug, &lang); err != nil {
		t.Fatalf("read created workspace: %v", err)
	}
	if name != "Brand New" || lang != "Czech" {
		t.Fatalf("unexpected row name=%q lang=%q", name, lang)
	}
	if err := h.db.QueryRow(
		"SELECT wm.role FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id WHERE w.slug = 'brand-new' AND wm.user_id = ?",
		uw.userID).Scan(&role); err != nil {
		t.Fatalf("read member: %v", err)
	}
	if role != "OWNER" {
		t.Fatalf("want OWNER member, got %q", role)
	}
}

func TestCovWsMCreate_DBError(t *testing.T) {
	h, uw := covWsMHandler(t)
	h.db.Close() // fault injection → 500 on slug lookup
	req := httptest.NewRequest("POST", "/api/v1/workspaces",
		strings.NewReader(`{"name":"Valid","slug":"validslug"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestCovWsMUpdate_Forbidden(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"name":"Renamed"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "MEMBER") // not manage-capable
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_InvalidJSON(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{bad`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_BadName(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"name":"x"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_BadSlug(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"slug":"x"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_BadLanguage(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"preferred_language":"Nope"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_DuplicateSlug(t *testing.T) {
	h, uw := covWsMHandler(t)
	// Seed a second workspace that owns "taken".
	if _, err := h.db.Exec(
		"INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'taken')"); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"slug":"taken"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_Happy(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"name":"Renamed Workspace","slug":"renamed","preferred_language":"cs"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := covWsMName(t, h, uw.wsID); got != "Renamed Workspace" {
		t.Fatalf("name not persisted: %q", got)
	}
	if got := covWsMSlug(t, h, uw.wsID); got != "renamed" {
		t.Fatalf("slug not persisted: %q", got)
	}
	var lang string
	if err := h.db.QueryRow("SELECT preferred_language FROM workspaces WHERE id = ?", uw.wsID).Scan(&lang); err != nil {
		t.Fatalf("read lang: %v", err)
	}
	if lang != "Czech" {
		t.Fatalf("lang not canonicalized: %q", lang)
	}
}

func TestCovWsMUpdate_ClearLanguage(t *testing.T) {
	h, uw := covWsMHandler(t)
	// First set a language.
	if _, err := h.db.Exec("UPDATE workspaces SET preferred_language = 'Czech' WHERE id = ?", uw.wsID); err != nil {
		t.Fatalf("preset lang: %v", err)
	}
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"preferred_language":""}`)) // empty → SetNull
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var lang *string
	if err := h.db.QueryRow("SELECT preferred_language FROM workspaces WHERE id = ?", uw.wsID).Scan(&lang); err != nil {
		t.Fatalf("read lang: %v", err)
	}
	if lang != nil {
		t.Fatalf("expected NULL language, got %q", *lang)
	}
}

func TestCovWsMUpdate_EmptyBodyNoChange(t *testing.T) {
	h, uw := covWsMHandler(t)
	// Empty update builder branch: no fields → skip ExecContext, still return the row.
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := covWsMSlug(t, h, uw.wsID); got != "test" {
		t.Fatalf("slug should be unchanged, got %q", got)
	}
}

func TestCovWsMUpdate_SlugCheckDBError(t *testing.T) {
	h, uw := covWsMHandler(t)
	h.db.Close() // fault injection → 500 on slug uniqueness lookup
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"slug":"newslug"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_ExecDBError(t *testing.T) {
	h, uw := covWsMHandler(t)
	h.db.Close() // fault injection → 500 on the UPDATE exec (name-only, no slug check)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"name":"Renamed"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
}

func TestCovWsMUpdate_NotFoundAfterUpdate(t *testing.T) {
	h, uw := covWsMHandler(t)
	// Soft-delete the workspace so the post-update SELECT (deleted_at IS NULL)
	// returns no row → 500 (handler does not special-case ErrNoRows here).
	if _, err := h.db.Exec("UPDATE workspaces SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?", uw.wsID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+uw.wsID,
		strings.NewReader(`{"name":"Renamed"}`))
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Get — remaining branches in workspaces.go (404 + 500)
// ---------------------------------------------------------------------------

func TestCovWsMGet_NotFound(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/workspaces/missing", nil)
	req = withWorkspaceUser(req, uw.userID, "missing", "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

func TestCovWsMGet_DBError(t *testing.T) {
	h, uw := covWsMHandler(t)
	h.db.Close()
	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+uw.wsID, nil)
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
}

func TestCovWsMGet_Happy(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+uw.wsID, nil)
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"currentUserRole":"OWNER"`) {
		t.Fatalf("expected currentUserRole in body: %s", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// List — remaining branches in workspaces.go (unauth + 500 + happy/empty)
// ---------------------------------------------------------------------------

func TestCovWsMList_DBError(t *testing.T) {
	h, uw := covWsMHandler(t)
	h.db.Close()
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
}

func TestCovWsMList_Happy(t *testing.T) {
	h, uw := covWsMHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = withWorkspaceUser(req, uw.userID, uw.wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), uw.wsID) {
		t.Fatalf("expected seeded workspace in list: %s", rr.Body.String())
	}
}
