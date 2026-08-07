package api

// The attachment blob collector (#1791 review, finding 1).
//
// The sweep itself has had tests since it was written. What had NO test — and
// what these pin — is that anything ever RUNS it. After the issue-delete path
// was narrowed to "unlink exactly the digests this delete orphaned", the only
// remaining call was in that handler's error arm, so on a healthy instance the
// sweep never executed and every blob orphaned by an FK cascade stayed on disk
// forever.
//
// Both tests below are about a cascade the application never sees: a crew wipe
// and a workspace wipe. Neither runs a line of Go, so neither can be fixed by
// anything on a request path.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitForBlobGone polls until a path disappears, or fails with why it matters.
//
// Polling rather than a single assertion because the collector is a goroutine on
// a ticker: the observable is "it happened", and the deadline is what turns "not
// yet" into a failure instead of a flake in either direction.
func waitForBlobGone(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s is still on disk after 5s (%s) — nothing collects blobs whose rows a "+
		"cascade removed, so they are permanent: unreachable, unaccounted for, and not "+
		"removable through any API", what, path)
}

// blobPathFor is the content-addressed path a digest lands at.
func blobPathFor(storage, wsID, sha string) string {
	return filepath.Join(storage, "attachments", wsID, sha[:2], sha)
}

// seedForeignWorkspaceAttachment gives a SECOND workspace a live issue
// attachment — row and blob — so the collector has something it must not touch
// while it is collecting the first workspace's orphans.
//
// Written directly rather than through the upload handler: the handler serves
// one crew/issue pair, and what this needs is a second tenant's tree existing at
// all. The row is shaped exactly as attachBytes writes it, content-addressed key
// included.
func seedForeignWorkspaceAttachment(t *testing.T, f attachFixture, content []byte) (wsID, sha string) {
	t.Helper()
	wsID = "ws-attachment-gc-other"
	sha = attachmentDigest(content)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.h.db.Exec(q, args...); err != nil {
			t.Fatalf("seed foreign workspace: %v\nquery: %s", err, q)
		}
	}
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-gc')`, wsID)
	exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-gc-other', ?, 'Other', 'other-gc', 'OTH')`, wsID)
	exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
	      VALUES ('agent-gc-other', ?, 'crew-gc-other', 'Lead', 'lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`, wsID)
	seedIssue(t, f.h.db, wsID, "crew-gc-other", "agent-gc-other", "OTH-1", "BACKLOG")

	if _, _, err := storeAttachmentBlob(f.storage, wsID, content); err != nil {
		t.Fatalf("store foreign blob: %v", err)
	}
	exec(`INSERT INTO attachments
	        (id, workspace_id, owner_type, mission_id, filename, content_type, size_bytes,
	         sha256, storage_key, created_at)
	      SELECT ?, ?, 'issue', id, 'live.log', 'text/plain; charset=utf-8', ?, ?, ?, datetime('now')
	      FROM missions WHERE identifier = 'OTH-1' AND workspace_id = ?`,
		generateCUID(), wsID, len(content), sha, attachmentStorageKey(wsID, sha), wsID)
	return wsID, sha
}

