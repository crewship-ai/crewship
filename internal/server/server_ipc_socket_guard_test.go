package server

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B-04 follow-up: startIPC used to unlink whatever sat at cfg.IPC.SocketPath
// and listen there unconditionally. A second crewshipd on the same host
// therefore did not fail, block or warn — it silently stole the AF_UNIX path
// out from under the first, healthy daemon, whose listener kept running on an
// inode nothing could reach any more. The victim was the instance that had
// done nothing wrong, and neither process logged a word.
//
// These tests pin both halves of the discrimination, because a fix that only
// refuses is as broken as the bug: the unlink-then-listen pattern exists to
// recover from a *stale* socket left by a crashed process (net.Listen on unix
// fails with EADDRINUSE if the file exists), and losing that would leave every
// unclean shutdown needing a manual `rm`.

// dialUnixOnce round-trips one byte over path to prove a listener is not just
// present but still answering. Used to assert the *first* daemon survived —
// the file merely existing proves nothing, since a thief would recreate it.
func dialUnixOnce(t *testing.T, path string) error {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("p")); err != nil {
		return err
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 'p' {
		return errors.New("echo mismatch")
	}
	return nil
}

// startEchoListener stands in for a healthy first daemon: a real listener
// accepting on path and echoing back, torn down with the test. Unlink-on-close
// is left at Go's default (true) so the listener owns its file the way
// net.Listen callers normally do.
func startEchoListener(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix %s: %v", path, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
	})
	if err := dialUnixOnce(t, path); err != nil {
		t.Fatalf("echo listener not answering after setup: %v", err)
	}
}

// makeStaleSocket leaves behind exactly what a crashed daemon leaves: a socket
// file whose owner is gone. SetUnlinkOnClose(false) is the only way to get one
// deliberately — Go's UnixListener normally removes the file on Close, so a
// clean shutdown never produces this state, only a SIGKILL/panic does.
func makeStaleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix %s: %v", path, err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stale socket file should have survived Close: %v", err)
	}
}

// TestEnsureSocketPathFree_Corners pins the decisions startIPC's guard makes
// on the paths that are awkward to reach through a full startIPC run. The rule
// under test is asymmetric on purpose: only *proof* of deadness permits the
// unlink, and every "cannot tell" answer is a refusal — treating an
// unverifiable path as stale is how a socket owned by another user would get
// deleted, which is the original bug wearing a different hat.
func TestEnsureSocketPathFree_Corners(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) string // returns the path to check
		wantErr bool
	}{
		{
			name: "missing path is free",
			setup: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "absent.sock")
			},
		},
		{
			name: "missing parent directory is free",
			setup: func(t *testing.T, dir string) string {
				// First boot on a fresh data dir: removeSocketFile creates the
				// directory afterwards, the guard must not object.
				return filepath.Join(dir, "sub", "nested", "i.sock")
			},
		},
		{
			name: "regular file is not a listener",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				if err := os.WriteFile(p, []byte("junk"), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
		{
			name: "dangling symlink is not a listener",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				if err := os.Symlink(filepath.Join(dir, "gone.sock"), p); err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
		{
			name: "stale socket is free",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				makeStaleSocket(t, p)
				return p
			},
		},
		{
			name: "live socket is refused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				startEchoListener(t, p)
				return p
			},
			wantErr: true,
		},
		{
			name: "symlink to a live socket is refused",
			setup: func(t *testing.T, dir string) string {
				real := filepath.Join(dir, "r.sock")
				startEchoListener(t, real)
				p := filepath.Join(dir, "i.sock")
				if err := os.Symlink(real, p); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: true,
		},
		{
			// "I am not allowed to look" must never read as "nothing is
			// there". A socket belonging to a daemon running as another user
			// lands here.
			name: "unreadable directory is refused rather than assumed stale",
			setup: func(t *testing.T, dir string) string {
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory permissions")
				}
				sub := filepath.Join(dir, "locked")
				if err := os.Mkdir(sub, 0o700); err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(sub, "i.sock")
				makeStaleSocket(t, p)
				if err := os.Chmod(sub, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(sub, 0o700) }) // let TempDir clean up
				return p
			},
			wantErr: true,
		},
		{
			// Same rule one layer down: the path is visible but connect() is
			// denied, so liveness is unknown and the file stays.
			name: "undialable socket is refused rather than assumed stale",
			setup: func(t *testing.T, dir string) string {
				if os.Geteuid() == 0 {
					t.Skip("root bypasses socket permissions")
				}
				p := filepath.Join(dir, "i.sock")
				startEchoListener(t, p)
				if err := os.Chmod(p, 0o000); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t, t.TempDir())

			err := ensureSocketPathFree(path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ensureSocketPathFree(%q) = nil, want a refusal", path)
				}
				if !strings.Contains(err.Error(), path) {
					t.Errorf("error %q does not name the socket path %q", err, path)
				}
				// A refusal must leave the path exactly as it found it — the
				// whole point is that the innocent process keeps its socket.
				if _, statErr := os.Lstat(path); statErr != nil && os.IsNotExist(statErr) {
					t.Errorf("refused path %q was removed anyway", path)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureSocketPathFree(%q) = %v, want nil", path, err)
			}
		})
	}
}

