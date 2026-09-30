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
	"time"
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
func TestStagingCipher_CopyCostIsComparable(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	src := t.TempDir()
	buf := make([]byte, 256<<10)
	for i := 0; i < 64; i++ { // 16 MiB in 64 files
		_, _ = rand.Read(buf)
		if err := os.WriteFile(filepath.Join(src, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), buf, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plainCopy := func(dst string) {
		entries, _ := os.ReadDir(src)
		for _, e := range entries {
			in, _ := os.Open(filepath.Join(src, e.Name()))
			out, _ := os.Create(filepath.Join(dst, e.Name()))
			h := sha256.New() // the index hashes every file, before and after
			_, _ = io.Copy(io.MultiWriter(out, h), in)
			_ = in.Close()
			_ = out.Close()
		}
	}
	best := func(f func(dst string)) int64 {
		var b int64 = 1 << 62
		for i := 0; i < 3; i++ {
			dst := t.TempDir()
			start := nowMono()
			f(dst)
			if d := nowMono() - start; d < b {
				b = d
			}
		}
		return b
	}
	plain := best(plainCopy)
	sc, _ := newStagingCipher()
	enc := best(func(dst string) {
		if _, _, err := copyTree(t.Context(), src, dst, nil, sc); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("16 MiB: hashed plaintext copy (before) %.1f ms, hashed encrypted copy (now) %.1f ms", float64(plain)/1e6, float64(enc)/1e6)
	// Generous, because CI machines are noisy.
	if enc > 3*plain+int64(200e6) {
		t.Fatalf("encrypted staging copy %.1f ms vs plain %.1f ms", float64(enc)/1e6, float64(plain)/1e6)
	}
}

var monoStart = time.Now()

func nowMono() int64 { return int64(time.Since(monoStart)) }
