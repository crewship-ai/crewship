package api

// The attachment store — one shape for every attached file (#1768, item 7).
//
// This file owns everything that is true of an attachment regardless of what it
// is attached to: how bytes become a blob on disk, what a filename is allowed to
// be, which content types are accepted, and when a blob may be unlinked. The
// per-owner HTTP surfaces (issue_attachments.go for humans, and the internal
// twin an agent reaches through the sidecar) are thin on top of it.
//
// ── The blob layout ────────────────────────────────────────────────────────
//
//	<storage-root>/attachments/<workspace_id>/<sha256[0:2]>/<sha256>
//
// Every component is derived from bytes WE computed. The uploader's filename is
// never a path component — it is a display label in the `filename` column and
// nothing else. That is the difference between "path traversal is refused" and
// "path traversal is not expressible": there is no user-controlled string on the
// write path for a `../` to appear in. The filename is still validated, because
// it is echoed back in Content-Disposition, but a validation bug there costs a
// bad download header, not a write outside the storage root.
//
// The two-character shard keeps directory fan-out sane on filesystems that
// degrade with tens of thousands of sibling entries; it carries no meaning.
//
// ── De-duplication and deletion ────────────────────────────────────────────
//
// The blob is content-addressed, so two owners in one workspace uploading
// identical bytes share it and the second upload writes no new file. The
// workspace is the de-duplication boundary and never wider — see the migration
// header for why (tenant erasure, and the cross-tenant existence oracle that a
// shared blob would create).
//
// Deletion is refcounted at delete time: the row goes, then the blob is
// unlinked only if no row in the same workspace still names that sha256 AND
// lives at the content-addressed key. The partial UNIQUE indexes make "the same
// bytes under the same name on one owner" a single row, so a client that retries
// an upload cannot inflate the count.
//
// The storage_key half of that predicate is not belt-and-braces. A chat
// attachment's blob is NOT content-addressed — it stays at
// <crew>/<agent>/attachments/<chat>/<filename>, which is the agent-visible
// contract — so a chat row can carry the same digest while naming an entirely
// different file. Counting by digest alone let that unrelated file pin an issue
// attachment's bytes forever: the user deleted the file, the API said ok, and the
// bytes stayed. The refcount is a question about ONE blob, so it is asked about
// the key, not about the hash.
//
// Rows removed by FK CASCADE never reach this code — SQLite deletes them with
// no application involvement — so their blobs would stay on disk.
// reclaimAttachmentBlobs is the sweep for that case.
//
// ── Why the sweep takes a lock ──────────────────────────────────────────────
//
// "A blob is garbage iff no row names it" is true of a QUIESCED store and false
// of a live one: an upload writes the blob and inserts the row as two steps, and
// in between the blob is exactly indistinguishable from garbage. A sweep that
// deletes by absence therefore deletes files belonging to uploads that are one
// statement from committing — the row lands, the bytes are gone, and the
// attachment 404s for the rest of its life.
//
// So the write of a blob and the insert of its row are one critical section,
// keyed by (workspace, digest), and both the refcounted unlink and the sweep
// take the same key before they remove anything. What that buys, stated
// exactly: within ONE crewshipd process, no reclaim can observe the gap. It buys
// nothing across processes — two servers on one storage root still race, and the
// fix for that is a lease in the store, not a mutex. See reclaimAttachmentBlobs
// for what remains racy even in-process.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/safepath"
)

// maxAttachmentBytes caps a single upload.
//
// 25 MiB is not a new number: ProxyHandler.AgentChatAttachment has used it for
// chat attachments since it was written, and one ceiling for both surfaces is
// worth more than a per-surface optimum nobody can remember. It is well above a
// screenshot or a log dump and well below anything that would make buffering an
// upload in memory a denial-of-service lever.
const maxAttachmentBytes = 25 << 20

// maxAttachmentFilename mirrors the 255-byte limit essentially every filesystem
// imposes. It bounds the Content-Disposition header we build from it.
const maxAttachmentFilename = 255

