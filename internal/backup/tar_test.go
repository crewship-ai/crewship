package backup

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestTarZstRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	entries := map[string][]byte{
		"MANIFEST.json":      []byte(`{"format_version":1}`),
		"payload/file-1.txt": []byte("hello"),
		"payload/file-2.txt": []byte("world with more text"),
	}
	modTime := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	for name, body := range entries {
		if err := w.WriteFile(name, 0o644, modTime, body); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	if buf.Len() == 0 {
		t.Fatal("tar.zst output is empty")
	}

	r, err := NewTarZstReader(&buf)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	defer func() { _ = r.Close() }()

	seen := map[string][]byte{}
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
			t.Fatalf("read body %q: %v", hdr.Name, err)
		}
		seen[hdr.Name] = body
	}

	if len(seen) != len(entries) {
		t.Errorf("got %d entries, want %d", len(seen), len(entries))
	}
	for name, want := range entries {
		got, ok := seen[name]
		if !ok {
			t.Errorf("missing entry %q", name)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("entry %q: got %q, want %q", name, got, want)
		}
	}
}

func TestTarZst_CorruptDetected(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewTarZstWriter(&buf)
	_ = w.WriteFile("a.txt", 0o644, time.Now(), []byte("hello"))
	_ = w.Close()

	b := buf.Bytes()
	// Corrupt several mid-stream bytes (zstd frame content).
	for i := len(b) / 3; i < len(b)/3+8 && i < len(b); i++ {
		b[i] ^= 0xFF
	}

	r, err := NewTarZstReader(bytes.NewReader(b))
	if err != nil {
		// Corruption detected at init — acceptable.
		return
	}
	defer func() { _ = r.Close() }()
	// Or during traversal.
	for {
		_, err := r.Next()
		if err == io.EOF {
			t.Error("expected error on corrupted stream, got clean EOF")
			break
		}
		if err != nil {
			return
		}
	}
}

func TestChecksum_Roundtrip(t *testing.T) {
	var sink bytes.Buffer
	hw := NewHashingWriter(&sink)
	data := []byte("the quick brown fox")
	if _, err := hw.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	sum := hw.Sum()
	if !bytes.Equal(sink.Bytes(), data) {
		t.Error("HashingWriter altered payload")
	}

	hr := NewHashingReader(bytes.NewReader(sink.Bytes()))
	out, err := io.ReadAll(hr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Error("HashingReader altered payload")
	}
	if err := VerifyChecksum(sum, hr.Sum()); err != nil {
		t.Errorf("checksum verify failed: %v", err)
	}
}

func TestChecksum_TamperDetected(t *testing.T) {
	hw := NewHashingWriter(io.Discard)
	_, _ = hw.Write([]byte("original"))
	expected := hw.Sum()

	hr := NewHashingReader(bytes.NewReader([]byte("tampered")))
	_, _ = io.ReadAll(hr)

	if err := VerifyChecksum(expected, hr.Sum()); err == nil {
		t.Error("expected checksum mismatch error")
	}
}

func TestChecksum_EmptyExpected(t *testing.T) {
	if err := VerifyChecksum("", "sha256:abc"); err == nil {
		t.Error("empty expected should error")
	}
}
