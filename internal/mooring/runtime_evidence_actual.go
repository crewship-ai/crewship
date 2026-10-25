//go:build linux && mooring_rust_integration

package mooring

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Integration builds only: stderr goes directly to a private file from process
// start, including handshake failures and hard kills. No pipe, observer or timer.
func actualRuntimeEvidence(cmd *exec.Cmd) (func(), error) {
	root := os.Getenv("MOORING_SESSION_OUTPUT_DIR")
	if root == "" {
		return func() {}, nil
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !filepath.IsAbs(root) || !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("actual runtime evidence requires an owned private directory")
	}
	file, err := os.CreateTemp(root, "rust-stderr-*.log")
	if err != nil {
		return nil, err
	}
	cmd.Stderr = file
	// Start duplicates this descriptor into the child; closing the parent's copy
	// afterwards preserves writes without delaying Wait or leaking parent handles.
	return func() { _ = file.Close() }, nil
}
