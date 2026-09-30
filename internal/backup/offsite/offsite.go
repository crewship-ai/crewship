// Package offsite moves finished, already-encrypted backup bundles to and
// from off-site object storage.
//
// It sits on top of the local staging layer (internal/backup StorageOps: dirs,
// permissions, temp files, atomic rename) and knows nothing about bundles,
// plans or the catalog. A caller hands it a local file and a key; it streams
// the bytes out and — the rule that matters — reports success only after the
// remote copy has been checked. A copy that was sent but not verified does
// not count.
//
// The package never reads environment variables and never logs. Credentials
// arrive in the destination config (later: from a vault credential) and are
// redacted by S3Config.String.
//
// Wiring (scheduler, API, CLI) is a separate track; see Upload, Download and
// Verify for the three calls it needs, and NewS3 for building a destination
// from stored configuration.
package offsite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Destination is one off-site store. Keys are relative to the destination's
// own prefix; implementations add and strip it.
type Destination interface {
	// Kind names the implementation ("s3").
	Kind() string
	// Put stores exactly size bytes read from r under key. sha256hex is the
	// lowercase hex SHA-256 of those bytes; the implementation checks the
	// bytes it read against it before the object becomes visible and stores
	// it as object metadata so Head can return it.
	Put(ctx context.Context, key string, r io.Reader, size int64, sha256hex string) (Object, error)
	// Get opens key for reading. The caller must Close the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	// List returns every object whose key starts with prefix, sorted by
	// key. Object.SHA256 is empty in list results (stores do not return
	// user metadata in listings); use Head for it.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Head returns key's metadata, or an error wrapping ErrNotFound.
	Head(ctx context.Context, key string) (Object, error)
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// Test probes connection and permissions: put, head and delete one
	// tiny object. A nil error means a backup upload can succeed.
	Test(ctx context.Context) error
}

// Object is the metadata of one stored object.
type Object struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256,omitempty"`
	ETag     string    `json:"etag,omitempty"`
	Modified time.Time `json:"modified"`
}

var (
	// ErrNotFound: the key does not exist at the destination.
	ErrNotFound = errors.New("offsite: object not found")
	// ErrVerifyFailed: the remote copy does not match the local file (size,
	// stored checksum or re-downloaded content). The copy does not count.
	ErrVerifyFailed = errors.New("offsite: remote copy failed verification")
	// ErrContentMismatch: the bytes read from the source do not match the
	// size or SHA-256 the caller declared; nothing was committed.
	ErrContentMismatch = errors.New("offsite: source content does not match declared size or sha256")
	// ErrInvalidKey: the key is empty, absolute, has a . or .. segment, or
	// contains control characters or invalid UTF-8.
	ErrInvalidKey = errors.New("offsite: invalid object key")
	// ErrInvalidConfig: the destination configuration is incomplete or
	// refused by the outbound-URL guard.
	ErrInvalidConfig = errors.New("offsite: invalid destination config")
)

// maxKeyBytes is the S3 object-key limit (1024 bytes of UTF-8), applied to
// the caller's key; the destination prefix is checked separately.
const maxKeyBytes = 1024

// ValidateKey rejects keys that would be ambiguous or unsafe on any store or
// when mirrored to a local path: empty, leading '/', '.' or '..' segments,
// empty segments, control characters, invalid UTF-8, or over 1024 bytes.
// Spaces, '+', '=', '%', '#', '?', '&' and non-ASCII letters are allowed —
// they are percent-encoded on the wire.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	if len(key) > maxKeyBytes {
		return fmt.Errorf("%w: longer than %d bytes", ErrInvalidKey, maxKeyBytes)
	}
	if !utf8.ValidString(key) {
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidKey)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: must not start with /", ErrInvalidKey)
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: control character", ErrInvalidKey)
		}
	}
	for _, seg := range strings.Split(key, "/") {
		switch seg {
		case ".", "..":
			return fmt.Errorf("%w: %q segment", ErrInvalidKey, seg)
		case "":
			// A trailing slash would name a "directory" marker, and "a//b"
			// is a different key than "a/b" on S3 but not on disk.
			return fmt.Errorf("%w: empty path segment", ErrInvalidKey)
		}
	}
	return nil
}

