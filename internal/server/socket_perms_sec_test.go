package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSecSocketDirMode asserts the socket directory is created with 0700
// (owner-only) rather than a group/other-accessible mode. Defense-in-depth:
// the socket file is already 0600, the containing dir should match.
func TestSecSocketDirMode(t *testing.T) {
	// shortSocketDir, not t.TempDir(): every socket path in this package has to
	// fit sockaddr_un, and the path built here is one rename of the test away
	// from being bound by a listener. See socket_test.go.
	path := shortSocketPath(t, "secdir", "test.sock")

	if err := removeSocketFile(path); err != nil {
		t.Fatalf("removeSocketFile: %v", err)
	}

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("socket dir mode = %#o, want 0700", got)
	}
}