// TestEnsureSocketPathFree_MissingPathIsCheap guards the first-boot cost: the
// guard runs on every start, and the common case must not pay the probe
// timeout. A missing path is answered by a single stat.
func TestEnsureSocketPathFree_MissingPathIsCheap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sock")
	start := time.Now()
	if err := ensureSocketPathFree(path); err != nil {
		t.Fatalf("ensureSocketPathFree: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= socketProbeTimeout {
		t.Errorf("missing path took %v, want well under the %v probe timeout", elapsed, socketProbeTimeout)
	}
}

// TestStartIPC_RefusesLiveSocketRecoversStale drives the real startIPC over
// every state the socket path can be in at boot.
func TestStartIPC_RefusesLiveSocketRecoversStale(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the socket path before startIPC runs.
		setup func(t *testing.T, path string)
		// wantErr: startIPC must refuse instead of binding.
		wantErr bool
		// wantErrSubstr: fragments the refusal message must carry, so the
		// operator learns which path and which instance is at fault rather
		// than reading a bare EADDRINUSE.
		wantErrSubstr []string
		// after runs once startIPC has returned (error case) or been shut
		// down (success case).
		after func(t *testing.T, path string)
	}{
		{
			// The bug. A live listener must be treated as a running peer.
			name:          "live socket is refused and left untouched",
			setup:         startEchoListener,
			wantErr:       true,
			wantErrSubstr: []string{"already", "running"},
			after: func(t *testing.T, path string) {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("live socket file was unlinked by the second instance: %v", err)
				}
				// The load-bearing assertion: the first daemon's IPC still
				// works. Everything else can pass while its sidecars are cut
				// off, which is precisely how this shipped unnoticed.
				if err := dialUnixOnce(t, path); err != nil {
					t.Fatalf("first daemon's socket is no longer reachable: %v", err)
				}
			},
		},
		{
			name:  "stale socket from a crashed daemon is reclaimed",
			setup: makeStaleSocket,
		},
		{
			// Not a socket at all: nothing can be listening on it, so there is
			// no peer to protect and the path must be reclaimed.
			name: "regular file at the socket path is reclaimed",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("junk"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// First boot. Must stay silent and must not slow startup down.
			name:  "missing path binds cleanly",
			setup: func(t *testing.T, path string) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Short dir name: sockaddr_un caps the path near 100 bytes and
			// t.TempDir() already embeds the subtest name.
			path := filepath.Join(t.TempDir(), "i.sock")
			tt.setup(t, path)

			s := newTestServerForT(t)
			s.cfg.IPC.SocketPath = path

			// startIPC blocks in Serve on the success path, so it always runs
			// on its own goroutine and is stopped via ipcServer.Shutdown.
			errCh := make(chan error, 1)
			go func() { errCh <- s.startIPC() }()

			if tt.wantErr {
				select {
				case err := <-errCh:
					if err == nil {
						t.Fatal("startIPC bound over a live socket; want a refusal")
					}
					for _, want := range tt.wantErrSubstr {
						if !strings.Contains(strings.ToLower(err.Error()), want) {
							t.Errorf("error %q missing %q", err, want)
						}
					}
					if !strings.Contains(err.Error(), path) {
						t.Errorf("error %q does not name the socket path %q", err, path)
					}
				case <-time.After(10 * time.Second):
					// The pre-fix shape of this failure: startIPC did not
					// refuse, it unlinked the live socket and blocked in
					// Serve() on its own listener. Say so, and say whether the
					// first daemon is still reachable, so the output names the
					// actual damage rather than just "timed out".
					reachable := dialUnixOnce(t, path) == nil
					t.Fatalf("startIPC neither refused nor returned within 10s "+
						"(it bound the live path instead); first daemon still reachable = %v", reachable)
				}
				tt.after(t, path)
				return
			}

			// Success path: the socket must actually accept connections.
			deadline := time.Now().Add(10 * time.Second)
			var dialErr error
			for time.Now().Before(deadline) {
				select {
				case err := <-errCh:
					t.Fatalf("startIPC returned early: %v", err)
				default:
				}
				var c net.Conn
				if c, dialErr = net.DialTimeout("unix", path, time.Second); dialErr == nil {
					_ = c.Close()
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if dialErr != nil {
				t.Fatalf("IPC socket never came up: %v", dialErr)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.ipcServer.Shutdown(ctx); err != nil {
				t.Fatalf("ipc shutdown: %v", err)
			}
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("startIPC: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("startIPC did not return after ipcServer.Shutdown")
			}
			if tt.after != nil {
				tt.after(t, path)
			}
		})
	}
}
