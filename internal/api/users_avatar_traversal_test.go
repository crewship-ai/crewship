package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Path-traversal pins for the avatar handler (#1595).
//
// avatarFilePath joins a caller-influenced id into a filesystem path and
// guards it with ContainsAny("/\\") + "..". That guard works. Nothing
// pinned it: deleting it outright left the whole internal/api suite
// green, so #1581 listed this handler as "clean" on the strength of a
// property no test would have noticed losing.
//
// The three surfaces are read (ServeAvatar), write (UploadAvatar) and
// remove (DeleteAvatar), and each is asserted by EFFECT — bytes served
// from outside the avatars dir, a file created outside it, a file
// deleted outside it — rather than by status code. That distinction is
// not pedantry: a mutation check on #1582's own pins showed several
// tests reddening only on their status assertion while the storage
// layer underneath quietly caught every effect, which makes the status
// assertion a test of the error path, not of containment.
//
// Teeth, verified by deleting the guard's body in avatarFilePath (so it
// returns the joined path for any id) and re-running:
//
//	--- FAIL: TestServeAvatar_TraversalCannotReadOutsideAvatarsDir
//	--- FAIL: TestUploadAvatar_TraversalCannotWriteOutsideAvatarsDir
//	--- FAIL: TestDeleteAvatar_TraversalCannotRemoveOutsideAvatarsDir
//
// All three redden on the effect assertion, not merely on a status.

// traversalIDs are the escape shapes the guard is meant to reject. Each
// one, joined under <root>/avatars/, resolves outside that directory.
//
// The bare "/" and "\" cases matter as much as the "..": an absolute
// path is not a traversal in the "climb out" sense, but filepath.Join
// treats it as a path segment, so an id like "/etc/passwd" lands at
// <root>/avatars/etc/passwd — outside the one-file-per-user layout the
// handler documents, and enough to read a sibling's avatar if the tree
// ever gains subdirectories.
var traversalIDs = []string{
	"../escaped",
	"../../escaped",
	`..\escaped`,
	"foo/../../escaped",
	"/escaped",
	`\escaped`,
	"..",
}

// outsideAvatars walks root and returns every path that is NOT inside
// root/avatars. The three tests below share it so "containment" means
// one thing across read, write and remove rather than three subtly
// different hand-rolled checks.
func outsideAvatars(t *testing.T, root string) []string {
	t.Helper()
	avatars := filepath.Join(root, "avatars")
	var stray []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(path, avatars+string(os.PathSeparator)) {
			return nil
		}
		stray = append(stray, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return stray
}

// TestServeAvatar_TraversalCannotReadOutsideAvatarsDir — the read
// surface. ServeAvatar is the only one of the three that takes its id
// straight off the URL, so it is the reachable one.
func TestServeAvatar_TraversalCannotReadOutsideAvatarsDir(t *testing.T) {
	const sentinel = "SENTINEL-avatar-traversal-must-not-serve-this"

	for _, id := range traversalIDs {
		t.Run(id, func(t *testing.T) {
			// A fixture per shape, not per test. Sharing one root lets
			// the first shape that escapes leave evidence the later
			// shapes are then blamed for — the failure output stops
			// naming which shape actually broke containment, which is
			// the only thing these subtests exist to tell you.
			h, _, root := newAvatarHandler(t)
			if err := os.WriteFile(filepath.Join(root, "escaped"), []byte(sentinel), 0o600); err != nil {
				t.Fatalf("plant sentinel: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(root, "avatars"), 0o755); err != nil {
				t.Fatalf("mkdir avatars: %v", err)
			}

			req := httptest.NewRequest("GET", "/api/v1/users/"+id+"/avatar", nil)
			req.SetPathValue("id", id)
			req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: "reader"}))
			rr := httptest.NewRecorder()

			h.ServeAvatar(rr, req)

			// The assertion that matters: no out-of-tree bytes in the
			// response, whatever the status says.
			if bytes.Contains(rr.Body.Bytes(), []byte(sentinel)) {
				t.Fatalf("ServeAvatar leaked a file from outside the avatars dir for id %q (status %d)",
					id, rr.Code)
			}
			if rr.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for a rejected id %q; body=%s", rr.Code, id, rr.Body.String())
			}
		})
	}
}

// TestUploadAvatar_TraversalCannotWriteOutsideAvatarsDir — the write
// surface. user.ID comes from the session rather than the URL, so this
// is defence in depth: it pins that a hostile id reaching the handler
// (a corrupted row, a future id scheme, a bug upstream) still cannot
// place a file outside the avatars dir.
func TestUploadAvatar_TraversalCannotWriteOutsideAvatarsDir(t *testing.T) {
	png := realPNG(t, 8, 8)

	for _, id := range traversalIDs {
		t.Run(id, func(t *testing.T) {
			h, _, root := newAvatarHandler(t) // per shape — see ServeAvatar's note
			rr := httptest.NewRecorder()
			h.UploadAvatar(rr, avatarUploadReq(t, id, "file", png))

			if stray := outsideAvatars(t, root); len(stray) > 0 {
				t.Fatalf("UploadAvatar wrote outside the avatars dir for id %q: %v (status %d)",
					id, stray, rr.Code)
			}
			if rr.Code == http.StatusOK {
				t.Errorf("status = 200 for a rejected id %q — the upload should not have succeeded", id)
			}
		})
	}
}

// TestDeleteAvatar_TraversalCannotRemoveOutsideAvatarsDir — the remove
// surface, and the one with the worst failure mode: os.Remove on an
// attacker-chosen path destroys data rather than merely disclosing it,
// and DeleteAvatar deliberately swallows a failed remove as a WARN, so
// nothing in the response would report it either way.
func TestDeleteAvatar_TraversalCannotRemoveOutsideAvatarsDir(t *testing.T) {
	for _, id := range traversalIDs {
		t.Run(id, func(t *testing.T) {
			h, _, root := newAvatarHandler(t) // per shape — see ServeAvatar's note
			victim := filepath.Join(root, "escaped")
			if err := os.WriteFile(victim, []byte("must survive"), 0o600); err != nil {
				t.Fatalf("plant victim: %v", err)
			}

			req := httptest.NewRequest("DELETE", "/api/v1/users/me/avatar", nil)
			req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: id}))
			rr := httptest.NewRecorder()

			h.DeleteAvatar(rr, req)

			if _, err := os.Stat(victim); err != nil {
				t.Fatalf("DeleteAvatar removed a file outside the avatars dir for id %q: %v", id, err)
			}
		})
	}
}

// TestAvatarFilePath_RejectsEveryTraversalShape pins the guard directly,
// below the handlers. The three effect tests above are the ones that
// matter — they assert what an attacker gets — but a unit-level pin
// names the guard itself, so a refactor that moves containment
// elsewhere fails here first with an unambiguous message rather than in
// three handler tests at once.
func TestAvatarFilePath_RejectsEveryTraversalShape(t *testing.T) {
	h := NewUserProfileHandler(nil, newTestLogger(), nil)
	h.SetAvatarRoot("/tmp/avatar-root")

	for _, id := range traversalIDs {
		if path, ok := h.avatarFilePath(id); ok {
			t.Errorf("avatarFilePath(%q) = (%q, true), want ok=false", id, path)
		}
	}

	// The guard must still admit an ordinary id, or it would fail closed
	// on every real user and the tests above would pass vacuously.
	if path, ok := h.avatarFilePath("cm3xk4p9q0000abcd1234efgh"); !ok {
		t.Errorf("avatarFilePath rejected a normal CUID; got (%q, false)", path)
	}
}
