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
//     attachmentTempReclaimAge;
//   - UNPUBLISHED chat attachments — a row still in state `pending` past the
//     grace period, together with whatever bytes it names. That pass is
//     row-driven rather than tree-driven and is described on
//     reclaimUnpublishedChatAttachments below.
//
// NOT collected:
//
//   - PUBLISHED chat attachment blobs. They are not content-addressed and live
//     outside <root>/attachments/ entirely (proxy_attachments.go); a live one
//     is removed by DELETE …/chats/{chatId}/attachments/{attachmentId}, and a
//     whole chat's tree by cleanupChatAttachments when the chat is deleted.
//     The tree sweep never leaves the attachments/ directory, which is what
//     keeps that true;
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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	// Registered rather than listed as an exception. This goroutine deletes
	// FILES, so a test that finishes while a sweep is mid-pass has its temp
	// storage root removed underneath the walk — and the failure would surface
	// in whichever unrelated test happened to be running next. Draining it is
	// also cheap: every wait point is either the ticker or ctx.Done(), and a
	// pass is bounded by the directory tree it walks.
	done := beginBackgroundWork()
	go func() {
		defer done()
		runAttachmentGCPass(ctx, db, logger, root)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runAttachmentGCPass(ctx, db, logger, root)
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
		if err != nil && logger != nil && !errors.Is(err, context.Canceled) {
			// Truthful rather than tidy: some blobs may have gone even on the
			// error path, and the count says how many.
			//
			// context.Canceled is excluded because it is not a failure — it is
			// shutdown. Logging it as one puts a WARN in every clean stop, and
			// an operator who learns to skip that line will skip the real ones
			// printed beside it. A sweep interrupted mid-walk resumes from
			// scratch on the next boot; nothing is lost by staying quiet.
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

// runAttachmentGCPass is one full pass: the content-addressed tree sweep, then
// the unpublished-chat-attachment reclaim.
//
// Two passes rather than one because they are two different enumerations of two
// different populations. The tree sweep asks "is there a file no row names?" and
// can only be answered by walking the tree. The reclaim below asks "is there a
// row no upload finished?" and can only be answered from the table — its blobs
// are not in the tree the sweep walks, and half of them do not exist at all.
func runAttachmentGCPass(ctx context.Context, db *sql.DB, logger *slog.Logger, root string) {
	sweepAttachmentBlobs(ctx, db, logger, root)
	reclaimUnpublishedChatAttachments(ctx, db, logger, root, chatAttachmentPublishGrace)
}

// chatAttachmentPublishGrace is how long a reservation may stay unpublished
// before the collector treats it as abandoned.
//
// The window it has to clear is one request: the row is inserted, the bytes are
// PUT through the IPC socket, the row is promoted. The IPC client's own timeout
// is 30 s, so an hour is roughly two orders of magnitude of headroom — long
// enough that no live upload can be collected underneath itself even on a
// pathologically slow host, short enough that an abandoned reservation and its
// bytes do not outlive the working day.
//
// It deliberately matches attachmentTempReclaimAge and the GC interval: three
// numbers with the same justification should not be three different numbers to
// remember.
const chatAttachmentPublishGrace = time.Hour

// reclaimUnpublishedChatAttachments removes rows that never finished publishing,
// and the bytes they name.
//
// ── What it is collecting ─────────────────────────────────────────────────
//
// AgentChatAttachment writes the row first (state `pending`), publishes the
// bytes second, and promotes the row third. Every ordinary failure compensates
// itself — the handler deletes its own reservation and answers an error — so the
// rows this pass sees are the ones where the PROCESS did not survive to do that:
// killed between the INSERT and the IPC PUT (no bytes), or between the PUT and
// the promotion (bytes, unpromoted).
//
// Both are collectable by the same rule, and safely, because a `pending` row was
// never returned to anybody: the list endpoint matches `stored` exactly, so no
// client has ever seen it, and no 201 was ever sent for it. Removing it cannot
// contradict something a user was told.
//
// ── Why it is row-driven ──────────────────────────────────────────────────
//
// Chat blobs live under <root>/<crewID>/<agentSlug>/attachments/… — outside the
// content-addressed tree, which is the whole point of the layout (the path is
// the agent-visible contract). A tree walk could not tell an unpublished blob
// from a live one there without consulting the table anyway, and would miss the
// half of the population that has no file at all. The table is the authority, so
// the table is the enumeration.
//
// Ordered bytes-then-row for the same reason the delete endpoint is, and GATED
// on the unlink rather than merely sequenced after it: a row that outlives its
// bytes is visible and retryable, a blob that outlives its row is neither. The
// count returned is what was actually reclaimed — an attachment whose bytes
// survived is not one of them.
//
// One limit, stated rather than discovered: it runs only where the collector
// runs, and the collector needs a storage root (StartAttachmentBlobGC). On an
// instance with no storage configured a `pending` row would therefore survive —
// which is bearable because such an instance cannot store bytes at all, so the
// upload fails at the IPC layer and the handler removes its own reservation on
// the way out. The uncollected case there needs a crash mid-request on a host
// that could never have completed it.
func reclaimUnpublishedChatAttachments(ctx context.Context, db *sql.DB, logger *slog.Logger, root string, grace time.Duration) int {
	if db == nil {
		return 0
	}
	cutoff := time.Now().UTC().Add(-grace).Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `
		SELECT id, storage_key FROM attachments
		 WHERE owner_type = ? AND state <> ? AND created_at < ?
		 LIMIT 500`,
		string(attachmentOwnerChat), attachmentStateStored, cutoff)
	if err != nil {
		if logger != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("chat attachment GC: query unpublished rows", "error", err)
		}
		return 0
	}
	type pending struct{ id, key string }
	var stale []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.key); err != nil {
			rows.Close()
			if logger != nil {
				logger.Warn("chat attachment GC: scan unpublished row", "error", err)
			}
			return 0
		}
		stale = append(stale, p)
	}
	rows.Close()

	var removed int
	for _, p := range stale {
		if err := ctx.Err(); err != nil {
			return removed
		}
		// The row is the ONLY thing that names these bytes — they are outside
		// the content-addressed tree sweepAttachmentBlobs walks — so a failed
		// unlink must stop here. Deleting the row anyway would turn a leak the
		// next pass retries into one nothing can ever find. The blob stays, the
		// row stays `pending` (invisible to every reader), and the hourly pass
		// tries again; the warning is already logged one level down.
		if err := removeChatAttachmentBlob(root, p.key, logger); err != nil {
			continue
		}
		if _, err := db.ExecContext(ctx,
			`DELETE FROM attachments WHERE id = ? AND state <> ?`, p.id, attachmentStateStored); err != nil {
			if logger != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("chat attachment GC: delete unpublished row",
					"attachment_id", p.id, "error", err)
			}
			continue
		}
		removed++
	}
	if removed > 0 && logger != nil {
		logger.Info("chat attachment GC: reclaimed unpublished attachments", "attachments", removed)
	}
	return removed
}

