package backup

// Encrypted staging for the whole-instance backup. The consistent copy is
// taken inside the quiet window and packed after it, so it has to wait
// somewhere in between; before this, it waited on disk in the clear (the
// database snapshot, every file store, the crew archives). Now every staged
// file is written through an ephemeral key that exists only in this
// process's memory — generated per run, never written anywhere, gone with
// the run — and decrypted again while packing. A staging directory left by
// a crash is unreadable noise, and it is wiped at the next start anyway.
//
// The format is a chunked AEAD stream (the STREAM construction):
//
//	"CSTG1\n" | 16-byte random nonce prefix | chunk*
//	chunk = XChaCha20-Poly1305(key, nonce = prefix | uint64 counter, plaintext ≤ 64 KiB, ad = final flag)
//
// The counter orders the chunks and the final flag on the last one makes a
// truncated file fail to open, so a staged file is read back exactly as it
// was written or not at all.

import (
	"bufio"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	stagingMagic      = "CSTG1\n"
	stagingPrefixSize = 16
	stagingChunk      = 64 << 10
)

var errStagingCorrupt = errors.New("backup: staged file is corrupt or truncated")

// stagingCipher encrypts the staged files of one run.
type stagingCipher struct {
	aead  cipher.AEAD
	mu    sync.Mutex
	sizes map[string]int64
}

// newStagingCipher draws a fresh key. It lives only in the AEAD.
func newStagingCipher() (*stagingCipher, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("backup: staging key: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	return &stagingCipher{aead: aead, sizes: map[string]int64{}}, nil
}

// Create opens a new staged file (it must not exist) for encrypted writing.
// Close records its plaintext size.
func (c *stagingCipher) Create(path string) (io.WriteCloser, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	w := &stagingWriter{c: c, path: path, f: f, bw: bufio.NewWriterSize(f, stagingChunk+c.aead.Overhead()+64)}
	w.nonce = make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(w.nonce[:stagingPrefixSize]); err != nil {
		_ = f.Close()
		return nil, err
	}
	_, err = w.bw.WriteString(stagingMagic)
	if err == nil {
		_, err = w.bw.Write(w.nonce[:stagingPrefixSize])
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	w.buf = make([]byte, 0, stagingChunk)
	return w, nil
}

// Size is the plaintext size of a staged file this cipher wrote.
func (c *stagingCipher) Size(path string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.sizes[path]
	return n, ok
}

// Open decrypts a staged file this cipher wrote, returning its plaintext
// size. The reader fails (errStagingCorrupt) on any tampering or truncation.
func (c *stagingCipher) Open(path string) (io.ReadCloser, int64, error) {
	size, ok := c.Size(path)
	if !ok {
		return nil, 0, fmt.Errorf("backup: %s was not staged by this run", path)
	}
	f, err := os.Open(path) // #nosec G304 -- a path this run created in its staging dir
	if err != nil {
		return nil, 0, err
	}
	br := bufio.NewReaderSize(f, stagingChunk+c.aead.Overhead()+64)
	head := make([]byte, len(stagingMagic)+stagingPrefixSize)
	if _, err := io.ReadFull(br, head); err != nil || string(head[:len(stagingMagic)]) != stagingMagic {
		_ = f.Close()
		return nil, 0, errStagingCorrupt
	}
	r := &stagingReader{c: c, f: f, br: br, nonce: make([]byte, c.aead.NonceSize())}
	copy(r.nonce, head[len(stagingMagic):])
	r.frame = make([]byte, stagingChunk+c.aead.Overhead())
	return r, size, nil
}

func (c *stagingCipher) setNonce(nonce []byte, counter uint64) {
	binary.BigEndian.PutUint64(nonce[stagingPrefixSize:], counter)
}

var (
	adMore  = []byte{0}
	adFinal = []byte{1}
)

type stagingWriter struct {
	c       *stagingCipher
	path    string
	f       *os.File
	bw      *bufio.Writer
	nonce   []byte
	counter uint64
	buf     []byte
	out     []byte
	n       int64
	err     error
	closed  bool
}

func (w *stagingWriter) seal(final bool) error {
	ad := adMore
	if final {
		ad = adFinal
	}
	w.c.setNonce(w.nonce, w.counter)
	w.counter++
	w.out = w.c.aead.Seal(w.out[:0], w.nonce, w.buf, ad)
	w.buf = w.buf[:0]
	_, err := w.bw.Write(w.out)
	return err
}

func (w *stagingWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("backup: write to a closed staged file")
	}
	written := 0
	for len(p) > 0 {
		// A full buffer is sealed only when more follows, so the last chunk
		// is always sealed by Close with the final flag.
		if len(w.buf) == stagingChunk {
			if err := w.seal(false); err != nil {
				w.err = err
				return written, err
			}
		}
		n := copy(w.buf[len(w.buf):stagingChunk], p)
		w.buf = w.buf[:len(w.buf)+n]
		p = p[n:]
		written += n
		w.n += int64(n)
	}
	return written, nil
}

func (w *stagingWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	err := w.err
	if err == nil {
		err = w.seal(true)
	}
	if err == nil {
		err = w.bw.Flush()
	}
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	clear(w.buf[:cap(w.buf)])
	if err != nil {
		w.err = err
		return err
	}
	w.c.mu.Lock()
	w.c.sizes[w.path] = w.n
	w.c.mu.Unlock()
	return nil
}

type stagingReader struct {
	c       *stagingCipher
	f       *os.File
	br      *bufio.Reader
	nonce   []byte
	counter uint64
	frame   []byte
	plain   []byte
	pos     int
	done    bool
	err     error
}

func (r *stagingReader) next() error {
	n, err := io.ReadFull(r.br, r.frame)
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF) || (err == nil && peekEOF(r.br)):
		// A short frame, or a full one with nothing after it: the last.
	case errors.Is(err, io.EOF):
		return errStagingCorrupt // the final chunk never came
	case err != nil:
		return err
	default:
		return r.open(r.frame[:n], adMore)
	}
	if err := r.open(r.frame[:n], adFinal); err != nil {
		return err
	}
	r.done = true
	return nil
}

func peekEOF(br *bufio.Reader) bool {
	_, err := br.Peek(1)
	return errors.Is(err, io.EOF)
}

func (r *stagingReader) open(frame, ad []byte) error {
	r.c.setNonce(r.nonce, r.counter)
	r.counter++
	plain, err := r.c.aead.Open(r.plain[:0], r.nonce, frame, ad)
	if err != nil {
		return errStagingCorrupt
	}
	r.plain, r.pos = plain, 0
	return nil
}

func (r *stagingReader) Read(p []byte) (int, error) {
	for r.pos >= len(r.plain) {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		if err := r.next(); err != nil {
			r.err = err
			return 0, err
		}
	}
	n := copy(p, r.plain[r.pos:])
	r.pos += n
	return n, nil
}

func (r *stagingReader) Close() error {
	clear(r.plain[:cap(r.plain)])
	return r.f.Close()
}