// attachmentOwner names which arc of the exclusive-arc schema a row uses. The
// values are the `owner_type` column's CHECK vocabulary, spelled once.
type attachmentOwner string

const (
	attachmentOwnerIssue   attachmentOwner = "issue"
	attachmentOwnerComment attachmentOwner = "comment"
	attachmentOwnerChat    attachmentOwner = "chat"
)

// ── The content-type allowlist ─────────────────────────────────────────────

// errAttachmentType is returned for a file whose extension is not on the
// allowlist. It is a precondition failure, not a server fault.
var errAttachmentType = errors.New("attachment type not allowed")

// errAttachmentFilename is returned for a filename that cannot be stored.
var errAttachmentFilename = errors.New("invalid filename")

// attachmentTypes is the allowlist, keyed by lowercase extension.
//
// It is an ALLOWLIST and the request's own Content-Type header is discarded
// entirely. That header is chosen by whoever is uploading; honouring it is how a
// stored file becomes stored XSS — upload bytes that are HTML, declare them
// text/html, and every later download serves attacker markup from our origin. So
// the type is resolved from the extension against this table and the resolved
// value is what is stored and what is served.
//
// The set is deliberately small and boring: what people actually attach to an
// issue. Nothing here is interpreted by a browser as active content. Archives
// are included because a repro bundle is a real use case and an archive is inert
// until something extracts it; executables, scripts and HTML are not, and are
// absent on purpose rather than by oversight.
//
// Adding a type is a one-line change and a deliberate one. Removing the
// allowlist entirely is not: the download path's Content-Disposition +
// nosniff pairing is the second layer, not the only one.
var attachmentTypes = map[string]string{
	// Text and logs — the overwhelmingly common case.
	".txt":   "text/plain; charset=utf-8",
	".log":   "text/plain; charset=utf-8",
	".md":    "text/markdown; charset=utf-8",
	".csv":   "text/csv; charset=utf-8",
	".tsv":   "text/tab-separated-values; charset=utf-8",
	".json":  "application/json",
	".yaml":  "application/yaml",
	".yml":   "application/yaml",
	".toml":  "application/toml",
	".xml":   "application/xml",
	".diff":  "text/x-diff; charset=utf-8",
	".patch": "text/x-diff; charset=utf-8",
	// Images — screenshots, mostly. SVG is absent deliberately: it is a
	// script-bearing document format, not an image, and serving one from our
	// origin is script execution however it is labelled.
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	// Documents and inert bundles.
	".pdf": "application/pdf",
	".zip": "application/zip",
	".gz":  "application/gzip",
	".tgz": "application/gzip",
}

// attachmentTextTypes are the resolved types whose bytes are text an agent can
// read directly. The agent read path fences these and inlines them; everything
// else is offered as base64 with a much smaller ceiling.
func attachmentIsText(contentType string) bool {
	return strings.HasPrefix(contentType, "text/") ||
		contentType == "application/json" ||
		contentType == "application/yaml" ||
		contentType == "application/toml" ||
		contentType == "application/xml"
}

// sanitizeAttachmentFilename reduces a supplied name to something storable.
//
// It keeps only the basename, which is what makes "../../etc/passwd" collapse to
// "passwd" rather than being refused — refusing would be defensible too, but the
// name is not a path here and treating it as one is what created the class of
// bug in the first place. Control characters are stripped because the value is
// echoed into a Content-Disposition header and printed by the CLI; a bare CR or
// LF there is header injection and terminal repainting respectively.
func sanitizeAttachmentFilename(name string) (string, error) {
	name = filepath.Base(strings.TrimSpace(name))
	// filepath.Base uses the HOST separator. A Windows-style name arriving at a
	// Linux server keeps its backslashes, so strip that component too.
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "", errAttachmentFilename
	}
	if !utf8.ValidString(name) {
		return "", errAttachmentFilename
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return "", errAttachmentFilename
		}
	}
	if len(name) > maxAttachmentFilename {
		return "", errAttachmentFilename
	}
	return name, nil
}

