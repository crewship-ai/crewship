package backup

// Coverage tests for bundle.go — SealPayload modes, the streaming
// reader/writer pair, WriteBundle/ReadBundle error + encryption
// branches, and recipientString.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// minValidManifest returns a manifest that passes Validate once a
// checksum is present.
func minValidManifest(scope Scope, sha string) *Manifest {
	return &Manifest{
		FormatVersion:     FormatVersion,
		Scope:             scope,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		CreatedBy:         Actor{UserID: "u_cov"},
		Checksums:         Checksums{PayloadSHA256: sha},
	}
}

func TestSealPayload_NoEncrypt(t *testing.T) {
	var dst bytes.Buffer
	payload := []byte("plaintext payload bytes")
	sha, n, err := SealPayload(&dst, bytes.NewReader(payload), WriteBundleOptions{NoEncrypt: true})
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("n = %d, want %d", n, len(payload))
	}
	if dst.String() != string(payload) {
		t.Errorf("dst = %q, want passthrough", dst.String())
	}
	want := sha256.Sum256(payload)
	if sha != "sha256:"+hex.EncodeToString(want[:]) {
		t.Errorf("sha = %s, want sha256:%s", sha, hex.EncodeToString(want[:]))
	}
}

func TestSealPayload_Recipients_RoundTrip(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var dst bytes.Buffer
	payload := []byte("asymmetric secret payload")
	sha, n, err := SealPayload(&dst, bytes.NewReader(payload), WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	if n != int64(dst.Len()) || n == 0 {
		t.Errorf("n = %d, dst len %d", n, dst.Len())
	}
	sum := sha256.Sum256(dst.Bytes())
	if sha != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("sha must hash the SEALED bytes")
	}
	r, err := DecryptStream(bytes.NewReader(dst.Bytes()), id)
	if err != nil {
		t.Fatalf("DecryptStream: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("decrypted = %q (%v), want %q", got, err, payload)
	}
}

func TestSealPayload_Passphrase_RoundTrip(t *testing.T) {
	var dst bytes.Buffer
	payload := []byte("passphrase secret payload")
	sha, _, err := SealPayload(&dst, bytes.NewReader(payload), WriteBundleOptions{Passphrase: "open sesame"})
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	if sha == "" {
		t.Error("empty sha")
	}
	r, err := DecryptStreamPassphrase(bytes.NewReader(dst.Bytes()), "open sesame")
	if err != nil {
		t.Fatalf("DecryptStreamPassphrase: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("decrypted = %q (%v)", got, err)
	}
}

func TestSealPayload_Errors(t *testing.T) {
	t.Run("no mode selected", func(t *testing.T) {
		_, _, err := SealPayload(io.Discard, strings.NewReader("x"), WriteBundleOptions{})
		if err == nil || !strings.Contains(err.Error(), "requires Recipients, Passphrase, or NoEncrypt") {
			t.Fatalf("err = %v", err)
		}
	})
	boom := errors.New("src torn")
	t.Run("plaintext copy error", func(t *testing.T) {
		_, _, err := SealPayload(io.Discard, errReaderCov{boom}, WriteBundleOptions{NoEncrypt: true})
		if err == nil || !strings.Contains(err.Error(), "copy plaintext payload") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("recipients copy error", func(t *testing.T) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = SealPayload(io.Discard, errReaderCov{boom}, WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
		if err == nil || !strings.Contains(err.Error(), "copy encrypted payload") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("passphrase copy error", func(t *testing.T) {
		_, _, err := SealPayload(io.Discard, errReaderCov{boom}, WriteBundleOptions{Passphrase: "p"})
		if err == nil || !strings.Contains(err.Error(), "copy encrypted payload") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWriteBundleStream_Branches(t *testing.T) {
	t.Run("nil manifest", func(t *testing.T) {
		err := WriteBundleStream(io.Discard, nil, strings.NewReader(""), 0)
		if err == nil || !strings.Contains(err.Error(), "manifest is nil") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("invalid manifest", func(t *testing.T) {
		m := minValidManifest(ScopeWorkspace, "abc")
		m.CreatedBy.UserID = "" // breaks Validate
		err := WriteBundleStream(io.Discard, m, strings.NewReader(""), 0)
		if err == nil || !strings.Contains(err.Error(), "manifest validate") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("format version defaulted and round-trips", func(t *testing.T) {
		sealed := []byte("sealed-bytes")
		m := minValidManifest(ScopeWorkspace, "deadbeef")
		m.FormatVersion = 0 // must default to FormatVersion
		var sink bytes.Buffer
		if err := WriteBundleStream(&sink, m, bytes.NewReader(sealed), int64(len(sealed))); err != nil {
			t.Fatalf("WriteBundleStream: %v", err)
		}
		gotM, payload, closeFn, err := ReadBundleStream(bytes.NewReader(sink.Bytes()))
		if err != nil {
			t.Fatalf("ReadBundleStream: %v", err)
		}
		defer func() { _ = closeFn() }()
		if gotM.FormatVersion != FormatVersion {
			t.Errorf("FormatVersion = %d, want %d", gotM.FormatVersion, FormatVersion)
		}
		body, err := io.ReadAll(payload)
		if err != nil || !bytes.Equal(body, sealed) {
			t.Fatalf("payload = %q (%v), want %q", body, err, sealed)
		}
	})
}

// buildOuterTarZst assembles a raw outer bundle from explicit entries so
// the reader's malformed-layout branches can be probed.
func buildOuterTarZst(t *testing.T, entries []struct {
	name string
	body []byte
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, e := range entries {
		if err := tw.WriteFile(e.name, 0o644, now, e.body); err != nil {
			t.Fatalf("write %s: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func validManifestJSON(t *testing.T, mutate func(*Manifest)) []byte {
	t.Helper()
	m := minValidManifest(ScopeWorkspace, "cafe")
	if mutate != nil {
		mutate(m)
	}
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadBundleStream_ErrorBranches(t *testing.T) {
	type entry = struct {
		name string
		body []byte
	}

	t.Run("not a zst stream", func(t *testing.T) {
		_, _, _, err := ReadBundleStream(strings.NewReader("garbage that is definitely not zstd"))
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("payload before manifest", func(t *testing.T) {
		raw := buildOuterTarZst(t, []entry{{payloadFileName, []byte("p")}})
		_, _, _, err := ReadBundleStream(bytes.NewReader(raw))
		if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "before MANIFEST.json") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("manifest missing entirely", func(t *testing.T) {
		raw := buildOuterTarZst(t, []entry{{restoreReadmeName, []byte("hi")}, {"stray.bin", []byte("z")}})
		_, _, _, err := ReadBundleStream(bytes.NewReader(raw))
		if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "MANIFEST.json missing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("payload missing", func(t *testing.T) {
		raw := buildOuterTarZst(t, []entry{{manifestFileName, validManifestJSON(t, nil)}})
		_, _, _, err := ReadBundleStream(bytes.NewReader(raw))
		if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "payload missing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("malformed manifest JSON", func(t *testing.T) {
		raw := buildOuterTarZst(t, []entry{{manifestFileName, []byte("{not json")}})
		_, _, _, err := ReadBundleStream(bytes.NewReader(raw))
		if !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("format too new returns manifest plus error", func(t *testing.T) {
		mj := validManifestJSON(t, func(m *Manifest) { m.FormatVersion = FormatVersion + 1 })
		raw := buildOuterTarZst(t, []entry{{manifestFileName, mj}, {payloadFileName, []byte("p")}})
		m, payload, closeFn, err := ReadBundleStream(bytes.NewReader(raw))
		if !errors.Is(err, ErrFormatTooNew) {
			t.Fatalf("err = %v, want ErrFormatTooNew", err)
		}
		if m == nil || m.FormatVersion != FormatVersion+1 {
			t.Errorf("manifest must be returned for diagnostics, got %+v", m)
		}
		if payload != nil || closeFn != nil {
			t.Errorf("payload reader must not be handed out on incompatible bundles")
		}
	})
	t.Run("unknown entries are skipped", func(t *testing.T) {
		raw := buildOuterTarZst(t, []entry{
			{"future-section.bin", []byte("???")},
			{manifestFileName, validManifestJSON(t, nil)},
			{restoreReadmeName, []byte(RestoreReadmeContent)},
			{payloadFileName, []byte("sealed!")},
		})
		m, payload, closeFn, err := ReadBundleStream(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("ReadBundleStream: %v", err)
		}
		defer func() { _ = closeFn() }()
		if m.Checksums.PayloadSHA256 != "cafe" {
			t.Errorf("manifest sha = %q", m.Checksums.PayloadSHA256)
		}
		body, _ := io.ReadAll(payload)
		if string(body) != "sealed!" {
			t.Errorf("payload = %q", body)
		}
	})
}

func TestWriteBundle_Branches(t *testing.T) {
	t.Run("nil manifest", func(t *testing.T) {
		err := WriteBundle(io.Discard, nil, strings.NewReader(""), WriteBundleOptions{NoEncrypt: true})
		if err == nil || !strings.Contains(err.Error(), "manifest is nil") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no encryption mode", func(t *testing.T) {
		m := minValidManifest(ScopeWorkspace, "")
		err := WriteBundle(io.Discard, m, strings.NewReader("x"), WriteBundleOptions{})
		if err == nil || !strings.Contains(err.Error(), "requires Recipients, Passphrase, or NoEncrypt") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("validate failure after seal", func(t *testing.T) {
		m := minValidManifest(ScopeWorkspace, "")
		m.CreatedBy.UserID = ""
		err := WriteBundle(io.Discard, m, strings.NewReader("x"), WriteBundleOptions{NoEncrypt: true})
		if err == nil || !strings.Contains(err.Error(), "manifest validate") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("plaintext copy error", func(t *testing.T) {
		m := minValidManifest(ScopeWorkspace, "")
		err := WriteBundle(io.Discard, m, errReaderCov{errors.New("torn")}, WriteBundleOptions{NoEncrypt: true})
		if err == nil || !strings.Contains(err.Error(), "copy plaintext payload") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("recipients mode records recipient strings", func(t *testing.T) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		m := minValidManifest(ScopeWorkspace, "")
		m.FormatVersion = 0
		var sink bytes.Buffer
		if err := WriteBundle(&sink, m, strings.NewReader("payload"), WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}}); err != nil {
			t.Fatalf("WriteBundle: %v", err)
		}
		if !m.Encryption.Enabled || m.Encryption.Algorithm != EncryptionAlgorithm {
			t.Errorf("encryption block = %+v", m.Encryption)
		}
		if len(m.Encryption.Recipients) != 1 || !strings.HasPrefix(m.Encryption.Recipients[0], "age1") {
			t.Errorf("recipients = %v", m.Encryption.Recipients)
		}
		// Round-trip through ReadBundle + decrypt.
		gotM, payload, err := ReadBundle(bytes.NewReader(sink.Bytes()))
		if err != nil {
			t.Fatalf("ReadBundle: %v", err)
		}
		if gotM.Checksums.PayloadSHA256 == "" {
			t.Error("checksum not stamped")
		}
		dec, err := DecryptStream(payload, id)
		if err != nil {
			t.Fatalf("DecryptStream: %v", err)
		}
		body, _ := io.ReadAll(dec)
		if string(body) != "payload" {
			t.Errorf("decrypted = %q", body)
		}
	})
	t.Run("passphrase mode sets scrypt KDF", func(t *testing.T) {
		m := minValidManifest(ScopeWorkspace, "")
		var sink bytes.Buffer
		if err := WriteBundle(&sink, m, strings.NewReader("pp-payload"), WriteBundleOptions{Passphrase: "hunter2"}); err != nil {
			t.Fatalf("WriteBundle: %v", err)
		}
		if m.Encryption.KeyDerivation != "scrypt" || !m.Encryption.Enabled {
			t.Errorf("encryption = %+v", m.Encryption)
		}
		_, payload, err := ReadBundle(bytes.NewReader(sink.Bytes()))
		if err != nil {
			t.Fatalf("ReadBundle: %v", err)
		}
		dec, err := DecryptStreamPassphrase(payload, "hunter2")
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		body, _ := io.ReadAll(dec)
		if string(body) != "pp-payload" {
			t.Errorf("decrypted = %q", body)
		}
	})
}

func TestReadBundle_ErrorBranches(t *testing.T) {
	type entry = struct {
		name string
		body []byte
	}
	t.Run("corrupt stream", func(t *testing.T) {
		_, _, err := ReadBundle(strings.NewReader("definitely not zstd content here"))
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("manifest missing", func(t *testing.T) {
		raw := buildOuterTarZst(t, []entry{{payloadFileName, []byte("p")}, {"junk", []byte("j")}, {restoreReadmeName, []byte("r")}})
		_, _, err := ReadBundle(bytes.NewReader(raw))
		if !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "MANIFEST.json missing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("format too new returns manifest and error", func(t *testing.T) {
		mj := validManifestJSON(t, func(m *Manifest) { m.FormatVersion = FormatVersion + 5 })
		raw := buildOuterTarZst(t, []entry{{manifestFileName, mj}, {payloadFileName, []byte("p")}})
		m, payload, err := ReadBundle(bytes.NewReader(raw))
		if !errors.Is(err, ErrFormatTooNew) {
			t.Fatalf("err = %v", err)
		}
		if m == nil {
			t.Error("manifest must still be returned")
		}
		if payload != nil {
			t.Error("payload must be nil on incompatible bundle")
		}
	})
}

func TestWriteBundle_EncryptedCopyErrors(t *testing.T) {
	boom := errors.New("payload source torn")
	t.Run("recipients mode", func(t *testing.T) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		m := minValidManifest(ScopeWorkspace, "")
		err = WriteBundle(io.Discard, m, errReaderCov{boom}, WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
		if err == nil || !strings.Contains(err.Error(), "copy encrypted payload") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("passphrase mode", func(t *testing.T) {
		m := minValidManifest(ScopeWorkspace, "")
		err := WriteBundle(io.Discard, m, errReaderCov{boom}, WriteBundleOptions{Passphrase: "p"})
		if err == nil || !strings.Contains(err.Error(), "copy encrypted payload") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestBundleReaders_TruncatedStream cuts a valid bundle in half so the
// outer tar reader fails mid-iteration ("read bundle entry") in both
// the buffered and streaming readers.
func TestBundleReaders_TruncatedStream(t *testing.T) {
	var full bytes.Buffer
	m := minValidManifest(ScopeWorkspace, "")
	if err := WriteBundle(&full, m, strings.NewReader(strings.Repeat("payload-", 4096)), WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatal(err)
	}
	cut := full.Bytes()[:full.Len()/2]

	if _, _, err := ReadBundle(bytes.NewReader(cut)); err == nil {
		t.Error("ReadBundle on truncated stream must error")
	}
	if _, _, _, err := ReadBundleStream(bytes.NewReader(cut)); err == nil {
		t.Error("ReadBundleStream on truncated stream must error")
	}
}

// failingSink errors once more than `limit` bytes have been written —
// incompressible payloads force the zstd encoder to flush early enough
// for the writer-side error branches to fire.
type failingSink struct {
	n     int
	limit int
}

func (f *failingSink) Write(p []byte) (int, error) {
	f.n += len(p)
	if f.n > f.limit {
		return 0, errors.New("sink full")
	}
	return len(p), nil
}

// incompressible returns deterministic pseudo-random bytes that zstd
// cannot shrink, so writes propagate to the sink promptly.
func incompressible(n int) []byte {
	out := make([]byte, n)
	state := uint64(0x9e3779b97f4a7c15)
	for i := range out {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		out[i] = byte(state)
	}
	return out
}

func TestWriteBundleStream_SinkWriteError(t *testing.T) {
	m := minValidManifest(ScopeWorkspace, "deadbeef")
	payload := incompressible(4 << 20)
	sink := &failingSink{limit: 1 << 10}
	err := WriteBundleStream(sink, m, bytes.NewReader(payload), int64(len(payload)))
	if err == nil {
		t.Fatal("expected sink write failure to propagate")
	}
}

func TestWriteBundle_SinkWriteError(t *testing.T) {
	m := minValidManifest(ScopeWorkspace, "")
	payload := incompressible(4 << 20)
	sink := &failingSink{limit: 1 << 10}
	err := WriteBundle(sink, m, bytes.NewReader(payload), WriteBundleOptions{NoEncrypt: true})
	if err == nil {
		t.Fatal("expected sink write failure to propagate")
	}
}

func TestRecipientString(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if got := recipientString(id.Recipient()); !strings.HasPrefix(got, "age1") {
		t.Errorf("X25519 recipient string = %q, want age1… public key", got)
	}
	// Non-Stringer falls back to the %T type name.
	if got := recipientString(plainRecipientCov{}); got != fmt.Sprintf("%T", plainRecipientCov{}) {
		t.Errorf("fallback = %q", got)
	}
}

// plainRecipientCov implements age.Recipient without fmt.Stringer.
type plainRecipientCov struct{}

func (plainRecipientCov) Wrap(fileKey []byte) ([]*age.Stanza, error) {
	return nil, errors.New("not used")
}
