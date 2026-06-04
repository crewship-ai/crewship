package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// SkillHandler.Delete removes a CUSTOM skill (OWNER/ADMIN only),
// refuses BUNDLED skills, and 404s unknown ids.

func TestSkillDelete_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-del", "del-skill", "CODING")

	h := NewSkillHandler(db, newTestLogger())
	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/sk-del", nil)
	req.SetPathValue("skillId", "sk-del")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER") // below manage tier
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSkillDelete_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewSkillHandler(db, newTestLogger())
	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/ghost", nil)
	req.SetPathValue("skillId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSkillDelete_BundledProtected(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Seed a BUNDLED skill directly — the binary re-seeds these and the
	// handler must refuse to delete them.
	if _, err := db.Exec(`INSERT INTO skills (id, name, slug, display_name, version, category, source, verification, downloads, rating_count, pricing_tier, featured, tags, content)
		VALUES ('sk-bundled', 'bundled', 'bundled', 'Bundled', '1.0.0', 'CODING', 'BUNDLED', 'VERIFIED', 0, 0, 'FREE', 0, '[]', '# b')`); err != nil {
		t.Fatalf("seed bundled skill: %v", err)
	}

	h := NewSkillHandler(db, newTestLogger())
	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/sk-bundled", nil)
	req.SetPathValue("skillId", "sk-bundled")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 (bundled protected); body=%s", rr.Code, rr.Body.String())
	}
}

func TestSkillDelete_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-gone", "gone", "CODING")

	h := NewSkillHandler(db, newTestLogger())
	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/sk-gone", nil)
	req.SetPathValue("skillId", "sk-gone")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM skills WHERE id = 'sk-gone'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("skill row still present after delete")
	}
}

// SkillBulkImportHandler.Import validation paths (the git-clone happy
// path needs a real git binary + network and is covered by acceptance
// tests, not here).

func TestSkillBulkImport_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewSkillBulkImportHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"git_url":"https://example.com/x.git"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/bulk-import", body)
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSkillBulkImport_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewSkillBulkImportHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{not json`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/bulk-import", body)
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSkillBulkImport_MissingGitURL(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewSkillBulkImportHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"git_url":"  "}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/bulk-import", body)
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestIsClientFacingImportError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("bulk import requires a git_url"), true},
		{errors.New("private/internal IP not allowed"), true},
		{errors.New("exit status 128: fatal: could not read /tmp/x"), false},
	}
	for _, c := range cases {
		if got := isClientFacingImportError(c.err); got != c.want {
			t.Errorf("isClientFacingImportError(%v)=%v want %v", c.err, got, c.want)
		}
	}
}
