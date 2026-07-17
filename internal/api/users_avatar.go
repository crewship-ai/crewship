package api

// Avatar upload / serve / clear for the caller's own profile (#889).
//
// Storage: one file per user at <avatarRoot>/avatars/<userID> (no
// extension — the content type is re-sniffed on serve, so a replace just
// overwrites in place). avatarRoot is the router's storagePath; when it is
// empty the endpoints fail closed rather than writing somewhere unintended.
//
// avatar_url is set to the authed serve endpoint with a ?v=<unix> cache
// buster so a replaced avatar actually refreshes in <img> tags. The GET
// serve route is authed-only (any signed-in user may fetch a member's
// avatar by id — rosters render each other), so an unauthenticated request
// gets a 401 from RequireAuth before reaching the handler.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Register the decoders image.DecodeConfig dispatches to. png/jpeg are
	// stdlib; webp comes from golang.org/x/image. Blank imports — we only need
	// the format registrations, not the packages' exported API.
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// maxAvatarBytes caps an avatar upload at 2 MiB — comfortably above any
// reasonable profile picture while keeping the buffered read bounded.
const maxAvatarBytes = 2 << 20

// maxAvatarDimension bounds width/height so a small file can't decode into a
// huge bitmap (decompression-bomb defense). 4096px is generous for an avatar
// that renders at ≤ 40px; anything larger is almost certainly not a real
// profile picture.
const maxAvatarDimension = 4096

// allowedAvatarContentType reports whether a sniffed content type is one of
// the image formats we accept (PNG / JPEG / WebP). http.DetectContentType
// recognises all three.
func allowedAvatarContentType(ct string) bool {
	switch ct {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

// avatarFilePath resolves the on-disk path for a user's avatar, guarding
// against a userID that could escape the avatars dir. Returns ok=false for
// an empty root or a suspicious id.
func (h *UserProfileHandler) avatarFilePath(userID string) (string, bool) {
	if h.avatarRoot == "" || userID == "" {
		return "", false
	}
	if strings.ContainsAny(userID, `/\`) || strings.Contains(userID, "..") {
		return "", false
	}
	return filepath.Join(h.avatarRoot, "avatars", userID), true
}

// UploadAvatar stores the caller's avatar and points avatar_url at the serve
// endpoint. POST /api/v1/users/me/avatar (multipart, field "file").
func (h *UserProfileHandler) UploadAvatar(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	path, ok := h.avatarFilePath(user.ID)
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "avatar storage is not configured")
		return
	}

	// Bound the read before touching the body so an oversized upload is
	// rejected without buffering it all.
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes)
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		replyError(w, http.StatusBadRequest, "invalid multipart form or file too large (max 2MB)")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, _, err := r.FormFile("file")
	if err != nil {
		replyError(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		// MaxBytesReader trips here for a stream that only reveals its size
		// past the cap — surface it as the same 400, not a 500.
		replyError(w, http.StatusBadRequest, "could not read file (max 2MB)")
		return
	}
	if len(data) == 0 {
		replyError(w, http.StatusBadRequest, "file is empty")
		return
	}

	ct := http.DetectContentType(data)
	if !allowedAvatarContentType(ct) {
		replyError(w, http.StatusBadRequest, "unsupported image type: must be PNG, JPEG, or WebP")
		return
	}

	// Decode the header (not just the magic bytes) to prove it's a real,
	// parseable image of an allowed format AND to read its dimensions. This
	// rejects a file with a valid content-type prefix but a corrupt body, and
	// bounds the pixel dimensions before anything downstream tries to render
	// it (decompression-bomb defense). DecodeConfig reads only the header, so
	// it never allocates the full bitmap.
	cfg, format, derr := image.DecodeConfig(bytes.NewReader(data))
	if derr != nil {
		replyError(w, http.StatusBadRequest, "file is not a valid PNG, JPEG, or WebP image")
		return
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		replyError(w, http.StatusBadRequest, "unsupported image type: must be PNG, JPEG, or WebP")
		return
	}
	if cfg.Width > maxAvatarDimension || cfg.Height > maxAvatarDimension || cfg.Width <= 0 || cfg.Height <= 0 {
		replyError(w, http.StatusBadRequest, fmt.Sprintf("image dimensions must be between 1 and %d px per side", maxAvatarDimension))
		return
	}

	// EXIF/metadata: we store the uploaded bytes verbatim, so any EXIF a JPEG
	// carries (incl. GPS) is preserved. That's an accepted trade-off — the
	// avatar is the user's own self-uploaded image and re-encoding to strip
	// metadata would cost a full decode/encode (and webp re-encode isn't in
	// stdlib). Documented in docs/api-reference/auth.mdx; stripping is a
	// possible follow-up if it becomes a concern.

	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		replyInternalError(w, h.logger, "avatar mkdir", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		replyInternalError(w, h.logger, "avatar write", err)
		return
	}

	// ?v busts the <img> cache so a replaced avatar shows immediately. Use
	// nanosecond precision — a fast replace within the same second must still
	// produce a distinct URL or the browser serves the stale image.
	url := fmt.Sprintf("/api/v1/users/%s/avatar?v=%d", user.ID, time.Now().UnixNano())
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET avatar_url = ?, updated_at = ? WHERE id = ?", url, now, user.ID); err != nil {
		replyInternalError(w, h.logger, "avatar update url", err)
		return
	}

	h.writeProfile(w, r, user.ID)
}

// DeleteAvatar clears the caller's avatar back to initials.
// DELETE /api/v1/users/me/avatar
func (h *UserProfileHandler) DeleteAvatar(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Best-effort remove of the stored file (a Google-populated avatar_url
	// has no local file — clearing the column is what matters).
	if path, ok := h.avatarFilePath(user.ID); ok {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			h.logger.Warn("avatar remove file", "error", err, "user", user.ID)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET avatar_url = NULL, updated_at = ? WHERE id = ?", now, user.ID); err != nil {
		replyInternalError(w, h.logger, "avatar clear url", err)
		return
	}

	h.writeProfile(w, r, user.ID)
}

// ServeAvatar streams a user's stored avatar bytes.
// GET /api/v1/users/{id}/avatar — authed (any signed-in user; rosters render
// each other's avatars). The query string (?v=) is ignored.
func (h *UserProfileHandler) ServeAvatar(w http.ResponseWriter, r *http.Request) {
	path, ok := h.avatarFilePath(r.PathValue("id"))
	if !ok {
		replyError(w, http.StatusNotFound, "avatar not found")
		return
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		replyError(w, http.StatusNotFound, "avatar not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "avatar read", err)
		return
	}

	ct := http.DetectContentType(data)
	if !allowedAvatarContentType(ct) {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	// Defense-in-depth for user-uploaded content: never let the browser
	// re-sniff these bytes into an executable type.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
