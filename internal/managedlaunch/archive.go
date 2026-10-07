package managedlaunch

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"path"
)

// CaptureArchive reads one Docker file-copy artifact without host extraction.
// Ownership is image evidence; the adapter separately proves immutable image
// identity and prevents writable mounts over the executable.
func CaptureArchive(executable string, reader io.Reader) (*Artifact, error) {
	denied := errors.New("managed launch: invalid executable archive")
	if path.Base(executable) != "codex" && path.Base(executable) != "claude" {
		return nil, denied
	}
	limited := &io.LimitedReader{R: reader, N: MaxArtifactBytes + 8192}
	tr := tar.NewReader(limited)
	h, err := tr.Next()
	if err != nil || h.Typeflag != tar.TypeReg || h.Name != path.Base(executable) || (h.Uid != 0 && h.Uid != 1001) ||
		h.Mode&0022 != 0 || h.Mode&0111 == 0 || h.Size <= 0 || h.Size > MaxArtifactBytes {
		return nil, denied
	}
	raw, err := io.ReadAll(tr)
	if err != nil || int64(len(raw)) != h.Size {
		return nil, denied
	}
	if _, err := tr.Next(); err != io.EOF || limited.N <= 0 {
		return nil, denied
	}
	return Capture(executable, raw)
}

// VerifyArchive compares one bounded file-copy entry with trusted host bytes.
// This is a consistency check, not proof of a running bind mount's inode.
func VerifyArchive(reader io.Reader, name string, expected []byte) error {
	denied := errors.New("managed launch: invalid launcher archive")
	if len(expected) == 0 || len(expected) > MaxArtifactBytes || path.Base(name) != name || name == "." || name == ".." {
		return denied
	}
	limited := &io.LimitedReader{R: reader, N: MaxArtifactBytes + 8192}
	tr := tar.NewReader(limited)
	h, err := tr.Next()
	if err != nil || h.Typeflag != tar.TypeReg || h.Name != name || h.Size != int64(len(expected)) {
		return denied
	}
	raw, err := io.ReadAll(tr)
	if err != nil || !bytes.Equal(raw, expected) {
		return denied
	}
	if _, err := tr.Next(); err != io.EOF || limited.N <= 0 {
		return denied
	}
	return nil
}
