package api

// The attachment blob collector — the only thing that ever runs the sweep.
//
// ── Why this exists ────────────────────────────────────────────────────────
//
// Deleting an attachment through the API is refcounted, and deleting an ISSUE
// reclaims the digests it read before the cascade (issue_handler_update.go).
// Between them those two cover exactly the paths where the application is
// holding the delete. Every other way an attachments row disappears is a
// SQLite FK CASCADE with no Go on the stack:
//
//	workspaces(id)      ON DELETE CASCADE  — a workspace wipe
//	missions(id)        ON DELETE CASCADE  — a crew wipe (crews_query.go deletes
//	                                         a crew's missions wholesale), and any
//	                                         other bulk mission delete
//	mission_comments(id) ON DELETE CASCADE — a comment cascade, once that arc has
//	                                         a producer
//
// None of those consult the refcount, so their blobs stayed on disk with nothing
// naming them. That is not a rounding error: a crew whose issues carry 500 MB of
// logs takes the whole 500 MB with it into permanent residency, and no operator
// action available today gets it back.
//
// reclaimAttachmentBlobs has been the answer to that for as long as it has
// existed, but after the delete path was correctly narrowed to "unlink exactly
// the digests this delete orphaned" the sweep was left reachable only from the
// error arm of one handler — i.e. effectively never. This file is the caller
// that makes it real.
//
// ── Why a background pass and not a request path ───────────────────────────
//
// A sweep does not belong on a request. It walks a tenant's whole blob tree to
// answer a question no request asked, and the request that pays for it is
// whichever unlucky delete happened to run first. It also must not be tied to a
// delete at all: the cascades above are precisely the deletes that never reach
// Go, so hanging the sweep off one of them would leave the workspace-wipe case —
// the biggest one — uncollected.
//
// So it runs on its own clock, off every request path, from the same place the
// other background collectors start (server_lifecycle.go). Nothing about it is
// urgent: the bytes are unreachable, not exposed — no row points at them and no
// route can name them — so "within the hour" is the correct urgency.
//
// ── What it collects, and what it does not ─────────────────────────────────
//
// Collected:
//
//   - blobs orphaned by ANY cascade above, in any workspace that still has a
//     directory under <root>/attachments/, including workspaces whose rows are
//     entirely gone (the directory is walked, not the workspaces table, so a
//     wiped tenant's tree is still visited and — with no rows left to name
//     anything — fully reclaimed);
//   - blobs left by a crash between storeAttachmentBlob and its INSERT;
//   - .tmp-* files from a write that died, once older than
//     attachmentTempReclaimAge.
//
// NOT collected:
//
//   - chat attachment blobs. They are not content-addressed and live outside
//     <root>/attachments/ entirely (proxy_attachments.go); deleting a chat's
//     bytes is the crew-files surface's job. The sweep never leaves the
//     attachments tree, which is what keeps that true;
//   - anything in the tree that is not named like a sha256 and is not a .tmp-*
//     file. An operator's own file is not this collector's to delete;
//   - empty shard/workspace DIRECTORIES. They cost an inode and removing them
//     races an uploader that has just run MkdirAll and is about to rename into
//     one — which would fail that upload with a 500 to save 4 KB;
//   - anything at all when the instance has no storage root configured.
//
// Still racy across processes, exactly as reclaimAttachmentBlobs documents: the
// (workspace, digest) lock is a process-local mutex, so two crewshipd instances
// sharing one storage root can still have one's sweep step on the other's
// in-flight upload. That is a property of the whole attachment design — the blob
// path assumes a local filesystem — and closing it needs a lease in the storage
// layer, not a different caller. Running this collector does not make it worse:
// a second process's uploads were already exposed to the first's issue-delete
// path.

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// attachmentGCInterval is how often the collector walks the blob tree.
//
// An hour is chosen against what the pass costs and what it is worth: the work
// is one stat per file plus one indexed COUNT per digest-named file, and the
// bytes it reclaims are already unreachable, so a shorter interval buys nothing
// a user can perceive. It matches attachmentTempReclaimAge, which puts the worst
// case for a crashed write's temp file at just under two hours — it has to age
// out of the floor first, and is then collected by the next pass.
const attachmentGCInterval = time.Hour

// StartAttachmentBlobGC runs the orphan sweep over every workspace under the
// storage root — once now, then every interval until ctx is done.
//
// It is a no-op without a storage root: an instance with no storage provider has
// no blob tree to walk, and walking "" would resolve to the process's working
// directory.
//
// The first pass runs immediately rather than after a delay. Boot is when the
// tree is most likely to hold leftovers from a crash, and the pass takes the
// same per-blob lock every upload takes, so running it while requests are
// already being served is safe by the same argument that makes it safe at any
// other moment.
func StartAttachmentBlobGC(ctx context.Context, db *sql.DB, logger *slog.Logger, root string, interval time.Duration) {
	if db == nil || root == "" {
		return
	}
	if interval <= 0 {
		interval = attachmentGCInterval
	}
	go func() {
		sweepAttachmentBlobs(ctx, db, logger, root)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweepAttachmentBlobs(ctx, db, logger, root)
			}
		}
	}()
}

// sweepAttachmentBlobs runs one pass over every workspace directory under the
// root and returns how many blobs it removed.
//
// The DIRECTORIES are the enumeration, not the workspaces table. A wiped tenant
// has no workspace row left and its orphaned blobs are the single largest thing
// this collector exists for, so a table-driven enumeration would skip exactly
// the case that motivated it.
//
// One workspace's failure does not stop the pass: a directory whose name is not
// a usable path component is skipped (a stray file an operator dropped in the
// tree, not a tenant), and a per-workspace error is logged and left behind.
func sweepAttachmentBlobs(ctx context.Context, db *sql.DB, logger *slog.Logger, root string) int {
	if db == nil || root == "" {
		// Not merely defensive: filepath.Join("", "attachments") is a RELATIVE
		// path, so an unconfigured root would have this walking whatever
		// ./attachments happens to be in the process's working directory.
		return 0
	}
	base := filepath.Join(root, "attachments")
	entries, err := os.ReadDir(base)
	if err != nil {
		// No attachments tree is the ordinary state of an instance where nobody
		// has attached anything. Anything else is worth one line.
		if !os.IsNotExist(err) && logger != nil {
			logger.Warn("attachment blob GC: read storage root", "path", base, "error", err)
		}
		return 0
	}

	start := time.Now()
	var removed, workspaces int
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return removed
		}
		if !e.IsDir() {
			continue
		}
		n, err := reclaimAttachmentBlobs(ctx, db, root, e.Name())
		removed += n
		workspaces++
		if err != nil && logger != nil {
			// Truthful rather than tidy: some blobs may have gone even on the
			// error path, and the count says how many.
			logger.Warn("attachment blob GC: sweep failed",
				"workspace_id", e.Name(), "removed", n, "error", err)
		}
	}
	if removed > 0 && logger != nil {
		logger.Info("attachment blob GC: reclaimed unreferenced blobs",
			"blobs", removed, "workspaces", workspaces, "duration", time.Since(start))
	}
	return removed
}
