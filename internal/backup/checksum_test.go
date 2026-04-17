package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// sha256Tag returns the "sha256:<hex>" digest for the given input — the
// same form HashingWriter.Sum / HashingReader.Sum emit.
func sha256Tag(data []byte) string {
	sum := sha256.Sum256(data)
	return ChecksumPrefix + hex.EncodeToString(sum[:])
}

func TestChecksumPrefix(t *testing.T) {
	t.Parallel()
	// The prefix is part of the on-disk manifest format; changing it would
	// invalidate every recorded checksum.
	if ChecksumPrefix != "sha256:" {
		t.Errorf("ChecksumPrefix: got %q, want %q", ChecksumPrefix, "sha256:")
	}
}

func TestHashingWriter_ForwardsAndHashes(t *testing.T) {
	t.Parallel()

	var inner bytes.Buffer
	w := NewHashingWriter(&inner)

	payload := []byte("hello world")
	n, err := w.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Errorf("written bytes: got %d, want %d", n, len(payload))
	}
	if inner.String() != "hello world" {
		t.Errorf("inner buffer: got %q", inner.String())
	}
	if got, want := w.Sum(), sha256Tag(payload); got != want {
		t.Errorf("Sum: got %q, want %q", got, want)
	}
}

func TestHashingWriter_EmptyInputProducesEmptyHash(t *testing.T) {
	t.Parallel()

	w := NewHashingWriter(io.Discard)
	if got, want := w.Sum(), sha256Tag(nil); got != want {
		t.Errorf("empty Sum: got %q, want %q", got, want)
	}
	// Canonical empty SHA-256 digest — hard-coded to catch any change to the
	// hash algorithm or prefix.
	const canonical = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if w.Sum() != canonical {
		t.Errorf("empty Sum differs from canonical SHA-256(''); got %q", w.Sum())
	}
}

// TestHashingWriter_ChunkedWritesMatchSingleWrite proves the hasher treats
// multi-chunk streaming identically to a single Write — a silent regression
// here would corrupt manifest checksums for any non-trivial payload.
func TestHashingWriter_ChunkedWritesMatchSingleWrite(t *testing.T) {
	t.Parallel()

	full := []byte("abcdefghijklmnopqrstuvwxyz0123456789")

	single := NewHashingWriter(io.Discard)
	_, _ = single.Write(full)

	chunked := NewHashingWriter(io.Discard)
	for i := 0; i < len(full); i += 4 {
		end := i + 4
		if end > len(full) {
			end = len(full)
		}
		_, _ = chunked.Write(full[i:end])
	}

	if single.Sum() != chunked.Sum() {
		t.Errorf("chunked Sum differs from single Sum: %q vs %q", chunked.Sum(), single.Sum())
	}
}

// shortWriter simulates a writer that accepts only the first N bytes per
// Write call (mirrors how net.Conn behaves under backpressure). The
// HashingWriter must only hash the bytes that were actually written.
type shortWriter struct {
	max int
	buf bytes.Buffer
}

func (s *shortWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n > s.max {
		n = s.max
	}
	_, _ = s.buf.Write(p[:n])
	return n, nil
}

func TestHashingWriter_OnlyHashesBytesAcceptedByInner(t *testing.T) {
	t.Parallel()

	inner := &shortWriter{max: 5}
	w := NewHashingWriter(inner)

	payload := []byte("hello world") // 11 bytes
	n, err := w.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("returned n: got %d, want 5", n)
	}

	// Sum must match "hello" (the 5 bytes inner actually accepted),
	// not the full payload.
	if got, want := w.Sum(), sha256Tag([]byte("hello")); got != want {
		t.Errorf("Sum: got %q, want %q", got, want)
	}
}

// errorWriter returns a write error immediately with zero bytes written.
type errorWriter struct{}

func (errorWriter) Write(p []byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestHashingWriter_InnerErrorBubblesUpAndNothingHashed(t *testing.T) {
	t.Parallel()

	w := NewHashingWriter(errorWriter{})
	n, err := w.Write([]byte("will fail"))
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("expected ErrClosedPipe; got %v", err)
	}
	if n != 0 {
		t.Errorf("n on error with 0-byte inner write: got %d, want 0", n)
	}
	// Nothing was written, so the hash is the canonical empty digest.
	if got, want := w.Sum(), sha256Tag(nil); got != want {
		t.Errorf("Sum after 0-byte error: got %q, want %q", got, want)
	}
}

func TestHashingReader_Roundtrip(t *testing.T) {
	t.Parallel()

	payload := []byte("the quick brown fox jumps over the lazy dog")
	reader := NewHashingReader(bytes.NewReader(payload))

	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(out) != string(payload) {
		t.Errorf("output: got %q, want %q", out, payload)
	}
	if got, want := reader.Sum(), sha256Tag(payload); got != want {
		t.Errorf("Sum: got %q, want %q", got, want)
	}
}

// TestHashingReader_MatchesHashingWriterForSameBytes is the integrity
// contract exercised by backup/restore: a bundle hashed on the way out
// must produce the same digest when re-hashed on the way in.
func TestHashingReader_MatchesHashingWriterForSameBytes(t *testing.T) {
	t.Parallel()

	payload := []byte(strings.Repeat("integrity-bits-", 50))

	var sink bytes.Buffer
	w := NewHashingWriter(&sink)
	_, _ = w.Write(payload)

	r := NewHashingReader(bytes.NewReader(sink.Bytes()))
	_, _ = io.ReadAll(r)

	if w.Sum() != r.Sum() {
		t.Errorf("writer/reader digests diverged: w=%q r=%q", w.Sum(), r.Sum())
	}
}

func TestVerifyChecksum(t *testing.T) {
	t.Parallel()

	good := sha256Tag([]byte("payload"))

	t.Run("match", func(t *testing.T) {
		t.Parallel()
		if err := VerifyChecksum(good, good); err != nil {
			t.Errorf("expected nil error; got %v", err)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		t.Parallel()
		err := VerifyChecksum(good, sha256Tag([]byte("tampered")))
		if !errors.Is(err, ErrInvalidChecksum) {
			t.Fatalf("expected ErrInvalidChecksum; got %v", err)
		}
		// Message must name both sides for diagnostics.
		msg := err.Error()
		if !strings.Contains(msg, good) || !strings.Contains(msg, sha256Tag([]byte("tampered"))) {
			t.Errorf("error should include both checksums; got %q", msg)
		}
	})

	t.Run("missing_expected", func(t *testing.T) {
		t.Parallel()
		err := VerifyChecksum("", sha256Tag([]byte("any")))
		if !errors.Is(err, ErrInvalidChecksum) {
			t.Errorf("empty expected should still be ErrInvalidChecksum; got %v", err)
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("error should explain missing expected; got %v", err)
		}
	})
}
