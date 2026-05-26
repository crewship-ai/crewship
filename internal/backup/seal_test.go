package backup

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"filippo.io/age"
)

// TestSealPayload_NoEncryptPath drives the cheap-write branch where
// payload bytes flow through hasher + counter without an AGE
// wrapper. Pin the SHA-256 + size accounting.
func TestSealPayload_NoEncryptPath(t *testing.T) {
	src := bytes.NewBufferString("hello world payload")
	dst := &bytes.Buffer{}
	sha, n, err := SealPayload(dst, src, WriteBundleOptions{NoEncrypt: true})
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	if !strings.HasPrefix(sha, "sha256:") {
		t.Errorf("hash should be sha256: prefixed, got %q", sha)
	}
	if n != int64(dst.Len()) {
		t.Errorf("counter %d != actual bytes %d", n, dst.Len())
	}
	if dst.String() != "hello world payload" {
		t.Errorf("NoEncrypt should not transform body, got %q", dst.String())
	}
}

func TestSealPayload_PassphrasePath(t *testing.T) {
	src := bytes.NewBufferString("secret data")
	dst := &bytes.Buffer{}
	sha, n, err := SealPayload(dst, src, WriteBundleOptions{Passphrase: "p4ssw0rd"})
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	if !strings.HasPrefix(sha, "sha256:") {
		t.Errorf("hash format unexpected: %q", sha)
	}
	if n <= int64(len("secret data")) {
		t.Errorf("encrypted size should exceed plaintext, got %d", n)
	}
	if dst.String() == "secret data" {
		t.Error("encrypted output should not equal plaintext")
	}
}

func TestSealPayload_RecipientsPath(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	src := bytes.NewBufferString("recipient encrypted")
	dst := &bytes.Buffer{}
	sha, n, err := SealPayload(dst, src, WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	if sha == "" || n == 0 {
		t.Errorf("got empty sha or zero bytes")
	}
}

func TestSealPayload_RejectsAmbiguousOptions(t *testing.T) {
	// Although CreateBackup pre-validates, SealPayload's switch hits
	// the default branch when NONE of the three modes is set.
	src := bytes.NewBufferString("x")
	dst := &bytes.Buffer{}
	_, _, err := SealPayload(dst, src, WriteBundleOptions{})
	if err == nil || !strings.Contains(err.Error(), "Recipients") {
		t.Errorf("expected 'Recipients/Passphrase/NoEncrypt required' error, got %v", err)
	}
}

// TestWriteBundleStream_RequiresNonNilManifest covers the up-front
// validation that prevents a corrupt bundle from being written.
func TestWriteBundleStream_RequiresNonNilManifest(t *testing.T) {
	err := WriteBundleStream(&bytes.Buffer{}, nil, &bytes.Buffer{}, 0)
	if err == nil || !strings.Contains(err.Error(), "manifest is nil") {
		t.Errorf("expected 'manifest is nil' error, got %v", err)
	}
}

func TestWriteBundleStream_AutoBumpsFormatVersion(t *testing.T) {
	// FormatVersion 0 should auto-bump to current FormatVersion.
	m := &Manifest{
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         nonZeroTime(),
		CreatedBy:         Actor{UserID: "u_test"},
		Checksums:         Checksums{PayloadSHA256: "sha256:abc"},
	}
	sealed := bytes.NewBufferString("body")
	out := &bytes.Buffer{}
	if err := WriteBundleStream(out, m, sealed, int64(sealed.Len())); err != nil {
		t.Fatalf("WriteBundleStream: %v", err)
	}
	if m.FormatVersion != FormatVersion {
		t.Errorf("FormatVersion should be bumped to %d, got %d", FormatVersion, m.FormatVersion)
	}
}

func TestWriteBundleStream_RejectsInvalidManifest(t *testing.T) {
	m := &Manifest{
		FormatVersion: 1,
		Scope:         "bogus",
	}
	err := WriteBundleStream(&bytes.Buffer{}, m, &bytes.Buffer{}, 0)
	if !errors.Is(err, ErrInvalidScope) {
		t.Errorf("expected ErrInvalidScope, got %v", err)
	}
}

// TestSealPayload_RoundTrip writes then reads the same payload via
// the production SealPayload + DecryptStreamPassphrase pair. Catches
// regressions where the writer-side encoding drifts from the
// reader-side.
func TestSealPayload_RoundTrip(t *testing.T) {
	const passphrase = "round-trip-seal-test"
	plain := []byte("round-trip payload contents that must come back exactly")

	sealed := &bytes.Buffer{}
	_, _, err := SealPayload(sealed, bytes.NewReader(plain),
		WriteBundleOptions{Passphrase: passphrase})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	r, err := DecryptStreamPassphrase(sealed, passphrase)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	got := &bytes.Buffer{}
	if _, err := got.ReadFrom(r); err != nil {
		t.Fatalf("read decrypted: %v", err)
	}
	if !bytes.Equal(got.Bytes(), plain) {
		t.Errorf("round-trip mismatch:\n  in=%q\n  out=%q", plain, got.Bytes())
	}
}

func TestSealPayload_DecryptWithWrongPassphraseFails(t *testing.T) {
	sealed := &bytes.Buffer{}
	_, _, err := SealPayload(sealed, bytes.NewBufferString("x"),
		WriteBundleOptions{Passphrase: "right"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := DecryptStreamPassphrase(sealed, "wrong"); err == nil {
		t.Error("decrypt with wrong passphrase should error")
	}
}
