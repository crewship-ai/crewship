package backup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStagingCipher_RoundTripTamperTruncate(t *testing.T) {
	sc, err := newStagingCipher()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := []byte("PLAINTEXT-MARKER-") // repeated so a leak cannot hide in noise
	for _, size := range []int{0, 1, stagingChunk - 1, stagingChunk, stagingChunk + 1, 3*stagingChunk + 17} {
		plain := bytes.Repeat(marker, size/len(marker)+1)[:size]
		path := filepath.Join(dir, "f")
		_ = os.Remove(path)
		w, err := sc.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		// Uneven writes cross chunk borders.
		for rest := plain; len(rest) > 0; {
			n := min(len(rest), 7000)
			if _, err := w.Write(rest[:n]); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(path)
		if size >= len(marker) && bytes.Contains(raw, marker) {
			t.Fatalf("size %d: staged file holds the plaintext", size)
		}
		r, n, err := sc.Open(path)
		if err != nil || n != int64(size) {
			t.Fatalf("size %d: open = %d, %v", size, n, err)
		}
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip = %d bytes, %v", size, len(got), err)
		}

		if len(raw) <= len(stagingMagic)+stagingPrefixSize {
			continue
		}
		// Any flipped byte fails the read.
		bad := append([]byte(nil), raw...)
		bad[len(bad)-1] ^= 1
		if err := os.WriteFile(path, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readStaged(sc, path); !errors.Is(err, errStagingCorrupt) {
			t.Fatalf("size %d: tampered file read: %v", size, err)
		}
		// Dropping the final chunk fails too, never a silently short file.
		if size > stagingChunk {
			frame := stagingChunk + sc.aead.Overhead()
			if err := os.WriteFile(path, raw[:len(stagingMagic)+stagingPrefixSize+frame], 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readStaged(sc, path); !errors.Is(err, errStagingCorrupt) {
				t.Fatalf("size %d: truncated file read: %v", size, err)
			}
		}
	}
	// A file from another run (another key) does not open.
	other, _ := newStagingCipher()
	path := filepath.Join(dir, "g")
	w, _ := other.Create(path)
	_, _ = w.Write([]byte("x"))
	_ = w.Close()
	sc.sizes[path] = 1
	if _, err := readStaged(sc, path); !errors.Is(err, errStagingCorrupt) {
		t.Fatalf("another run's file opened: %v", err)
	}
}

func readStaged(sc *stagingCipher, path string) ([]byte, error) {
	r, _, err := sc.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// The encrypted copy must not make the quiet window much longer: copying a
// store through the staging cipher costs about what a plain copy does.
// Wall-clock ratios are noise on shared CI runners, so this is a pair of
// benchmarks rather than an asserting test; compare them with
//
//	go test ./internal/backup -run '^$' -bench 'StagingCopy' -benchtime 5x
func stagingCopyFixture(b *testing.B) string {
	b.Helper()
	src := b.TempDir()
	buf := make([]byte, 256<<10)
	for i := 0; i < 64; i++ { // 16 MiB in 64 files
		_, _ = rand.Read(buf)
		if err := os.WriteFile(filepath.Join(src, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), buf, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	return src
}

// BenchmarkStagingCopyPlain is the baseline: a hashed plaintext copy, as
// the index hashes every file.
func BenchmarkStagingCopyPlain(b *testing.B) {
	src := stagingCopyFixture(b)
	b.SetBytes(16 << 20)
	for b.Loop() {
		dst := b.TempDir()
		entries, _ := os.ReadDir(src)
		for _, e := range entries {
			in, _ := os.Open(filepath.Join(src, e.Name()))
			out, _ := os.Create(filepath.Join(dst, e.Name()))
			h := sha256.New()
			_, _ = io.Copy(io.MultiWriter(out, h), in)
			_ = in.Close()
			_ = out.Close()
		}
	}
}

// BenchmarkStagingCopyEncrypted is the same store through the staging cipher.
func BenchmarkStagingCopyEncrypted(b *testing.B) {
	src := stagingCopyFixture(b)
	sc, err := newStagingCipher()
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(16 << 20)
	for b.Loop() {
		if _, _, _, err := copyTree(b.Context(), src, b.TempDir(), nil, sc); err != nil {
			b.Fatal(err)
		}
	}
}
