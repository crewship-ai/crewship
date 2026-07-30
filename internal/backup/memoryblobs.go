package backup

// Memory-version blob collection + restore.
//
// memory_versions rides every workspace-scope bundle (see intent.go
// and dbdump.go's BackupTables), but the row is only an audit-trail
// pointer: `payload_ref` names a content-addressed blob file on disk
// at {MemoryRoot}/versions/<sha[:2]>/<sha> (internal/memory/versions.go
// RecordVersion). Before this file existed, CreateBackup never
// collected those blob files, so a restore landed memory_versions rows
// whose payload_ref pointed at files that do not exist on the target —
// memory history / HITL review / memory restore silently break because
// the DB row is present and looks fine until something tries to read
// the content.
//
// The fix mirrors the pattern the collector already uses for per-crew
// filesystem sections (collector.go / restorer.go): a dedicated
// top-level entry in the SAME payload tar (so it rides through the
// SAME AGE-encryption boundary as everything else — see bundle.go
// SealPayload, which seals the whole payload stream, not per-section),
// extracted back out on restore via ExtractedPayload the same way
// workspace/volumes/memory/system sections are.
//
// Unlike those per-crew sections, memory-version blobs are host-side
// (BlobRoot is a Crewship-instance-wide directory, never inside a crew
// container — see cmd_start.go wiring cfg.Storage.MemoryRoot), so the
// restore side writes directly to the local filesystem instead of
// going through DockerOps.

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"
)

// memoryBlobsSectionPrefix is the top-level payload-tar prefix used by
// WriteMemoryBlobsSection / ExtractPayload / RestoreMemoryBlobs.
const memoryBlobsSectionPrefix = "memory-blobs/"

// memoryBlobsSinkKey is the ExtractPayload sink bucket name for the
// memory-blobs section. Unlike workspace/volumes/memory/system, this
// section carries no per-crew slug (memory_versions is workspace-scoped,
// not per-crew), so it gets a single fixed key instead of one per slug.
const memoryBlobsSinkKey = "memory-blobs"

// MemoryBlobsResult reports what WriteMemoryBlobsSection did.
type MemoryBlobsResult struct {
	// Included is the number of blobs actually written into the bundle.
	Included int
	// Missing carries the sha256 of every memory_versions row whose
	// blob did not exist on disk at backup time. Not a hard failure —
	// see the WriteMemoryBlobsSection doc comment.
	Missing []string
}

// distinctMemoryVersionShas extracts the set of sha256 values referenced
// by dump's memory_versions rows, in first-seen order. Returns nil when
// the dump carries no such table — crew-scope bundles never do (DumpCrew
// does not export memory_versions), so this is the normal, non-error
// case for that scope.
func distinctMemoryVersionShas(dump *DBDump) []string {
	if dump == nil {
		return nil
	}
	rows, ok := dump.Tables["memory_versions"]
	if !ok {
		return nil
	}
	seen := make(map[string]struct{}, len(rows))
	var out []string
	for _, row := range rows {
		sha, _ := row["sha256"].(string)
		if sha == "" {
			continue
		}
		if _, dup := seen[sha]; dup {
			continue
		}
		seen[sha] = struct{}{}
		out = append(out, sha)
	}
	return out
}

// validSha256Hex reports whether s looks like a lowercase-hex sha256
// digest (64 chars) — the format RecordVersion produces via
// hex.EncodeToString(sha256.Sum256(...)). Guards every place a sha
// from bundle content (dump rows or tar entry names) gets joined onto
// a filesystem path, so a corrupted or tampered value cannot become a
// path-traversal vector.
func validSha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// WriteMemoryBlobsSection copies the content-addressed memory-version
// blobs referenced by dump's memory_versions rows into dst, under
// memory-blobs/<sha[:2]>/<sha>. blobRoot is the SOURCE instance's
// configured blob root ({MemoryRoot}/versions) — the same value
// RecordVersion used when it wrote the blob. Empty blobRoot means
// memory versioning was never enabled on this instance; the section is
// skipped entirely (matches the rest of the memory subsystem's "empty
// BlobRoot disables versioning" convention).
//
// Blobs stream via WriteStream (not buffered into memory) so this
// scales the same way the per-crew sections do; individual memory
// version blobs are markdown-scale (KB), but the set referenced by a
// busy workspace's audit trail is not bounded.
//
// A referenced sha with no blob on disk is NOT a hard failure — it can
// legitimately happen after a retention sweep, or (the scenario that
// motivated this fix) a prior restore that dropped blobs while keeping
// rows. Missing shas are collected into the result so the caller can
// warn the operator; backup creation proceeds with what it CAN find
// rather than aborting the whole bundle over one stale row.
func WriteMemoryBlobsSection(dst *TarZstWriter, blobRoot string, dump *DBDump, now time.Time) (*MemoryBlobsResult, error) {
	res := &MemoryBlobsResult{}
	if blobRoot == "" {
		return res, nil
	}
	for _, sha := range distinctMemoryVersionShas(dump) {
		if !validSha256Hex(sha) {
			res.Missing = append(res.Missing, sha)
			continue
		}
		blobPath := filepath.Join(blobRoot, sha[:2], sha)
		info, err := os.Stat(blobPath)
		if err != nil {
			if os.IsNotExist(err) {
				res.Missing = append(res.Missing, sha)
				continue
			}
			return res, fmt.Errorf("backup: stat memory blob %s: %w", sha, err)
		}
		if info.IsDir() {
			res.Missing = append(res.Missing, sha)
			continue
		}
		if err := writeMemoryBlobEntry(dst, blobPath, sha, info.Size(), now); err != nil {
			return res, err
		}
		res.Included++
	}
	return res, nil
}