// resolveAttachmentType returns the canonical content type for a filename, or
// errAttachmentType when the extension is not on the allowlist.
//
// The request's Content-Type is never consulted — see attachmentTypes.
func resolveAttachmentType(filename string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "", fmt.Errorf("%w: a file with no extension cannot be typed", errAttachmentType)
	}
	ct, ok := attachmentTypes[ext]
	if !ok {
		return "", fmt.Errorf("%w: %s", errAttachmentType, ext)
	}
	// mime.FormatMediaType normalises spacing so the stored value is stable
	// regardless of how the table above is written.
	if mt, params, err := mime.ParseMediaType(ct); err == nil {
		return mime.FormatMediaType(mt, params), nil
	}
	return ct, nil
}

// ── Blob paths ─────────────────────────────────────────────────────────────

// attachmentDigest is the content address of a byte slice, spelled once so the
// key a caller LOCKS is provably the key the blob is written under.
func attachmentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// attachmentStorageKey is the storage-root-relative key for a blob. It is a
// pure function of (workspace, sha256) — nothing the uploader chose appears in
// it. The key is stored on the row so a future layout change is a migration
// rather than a re-derivation everywhere.
func attachmentStorageKey(workspaceID, sha string) string {
	return "attachments/" + workspaceID + "/" + sha[:2] + "/" + sha
}

// attachmentBlobPath resolves a storage key to an absolute path under root.
//
// safepath.JoinUnder is applied even though every component is machine-derived.
// The check costs nothing and it is what keeps the guarantee true if a later
// caller ever passes a key read back from the database — a row an operator
// edited by hand, or a restored bundle from an instance running different code,
// is not a value this function should trust on our say-so.
func attachmentBlobPath(root, workspaceID, sha string) (string, error) {
	if len(sha) != 64 {
		return "", fmt.Errorf("attachment digest is not a sha256")
	}
	if !isAttachmentDigest(sha) {
		return "", fmt.Errorf("attachment digest is not lowercase hex")
	}
	return safepath.JoinUnder(root, "attachments", workspaceID, sha[:2], sha)
}

// isAttachmentDigest reports whether s is a lowercase-hex sha256.
//
// The sweep uses it to decide whether a filename in the blob tree is one of
// OURS. A file that is not named like a digest cannot be matched against the
// table at all, and deleting it because we could not identify it is how a sweep
// removes something an operator put there.
func isAttachmentDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// ── The (workspace, digest) critical section ───────────────────────────────

// attachmentBlobLockStripes is the number of mutexes the blob lock is spread
// over. Striping rather than a map keyed by digest: a map grows one entry per
// distinct digest the process ever sees and never shrinks, which is a slow leak
// on a busy instance. Two unrelated digests sharing a stripe cost a little
// contention on a path that is already doing filesystem IO; nothing else.
const attachmentBlobLockStripes = 256

var attachmentBlobLocks [attachmentBlobLockStripes]sync.Mutex

// lockAttachmentBlob enters the critical section for one (workspace, digest) and
// returns the release. Callers use it as `defer lockAttachmentBlob(ws, sha)()`
// or hold the returned func when the section is not the whole scope.
//
// It is held across "write the blob + insert the row" on the write path, and
// across "count the rows + remove the blob" on both delete paths. Those are the
// only two sections that exist, and they are the two that must not interleave.
func lockAttachmentBlob(workspaceID, sha string) (unlock func()) {
	m := &attachmentBlobLocks[attachmentBlobStripe(workspaceID, sha)]
	m.Lock()
	return m.Unlock
}

func attachmentBlobStripe(workspaceID, sha string) uint32 {
	h := fnv.New32a()
	_, _ = io.WriteString(h, workspaceID)
	_, _ = io.WriteString(h, "/")
	_, _ = io.WriteString(h, sha)
	return h.Sum32() % attachmentBlobLockStripes
}

