package backup

// Attachment-blob collection + restore.
//
// `attachments` rides every workspace bundle (intent.go), but the row is
// metadata only: the file it describes is a content-addressed blob on the
// host at <root>/attachments/<workspace_id>/<sha[:2]>/<sha> (internal/api
// attachmentBlobPath). Nothing collected those files, so every restore landed
// attachment rows whose download 404s — the same silent-loss shape
// memoryblobs.go fixed for memory versions, and fixed here the same way: a
// dedicated section in the SAME payload tar (inside the same encryption
// boundary), streamed file by file, extracted by ExtractPayload and written
// back to the local filesystem on restore.
//
// Two differences from memory blobs:
//
//   - The on-disk key includes the WORKSPACE id, and a forked restore
//     (--as-workspace / --as-crew) regenerates that id. Downloads resolve the
//     blob from (row.workspace_id, row.sha256), never from storage_key (see
//     remap_attachments_test.go), so restore writes each blob under the
//     workspace id the row carries AFTER RemapIDs — the target id.
//   - A blob missing at create time is recorded in the manifest
//     (Contents.AttachmentsMissing + an Incomplete item), not only logged.
//
// Format version: the section is additive. ExtractPayload's default branch
// discards unknown top-level entries, so an older reader restores a bundle
// carrying this section exactly as before (rows, no files — what it always
// did), and a newer reader restores an older bundle with nothing to land.
// Per format.go's policy additive sections do not bump FormatVersion.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"time"

	"github.com/crewship-ai/crewship/internal/safepath"
)

// attachmentBlobsSectionPrefix is the top-level payload-tar prefix. Entries
// are attachment-blobs/<sha[:2]>/<sha> — no workspace segment, because the
// destination workspace is decided at restore time from the rows.
const attachmentBlobsSectionPrefix = "attachment-blobs/"

// attachmentBlobsSinkKey is the ExtractPayload sink bucket name.
const attachmentBlobsSinkKey = "attachment-blobs"

// AttachmentBlobsResult reports what WriteAttachmentBlobsSection did.
type AttachmentBlobsResult struct {
	// Included is the number of distinct blobs written into the bundle.
	Included int
	// Missing carries every referenced sha256 whose blob was not collected.
	Missing []string
	// RootUnset is true when rows referenced blobs but no attachment root
	// was configured, so nothing could be collected at all.
	RootUnset bool
}

// attachmentRefs returns, per distinct valid sha256 referenced by the dump's
// attachments rows, the workspace ids whose rows reference it — first-seen
// order for both. Rows with an invalid sha or an unsafe workspace component
// are reported in invalid (they can never resolve to a file).
func attachmentRefs(dump *DBDump) (order []string, byWorkspace map[string][]string, invalid []string) {
	byWorkspace = map[string][]string{}
	if dump == nil {
		return nil, byWorkspace, nil
	}
	seenPair := map[string]bool{}
	for _, row := range dump.Tables["attachments"] {
		sha, _ := row["sha256"].(string)
		ws, _ := row["workspace_id"].(string)
		if !validSha256Hex(sha) {
			invalid = append(invalid, sha)
			continue
		}
		if _, err := safepath.ValidateComponent(ws); err != nil || ws == "" {
			invalid = append(invalid, sha)
			continue
		}
		if _, ok := byWorkspace[sha]; !ok {
			order = append(order, sha)
		}
		if !seenPair[ws+"/"+sha] {
			seenPair[ws+"/"+sha] = true
			byWorkspace[sha] = append(byWorkspace[sha], ws)
		}
	}
	return order, byWorkspace, invalid
}

