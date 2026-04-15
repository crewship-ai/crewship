package backup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestWriteReadBundle_Passphrase(t *testing.T) {
	const pass = "hunter2-is-taken"
	manifest := newValidManifest()
	// WriteBundle fills these in.
	manifest.Checksums = Checksums{}
	manifest.Encryption = Encryption{}

	payload := []byte("my secret workspace contents\nmulti-line\n")

	var out bytes.Buffer
	if err := WriteBundle(&out, manifest, bytes.NewReader(payload), WriteBundleOptions{
		Passphrase: pass,
	}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	read, sealed, err := ReadBundle(&out)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if !read.Encryption.Enabled {
		t.Error("expected Encryption.Enabled=true")
	}
	if read.Checksums.PayloadSHA256 == "" {
		t.Error("expected checksum to be populated")
	}

	// Verify checksum matches and decrypt.
	hr := NewHashingReader(sealed)
	dec, err := DecryptStreamPassphrase(hr, pass)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	recovered, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("read plaintext: %v", err)
	}
	if !bytes.Equal(recovered, payload) {
		t.Errorf("payload mismatch; got %q want %q", recovered, payload)
	}
	if err := VerifyChecksum(read.Checksums.PayloadSHA256, hr.Sum()); err != nil {
		t.Errorf("checksum: %v", err)
	}
}

func TestWriteReadBundle_Recipient(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("gen identity: %v", err)
	}

	manifest := newValidManifest()
	manifest.Checksums = Checksums{}
	manifest.Encryption = Encryption{}

	payload := []byte("asymmetric-target payload")

	var out bytes.Buffer
	if err := WriteBundle(&out, manifest, bytes.NewReader(payload), WriteBundleOptions{
		Recipients: []age.Recipient{id.Recipient()},
	}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	read, sealed, err := ReadBundle(&out)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if len(read.Encryption.Recipients) != 1 {
		t.Errorf("expected 1 recipient recorded, got %d", len(read.Encryption.Recipients))
	}
	if !strings.HasPrefix(read.Encryption.Recipients[0], "age1") {
		t.Errorf("recipient string should begin with age1, got %q", read.Encryption.Recipients[0])
	}

	dec, err := DecryptStream(sealed, id)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	recovered, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("read plaintext: %v", err)
	}
	if !bytes.Equal(recovered, payload) {
		t.Error("payload mismatch")
	}
}

func TestWriteReadBundle_NoEncrypt(t *testing.T) {
	manifest := newValidManifest()
	manifest.Checksums = Checksums{}
	manifest.Encryption = Encryption{}

	payload := []byte("plaintext; use at your own risk")

	var out bytes.Buffer
	if err := WriteBundle(&out, manifest, bytes.NewReader(payload), WriteBundleOptions{
		NoEncrypt: true,
	}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	read, sealed, err := ReadBundle(&out)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if read.Encryption.Enabled {
		t.Error("expected Encryption.Enabled=false")
	}

	got, err := io.ReadAll(sealed)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("payload mismatch")
	}
}

func TestWriteBundle_NoOptions_Errors(t *testing.T) {
	m := newValidManifest()
	err := WriteBundle(&bytes.Buffer{}, m, bytes.NewReader([]byte("x")), WriteBundleOptions{})
	if err == nil {
		t.Error("expected error when no encryption option supplied")
	}
}

func TestReadBundle_IncompatibleFormat(t *testing.T) {
	m := newValidManifest()
	m.FormatVersion = FormatVersion + 10
	m.Checksums = Checksums{}
	m.Encryption = Encryption{}

	var out bytes.Buffer
	if err := WriteBundle(&out, m, bytes.NewReader([]byte("x")), WriteBundleOptions{
		NoEncrypt: true,
	}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	_, _, err := ReadBundle(&out)
	if !errors.Is(err, ErrFormatTooNew) {
		t.Errorf("expected ErrFormatTooNew, got %v", err)
	}
}

func TestReadBundle_UnknownEntryIgnored(t *testing.T) {
	// Forward-compat: future writers may add entries in the outer tar;
	// readers tolerate them. Build a bundle the normal way, then append
	// a spurious entry by re-tar'ing.
	m := newValidManifest()
	m.Checksums = Checksums{}
	m.Encryption = Encryption{}

	var out bytes.Buffer
	if err := WriteBundle(&out, m, bytes.NewReader([]byte("x")), WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	// We cannot easily append to a zstd stream; instead rebuild by
	// decompressing + re-taring with an extra file.
	r, err := NewTarZstReader(&out)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	var rebuilt bytes.Buffer
	w, _ := NewTarZstWriter(&rebuilt)
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("body: %v", err)
		}
		if err := w.WriteFile(hdr.Name, hdr.Mode, hdr.ModTime, body); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Add a future-format marker file.
	if err := w.WriteFile("V2-PROVENANCE.json", 0o644, m.CreatedAt, []byte(`{"v":2}`)); err != nil {
		t.Fatalf("write unknown: %v", err)
	}
	_ = w.Close()
	_ = r.Close()

	if _, _, err := ReadBundle(&rebuilt); err != nil {
		t.Errorf("reader should tolerate unknown entries, got %v", err)
	}
}