// ── Writing a blob ─────────────────────────────────────────────────────────

// storeAttachmentBlob writes data to its content-addressed location under root
// and returns the digest and the storage key.
//
// Writing an identical blob twice is a no-op rather than a rewrite. The bytes
// are already there by definition — the path IS their hash — so re-writing them
// buys nothing and, worse, would put a window in which the file is truncated
// while another request is reading it.
//
// It does NOT take the blob lock, and that is deliberate: the section that must
// be atomic is "write the blob AND insert the row", which only the caller can
// bound. attachBytes holds lockAttachmentBlob across both. A version that locked
// here would look safe and protect nothing — the gap the sweep sees opens after
// this function returns.
func storeAttachmentBlob(root, workspaceID string, data []byte) (sha, key string, err error) {
	sum := sha256.Sum256(data)
	sha = hex.EncodeToString(sum[:])
	key = attachmentStorageKey(workspaceID, sha)

	abs, err := attachmentBlobPath(root, workspaceID, sha)
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		return sha, key, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", "", fmt.Errorf("create attachment dir: %w", err)
	}
	// Write to a temp file in the same directory and rename into place, so a
	// reader never sees a partially written blob at a path whose whole contract
	// is "these bytes hash to this name".
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".tmp-*")
	if err != nil {
		return "", "", fmt.Errorf("create attachment temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("write attachment: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", "", fmt.Errorf("close attachment: %w", err)
	}
	if err = os.Chmod(tmpName, 0o640); err != nil {
		return "", "", fmt.Errorf("chmod attachment: %w", err)
	}
	if err = os.Rename(tmpName, abs); err != nil {
		return "", "", fmt.Errorf("commit attachment: %w", err)
	}
	return sha, key, nil
}

// readAttachmentBlob returns the bytes of one blob, bounded by the same ceiling
// the upload path enforces.
//
// The bound is not redundant with the upload cap. The file on disk was written
// by some earlier version of this code, or restored from a bundle, or — on a
// self-hosted box — is simply a file in a directory an operator can write to.
// Reading it into memory is a decision this function makes now, not one the
// uploader made then.
func readAttachmentBlob(root, workspaceID, sha string) ([]byte, error) {
	abs, err := attachmentBlobPath(root, workspaceID, sha)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxAttachmentBytes+1))
}

// ── Refcounted deletion ────────────────────────────────────────────────────

// attachmentContentAddressedPredicate is the SQL half of "this row names THIS
// blob", spelled once because both the refcount and the sweep must ask the same
// question or the two disagree about what is garbage.
//
// It reconstructs attachmentStorageKey in SQL and compares it to the stored key,
// so a row whose blob lives anywhere else — today a chat attachment, tomorrow
// any producer that keeps its own layout — is not counted as a reference to the
// content-addressed file. `storage_key` is the authority on where a row's bytes
// are; the digest alone never was.
const attachmentContentAddressedPredicate = `storage_key = 'attachments/' || workspace_id || '/' || substr(sha256, 1, 2) || '/' || sha256`