// WriteAttachmentBlobsSection copies every blob the dump's attachments rows
// reference into dst under attachment-blobs/<sha[:2]>/<sha>, streaming each
// file. root is the SOURCE instance's storage root. A referenced blob that is
// not on disk is not a create failure — the rows still ride — but it is
// returned in Missing so the caller records it in the manifest.
func WriteAttachmentBlobsSection(dst *TarZstWriter, root string, dump *DBDump, now time.Time) (*AttachmentBlobsResult, error) {
	res := &AttachmentBlobsResult{}
	order, byWorkspace, invalid := attachmentRefs(dump)
	res.Missing = append(res.Missing, invalid...)
	if len(order) == 0 {
		return res, nil
	}
	if root == "" {
		res.RootUnset = true
		res.Missing = append(res.Missing, order...)
		return res, nil
	}
	for _, sha := range order {
		var found string
		var size int64
		for _, ws := range byWorkspace[sha] {
			p, err := safepath.JoinUnder(root, "attachments", ws, sha[:2], sha)
			if err != nil {
				continue
			}
			info, err := os.Stat(p)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return res, fmt.Errorf("backup: stat attachment blob %s: %w", sha, err)
			}
			if info.IsDir() {
				continue
			}
			found, size = p, info.Size()
			break
		}
		if found == "" {
			res.Missing = append(res.Missing, sha)
			continue
		}
		if err := writeAttachmentBlobEntry(dst, found, sha, size, now); err != nil {
			return res, err
		}
		res.Included++
	}
	return res, nil
}

func writeAttachmentBlobEntry(dst *TarZstWriter, blobPath, sha string, size int64, now time.Time) error {
	f, err := os.Open(blobPath)
	if err != nil {
		return fmt.Errorf("backup: open attachment blob %s: %w", sha, err)
	}
	defer func() { _ = f.Close() }()
	if err := dst.WriteStream(attachmentBlobsSectionPrefix+sha[:2]+"/"+sha, 0o600, now, size, f); err != nil {
		return fmt.Errorf("backup: write attachment blob %s: %w", sha, err)
	}
	return nil
}

// attachmentIncomplete turns a create-time result into the manifest's
// Incomplete item (nil when nothing is missing).
func attachmentIncomplete(res *AttachmentBlobsResult, workspaceID string) *IncompleteItem {
	if res == nil || len(res.Missing) == 0 {
		return nil
	}
	detail := fmt.Sprintf("%d attached file(s) were not on disk when the backup ran; their rows are in the bundle and their downloads will fail after a restore", len(res.Missing))
	if res.RootUnset {
		detail = fmt.Sprintf("attachment storage was not configured for this backup, so none of the %d attached file(s) were collected", len(res.Missing))
	}
	return &IncompleteItem{Kind: IncompleteAttachmentMissing, Detail: detail, Count: len(res.Missing), Workspace: workspaceID}
}

// AttachmentRestoreStats reports what RestoreAttachmentBlobs did, counted per
// (workspace, blob) destination.
type AttachmentRestoreStats struct {
	Restored       int
	AlreadyPresent int
	Missing        int
	Conflicts      int
	// RootUnset: the target has no attachment root, so nothing could land.
	RootUnset bool
}

// Incomplete returns the restore-side gap items for these stats.
func (s AttachmentRestoreStats) Incomplete(workspaceID string) []IncompleteItem {
	var out []IncompleteItem
	if s.Missing > 0 {
		detail := fmt.Sprintf("%d attached file(s) are not in this bundle; their rows restored and their downloads will fail", s.Missing)
		if s.RootUnset {
			detail = fmt.Sprintf("this instance has no attachment storage configured, so %d attached file(s) were not written", s.Missing)
		}
		out = append(out, IncompleteItem{Kind: IncompleteAttachmentMissing, Detail: detail, Count: s.Missing, Workspace: workspaceID})
	}
	if s.Conflicts > 0 {
		out = append(out, IncompleteItem{Kind: IncompleteAttachmentConflict,
			Detail: fmt.Sprintf("%d attached file(s) already existed on this instance with different content and were left as they were", s.Conflicts),
			Count:  s.Conflicts, Workspace: workspaceID})
	}
	return out
}