// removeChatAttachmentBlob unlinks one chat attachment's bytes from the local
// storage root, and the directory that held them if it is now empty.
//
// It REPORTS whether the bytes are gone, and that answer is what the caller
// gates the row delete on. Anything else inverts the ordering the whole reclaim
// rests on: the row is the only name these bytes have, so a collector that logs
// a failed unlink and deletes the row regardless is the one thing that can make
// them permanently unreachable. nil means "there are no bytes at this key any
// more", which includes the case where there never were any — half the
// unpublished population died before its PUT — and the case of a row that names
// nothing at all.
//
// The storage key is a value this process computed, but it is re-validated
// against the root before it is used as a path: a corrupted row must never be
// able to make the collector unlink something outside the storage tree. That
// refusal is an error rather than a shrug, for the same reason: nothing was
// removed, so nothing may be forgotten. Same defence-in-depth as
// cleanupChatAttachments, and the same limitation — this walks the local
// filesystem, so a non-local StorageProvider would need the removal routed
// through its Delete (TODO(#1768), tracked there).
//
// The parent is removed with Remove, not RemoveAll: for a current key the parent
// is the attachment's own <attachmentId>/ directory and removing it is exactly
// right, while for a LEGACY key (uploaded before the id segment existed) the
// parent is the chat's shared directory — and Remove on a non-empty directory
// fails harmlessly, which is what makes one call correct for both. It stays
// best-effort: an empty directory is not what the row was accounting for.
func removeChatAttachmentBlob(root, storageKey string, logger *slog.Logger) error {
	if storageKey == "" {
		// A row that names no bytes has none to lose.
		return nil
	}
	if root == "" {
		// Nothing local to remove from, so nothing was removed. The collector
		// does not run without a root (StartAttachmentBlobGC), so this is a
		// direct caller, not the hourly pass — logged all the same, so that
		// every path out of here that keeps a row also says why.
		if logger != nil {
			logger.Warn("chat attachment GC: no storage root to unlink from",
				"storage_key", storageKey)
		}
		return errors.New("no storage root configured")
	}
	full := filepath.Clean(filepath.Join(root, storageKey))
	base := filepath.Clean(root)
	if full == base || !strings.HasPrefix(full, base+string(filepath.Separator)) {
		if logger != nil {
			logger.Warn("chat attachment GC: refusing to unlink outside the storage root",
				"storage_key", storageKey)
		}
		return fmt.Errorf("storage key %q resolves outside the storage root", storageKey)
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		if logger != nil {
			logger.Warn("chat attachment GC: unlink failed", "path", full, "error", err)
		}
		return err
	}
	_ = os.Remove(filepath.Dir(full))
	return nil
}
