//go:build unix

package server

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The inode types only unix can create at the socket path. They belong to the
// same finding as the directory case in server_ipc_socket_guard_test.go: none
// of them can have an AF_UNIX listener bound to it, so connect() answers
// ECONNREFUSED and the staleness test used to read that as "the previous owner
// crashed, unlink it". For a FIFO or a device node os.Remove then succeeds,
// which is worse than the directory case, not better: nothing fails, and the
// thing that disappears belongs to something else on the box.

// mkfifo creates a named pipe at path. Opening a FIFO blocks until the other
// end shows up, so nothing in these tests opens it — Lstat and connect() do
// not, which is the whole point of classifying it before the dial.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo(%s): %v", path, err)
	}
}

// TestEnsureSocketPathFree_UnixInodes pins the classification for the file
// types that are not reachable through portable os calls.
func TestEnsureSocketPathFree_UnixInodes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string) string
		// wantKind: the noun the refusal must use, so the operator is told what
		// is actually sitting at the path rather than "not a socket".
		wantKind string
	}{
		{
			name: "named pipe is refused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				mkfifo(t, p)
				return p
			},
			wantKind: "named pipe",
		},
		{
			// The accident this covers: ipc.socket_path set to /dev/null,
			// which is writable, refuses connect() like every other non-socket
			// inode, and is removable by root. Not created in TempDir because
			// mknod needs privileges; the real node is safe to *inspect*,
			// since ensureSocketPathFree never removes anything itself.
			name: "character device is refused",
			setup: func(t *testing.T, dir string) string {
				const p = "/dev/null"
				info, err := os.Lstat(p)
				if err != nil || info.Mode()&os.ModeCharDevice == 0 {
					t.Skipf("no character device at %s (%v)", p, err)
				}
				return p
			},
			wantKind: "character device",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t, t.TempDir())

			err := ensureSocketPathFree(path)
			if err == nil {
				t.Fatalf("ensureSocketPathFree(%q) = nil, want a refusal", path)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name the socket path %q", err, path)
			}
			if !strings.Contains(err.Error(), tt.wantKind) {
				t.Errorf("error %q does not say the path is a %s", err, tt.wantKind)
			}
			if !strings.Contains(err.Error(), "ipc.socket_path") {
				t.Errorf("error %q does not point at ipc.socket_path", err)
			}
			if _, statErr := os.Lstat(path); statErr != nil {
				t.Errorf("refused path %q no longer stats: %v", path, statErr)
			}
		})
	}
}

// TestStartIPC_DoesNotDeleteFIFO drives the real startIPC, because that is
// where the deletion happened: ensureSocketPathFree only classifies, and
// removeSocketFile unlinks whatever it is handed. The assertion is that the
// pipe is still a pipe afterwards — an error alone would also be returned by
// an implementation that unlinked it and then failed to listen.
func TestStartIPC_DoesNotDeleteFIFO(t *testing.T) {
	// Short dir name: sockaddr_un caps the path near 100 bytes.
	path := filepath.Join(t.TempDir(), "i.sock")
	mkfifo(t, path)

	startIPCExpectingRefusal(t, path)

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("the FIFO at the socket path was deleted: %v", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the FIFO at the socket path was replaced by %s", info.Mode().Type())
	}
}
