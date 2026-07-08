package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Avatar upload/serve/clear (#889).

// pngBytes returns a byte slice that http.DetectContentType classifies as
// image/png: the 8-byte PNG signature plus padding.
func pngBytes(pad int) []byte {
	sig := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	return append(sig, bytes.Repeat([]byte{0}, pad)...)
}

func newAvatarHandler(t *testing.T) (*UserProfileHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	root := t.TempDir()
	h := NewUserProfileHandler(db, newTestLogger(), nil)
	h.SetAvatarRoot(root)
	return h, userID, root
}

func avatarUploadReq(t *testing.T, userID, field string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, "avatar.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/users/me/avatar", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	ctx := context.WithValue(req.Context(), ctxUser, &AuthUser{ID: userID})
	return req.WithContext(ctx)
}

func TestUploadAvatar_RejectsNonImage(t *testing.T) {
	h, userID, root := newAvatarHandler(t)
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", []byte("this is plainly text, not an image")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	// Nothing must have been written for a rejected type.
	if _, err := os.Stat(filepath.Join(root, "avatars", userID)); !os.IsNotExist(err) {
		t.Errorf("a rejected upload must not write a file (err=%v)", err)
	}
}

func TestUploadAvatar_RejectsOversize(t *testing.T) {
	h, userID, _ := newAvatarHandler(t)
	rr := httptest.NewRecorder()
	// A valid PNG header but past the 2MB cap → MaxBytesReader trips.
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", pngBytes(maxAvatarBytes+1)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversize; body=%s", rr.Code, rr.Body.String())
	}
}

func TestUploadAvatar_MissingFileField(t *testing.T) {
	h, userID, _ := newAvatarHandler(t)
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "wrongfield", pngBytes(32)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when 'file' field absent", rr.Code)
	}
}

func TestUploadAvatar_NotConfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewUserProfileHandler(db, newTestLogger(), nil) // no SetAvatarRoot
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", pngBytes(32)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when storage unconfigured", rr.Code)
	}
}

func TestUploadAvatar_HappyPath_SetsURLAndWritesFile(t *testing.T) {
	h, userID, root := newAvatarHandler(t)
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", pngBytes(64)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp userProfileResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.AvatarURL == nil || *resp.AvatarURL == "" {
		t.Fatalf("avatar_url not set on the returned profile")
	}
	want := "/api/v1/users/" + userID + "/avatar"
	if got := *resp.AvatarURL; len(got) < len(want) || got[:len(want)] != want {
		t.Errorf("avatar_url = %q, want prefix %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, "avatars", userID)); err != nil {
		t.Errorf("avatar file not written: %v", err)
	}
}

func TestServeAvatar_ReturnsBytesAndContentType(t *testing.T) {
	h, userID, _ := newAvatarHandler(t)
	content := pngBytes(128)
	up := httptest.NewRecorder()
	h.UploadAvatar(up, avatarUploadReq(t, userID, "file", content))
	if up.Code != http.StatusOK {
		t.Fatalf("upload status = %d; body=%s", up.Code, up.Body.String())
	}

	req := httptest.NewRequest("GET", "/api/v1/users/"+userID+"/avatar", nil)
	req.SetPathValue("id", userID)
	// Authed context (the real route sits behind RequireAuth, which 401s an
	// anonymous caller before this handler runs).
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.ServeAvatar(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("serve status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if !bytes.Equal(rr.Body.Bytes(), content) {
		t.Errorf("served bytes differ from uploaded bytes (%d vs %d)", rr.Body.Len(), len(content))
	}
}

func TestServeAvatar_NotFound(t *testing.T) {
	h, _, _ := newAvatarHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/users/nobody/avatar", nil)
	req.SetPathValue("id", "nobody")
	rr := httptest.NewRecorder()
	h.ServeAvatar(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unset avatar", rr.Code)
	}
}

func TestServeAvatar_RejectsPathTraversalID(t *testing.T) {
	h, _, _ := newAvatarHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/users/x/avatar", nil)
	req.SetPathValue("id", "../../etc/passwd")
	rr := httptest.NewRecorder()
	h.ServeAvatar(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (traversal id must not resolve)", rr.Code)
	}
}

func TestDeleteAvatar_ClearsURLAndFile(t *testing.T) {
	h, userID, root := newAvatarHandler(t)
	up := httptest.NewRecorder()
	h.UploadAvatar(up, avatarUploadReq(t, userID, "file", pngBytes(64)))
	if up.Code != http.StatusOK {
		t.Fatalf("upload status = %d", up.Code)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/users/me/avatar", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.DeleteAvatar(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp userProfileResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.AvatarURL != nil {
		t.Errorf("avatar_url = %v, want null after delete", *resp.AvatarURL)
	}
	if _, err := os.Stat(filepath.Join(root, "avatars", userID)); !os.IsNotExist(err) {
		t.Errorf("avatar file must be removed after delete (err=%v)", err)
	}
}
