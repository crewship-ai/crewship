package craftengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Native is a development override: an operator-provided command-line engine
// at an absolute path, pinned by its SHA-256. It runs only on Linux, where
// /proc/self/fd executes exactly the descriptor that was hashed. Releases use
// WASM instead.
type Native struct {
	application string
	path        string
	sha         string
}

// NewNative checks the path and digest once; every run hashes the opened file
// again before executing it.
func NewNative(application, path, sha256Hex string) (*Native, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("%w: a native engine override runs only on Linux", ErrUnavailable)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(sha256Hex) != 64 {
		return nil, fmt.Errorf("%w: invalid native engine pin", ErrUnavailable)
	}
	n := &Native{application: application, path: path, sha: sha256Hex}
	f, err := n.open()
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return n, nil
}

func (n *Native) Application() string    { return n.application }
func (n *Native) Runtime() string        { return RuntimeNative }
func (n *Native) SHA256() string         { return n.sha }
func (n *Native) Root(dir string) string { return dir }

func (n *Native) open() (*os.File, error) {
	expected, err := hex.DecodeString(n.sha)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid native engine pin", ErrUnavailable)
	}
	f, err := os.Open(n.path)
	if err != nil {
		return nil, fmt.Errorf("%w: native engine missing", ErrUnavailable)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() <= 0 || info.Size() > 512<<20 {
		_ = f.Close()
		return nil, fmt.Errorf("%w: native engine is not an executable file", ErrUnavailable)
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, f); err != nil || !bytes.Equal(digest.Sum(nil), expected) {
		_ = f.Close()
		return nil, fmt.Errorf("%w: native engine digest mismatch", ErrUnavailable)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

// Run executes the descriptor it hashed, never looking the path up again.
func (n *Native) Run(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	f, err := n.open()
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	cmd.ExtraFiles = []*os.File{f}
	cmd.Dir = dir
	cmd.Env = []string{"LANG=C.UTF-8", "HOME=" + dir, "TMPDIR=" + dir}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil {
		return ErrFailed
	}
	return nil
}

// Path is the pinned executable's location.
func (n *Native) Path() string { return n.path }