// UploadOptions tunes Upload. The zero value is a verified, unthrottled
// upload that hashes the file first.
type UploadOptions struct {
	// SHA256 is the file's known lowercase hex SHA-256 (the catalog
	// already has it for every bundle). Empty → Upload hashes the file
	// first, which reads it once more.
	SHA256 string
	// VerifyDownload additionally downloads the object after upload and
	// hashes it. Head-level verification (size + stored checksum) always
	// runs; this is the expensive, stronger proof.
	VerifyDownload bool
	// BytesPerSecond caps upload (and verify-download) throughput. <= 0 is
	// unlimited. Ignored when Limiter is set.
	BytesPerSecond int64
	// Limiter shares one bandwidth cap across several transfers (the
	// instance-wide upload_mbps setting). Takes precedence over
	// BytesPerSecond.
	Limiter *Limiter
	// Clock backs the limiter built from BytesPerSecond. nil → wall clock.
	Clock Clock
}

func (o UploadOptions) limiter() *Limiter {
	if o.Limiter != nil {
		return o.Limiter
	}
	return NewLimiter(o.BytesPerSecond, o.Clock)
}

// Upload streams the local file at localPath to dst under key and returns
// only once the remote copy is verified: Head must report the same size and
// the same stored SHA-256 (and, with VerifyDownload, the re-downloaded bytes
// must hash to it). On verification failure the remote object is deleted
// (best effort) so a bad copy is never mistaken for a good one, and the
// error wraps ErrVerifyFailed.
func Upload(ctx context.Context, dst Destination, localPath, key string, opts UploadOptions) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}
	f, err := os.Open(localPath) // #nosec G304 -- caller-owned staging path
	if err != nil {
		return Object{}, fmt.Errorf("offsite: open %s: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Object{}, fmt.Errorf("offsite: stat %s: %w", localPath, err)
	}
	if !st.Mode().IsRegular() {
		return Object{}, fmt.Errorf("offsite: %s is not a regular file", localPath)
	}
	size := st.Size()

	sum := strings.ToLower(strings.TrimSpace(opts.SHA256))
	if sum == "" {
		h := sha256.New()
		if _, err := io.Copy(h, readerWithContext(ctx, f)); err != nil {
			return Object{}, fmt.Errorf("offsite: hash %s: %w", localPath, err)
		}
		sum = hex.EncodeToString(h.Sum(nil))
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return Object{}, fmt.Errorf("offsite: rewind %s: %w", localPath, err)
		}
	} else if !isHexSHA256(sum) {
		return Object{}, fmt.Errorf("offsite: UploadOptions.SHA256 is not a hex sha256")
	}

	lim := opts.limiter()
	if _, err := dst.Put(ctx, key, NewRateLimitedReader(ctx, f, lim), size, sum); err != nil {
		return Object{}, err
	}
	obj, err := verify(ctx, dst, key, size, sum, opts.VerifyDownload, lim)
	if err != nil {
		if errors.Is(err, ErrVerifyFailed) {
			delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			_ = dst.Delete(delCtx, key)
			cancel()
		}
		return Object{}, err
	}
	return obj, nil
}

// Verify checks that key exists at dst with the given size and stored
// SHA-256; deep additionally downloads and hashes the content. It is the
// check Upload runs, exported for periodic re-verification of copies already
// off-site. Mismatches wrap ErrVerifyFailed; a missing key wraps ErrNotFound.
func Verify(ctx context.Context, dst Destination, key string, size int64, sha256hex string, deep bool) (Object, error) {
	return verify(ctx, dst, key, size, strings.ToLower(sha256hex), deep, nil)
}

