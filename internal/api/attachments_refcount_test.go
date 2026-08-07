package api

// The refcount and the download path must ask the same question (#1791 review,
// finding 2).
//
// Both readers of an issue attachment resolve the bytes with
// readAttachmentBlob(root, workspace_id, sha256) and never look at storage_key.
// The refcount used to require storage_key to be byte-identical to that
// reconstruction, so any row whose key had drifted was invisible to it: the blob
// was unlinked while the issue still listed the file and every download 404'd.
//
// The drifted shape is not hypothetical. `crewship backup restore
// --as-workspace` forks a bundle into a new workspace; backup.RemapIDs rewrites
// attachments.workspace_id (an FK to workspaces) and leaves storage_key — a
// plain TEXT column — spelling the OLD workspace. See
// internal/backup/remap_attachments_test.go, which pins that behaviour from the
// other side.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// driftRowStorageKey rewrites one row's storage_key to the shape a
// --as-workspace restore leaves behind: the content-addressed layout, but naming
// the workspace the bundle came FROM.
func driftRowStorageKey(t *testing.T, f attachFixture, attachmentID, sha string) string {
	t.Helper()
	stale := attachmentStorageKey("ws-the-bundle-came-from", sha)
	res, err := f.h.db.Exec(`UPDATE attachments SET storage_key = ? WHERE id = ?`, stale, attachmentID)
	if err != nil {
		t.Fatalf("drift storage_key: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("drift storage_key touched %d rows, want 1", n)
	}
	return stale
}

// The sweep must not collect a blob a live row still serves, even when that
// row's storage_key names another workspace.
func TestAttachment_SweepKeepsTheBlobOfARowWhoseKeyDrifted(t *testing.T) {
	f := newAttachmentFixture(t)

	content := []byte("restored from a bundle, forked into a new workspace\n")
	att, code := upload(t, f, "restored.log", content)
	if code != http.StatusCreated {
		t.Fatalf("upload = %d", code)
	}
	driftRowStorageKey(t, f, att.ID, att.SHA256)

	// The row still serves its bytes — this is what makes deleting them a loss
	// rather than a reclaim, and it is the fact the refcount has to agree with.
	rr := httptest.NewRecorder()
	f.h.Download(rr, scopedReq(t, f, "GET", "ENG-1", att.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Fatalf("download before the sweep = %d, want 200 — the premise of this test is that "+
			"the drifted row is still readable", rr.Code)
	}

	if n := sweepAttachmentBlobs(t.Context(), f.h.db, newTestLogger(), f.storage); n != 0 {
		t.Errorf("the sweep reclaimed %d blob(s) belonging to a live row", n)
	}
	blob := blobPathFor(f.storage, f.wsID, att.SHA256)
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("the sweep deleted the bytes of a row that still lists the file (%v) — "+
			"the refcount asked about the stored key, the download asks about "+
			"(workspace, digest), and the two disagreed", err)
	}

	rr = httptest.NewRecorder()
	f.h.Download(rr, scopedReq(t, f, "GET", "ENG-1", att.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Errorf("download after the sweep = %d, want 200 — the file is listed and 404s forever", rr.Code)
	}
}

// Deleting one attachment must not take the bytes another row — whose key has
// drifted — is still serving.
//
// Same disagreement, reached through the refcounted unlink instead of the sweep,
// because that is the path a user hits by hand.
func TestAttachment_DeleteKeepsTheBlobADriftedRowStillServes(t *testing.T) {
	f := newAttachmentFixture(t)

	content := []byte("one file, attached under two names\n")
	kept, code := upload(t, f, "kept.log", content)
	if code != http.StatusCreated {
		t.Fatalf("upload kept = %d", code)
	}
	// The same bytes under a second name are a second row sharing one blob.
	removed, code := upload(t, f, "removed.log", content)
	if code != http.StatusCreated {
		t.Fatalf("upload removed = %d", code)
	}
	if kept.SHA256 != removed.SHA256 {
		t.Fatalf("the two uploads did not share a digest (%s vs %s)", kept.SHA256, removed.SHA256)
	}
	driftRowStorageKey(t, f, kept.ID, kept.SHA256)

	rr := httptest.NewRecorder()
	f.h.Delete(rr, scopedReq(t, f, "DELETE", "ENG-1", removed.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d body=%s", rr.Code, rr.Body.String())
	}

	blob := blobPathFor(f.storage, f.wsID, kept.SHA256)
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("deleting one attachment took the bytes of another that still lists them (%v)", err)
	}
	rr = httptest.NewRecorder()
	f.h.Download(rr, scopedReq(t, f, "GET", "ENG-1", kept.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Errorf("download of the surviving attachment = %d, want 200", rr.Code)
	}
}

// The widened predicate must not re-open what narrowing it fixed: a CHAT row's
// blob is somewhere else entirely, so it never counts as a reference to the
// content-addressed file — with or without a drifted key.
//
// TestAttachment_ChatRowDoesNotPinAnIssueBlob covers the delete path; this one
// covers the sweep and states the invariant as a property of the owner arc
// rather than of one handler.
func TestAttachment_ChatRowsAreStillNotReferences(t *testing.T) {
	f := newAttachmentFixture(t)

	content := []byte("the same bytes, pasted into a chat\n")
	sha := attachmentDigest(content)
	if _, _, err := storeAttachmentBlob(f.storage, f.wsID, content); err != nil {
		t.Fatalf("store blob: %v", err)
	}
	if _, err := f.h.db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat-refcount', 'agent-worker', ?)`, f.wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := f.h.db.Exec(`
		INSERT INTO attachments (id, workspace_id, owner_type, chat_id, filename, content_type,
			size_bytes, sha256, storage_key, created_at)
		VALUES (?, ?, 'chat', 'chat-refcount', 'notes.md', 'text/markdown; charset=utf-8', ?, ?, ?, datetime('now'))`,
		generateCUID(), f.wsID, len(content), sha,
		fmt.Sprintf("%s/worker/attachments/chat-refcount/notes.md", f.crewID)); err != nil {
		t.Fatalf("insert chat attachment: %v", err)
	}

	if n := sweepAttachmentBlobs(t.Context(), f.h.db, newTestLogger(), f.storage); n != 1 {
		t.Errorf("the sweep reclaimed %d blob(s), want 1 — a chat row, whose bytes live at "+
			"<crew>/<agent>/attachments/..., is holding a content-addressed blob alive", n)
	}
	if _, err := os.Stat(blobPathFor(f.storage, f.wsID, sha)); !os.IsNotExist(err) {
		t.Errorf("the unreferenced content-addressed blob survived: %v", err)
	}
}
