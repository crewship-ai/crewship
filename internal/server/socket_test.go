package server

import (
	"os"
	"path/filepath"
	"testing"
)

// --- AF_UNIX path budget for tests -----------------------------------------
//
// t.TempDir() must never be used to build a socket path in this package. It
// interpolates the test (and subtest) name into the directory, and on macOS the
// temp root is already ~49 bytes
// (/var/folders/<2>/<28>/T/), so a path like
//
//	/var/folders/g3/pffjr…/T/TestEnsureSocketPathFree_Cornersstale_socket_is_free355865998/001/i.sock
//
// blows past what sockaddr_un.sun_path can hold. bind() and connect() then
// return EINVAL before any of the logic under test runs, so the failure looks
// like a broken guard rather than a broken path — and it only shows up on the
// macos-arm64 runner, because the same names fit on Linux.
//
// The ceiling was measured, not assumed: on this Linux host net.Listen("unix",
// p) succeeds at a 107-byte path and fails with EINVAL at 108 — sun_path[108]
// minus the NUL terminator. Apple/BSD document sun_path as 104 bytes, so the
// darwin ceiling is 103. We target 100, the same number
// internal/config.maxSocketPath uses for the production derivation, which
// leaves 3 bytes of slack under the tightest platform and keeps tests and
// production agreeing about the limit the code under test exists to respect.
const testMaxSocketPath = 100

// socketPathHeadroom is what shortSocketDir reserves for whatever the caller
// appends. The longest suffix any test in this package adds is
// "/sub/nested/i.sock" (18 bytes); 28 leaves room for a case to grow a
// component without the reservation quietly becoming a lie.
const socketPathHeadroom = 28

// shortSocketDir returns a directory that is provably short enough to hold an
// AF_UNIX socket path, removed when the test ends.
//
// os.MkdirTemp respects TMPDIR exactly like t.TempDir() does, so it is not the
// temp *root* that makes this safe — it is the two-character prefix: the leaf
// is ~12 bytes instead of the 60+ a test name produces. The assertion below is
// the part that matters, because a runner with a longer TMPDIR would otherwise
// reintroduce the same EINVAL silently.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cs")
	if err != nil {
		t.Fatalf("create short socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if len(dir)+socketPathHeadroom > testMaxSocketPath {
		t.Fatalf("temp root is too long for AF_UNIX: %q is %d bytes and leaves "+
			"under %d for the socket name (budget %d). Point TMPDIR somewhere shorter.",
			dir, len(dir), socketPathHeadroom, testMaxSocketPath)
	}
	return dir
}

// shortSocketPath is shortSocketDir plus the socket file name, for the tests
// that need exactly one path.
func shortSocketPath(t *testing.T, elem ...string) string {
	t.Helper()
	return requireShortSocketPath(t, filepath.Join(append([]string{shortSocketDir(t)}, elem...)...))
}

// requireShortSocketPath fails loudly when p cannot fit in sockaddr_un, and
// returns it otherwise. Callers that assemble a path themselves run it so an
// over-long path is reported as an over-long path, instead of surfacing as a
// bare "invalid argument" from a bind() or connect() several frames down.
func requireShortSocketPath(t *testing.T, p string) string {
	t.Helper()
	if len(p) > testMaxSocketPath {
		t.Fatalf("socket path %q is %d bytes, over the %d-byte test budget "+
			"(sockaddr_un.sun_path holds 104 on darwin/BSD, 108 on Linux)",
			p, len(p), testMaxSocketPath)
	}
	return p
}

func TestRemoveSocketFileNonexistent(t *testing.T) {
	dir := shortSocketDir(t)
	err := removeSocketFile(filepath.Join(dir, "nonexistent.sock"))
	if err != nil {
		t.Fatalf("expected no error for nonexistent file, got: %v", err)
	}
}

func TestRemoveSocketFileExisting(t *testing.T) {
	path := shortSocketPath(t, "test.sock")

	if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	err := removeSocketFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected socket file to be removed")
	}
}

func TestRemoveSocketFileCreatesDirectory(t *testing.T) {
	path := shortSocketPath(t, "sub", "dir", "test.sock")

	err := removeSocketFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parentDir := filepath.Dir(path)
	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("expected parent dir to exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected parent to be a directory")
	}
}
