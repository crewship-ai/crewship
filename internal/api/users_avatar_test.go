package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Avatar upload/serve/clear (#889).

// pngBytes returns a slice that http.DetectContentType classifies as image/png
// (the 8-byte signature) but which image.DecodeConfig CANNOT decode — used to
// exercise the "valid magic, invalid image" rejection path.
func pngBytes(pad int) []byte {
	sig := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	return append(sig, bytes.Repeat([]byte{0}, pad)...)
}

// realPNG encodes an actual w×h PNG so it passes the DecodeConfig gate.
func realPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
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
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "wrongfield", realPNG(t, 8, 8)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when 'file' field absent", rr.Code)
	}
}

// A file whose magic bytes say PNG but which won't decode is rejected by the
// DecodeConfig gate (not just the content-type sniff).
func TestUploadAvatar_RejectsUndecodable(t *testing.T) {
	h, userID, root := newAvatarHandler(t)
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", pngBytes(64)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-decodable PNG; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "avatars", userID)); !os.IsNotExist(err) {
		t.Errorf("undecodable upload must not write a file (err=%v)", err)
	}
}

// A real image whose dimensions exceed the cap is rejected (bomb defense).
func TestUploadAvatar_RejectsHugeDimensions(t *testing.T) {
	h, userID, _ := newAvatarHandler(t)
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", realPNG(t, maxAvatarDimension+1, 1)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversize dimensions; body=%s", rr.Code, rr.Body.String())
	}
}

func TestUploadAvatar_NotConfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewUserProfileHandler(db, newTestLogger(), nil) // no SetAvatarRoot
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", realPNG(t, 8, 8)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when storage unconfigured", rr.Code)
	}
}

func TestUploadAvatar_HappyPath_SetsURLAndWritesFile(t *testing.T) {
	h, userID, root := newAvatarHandler(t)
	rr := httptest.NewRecorder()
	h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", realPNG(t, 16, 16)))
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
	content := realPNG(t, 24, 24)
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
	if nosniff := rr.Header().Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
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
	h.UploadAvatar(up, avatarUploadReq(t, userID, "file", realPNG(t, 12, 12)))
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

// Deleting when no avatar was ever uploaded is a no-op success (clears the
// column, tolerates the absent file) — CodeRabbit round-1 catch.
func TestDeleteAvatar_NoFile_OK(t *testing.T) {
	h, userID, _ := newAvatarHandler(t)
	req := httptest.NewRequest("DELETE", "/api/v1/users/me/avatar", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.DeleteAvatar(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete with no prior upload = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp userProfileResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.AvatarURL != nil {
		t.Errorf("avatar_url = %v, want null", *resp.AvatarURL)
	}
}

// Re-uploading overwrites the file in place and mints a DISTINCT cache-buster
// URL even for a rapid replacement (nanosecond precision) — CodeRabbit catch.
func TestUploadAvatar_ReplaceOverwritesAndChangesURL(t *testing.T) {
	h, userID, root := newAvatarHandler(t)

	upload := func(png []byte) string {
		rr := httptest.NewRecorder()
		h.UploadAvatar(rr, avatarUploadReq(t, userID, "file", png))
		if rr.Code != http.StatusOK {
			t.Fatalf("upload = %d; body=%s", rr.Code, rr.Body.String())
		}
		var resp userProfileResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || resp.AvatarURL == nil {
			t.Fatalf("bad profile resp: %v", err)
		}
		return *resp.AvatarURL
	}

	first := upload(realPNG(t, 10, 10))
	second := upload(realPNG(t, 20, 20))
	if first == second {
		t.Errorf("replacement reused the same cache-buster URL: %q", first)
	}
	// One file per user — the replace overwrote in place, not a second file.
	entries, err := os.ReadDir(filepath.Join(root, "avatars"))
	if err != nil {
		t.Fatalf("read avatars dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("want exactly 1 stored avatar after replace, got %d", len(entries))
	}
}
