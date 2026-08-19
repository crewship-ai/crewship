package localfs

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The #922 container-replay fallback in internal/server/routes_files.go hangs on
// exactly one boolean: errors.Is(err, fs.ErrPermission) for the error THIS
// package returns when the kernel refuses a write with EACCES. Until now
// nothing tested it.
//
// It is worth a test of its own because the error travels through three layers
// on the way out — syscall.Errno → *fs.PathError (os.Root's openat/mkdirat) →
// fmt.Errorf(...%w) — and any one of them being changed to %v, or to a
// hand-rolled errors.New, silently disarms the fallback rather than failing a
// test. A guard that cannot fire is worse than no guard: the comment above it
// claims coverage.
//
// The seam is the one the existing permission tests use (chmod 0555 on a real
// temp dir, skipped for root). What it proves: a genuine kernel EACCES from
// this package satisfies errors.Is(err, fs.ErrPermission). What it does NOT
// prove: anything about uid 1001 specifically — a test process cannot chown a
// tree to another uid without root. EACCES is EACCES either way, and the
// mkdirat case below reproduces the exact syscall from the field report
// ("create parent dir: mkdirat <crew>/<agent>/attachments: permission denied").
func TestWrite_EACCESSatisfiesErrPermission(t *testing.T) {
	t.Parallel()
	// SKIP-WAIVER(#1977): the setup is a chmod 0555 that root ignores, so as

	// uid 0 this test would pass while proving nothing — the assertion needs a

	// real kernel EACCES to have anything to observe.

	if os.Getuid() == 0 {
		t.Skip("permission bits are advisory for root")
	}

	cases := []struct {
		name string
		// setup prepares the tree under base and returns the write path.
		setup func(t *testing.T, base string) string
		// wantMsg is a fragment the error must carry so the log line names
		// the failing operation.
		wantMsg string
	}{
		{
			// The field failure: the parent directory of the destination does
			// not exist and cannot be created, because the directory above it
			// belongs to the crew runtime (uid 1001, mode 0755).
			name: "parent dir cannot be created (mkdirat)",
			setup: func(t *testing.T, base string) string {
				agentDir := filepath.Join(base, "crew1", "alex")
				if err := os.MkdirAll(agentDir, 0o755); err != nil {
					t.Fatal(err)
				}
				lockDir(t, agentDir)
				return "crew1/alex/attachments/chat1/Sešit1.xlsx"
			},
			wantMsg: "create parent dir",
		},
		{
			// The parent exists but is not writable: the file itself cannot be
			// created.
			name: "file cannot be created in a read-only dir (openat)",
			setup: func(t *testing.T, base string) string {
				dir := filepath.Join(base, "crew1", "alex")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				lockDir(t, dir)
				return "crew1/alex/report.txt"
			},
			wantMsg: "create crew1/alex/report.txt",
		},
		// NOT covered, deliberately: an EXISTING file owned by another uid.
		// Write's best-effort `root.Chmod(rel, 0664)` succeeds on a file this
		// process owns, so a 0444 file in a locked dir is simply re-opened and
		// written — no EACCES to assert. Reproducing that case needs a real
		// foreign owner, which needs root. The overwrite half of #922 is
		// covered at the handler level instead, with an injected EACCES
		// (routes_files_container_test.go).
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tempProvider(t)
			path := tc.setup(t, p.basePath)

			err := p.Write(context.Background(), path, bytes.NewReader([]byte("x")))
			if err == nil {
				t.Fatalf("Write(%q) succeeded; expected EACCES", path)
			}
			// THE assertion the fallback depends on.
			if !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("errors.Is(err, fs.ErrPermission) = false for %#v (%q) — "+
					"the #922 container replay in routes_files.go can never fire", err, err)
			}
			// And the trap next to it: os.IsPermission — the older spelling a
			// reviewer might "simplify" the guard to — reports FALSE here. It
			// unwraps *fs.PathError one level but does not follow fmt.Errorf's
			// %w chain, so it cannot see through the "create parent dir: "
			// wrap that this package adds. (Inside localfs.Write it is applied
			// to the raw, unwrapped Create error, where it works fine.)
			//
			// This row exists so that anyone rewriting the routes_files.go
			// guard as os.IsPermission finds out here rather than in
			// production. If a future Go release makes the two agree, this
			// assertion fails loudly and can simply be deleted.
			if os.IsPermission(err) {
				t.Errorf("os.IsPermission now sees through the wrap for %q — "+
					"the errors.Is-vs-os.IsPermission warning in this test is stale", err)
			}
			// The chain must still be a *fs.PathError naming the syscall, so
			// the log line says which operation was refused.
			var pe *fs.PathError
			if !errors.As(err, &pe) {
				t.Errorf("error %q does not unwrap to *fs.PathError; the log loses the syscall", err)
			} else if pe.Op == "" {
				t.Errorf("*fs.PathError has no Op: %#v", pe)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q should contain %q", err, tc.wantMsg)
			}
		})
	}
}

// lockDir drops write permission on dir for the duration of the test. The
// cleanup restores it so t.TempDir's own RemoveAll can still tear the tree
// down.
func lockDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
