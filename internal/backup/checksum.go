package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// ChecksumPrefix is the algorithm tag prepended to hex digests written
// into the manifest. Keeping it explicit means future format versions
// can introduce a different hash without ambiguity.
const ChecksumPrefix = "sha256:"

// HashingWriter wraps an io.Writer with a SHA-256 hasher. Bytes written
// to the wrapper are forwarded to the inner writer AND fed through the
// hasher. Call Sum() after all writes complete to obtain the digest.
type HashingWriter struct {
	inner io.Writer
	h     hash.Hash
}

// NewHashingWriter returns a HashingWriter wrapping w.
func NewHashingWriter(w io.Writer) *HashingWriter {
	return &HashingWriter{inner: w, h: sha256.New()}
}

// Write implements io.Writer.
func (hw *HashingWriter) Write(p []byte) (int, error) {
	n, err := hw.inner.Write(p)
	if n > 0 {
		// hash.Hash never returns an error from Write.
		_, _ = hw.h.Write(p[:n])
	}
	return n, err
}

// Sum returns the SHA-256 digest of all bytes written so far, formatted
// as "sha256:<hex>" for direct inclusion in the manifest.
func (hw *HashingWriter) Sum() string {
	return ChecksumPrefix + hex.EncodeToString(hw.h.Sum(nil))
}

// HashingReader is the read-side counterpart. It forwards reads from
// inner while hashing them, so callers can verify integrity at the same
// time as consuming the stream.
type HashingReader struct {
	inner io.Reader
	h     hash.Hash
}

// NewHashingReader returns a HashingReader wrapping r.
func NewHashingReader(r io.Reader) *HashingReader {
	return &HashingReader{inner: r, h: sha256.New()}
}

// Read implements io.Reader.
func (hr *HashingReader) Read(p []byte) (int, error) {
	n, err := hr.inner.Read(p)
	if n > 0 {
		_, _ = hr.h.Write(p[:n])
	}
	return n, err
}

// Sum returns the SHA-256 digest of all bytes read so far, formatted
// the same way as HashingWriter.Sum.
func (hr *HashingReader) Sum() string {
	return ChecksumPrefix + hex.EncodeToString(hr.h.Sum(nil))
}

// VerifyChecksum compares expected (as emitted by HashingWriter.Sum)
// against actual (from HashingReader.Sum) and returns ErrInvalidChecksum
// on mismatch. Callers should use this at the end of a restore flow,
// after the entire payload has been streamed, to refuse corrupted or
// tampered bundles.
func VerifyChecksum(expected, actual string) error {
	if expected == "" {
		return fmt.Errorf("%w: expected checksum missing from manifest", ErrInvalidChecksum)
	}
	if expected != actual {
		return fmt.Errorf("%w: expected %s, got %s", ErrInvalidChecksum, expected, actual)
	}
	return nil
}
