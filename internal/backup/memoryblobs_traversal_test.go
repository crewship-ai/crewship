package backup_test

// Pins the fix for a CodeQL-flagged "Arbitrary file access during
// archive extraction (Zip Slip)" finding on RestoreMemoryBlobs
// (internal/backup/memoryblobs.go): the write destination for every
// memory-version blob restore ever touches is now derived from the DB
// dump's memory_versions rows (memoryVersionShaSet), not from the tar
// entry name being walked. The archive-derived name is used only as a
// lookup KEY into that DB-sourced set; an entry whose name doesn't
// match a row that's actually being restored is skipped, whatever it's
// named.
//
// Two scenarios, matching the two ways a hostile bundle could try to
// abuse this code path:
//
//   - A tar entry name containing "..". This is ALREADY rejected —
//     before and after this fix — by ExtractPayload's own top-level
//     guard (restorer.go), which runs on every payload entry
//     regardless of section. RestoreMemoryBlobs's DB-membership gate
//     is a second, independent layer: even a validly-shaped sha256
//     name is worthless to an attacker if it doesn't match a row being
//     restored. Kept here as a regression pin, not a red-before-fix
//     case — see TestExtractPayload_RejectsTraversalEntryInMemoryBlobsSection.
//   - A tar entry named after a syntactically valid sha256 that no
//     memory_versions row references. THIS is the scenario the fix
//     actually changes: the pre-fix RestoreMemoryBlobs had no DB check
//     at all, so it wrote any hex-shaped entry straight into blobRoot
//     — an "orphan" blob nobody points at, but still successfully
//     smuggled into the content-addressed store by a crafted bundle.
//     Verified empirically against the pre-fix implementation (a
//     standalone reproduction of the old function body, run outside
//     this test suite): it wrote the orphan file every time. See
//     TestRestoreMemoryBlobs_SkipsEntryNotReferencedByDB below, which
//     is green against current code and would fail to compile against
//     the pre-fix 3-argument RestoreMemoryBlobs signature — the
//     expectedShas parameter IS the fix, not an incidental addition.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// buildMemoryBlobsPayload assembles a minimal payload tar.zst (the
// same shape CreateBackup produces, just without the DB-dump / crew
// sections this test doesn't need) containing one memory-blobs/ entry
// per (name, content) pair in entries, and returns the raw sealed
// bytes. name is the FULL entry name including the "memory-blobs/"
// prefix, exactly as it would appear in a real bundle's payload tar.
// Returning []byte (not the *bytes.Buffer) lets callers build a fresh
// bytes.Reader per ExtractPayload call — ExtractPayload consumes its
// input, so a single buffer can't be reused across two extractions of
// the same synthetic payload.
func buildMemoryBlobsPayload(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw, err := backup.NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("NewTarZstWriter: %v", err)
	}
	now := time.Now().UTC()
	for name, content := range entries {
		if err := tw.WriteFile(name, 0o600, now, content); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close payload writer: %v", err)
	}
	return buf.Bytes()
}

// sha256Hex returns the lowercase-hex sha256 digest of content, in the
// same shape RecordVersion produces.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// TestExtractPayload_RejectsTraversalEntryInMemoryBlobsSection proves
// the pre-existing, unmodified-by-this-fix defense: a tar entry name
// containing ".." anywhere under the memory-blobs/ prefix (or any
// other section) makes the WHOLE payload extraction fail, before
// RestoreMemoryBlobs ever runs. Confirmed to behave identically before
// and after the CodeQL-motivated fix — included as a regression pin,
// not a red-before-fix case.
func TestExtractPayload_RejectsTraversalEntryInMemoryBlobsSection(t *testing.T) {
	ctx := context.Background()
	payloadBytes := buildMemoryBlobsPayload(t, map[string][]byte{
		"memory-blobs/ev/../../evil": []byte("malicious payload"),
	})

	extracted, err := backup.ExtractPayload(ctx, bytes.NewReader(payloadBytes))
	if err == nil {
		if extracted != nil {
			_ = extracted.Close()
		}
		t.Fatal("ExtractPayload accepted a tar entry containing '..' — expected a hard rejection")
	}
	if extracted != nil {
		t.Errorf("ExtractPayload returned a non-nil ExtractedPayload alongside an error: %v", err)
	}

	// Nothing should exist anywhere resembling the traversal target —
	// belt-and-suspenders, since a rejected extraction should not have
	// written anything at all.
	if _, statErr := os.Stat(filepath.Join(os.TempDir(), "evil")); statErr == nil {
		t.Error("traversal target file exists on disk — extraction wrote outside its scratch directory")
	}
}