// attachmentBlobIsUnreferenced reports whether the content-addressed blob for
// (workspace, digest) has no row naming it.
//
// The caller must hold lockAttachmentBlob for the same key: the answer is only
// meaningful for as long as no upload can commit a row behind it.
func attachmentBlobIsUnreferenced(ctx context.Context, db *sql.DB, workspaceID, sha string) (bool, error) {
	var refs int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM attachments WHERE workspace_id = ? AND sha256 = ? AND `+
			attachmentContentAddressedPredicate,
		workspaceID, sha).Scan(&refs); err != nil {
		return false, err
	}
	return refs == 0, nil
}

// unlinkAttachmentBlobIfUnreferenced removes a blob when no row in the same
// workspace still names it.
//
// Order matters and it is the safe one: the caller deletes the ROW first, then
// calls this. A crash between the two leaves a blob with no row — reclaimable,
// invisible, harmless. The other order would leave a row whose blob is gone,
// which is a 404 on a file the UI still lists.
//
// The count and the removal happen under the blob's lock, so an upload that is
// between its blob write and its INSERT cannot be mistaken for an absent
// reference. Without that, deleting the last row naming a digest while someone
// re-uploads the same bytes elsewhere deletes the file out from under them.
//
// Best-effort by design: the deletion the user asked for has already been
// committed, and a failed unlink must not be reported as a failed delete.
func unlinkAttachmentBlobIfUnreferenced(
	ctx context.Context, db *sql.DB, logger *slog.Logger, root, workspaceID, sha string,
) {
	if root == "" {
		return
	}
	abs, err := attachmentBlobPath(root, workspaceID, sha)
	if err != nil {
		return
	}
	defer lockAttachmentBlob(workspaceID, sha)()

	unreferenced, err := attachmentBlobIsUnreferenced(ctx, db, workspaceID, sha)
	if err != nil {
		// Fail CLOSED on the blob: an unknown refcount means we cannot prove the
		// blob is unreferenced, and keeping bytes we could have deleted is a
		// reclaimable waste, while deleting bytes another row still points at is
		// data loss.
		if logger != nil {
			logger.Warn("attachment refcount", "workspace_id", workspaceID, "error", err)
		}
		return
	}
	if !unreferenced {
		return
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) && logger != nil {
		logger.Warn("unlink attachment blob", "workspace_id", workspaceID, "error", err)
	}
}

// reclaimAttachmentDigests unlinks each of a known set of digests whose rows
// have just gone, and returns how many blobs it removed.
//
// This is what a caller uses when it KNOWS what it orphaned — the issue-delete
// handler reads the digests before the cascade takes the rows. It is a loop over
// the same refcounted unlink the ordinary delete uses, which is the point:
// nothing here deletes a file because it failed to find a reference, only
// because it asked about a specific digest and got zero.
func reclaimAttachmentDigests(
	ctx context.Context, db *sql.DB, logger *slog.Logger, root, workspaceID string, shas []string,
) int {
	if root == "" {
		return 0
	}
	var removed int
	for _, sha := range shas {
		abs, err := attachmentBlobPath(root, workspaceID, sha)
		if err != nil {
			continue
		}
		before := blobExists(abs)
		unlinkAttachmentBlobIfUnreferenced(ctx, db, logger, root, workspaceID, sha)
		if before && !blobExists(abs) {
			removed++
		}
	}
	return removed
}

// blobExists is the "did that actually go" check reclaimAttachmentDigests counts
// with. It is deliberately not an error-reporting stat: a blob that cannot be
// stat'd is reported as absent, which under-counts rather than claiming a
// removal that did not happen.
func blobExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// attachmentDigestsOfIssue returns the digests of every attachment a hard-delete
// of one issue is about to orphan — the issue's own, plus its comments'.
//
// Read BEFORE the DELETE, because after it the rows are gone: SQLite cascades
// them without the application ever seeing them, which is the whole reason this
// exists. Comment attachments have no producer yet; they are included because
// the arc and its cascade are already in the schema, and a sweep that quietly
// skipped them would be wrong the day the route lands rather than the day
// someone remembers it.
func attachmentDigestsOfIssue(
	ctx context.Context, db *sql.DB, workspaceID, crewID, identifier string,
) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT a.sha256
		FROM attachments a
		JOIN missions m ON m.id = COALESCE(
			a.mission_id,
			(SELECT c.mission_id FROM mission_comments c WHERE c.id = a.comment_id))
		WHERE a.workspace_id = ? AND m.identifier = ? AND m.crew_id = ? AND m.workspace_id = ?`,
		workspaceID, identifier, crewID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out = append(out, sha)
	}
	return out, rows.Err()
}