// A crew wipe cascades the attachment rows away inside SQLite. The collector is
// what gets the bytes back.
//
// This is the concrete leak: a crew's issues carry files, an operator deletes
// the crew, and CrewHandler.Delete (crews_query.go) hard-deletes the crew's
// missions in one statement — `DELETE FROM missions WHERE crew_id = ?`, copied
// verbatim below. `attachments.mission_id ON DELETE CASCADE` then takes every
// attachment row inside SQLite, with no attachment code on the stack and nothing
// having read what it was about to orphan. Before the collector existed those
// bytes stayed for the life of the instance.
func TestAttachmentGC_CollectsBlobsOrphanedByACrewWipe(t *testing.T) {
	f := newAttachmentFixture(t)

	one, code := upload(t, f, "one.log", []byte("attached before the crew was wiped\n"))
	if code != http.StatusCreated {
		t.Fatalf("upload one = %d", code)
	}
	two, code := upload(t, f, "two.log", []byte("also attached before the wipe\n"))
	if code != http.StatusCreated {
		t.Fatalf("upload two = %d", code)
	}
	otherWS, otherSHA := seedForeignWorkspaceAttachment(t, f, []byte("another tenant's live file\n"))

	// The wipe, exactly as crews_query.go issues it. No attachment code is on
	// this path — that is the point.
	if _, err := f.h.db.Exec(`DELETE FROM missions WHERE crew_id = ?`, f.crewID); err != nil {
		t.Fatalf("wipe crew's missions: %v", err)
	}
	var rows int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE workspace_id = ?`, f.wsID).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("the cascade left %d attachment row(s) — the test is not exercising a cascade", rows)
	}
	oneBlob := blobPathFor(f.storage, f.wsID, one.SHA256)
	if _, err := os.Stat(oneBlob); err != nil {
		t.Fatalf("the cascade removed the blob as well as the row (%v) — nothing to collect", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartAttachmentBlobGC(ctx, f.h.db, newTestLogger(), f.storage, 10*time.Millisecond)

	waitForBlobGone(t, oneBlob, "the first cascade-orphaned blob")
	waitForBlobGone(t, blobPathFor(f.storage, f.wsID, two.SHA256), "the second cascade-orphaned blob")

	// And the other tenant's live file is untouched: the collector visits every
	// workspace directory, so "it swept them all" must not mean "it deleted them
	// all".
	if _, err := os.Stat(blobPathFor(f.storage, otherWS, otherSHA)); err != nil {
		t.Errorf("the collector removed a blob another workspace's live row still names: %v", err)
	}
}

// A workspace wipe leaves a directory full of blobs and NO rows anywhere.
//
// This is why the collector enumerates the directories under
// <root>/attachments/ instead of the workspaces table: the deleted tenant is
// absent from every table, so a table-driven pass would visit exactly the
// workspaces that have nothing to reclaim and skip the one holding all of it.
func TestAttachmentGC_CollectsAWipedWorkspacesWholeTree(t *testing.T) {
	f := newAttachmentFixture(t)

	att, code := upload(t, f, "gone-with-the-tenant.log", []byte("500MB of logs, in spirit\n"))
	if code != http.StatusCreated {
		t.Fatalf("upload = %d", code)
	}
	blob := blobPathFor(f.storage, f.wsID, att.SHA256)

	// The tenant goes: its missions first (missions.workspace_id is NO ACTION,
	// so the application has to clear them before the workspace row can go), then
	// the workspace itself, which cascades crews, agents and everything under
	// them. What is left is a directory of blobs and not one row anywhere.
	if _, err := f.h.db.Exec(`DELETE FROM missions WHERE workspace_id = ?`, f.wsID); err != nil {
		t.Fatalf("wipe workspace missions: %v", err)
	}
	if _, err := f.h.db.Exec(`DELETE FROM workspaces WHERE id = ?`, f.wsID); err != nil {
		t.Fatalf("wipe workspace: %v", err)
	}
	var wsRows int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id = ?`, f.wsID).Scan(&wsRows); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if wsRows != 0 {
		t.Fatalf("the workspace row survived; this test needs it gone")
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("no blob to collect: %v", err)
	}

	if n := sweepAttachmentBlobs(context.Background(), f.h.db, newTestLogger(), f.storage); n != 1 {
		t.Fatalf("the pass reclaimed %d blob(s), want 1 — the wiped tenant's directory was not "+
			"visited, which is what happens when the enumeration comes from the workspaces table", n)
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Errorf("the wiped workspace's blob survived: %v", err)
	}
}

// The collector is a no-op without a storage root, and does not walk the
// process's working directory when handed "".
func TestAttachmentGC_NoStorageRootIsANoOp(t *testing.T) {
	f := newAttachmentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartAttachmentBlobGC(ctx, f.h.db, newTestLogger(), "", time.Millisecond)
	if n := sweepAttachmentBlobs(ctx, f.h.db, newTestLogger(), ""); n != 0 {
		t.Errorf("a sweep with no storage root removed %d file(s)", n)
	}
}