func writeMemoryBlobEntry(dst *TarZstWriter, blobPath, sha string, size int64, now time.Time) error {
	f, err := os.Open(blobPath)
	if err != nil {
		return fmt.Errorf("backup: open memory blob %s: %w", sha, err)
	}
	defer func() { _ = f.Close() }()
	name := memoryBlobsSectionPrefix + sha[:2] + "/" + sha
	if err := dst.WriteStream(name, 0o600, now, size, f); err != nil {
		return fmt.Errorf("backup: write memory blob %s: %w", sha, err)
	}
	return nil
}

// rewriteMemoryVersionsPayloadRef updates every memory_versions row's
// payload_ref to point at the TARGET instance's blob root instead of
// the source's. payload_ref is an absolute filesystem path baked in at
// RecordVersion time; restoring the row verbatim onto a different
// instance (a different host, or the same host with MemoryRoot
// configured under a different path) would otherwise leave it pointing
// at a path that never existed there — even after this same restore
// writes the actual blob bytes under blobRoot. Content-addressing makes
// the fix mechanical: the new payload_ref is always
// {blobRoot}/{sha[:2]}/{sha}, recomputed from the row's own sha256
// column rather than trusted from the bundle.
//
// Runs unconditionally whenever blobRoot is configured on the target —
// not gated on --as-workspace/--as-crew like the slug/ID rewrites,
// because the path problem exists on every restore, including a
// same-slug disaster-recovery restore onto a fresh instance.
func rewriteMemoryVersionsPayloadRef(dump *DBDump, blobRoot string) {
	if dump == nil || blobRoot == "" {
		return
	}
	rows, ok := dump.Tables["memory_versions"]
	if !ok {
		return
	}
	for _, row := range rows {
		sha, _ := row["sha256"].(string)
		if !validSha256Hex(sha) {
			continue
		}
		row["payload_ref"] = filepath.Join(blobRoot, sha[:2], sha)
	}
}

// RestoreMemoryBlobs writes the content-addressed memory-version blobs
// carried in payload's memory-blobs section back onto the target
// host's blobRoot. Mirrors RestoreCrew's per-crew docker restore, but
// this section is host-side (memory_versions blobs never lived inside
// a crew container) so it writes directly to the local filesystem
// instead of going through DockerOps.
//
// Idempotent per blob: content-addressed means the same sha always
// carries the same bytes, so an existing file at the destination is
// left untouched rather than rewritten — safe to re-run restore
// against a target that already has some blobs.
func RestoreMemoryBlobs(ctx context.Context, blobRoot string, payload *ExtractedPayload) (int, error) {
	if payload == nil || blobRoot == "" {
		return 0, nil
	}
	r, ok, err := payload.OpenMemoryBlobs(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	defer func() { _ = r.Close() }()

	tr := tar.NewReader(r)
	written := 0
	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, fmt.Errorf("backup: read memory blob section: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// hdr.Name is "<sha[:2]>/<sha>" — repackIntoSink already strips
		// the memory-blobs/ prefix at extract time.
		sha := path.Base(hdr.Name)
		if !validSha256Hex(sha) {
			// Defence in depth; ExtractPayload's ".." / symlink checks
			// already reject a tampered entry name before this point.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return written, fmt.Errorf("backup: drain rejected memory blob entry %q: %w", hdr.Name, err)
			}
			continue
		}
		dst := filepath.Join(blobRoot, sha[:2], sha)
		if _, statErr := os.Stat(dst); statErr == nil {
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return written, fmt.Errorf("backup: drain existing memory blob %s: %w", sha, err)
			}
			continue
		}
		if err := restoreMemoryBlobFile(dst, tr); err != nil {
			return written, fmt.Errorf("backup: restore memory blob %s: %w", sha, err)
		}
		written++
	}
	return written, nil
}

// restoreMemoryBlobFile atomically writes r's remaining bytes to dst:
// tempfile in the same directory, then rename. Matches the write
// pattern internal/memory/versions.go writeBlobIfMissing uses for the
// original write, so a blob landed by restore is indistinguishable
// on-disk from one RecordVersion wrote directly.
func restoreMemoryBlobFile(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp := dst + ".restore.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
