package backup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// Bundle streaming round-trip: WriteBundleStream → ReadBundleStream
// covers ~75% of bundle.go between the two functions including all
// header / payload / readme paths.

func TestWriteAndReadBundleStream_RoundTrip(t *testing.T) {
	m := &Manifest{
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         nonZeroTime(),
		CreatedBy:         Actor{UserID: "u_test", Email: "rt@test", Role: "OWNER"},
		Encryption:        Encryption{Enabled: false},
		Checksums:         Checksums{PayloadSHA256: "sha256:deadbeef"},
		Contents: Contents{
			Workspace: &WorkspaceSummary{ID: "ws_rt", Slug: "rt", Name: "RT"},
		},
	}
	sealed := []byte("payload bytes for round trip")
	sink := &bytes.Buffer{}
	if err := WriteBundleStream(sink, m, bytes.NewReader(sealed), int64(len(sealed))); err != nil {
		t.Fatalf("write: %v", err)
	}

	gotManifest, payloadReader, closer, err := ReadBundleStream(sink)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer closer()
	if gotManifest.FormatVersion != FormatVersion {
		t.Errorf("format version drift: %d vs %d", gotManifest.FormatVersion, FormatVersion)
	}
	if gotManifest.Contents.Workspace == nil || gotManifest.Contents.Workspace.Slug != "rt" {
		t.Errorf("workspace summary missing in round-tripped manifest")
	}
	out, err := io.ReadAll(payloadReader)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !bytes.Equal(out, sealed) {
		t.Errorf("payload drift after round-trip:\n  in=%q\n  out=%q", sealed, out)
	}
}

// TestReadBundleStream_RejectsMalformed catches a bundle that isn't
// a valid tar.zst at all (e.g. truncated or random bytes).
func TestReadBundleStream_RejectsMalformed(t *testing.T) {
	garbage := bytes.NewBufferString("not a tar.zst file at all")
	_, _, _, err := ReadBundleStream(garbage)
	if err == nil {
		t.Error("garbage input should produce an error")
	}
}

// TestReadBundleStream_RejectsMissingManifest builds a tar.zst bundle
// that has a payload entry but no MANIFEST.json. The reader must
// detect the missing manifest after the stream ends rather than
// returning nil and crashing the caller downstream.
func TestReadBundleStream_RejectsMissingManifest(t *testing.T) {
	buf := &bytes.Buffer{}
	tw, err := NewTarZstWriter(buf)
	if err != nil {
		t.Fatalf("tar writer: %v", err)
	}
	// Write only the payload entry — no manifest.
	if err := tw.WriteFile(payloadFileName, 0o600, nonZeroTime(), []byte("payload-only")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, _, closer, err := ReadBundleStream(buf)
	if closer != nil {
		_ = closer()
	}
	if err == nil {
		t.Error("missing manifest should produce an error")
	}
}

// TestReadBundle_PayloadTooLargeBoundedReader covers the size-cap
// guard against tar-bomb manifest claims. A bundle that claims a
// 10 GB manifest must be bounded by maxBackupManifestBytes so the
// reader's io.ReadAll doesn't allocate forever.
//
// We can't easily construct a bundle that LIES about its tar header
// size from clean Go code (the writer correctly emits Size = body
// length). But we can validate that the constant is in a sane range
// for the documented threat model.
func TestMaxBackupManifestBytes_IsSensibleCap(t *testing.T) {
	if maxBackupManifestBytes <= 0 {
		t.Errorf("manifest cap must be positive, got %d", maxBackupManifestBytes)
	}
	if maxBackupManifestBytes >= 100<<20 {
		t.Errorf("manifest cap unreasonably high (>=100MB): %d", maxBackupManifestBytes)
	}
}

// === Inspect / Verify smoke ===

func TestInspect_RejectsMissingFile(t *testing.T) {
	_, err := Inspect(t.Context(), t.TempDir()+"/does-not-exist.tar.zst")
	if err == nil {
		t.Error("missing file should produce an error")
	}
}

// === WriteBundle (non-streaming) smoke ===

func TestWriteBundle_RoundTripSmall(t *testing.T) {
	m := &Manifest{
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         nonZeroTime(),
		CreatedBy:         Actor{UserID: "u_test"},
		Encryption:        Encryption{Enabled: false},
		Checksums:         Checksums{PayloadSHA256: ""}, // backfilled by WriteBundle
	}
	out := &bytes.Buffer{}
	if err := WriteBundle(out, m, bytes.NewReader([]byte("small payload")),
		WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	if out.Len() == 0 {
		t.Error("WriteBundle produced empty output")
	}
}

func TestWriteBundle_NilManifestError(t *testing.T) {
	err := WriteBundle(&bytes.Buffer{}, nil, &bytes.Buffer{}, WriteBundleOptions{NoEncrypt: true})
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Errorf("nil manifest should error, got %v", err)
	}
}

// === Checksum verify edge cases ===

func TestVerifyChecksum_MismatchReturnsInvalidChecksum(t *testing.T) {
	err := VerifyChecksum("sha256:deadbeef", "sha256:cafe")
	if !errors.Is(err, ErrInvalidChecksum) {
		t.Errorf("expected ErrInvalidChecksum, got %v", err)
	}
}

func TestVerifyChecksum_MatchIsNoError(t *testing.T) {
	if err := VerifyChecksum("sha256:abc", "sha256:abc"); err != nil {
		t.Errorf("matching checksums should not error, got %v", err)
	}
}