func verify(ctx context.Context, dst Destination, key string, size int64, sum string, deep bool, lim *Limiter) (Object, error) {
	obj, err := dst.Head(ctx, key)
	if err != nil {
		return Object{}, fmt.Errorf("offsite: verify %s: %w", key, err)
	}
	if obj.Size != size {
		return Object{}, fmt.Errorf("%w: %s: remote size %d, local size %d", ErrVerifyFailed, key, obj.Size, size)
	}
	if !strings.EqualFold(obj.SHA256, sum) {
		if obj.SHA256 == "" {
			return Object{}, fmt.Errorf("%w: %s: remote object carries no sha256 metadata", ErrVerifyFailed, key)
		}
		return Object{}, fmt.Errorf("%w: %s: remote sha256 %s, local sha256 %s", ErrVerifyFailed, key, obj.SHA256, sum)
	}
	if !deep {
		return obj, nil
	}
	rc, _, err := dst.Get(ctx, key)
	if err != nil {
		return Object{}, fmt.Errorf("offsite: verify download %s: %w", key, err)
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, NewRateLimitedReader(ctx, rc, lim))
	if err != nil {
		return Object{}, fmt.Errorf("offsite: verify download %s: %w", key, err)
	}
	if n != size {
		return Object{}, fmt.Errorf("%w: %s: downloaded %d bytes, expected %d", ErrVerifyFailed, key, n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return Object{}, fmt.Errorf("%w: %s: downloaded content sha256 %s, expected %s", ErrVerifyFailed, key, got, sum)
	}
	return obj, nil
}

// DownloadOptions tunes Download.
type DownloadOptions struct {
	// BytesPerSecond caps throughput; <= 0 is unlimited. Ignored when
	// Limiter is set.
	BytesPerSecond int64
	Limiter        *Limiter
	Clock          Clock
}

// Download fetches key from dst into localPath. The bytes land in a temp
// file beside localPath, are checked against the object's size and stored
// SHA-256, fsynced, and only then renamed into place — a failed or
// mismatched download never leaves a partial file at localPath. Returns the
// object's metadata; a mismatch wraps ErrVerifyFailed. An object with no
// stored SHA-256 (not written by this package) is refused.
func Download(ctx context.Context, dst Destination, key, localPath string, opts DownloadOptions) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}
	rc, obj, err := dst.Get(ctx, key)
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = rc.Close() }()
	if !isHexSHA256(strings.ToLower(obj.SHA256)) {
		return Object{}, fmt.Errorf("%w: %s: remote object carries no sha256 metadata", ErrVerifyFailed, key)
	}

	dir := filepath.Dir(localPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(localPath)+".offsite-*")
	if err != nil {
		return Object{}, fmt.Errorf("offsite: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return Object{}, fmt.Errorf("offsite: chmod temp: %w", err)
	}

	lim := opts.Limiter
	if lim == nil {
		lim = NewLimiter(opts.BytesPerSecond, opts.Clock)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), NewRateLimitedReader(ctx, rc, lim))
	if err != nil {
		return Object{}, fmt.Errorf("offsite: download %s: %w", key, err)
	}
	if n != obj.Size {
		return Object{}, fmt.Errorf("%w: %s: downloaded %d bytes, object size %d", ErrVerifyFailed, key, n, obj.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, obj.SHA256) {
		return Object{}, fmt.Errorf("%w: %s: downloaded content sha256 %s, stored sha256 %s", ErrVerifyFailed, key, got, obj.SHA256)
	}
	if err := tmp.Sync(); err != nil {
		return Object{}, fmt.Errorf("offsite: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Object{}, fmt.Errorf("offsite: close temp: %w", err)
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		return Object{}, fmt.Errorf("offsite: rename into %s: %w", localPath, err)
	}
	committed = true
	return obj, nil
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// ctxReader stops a long local read (hashing a multi-GB file) when ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func readerWithContext(ctx context.Context, r io.Reader) io.Reader { return &ctxReader{ctx: ctx, r: r} }

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
