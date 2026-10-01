package backup

import (
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/klauspost/compress/zstd"
)

// Staging encrypted before it touches disk.
//
// A bundle used to be built in two temp files: the plaintext payload
// (tar → zstd) and then its sealed copy (age), so a whole workspace sat
// unencrypted in the temp directory for as long as the seal took, and a crash
// in between left it there. The payload is now streamed through the sealer:
//
//	tar → zstd → age → sha256/count → (rate limit) → sealed temp file
//
// so the only staging file is ciphertext, and its SHA-256 and size are known
// the moment the stream closes — no second pass over the data.

// Throttle caps a byte stream (the instance-wide disk_mbps setting). Its one
// method blocks until n bytes may pass; *offsite.Limiter satisfies it, and a
// nil *offsite.Limiter (no cap) is valid.
type Throttle interface {
	WaitN(ctx context.Context, n int) error
}

// throttledWriter passes writes through t in slices no larger than chunk.
type throttledWriter struct {
	ctx   context.Context
	w     io.Writer
	t     Throttle
	chunk int
}

// NewThrottledWriter wraps w so writes proceed at most at t's rate. A nil t
// returns w unchanged.
func NewThrottledWriter(ctx context.Context, w io.Writer, t Throttle) io.Writer {
	if t == nil {
		return w
	}
	return &throttledWriter{ctx: ctx, w: w, t: t, chunk: 256 << 10}
}

func (tw *throttledWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := len(p)
		if n > tw.chunk {
			n = tw.chunk
		}
		if err := tw.t.WaitN(tw.ctx, n); err != nil {
			return written, err
		}
		m, err := tw.w.Write(p[:n])
		written += m
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

// SealWriter encrypts everything written to it straight into dst, hashing
// and counting the sealed bytes as they pass. Close flushes the final age
// chunk; Sum and Size are valid after it.
type SealWriter struct {
	enc     io.WriteCloser // nil for NoEncrypt
	hasher  *HashingWriter
	counter *countingWriter
}

// NewSealWriter returns a writer that seals into dst with exactly one of
// opts.Recipients, opts.Passphrase or opts.NoEncrypt (legacy/tests).
func NewSealWriter(dst io.Writer, opts WriteBundleOptions) (*SealWriter, error) {
	counter := &countingWriter{w: dst}
	hasher := NewHashingWriter(counter)
	s := &SealWriter{hasher: hasher, counter: counter}
	var err error
	switch {
	case opts.NoEncrypt:
	case len(opts.Recipients) > 0:
		s.enc, err = EncryptStream(hasher, opts.Recipients...)
	case opts.Passphrase != "":
		s.enc, err = EncryptStreamPassphrase(hasher, opts.Passphrase)
	default:
		return nil, fmt.Errorf("backup: NewSealWriter requires Recipients, Passphrase, or NoEncrypt=true")
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SealWriter) Write(p []byte) (int, error) {
	if s.enc == nil {
		return s.hasher.Write(p)
	}
	return s.enc.Write(p)
}

// Close flushes the encryptor. It does not close dst.
func (s *SealWriter) Close() error {
	if s.enc == nil {
		return nil
	}
	if err := s.enc.Close(); err != nil {
		return fmt.Errorf("backup: close AGE writer: %w", err)
	}
	return nil
}

// Sum is the "sha256:…" digest of the sealed bytes (the manifest's
// payload_sha256).
func (s *SealWriter) Sum() string { return s.hasher.Sum() }

// Size is how many sealed bytes reached dst.
func (s *SealWriter) Size() int64 { return s.counter.n }

// encoderConcurrency is the zstd encoder's goroutine count for n requested
// cores: at least 1, at most GOMAXPROCS.
func encoderConcurrency(n int) int {
	if n <= 0 {
		return 0
	}
	if max := runtime.GOMAXPROCS(0); n > max {
		n = max
	}
	return n
}

// NewTarZstWriterConcurrency is NewTarZstWriter with the zstd encoder capped
// at n goroutines (the cpu_cores setting). n <= 0 keeps the library default.
func NewTarZstWriterConcurrency(sink io.Writer, n int) (*TarZstWriter, error) {
	var opts []zstd.EOption
	if c := encoderConcurrency(n); c > 0 {
		opts = append(opts, zstd.WithEncoderConcurrency(c))
	}
	return newTarZstWriter(sink, opts...)
}
