package server

// Path-traversal pins for the LIVE crew file handlers —
// /crews/{id}/files, /files/download, /files/save, /files/delete
// (handleFileList / handleFileDownload / handleFileSave /
// handleFileDelete in routes_files.go).
//
// These handlers were audited alongside internal/api's crew-shared file
// surface after PR #1569 found the same two classes in
// internal/provider/localfs and internal/fileserver. Result: CLEAN. The
// crew id already goes through safeCrewID before any join, and every
// client-supplied path goes through resolveCrewFileKey.
//
// A clean audit is only worth as much as the test that keeps it clean.
// This file asserts two different things, and it is worth being precise
// about which one does the work, because the original version of this
// header got it backwards (corrected under #1595):
//
//   - The ESCAPE assertions — nothing outside the storage base may be
//     read, listed, written or removed — are a BACKSTOP. Under the
//     strongest available mutation of the gate they exist to pin
//     (safeCrewID → `return true`) not one of them fires, because
//     localfs refuses the resulting key underneath. They would only ever
//     go red if the storage layer lost containment as well. That is
//     worth keeping, and it is not what keeps safeCrewID honest.
//   - The STATUS assertions are what keep safeCrewID honest, and only
//     since #1595: they now require 400 from the gate rather than merely
//     "not 200". With the gate deleted every unsafe id answers 404 from
//     storage, which the old assertion accepted — so this file passed in
//     full with safeCrewID removed. See assertGateRefused.
//
// The escape target is a sibling of the storage base, so "outside" is
// unambiguous.
//
// Note on layering: localfs would also refuse most of these keys, so
// several assertions hold twice over. The handler gate is the one that
// turns them into a 400 instead of a silent empty result, and it is the
// one a refactor of the storage backend cannot take away — which is
// precisely why the pin has to name the 400.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
)

// newEscapeFileServer is newFileServer with the storage base nested one
// level down, so the test has a sibling directory that is unambiguously
// outside the storage tree. Returns the server, the storage base, and the
// outside directory (seeded with a secret file).
func newEscapeFileServer(t *testing.T) (s *Server, storageBase, outside string) {
	t.Helper()
	base := t.TempDir()
	storageBase = filepath.Join(base, "storage")
	outside = filepath.Join(base, "outside", "shared")
	for _, d := range []string{storageBase, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-routes-files-escape-32"
	cfg.Storage.BasePath = storageBase
	stor, err := localfs.New(storageBase)
	if err != nil {
		t.Fatalf("localfs: %v", err)
	}
	s = New(cfg, logging.New("error", "json", nil), &Deps{Storage: stor, DB: openTestDB(t)})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()
	return s, storageBase, outside
}

// unsafeCrewIDs are the shapes r.PathValue("id") can carry that must never
// reach a filepath.Join: an encoded-slash URL decodes to a value with a
// separator, and "../.." would otherwise make the crew directory the
// containment check's own base (the trap PR #1569 named).
var unsafeCrewIDs = []string{
	"..",
	".",
	"",
	"../../outside",
	"a/b",
	`a\b`,
	"crews/other",
}

// assertGateRefused pins that the REFUSAL CAME FROM safeCrewID, not from
// the storage layer behind it.
//
// This distinction is the whole sensitivity of these tests, and the
// original version missed it (#1595). Mutating safeCrewID to `return
// true` leaves every unsafe id answering 404 "file not found" — localfs
// refuses the key underneath — while the pins only rejected 200. So a
// suite that existed to keep safeCrewID honest passed with safeCrewID
// deleted, and the file header's claim that it pins "by EFFECT rather
// than by status code alone" was backwards: the ESCAPE assertions never
// fired at all, and the status assertion was too weak to fire either.
//
// 400 "invalid path" is the gate's own answer; 404 is the storage
// layer's. Asserting the gate's status is what makes removing the gate
// visible, and it is exactly the property the header already claimed:
// "the handler gate is the one that turns them into a 400 instead of a
// silent empty result".
func assertGateRefused(t *testing.T, rec *httptest.ResponseRecorder, id string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d for crew id %q; want 400 from the safeCrewID gate.\n"+
			"A 404 here means the storage layer refused the key and the gate did not run — "+
			"the pin would survive deleting safeCrewID entirely (#1595).", rec.Code, id)
	}
}

// Download must never stream bytes from outside the storage base, whatever
// the crew id. Asserted on the response body, not just the status.
func TestSecCrewFiles_DownloadUnsafeCrewIDCannotLeakOutsideStorage(t *testing.T) {
	for _, id := range unsafeCrewIDs {
		t.Run("id="+id, func(t *testing.T) {
			s, _, _ := newEscapeFileServer(t)
			req := httptest.NewRequest("GET", "/crews/x/files/download?path=shared/secret.txt", nil)
			req.SetPathValue("id", id)
			rec := httptest.NewRecorder()
			s.handleFileDownload(rec, req)

			if strings.Contains(rec.Body.String(), "TOP-SECRET") {
				t.Fatalf("ESCAPE: crew id %q served a file outside the storage base (status %d)", id, rec.Code)
			}
			assertGateRefused(t, rec, id)
		})
	}
}

