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
// unlinked only if no row in the same workspace still names that sha256. The
// partial UNIQUE indexes make "the same bytes twice on one owner" a single row,
// so a client that retries an upload cannot inflate the count.
//
// Rows removed by FK CASCADE never reach this code — SQLite deletes them with
// no application involvement — so their blobs would stay on disk. reclaimBlobs
// is the sweep for that case, and it is derived purely from the table: a blob is
// garbage iff no row names its sha256, which is why running it is always safe
// and never needs to be scheduled against anything.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
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
	for _, c := range sha {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", fmt.Errorf("attachment digest is not lowercase hex")
		}
	}
	return safepath.JoinUnder(root, "attachments", workspaceID, sha[:2], sha)
}

// ── Writing a blob ─────────────────────────────────────────────────────────

// storeAttachmentBlob writes data to its content-addressed location under root
// and returns the digest and the storage key.
//
// Writing an identical blob twice is a no-op rather than a rewrite. The bytes
// are already there by definition — the path IS their hash — so re-writing them
// buys nothing and, worse, would put a window in which the file is truncated
// while another request is reading it.
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

// unlinkAttachmentBlobIfUnreferenced removes a blob when no row in the same
// workspace still names it.
//
// Order matters and it is the safe one: the caller deletes the ROW first, then
// calls this. A crash between the two leaves a blob with no row — reclaimable,
// invisible, harmless. The other order would leave a row whose blob is gone,
// which is a 404 on a file the UI still lists.
//
// Best-effort by design: the deletion the user asked for has already been
// committed, and a failed unlink must not be reported as a failed delete.
func unlinkAttachmentBlobIfUnreferenced(
	ctx context.Context, db *sql.DB, logger *slog.Logger, root, workspaceID, sha string,
) {
	if root == "" {
		return
	}
	var refs int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM attachments WHERE workspace_id = ? AND sha256 = ?`,
		workspaceID, sha).Scan(&refs); err != nil {
		// Fail CLOSED on the blob: an unknown refcount means we cannot prove the
		// blob is unreferenced, and keeping bytes we could have deleted is a
		// reclaimable waste, while deleting bytes another row still points at is
		// data loss.
		if logger != nil {
			logger.Warn("attachment refcount", "workspace_id", workspaceID, "error", err)
		}
		return
	}
	if refs > 0 {
		return
	}
	abs, err := attachmentBlobPath(root, workspaceID, sha)
	if err != nil {
		return
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) && logger != nil {
		logger.Warn("unlink attachment blob", "workspace_id", workspaceID, "error", err)
	}
}

// reclaimAttachmentBlobs deletes every blob in one workspace that no row names.
//
// This is the sweep for rows the application never saw go: an issue
// hard-deleted, a crew wiped, a workspace removed. SQLite cascades those without
// running any Go, so the refcount above is never consulted and the blobs remain.
//
// It is derived entirely from the table — a blob is garbage iff no row names its
// sha256 — which makes it idempotent, safe to run at any moment, and impossible
// to get wrong by running it at the wrong time. It returns the number of blobs
// removed so a caller can log something truthful.
//
// The workspace directory is walked rather than the table: the question is
// "which files on disk are unreferenced", and only the filesystem can enumerate
// those. A missing directory is not an error — a workspace with no attachments
// has nothing to reclaim.
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

	live := map[string]struct{}{}
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT sha256 FROM attachments WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return 0, err
		}
		live[sha] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var removed int
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
		name := d.Name()
		if _, still := live[name]; still {
			return nil
		}
		// A leftover .tmp-* from an interrupted write is garbage by the same
		// definition — it names no digest at all.
		if err := os.Remove(path); err == nil {
			removed++
		}
		return nil
	})
	return removed, walkErr
}
