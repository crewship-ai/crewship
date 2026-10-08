package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Workspace logo upload / serve / clear (#3005). Same rules as the profile
// picture: PNG, JPEG or WebP, at most 2MB and 4096px a side, stored under the
// router's storage path and served by an authed route.

func newLogoHandler(t *testing.T) (*WorkspaceHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	root := t.TempDir()
	h := NewWorkspaceHandler(db, newTestLogger())
	h.SetStorageRoot(root)
	return h, userID, wsID, root
}

func logoUploadReq(t *testing.T, userID, wsID, role string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/logo", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("workspaceId", wsID)
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, role)
	return req.WithContext(ctx)
}

func logoURLOf(t *testing.T, h *WorkspaceHandler, wsID string) *string {
	t.Helper()
	var u *string
	if err := h.db.QueryRow(`SELECT logo_url FROM workspaces WHERE id = ?`, wsID).Scan(&u); err != nil {
		t.Fatalf("read logo_url: %v", err)
	}
	return u
}

func TestUploadWorkspaceLogo_SetsLogoURLAndServes(t *testing.T) {
	h, userID, wsID, root := newLogoHandler(t)
	img := realPNG(t, 16, 16)
	rr := httptest.NewRecorder()
	h.UploadLogo(rr, logoUploadReq(t, userID, wsID, "ADMIN", img))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		LogoURL *string `json:"logo_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.LogoURL == nil {
		t.Fatalf("response logo_url missing: %v body=%s", err, rr.Body.String())
	}
	if !strings.HasPrefix(*got.LogoURL, "/api/v1/workspaces/"+wsID+"/logo?v=") {
		t.Errorf("logo_url = %q, want the serve route with a cache buster", *got.LogoURL)
	}
	if u := logoURLOf(t, h, wsID); u == nil || *u != *got.LogoURL {
		t.Errorf("stored logo_url = %v, want %q", u, *got.LogoURL)
	}
	if _, err := os.Stat(filepath.Join(root, "workspace-logos", wsID)); err != nil {
		t.Fatalf("logo file not written: %v", err)
	}

	serve := httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/logo", nil)
	serve.SetPathValue("workspaceId", wsID)
	srr := httptest.NewRecorder()
	h.ServeLogo(srr, serve)
	if srr.Code != http.StatusOK || srr.Header().Get("Content-Type") != "image/png" || !bytes.Equal(srr.Body.Bytes(), img) {
		t.Fatalf("serve = %d %q (%d bytes), want the PNG back", srr.Code, srr.Header().Get("Content-Type"), srr.Body.Len())
	}
	if srr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("serve must set nosniff on user-uploaded bytes")
	}
}

func TestUploadWorkspaceLogo_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		content func(t *testing.T) []byte
		want    int
	}{
		{"not an image", "OWNER", func(*testing.T) []byte { return []byte("plain text") }, http.StatusBadRequest},
		{"PNG magic that does not decode", "OWNER", func(*testing.T) []byte { return pngBytes(64) }, http.StatusBadRequest},
		{"over 2MB", "OWNER", func(*testing.T) []byte { return pngBytes(maxAvatarBytes + 1) }, http.StatusBadRequest},
		{"too many pixels", "OWNER", func(t *testing.T) []byte { return realPNG(t, maxAvatarDimension+1, 1) }, http.StatusBadRequest},
		{"a member cannot change it", "MEMBER", func(t *testing.T) []byte { return realPNG(t, 8, 8) }, http.StatusForbidden},
		{"a manager cannot change it", "MANAGER", func(t *testing.T) []byte { return realPNG(t, 8, 8) }, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID, root := newLogoHandler(t)
			rr := httptest.NewRecorder()
			h.UploadLogo(rr, logoUploadReq(t, userID, wsID, tc.role, tc.content(t)))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.want, rr.Body.String())
			}
			if u := logoURLOf(t, h, wsID); u != nil {
				t.Errorf("a refused upload set logo_url = %q", *u)
			}
			if _, err := os.Stat(filepath.Join(root, "workspace-logos", wsID)); !os.IsNotExist(err) {
				t.Errorf("a refused upload wrote a file (err=%v)", err)
			}
		})
	}
}

func TestDeleteWorkspaceLogo_Clears(t *testing.T) {
	h, userID, wsID, root := newLogoHandler(t)
	rr := httptest.NewRecorder()
	h.UploadLogo(rr, logoUploadReq(t, userID, wsID, "OWNER", realPNG(t, 8, 8)))
	if rr.Code != http.StatusOK {
		t.Fatalf("upload = %d", rr.Code)
	}

	del := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/logo", nil)
	del.SetPathValue("workspaceId", wsID)
	del = del.WithContext(withWorkspace(withUser(del.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	drr := httptest.NewRecorder()
	h.DeleteLogo(drr, del)
	if drr.Code != http.StatusOK {
		t.Fatalf("delete = %d; body=%s", drr.Code, drr.Body.String())
	}
	if u := logoURLOf(t, h, wsID); u != nil {
		t.Errorf("logo_url = %q after delete, want NULL", *u)
	}
	if _, err := os.Stat(filepath.Join(root, "workspace-logos", wsID)); !os.IsNotExist(err) {
		t.Errorf("logo file left behind (err=%v)", err)
	}

	// A member may not remove it.
	mdel := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/logo", nil)
	mdel.SetPathValue("workspaceId", wsID)
	mdel = mdel.WithContext(withWorkspace(withUser(mdel.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	mrr := httptest.NewRecorder()
	h.DeleteLogo(mrr, mdel)
	if mrr.Code != http.StatusForbidden {
		t.Fatalf("member delete = %d, want 403", mrr.Code)
	}
}

func TestUploadWorkspaceLogo_NotConfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkspaceHandler(db, newTestLogger()) // no SetStorageRoot
	rr := httptest.NewRecorder()
	h.UploadLogo(rr, logoUploadReq(t, userID, wsID, "OWNER", realPNG(t, 8, 8)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 without storage", rr.Code)
	}
}

func TestServeWorkspaceLogo_RejectsTraversal(t *testing.T) {
	h, _, _, _ := newLogoHandler(t)
	for _, id := range []string{"../etc", "a/b", `a\b`, ""} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("workspaceId", id)
		rr := httptest.NewRecorder()
		h.ServeLogo(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("id %q: status = %d, want 404", id, rr.Code)
		}
	}
}
