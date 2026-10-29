package managedlaunch

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"runtime"
)

// CaptureArchive reads one bounded Docker file-copy artifact. Forward-only
// archive data is spooled to an owned 0600 temporary file for ELF random reads.
// The file is never executed or reopened; every exit closes and removes it.
// This bounds Go buffering at the cost of up to MaxArtifactBytes temporary
// filesystem storage per call. TMPDIR may be memory-backed.
func CaptureArchive(executable string, reader io.Reader) (artifact *Artifact, err error) {
	denied := errors.New("managed launch: invalid executable archive")
	if path.Base(executable) != "codex" && path.Base(executable) != "claude" {
		return nil, denied
	}
	limited := &io.LimitedReader{R: reader, N: MaxArtifactBytes + 8192}
	tr := tar.NewReader(limited)
	h, e := tr.Next()
	if e != nil || h.Typeflag != tar.TypeReg || h.Name != path.Base(executable) || (h.Uid != 0 && h.Uid != 1001) || h.Mode&0022 != 0 || h.Mode&0111 == 0 || h.Size <= 0 || h.Size > MaxArtifactBytes {
		return nil, denied
	}
	// Preserve the existing Windows in-memory path. Portable named temporary
	// files do not provide Unix unlink-on-open semantics; do not introduce
	// crash-persistent staging or unverified native Windows handle behavior.
	if runtime.GOOS == "windows" {
		raw, e := io.ReadAll(tr)
		if e != nil || int64(len(raw)) != h.Size {
			return nil, denied
		}
		if _, e := tr.Next(); e != io.EOF || limited.N <= 0 {
			return nil, denied
		}
		return Capture(executable, raw)
	}
	file, e := os.CreateTemp("", "crewship-capture-*")
	if e != nil {
		return nil, denied
	}
	if e := os.Remove(file.Name()); e != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, denied
	}
	// No named spool survives cancellation or process death. The kernel frees
	// the unlinked inode when its only open descriptor closes.
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			artifact = nil
			err = denied
		}
	}()
	hash := sha256.New()
	n, e := io.CopyBuffer(io.MultiWriter(file, hash), tr, make([]byte, 64<<10))
	if e != nil || n != h.Size {
		return nil, denied
	}
	if _, e := tr.Next(); e != io.EOF || limited.N <= 0 {
		return nil, denied
	}
	return qualifyReader(executable, file, n, hex.EncodeToString(hash.Sum(nil)))
}

// VerifyArchive preserves the byte API while comparing in fixed-size chunks.
func VerifyArchive(reader io.Reader, name string, expected []byte) error {
	return verifyArchiveReaderAt(reader, name, bytes.NewReader(expected), int64(len(expected)))
}

// VerifyArchiveFile compares against the same immutable open file that the
// caller qualified. It does not reopen a path or attest a live bind mount.
func VerifyArchiveFile(reader io.Reader, name string, expected *os.File) error {
	if expected == nil {
		return errors.New("managed launch: invalid launcher archive")
	}
	info, e := expected.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return errors.New("managed launch: invalid launcher archive")
	}
	return verifyArchiveReaderAt(reader, name, expected, info.Size())
}
func verifyArchiveReaderAt(reader io.Reader, name string, expected io.ReaderAt, size int64) error {
	denied := errors.New("managed launch: invalid launcher archive")
	if size <= 0 || size > MaxArtifactBytes || path.Base(name) != name || name == "." || name == ".." {
		return denied
	}
	limited := &io.LimitedReader{R: reader, N: MaxArtifactBytes + 8192}
	tr := tar.NewReader(limited)
	h, e := tr.Next()
	if e != nil || h.Typeflag != tar.TypeReg || h.Name != name || h.Size != size {
		return denied
	}
	var actual, want [64 << 10]byte
	for offset := int64(0); offset < size; {
		count := int(min(int64(len(actual)), size-offset))
		if n, e := io.ReadFull(tr, actual[:count]); e != nil || n != count {
			return denied
		}
		if n, e := expected.ReadAt(want[:count], offset); e != nil || n != count {
			return denied
		}
		if !bytes.Equal(actual[:count], want[:count]) {
			return denied
		}
		offset += int64(count)
	}
	if _, e := tr.Next(); e != io.EOF || limited.N <= 0 {
		return denied
	}
	return nil
}