// RestoreAttachmentBlobs writes the blobs carried in payload's
// attachment-blobs section back under root/attachments/<ws>/<sha[:2]>/<sha>,
// for every (ws, sha) the dump's attachments rows name. dump must be the
// dump THIS restore lands, after RemapIDs, so ws is the target id.
//
// The write destination is derived from the dump, never from the archive
// entry's name (which is only a lookup key), and every written blob is
// verified against its sha256 before it is renamed into place. An existing
// file is never overwritten: identical bytes count as AlreadyPresent,
// different bytes as a Conflict. With dryRun nothing is written; the stats
// say what would happen.
func RestoreAttachmentBlobs(ctx context.Context, root string, payload *ExtractedPayload, dump *DBDump, dryRun bool) (AttachmentRestoreStats, error) {
	var st AttachmentRestoreStats
	order, byWorkspace, invalid := attachmentRefs(dump)
	st.Missing += len(invalid)
	if len(order) == 0 {
		return st, nil
	}
	pending := make(map[string]pendingBlob, len(byWorkspace))
	for sha, wss := range byWorkspace {
		pending[sha] = pendingBlob{sha: sha, workspaces: wss}
	}
	if root == "" && !dryRun {
		st.RootUnset = true
	} else if err := walkAttachmentBlobs(ctx, root, payload, pending, dryRun, &st); err != nil {
		return st, err
	}
	// Whatever no bundle entry matched is missing.
	for _, pb := range pending {
		st.Missing += len(pb.workspaces)
	}
	return st, nil
}

// pendingBlob is one blob the dump references. sha is the dump's own copy
// of the digest: the archive entry's name only finds the entry, it never
// reaches a path.
type pendingBlob struct {
	sha        string
	workspaces []string
}

// walkAttachmentBlobs streams the section, landing every entry pending names
// and removing it from pending.
func walkAttachmentBlobs(ctx context.Context, root string, payload *ExtractedPayload, pending map[string]pendingBlob, dryRun bool, st *AttachmentRestoreStats) error {
	if payload == nil {
		return nil
	}
	r, ok, err := payload.OpenAttachmentBlobs(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer func() { _ = r.Close() }()
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup: read attachment blob section: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// hdr.Name is archive-controlled and used only as a lookup key; the
		// destination comes from the dump's rows (see attachmentRefs).
		pb, known := pending[path.Base(hdr.Name)]
		if !known {
			continue
		}
		delete(pending, pb.sha)
		if err := restoreOneAttachmentBlob(root, pb.sha, pb.workspaces, tr, dryRun, st); err != nil {
			return err
		}
	}
}

// restoreOneAttachmentBlob lands one bundle entry at every destination that
// needs it. The first destination written streams from the archive; any
// further one is copied from that file (and verified again).
func restoreOneAttachmentBlob(root, sha string, wss []string, body io.Reader, dryRun bool, st *AttachmentRestoreStats) error {
	var written string
	for i, ws := range wss {
		var dst string
		if root != "" {
			p, err := safepath.JoinUnder(root, "attachments", ws, sha[:2], sha)
			if err != nil {
				st.Missing++
				continue
			}
			dst = p
			if same, exists, err := fileHasDigest(dst, sha); err != nil {
				return fmt.Errorf("backup: check existing attachment blob %s: %w", sha, err)
			} else if exists {
				if same {
					st.AlreadyPresent++
				} else {
					st.Conflicts++
					slog.Warn("backup restore: attachment blob path already holds different content; left untouched", "sha256", sha, "workspace_id", ws)
				}
				continue
			}
		}
		if dryRun {
			st.Restored++
			continue
		}
		var src io.Reader = body
		var closeSrc func()
		if written != "" {
			f, err := os.Open(written)
			if err != nil {
				return fmt.Errorf("backup: reopen attachment blob %s: %w", sha, err)
			}
			src, closeSrc = f, func() { _ = f.Close() }
		}
		err := writeBlobDurable(dst, src, sha)
		if closeSrc != nil {
			closeSrc()
		}
		if errors.Is(err, errBlobDigestMismatch) {
			// The bundle carried bytes that do not match the row's digest.
			// Refuse to plant them under a name that lies; report the file
			// as missing instead.
			slog.Warn("backup restore: attachment blob in bundle does not match its sha256; not restored", "sha256", sha)
			st.Missing += len(wss) - i
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup: restore attachment blob %s: %w", sha, err)
		}
		written = dst
		st.Restored++
	}
	// An unread remainder of the entry is skipped by the next tr.Next().
	return nil
}

// fileHasDigest reports whether p exists and, if so, whether its content
// hashes to sha.
func fileHasDigest(p, sha string) (same, exists bool, err error) {
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, true, err
	}
	if info.IsDir() {
		return false, true, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, true, err
	}
	return hex.EncodeToString(h.Sum(nil)) == sha, true, nil
}