// attachmentTempReclaimAge is how long a .tmp-* file must have sat untouched
// before the sweep treats it as the leftover of a crashed write.
//
// The digest-named files are protected by the blob lock; a temp file cannot be,
// because its name carries no digest and the sweep has no way to work out which
// key its writer holds. The age floor is what stands in for the lock there. One
// hour is far longer than any upload can take — the request itself is capped at
// 25 MiB and the write is a single buffered os.File — and short enough that a
// crashed write is not a permanent leak.
const attachmentTempReclaimAge = time.Hour

// reclaimAttachmentBlobs deletes every blob in one workspace that no row names.
//
// This is the sweep for rows the application never saw go: a crew wiped, a
// workspace removed, a restore that carried blobs without their rows. SQLite
// cascades those without running any Go, so the refcount above is never
// consulted and the blobs remain. It returns the number of blobs removed so a
// caller can log something truthful.
//
// The workspace directory is walked rather than the table: the question is
// "which files on disk are unreferenced", and only the filesystem can enumerate
// those. A missing directory is not an error — a workspace with no attachments
// has nothing to reclaim.
//
// ── What makes it safe, and what is still not ──────────────────────────────
//
// It used to snapshot the live digests and then delete every file absent from
// the snapshot. That races every upload by construction: between the uploader's
// blob write and its INSERT the file is indistinguishable from garbage, so the
// sweep deleted blobs belonging to rows that committed a moment later — listed
// in the UI, 404 forever. Narrowing the sweep would have made that rarer, not
// wrong-free.
//
// So there is no snapshot. Each candidate is checked individually while holding
// its own (workspace, digest) lock — the same lock the upload holds across write
// AND insert — so within this process the sweep cannot see the gap. Files that
// are not named like a digest are left alone entirely; .tmp-* files, which
// cannot be locked because their name says nothing about which key is being
// written, are removed only when older than attachmentTempReclaimAge.
//
// Still racy, plainly:
//
//   - ACROSS PROCESSES. Two crewshipd instances on one storage root do not share
//     these mutexes, and nothing in the store arbitrates. A second writer is out
//     of scope for the whole attachment design today (the blob path assumes a
//     local filesystem), and closing it needs a lease in the storage layer.
//   - A .tmp-* file whose write stalls longer than the age floor is still
//     removable, which fails that one upload with a rename error rather than
//     corrupting anything.
//
// A per-file query error is not fatal to the sweep: that file is skipped
// (fail-closed, the blob stays) and the first such error is returned so the
// caller logs a truthful "reclaimed n, with errors".
func reclaimAttachmentBlobs(ctx context.Context, db *sql.DB, root, workspaceID string) (int, error) {
	if root == "" || workspaceID == "" {
		return 0, nil
	}
	if _, err := safepath.ValidateComponent(workspaceID); err != nil {
		return 0, err
	}
	base := filepath.Join(root, "attachments", workspaceID)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return 0, nil
	}

	var removed int
	var firstErr error
	walkErr := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A directory that vanished under us (a concurrent reclaim) is not a
			// failure of this one.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()

		if strings.HasPrefix(name, ".tmp-") {
			info, statErr := d.Info()
			if statErr != nil || time.Since(info.ModTime()) < attachmentTempReclaimAge {
				return nil
			}
			if os.Remove(path) == nil {
				removed++
			}
			return nil
		}
		if !isAttachmentDigest(name) {
			// Not a file this store wrote. Deleting something we cannot identify
			// is how a sweep removes an operator's file.
			return nil
		}

		unlock := lockAttachmentBlob(workspaceID, name)
		defer unlock()
		unreferenced, qErr := attachmentBlobIsUnreferenced(ctx, db, workspaceID, name)
		if qErr != nil {
			if firstErr == nil {
				firstErr = qErr
			}
			return nil
		}
		if !unreferenced {
			return nil
		}
		if os.Remove(path) == nil {
			removed++
		}
		return nil
	})
	if walkErr != nil {
		return removed, walkErr
	}
	return removed, firstErr
}