// TestRestoreMemoryBlobs_SkipsEntryNotReferencedByDB is the test that
// actually pins the CodeQL fix's behavior change: a memory-blobs
// section can carry a syntactically valid sha256-named entry that no
// memory_versions row references. RestoreMemoryBlobs must skip writing
// it — the write destination for every blob it lands is derived from
// expectedShas (DB-sourced), never from the archive entry name alone.
//
// A second, referenced entry in the SAME archive is asserted to land
// normally, so this test also proves the DB gate isn't overly strict —
// it discriminates, it doesn't just refuse everything.
func TestRestoreMemoryBlobs_SkipsEntryNotReferencedByDB(t *testing.T) {
	ctx := context.Background()

	referencedContent := []byte("this blob has a memory_versions row pointing at it")
	referencedSha := sha256Hex(referencedContent)

	orphanContent := []byte("this blob has NO memory_versions row pointing at it")
	orphanSha := sha256Hex(orphanContent)

	payloadBytes := buildMemoryBlobsPayload(t, map[string][]byte{
		"memory-blobs/" + referencedSha[:2] + "/" + referencedSha: referencedContent,
		"memory-blobs/" + orphanSha[:2] + "/" + orphanSha:         orphanContent,
	})

	extracted, err := backup.ExtractPayload(ctx, bytes.NewReader(payloadBytes))
	if err != nil {
		t.Fatalf("ExtractPayload: %v", err)
	}
	defer func() { _ = extracted.Close() }()

	blobRoot := t.TempDir()
	// expectedShas simulates memoryVersionShaSet's output for a DB dump
	// whose memory_versions rows reference ONLY referencedSha — the
	// orphanSha entry above rides the archive but nothing in the
	// (simulated) DB dump ever pointed at it.
	expectedShas := map[string]string{referencedSha: referencedSha}

	n, err := backup.RestoreMemoryBlobs(ctx, blobRoot, extracted, expectedShas)
	if err != nil {
		t.Fatalf("RestoreMemoryBlobs: %v", err)
	}
	if n != 1 {
		t.Errorf("RestoreMemoryBlobs wrote %d blob(s), want 1 (only the referenced one)", n)
	}

	referencedDst := filepath.Join(blobRoot, referencedSha[:2], referencedSha)
	got, err := os.ReadFile(referencedDst)
	if err != nil {
		t.Fatalf("referenced blob was not written to %s: %v", referencedDst, err)
	}
	if string(got) != string(referencedContent) {
		t.Errorf("referenced blob content = %q, want %q", got, referencedContent)
	}

	orphanDst := filepath.Join(blobRoot, orphanSha[:2], orphanSha)
	if _, statErr := os.Stat(orphanDst); !os.IsNotExist(statErr) {
		t.Errorf("orphan blob (not referenced by any memory_versions row) was written to %s (statErr=%v) — "+
			"a crafted bundle can smuggle an unreferenced blob into the store", orphanDst, statErr)
	}

	// Re-running restore with an EMPTY expectedShas (e.g. a workspace
	// with no memory_versions rows in this dump at all) must skip
	// everything, not fall back to "restore whatever the archive has".
	// The first ExtractedPayload's inner tar reader is already consumed,
	// so this re-extracts a fresh ExtractedPayload from the same bytes.
	extracted2, err := backup.ExtractPayload(ctx, bytes.NewReader(payloadBytes))
	if err != nil {
		t.Fatalf("re-ExtractPayload: %v", err)
	}
	defer func() { _ = extracted2.Close() }()

	blobRootEmpty := t.TempDir()
	n2, err := backup.RestoreMemoryBlobs(ctx, blobRootEmpty, extracted2, map[string]string{})
	if err != nil {
		t.Fatalf("RestoreMemoryBlobs with empty expectedShas: %v", err)
	}
	if n2 != 0 {
		t.Errorf("RestoreMemoryBlobs with empty expectedShas wrote %d blob(s), want 0", n2)
	}
}