// Save must never create a file outside the storage base.
func TestSecCrewFiles_SaveUnsafeCrewIDCannotWriteOutsideStorage(t *testing.T) {
	for _, id := range unsafeCrewIDs {
		t.Run("id="+id, func(t *testing.T) {
			s, _, outside := newEscapeFileServer(t)
			req := httptest.NewRequest("PUT", "/crews/x/files/save?path=shared/planted.txt",
				strings.NewReader("PWNED"))
			req.SetPathValue("id", id)
			rec := httptest.NewRecorder()
			s.handleFileSave(rec, req)

			if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
				t.Fatalf("ESCAPE: crew id %q wrote outside the storage base (status %d)", id, rec.Code)
			}
			assertGateRefused(t, rec, id)
		})
	}
}

// Delete must never unlink anything outside the storage base — the
// counterpart of localfs Delete("")/Delete(".") running RemoveAll over the
// storage root, refused here one layer earlier by resolveCrewFileKey.
func TestSecCrewFiles_DeleteUnsafeCrewIDCannotRemoveOutsideStorage(t *testing.T) {
	for _, id := range unsafeCrewIDs {
		t.Run("id="+id, func(t *testing.T) {
			s, _, outside := newEscapeFileServer(t)
			req := httptest.NewRequest("DELETE", "/crews/x/files/delete?path=shared/secret.txt", nil)
			req.SetPathValue("id", id)
			rec := httptest.NewRecorder()
			s.handleFileDelete(rec, req)

			if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
				t.Fatalf("ESCAPE: crew id %q removed a file outside the storage base: %v (status %d)", id, err, rec.Code)
			}
			assertGateRefused(t, rec, id)
		})
	}
}

// A path that collapses to the storage base itself must be refused before
// it reaches localfs.Delete — otherwise a RemoveAll would take out every
// crew's output at once.
func TestSecCrewFiles_DeleteCannotTargetStorageRoot(t *testing.T) {
	for _, p := range []string{".", "./", "..", "/", ""} {
		t.Run("path="+p, func(t *testing.T) {
			s, storageBase, _ := newEscapeFileServer(t)
			marker := filepath.Join(storageBase, "crewX", "keep.txt")
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
				t.Fatalf("write marker: %v", err)
			}

			req := httptest.NewRequest("DELETE", "/crews/crewX/files/delete?path="+p, nil)
			req.SetPathValue("id", "crewX")
			rec := httptest.NewRecorder()
			s.handleFileDelete(rec, req)

			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("ESCAPE: path %q wiped the storage base: %v (status %d)", p, err, rec.Code)
			}
			if _, err := os.Stat(storageBase); err != nil {
				t.Fatalf("ESCAPE: path %q removed the storage base itself: %v", p, err)
			}
		})
	}
}

// List must not enumerate a directory outside the storage base, whether the
// escape is attempted through the crew id, agent_slug, or subdir. The three
// parameters are joined at different points in the handler, so each gets its
// own case.
func TestSecCrewFiles_ListCannotEnumerateOutsideStorage(t *testing.T) {
	cases := []struct {
		name, id, query string
	}{
		{"crew id", "../../outside", ""},
		{"crew id separator", "a/b", ""},
		{"agent_slug", "crewX", "?agent_slug=../../../outside/shared"},
		{"agent_slug separator", "crewX", "?agent_slug=a/b"},
		{"subdir", "crewX", "?subdir=../../outside/shared"},
		{"subdir under shared", "crewX", "?subdir=shared/../../../../outside/shared"},
		{"subdir recursive", "crewX", "?subdir=../../outside&recursive=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newEscapeFileServer(t)
			req := httptest.NewRequest("GET", "/crews/x/files"+tc.query, nil)
			req.SetPathValue("id", tc.id)
			rec := httptest.NewRecorder()
			s.handleFileList(rec, req)

			if strings.Contains(rec.Body.String(), "secret.txt") {
				t.Fatalf("ESCAPE: %s listed a directory outside the storage base (status %d, body %s)",
					tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// The happy path must keep working — a pin that only ever refuses would be
// satisfied by a handler that refuses everything.
func TestSecCrewFiles_ValidCrewIDStillRoundTrips(t *testing.T) {
	s, storageBase, _ := newEscapeFileServer(t)
	const crewID = "cmr9bella0046b7ac37ed"

	save := httptest.NewRequest("PUT", "/crews/x/files/save?path=shared/ok.txt", strings.NewReader("fine"))
	save.SetPathValue("id", crewID)
	rec := httptest.NewRecorder()
	s.handleFileSave(rec, save)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(storageBase, "crews", crewID, "shared", "ok.txt")); err != nil || string(b) != "fine" {
		t.Fatalf("saved file = %q, err = %v", b, err)
	}

	dl := httptest.NewRequest("GET", "/crews/x/files/download?path=shared/ok.txt", nil)
	dl.SetPathValue("id", crewID)
	rec = httptest.NewRecorder()
	s.handleFileDownload(rec, dl)
	if rec.Code != http.StatusOK || rec.Body.String() != "fine" {
		t.Fatalf("download: status = %d, body = %q", rec.Code, rec.Body.String())
	}

	del := httptest.NewRequest("DELETE", "/crews/x/files/delete?path=shared/ok.txt", nil)
	del.SetPathValue("id", crewID)
	rec = httptest.NewRecorder()
	s.handleFileDelete(rec, del)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(storageBase, "crews", crewID, "shared", "ok.txt")); !os.IsNotExist(err) {
		t.Fatalf("file survived delete: %v", err)
	}
}
