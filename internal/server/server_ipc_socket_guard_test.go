package server

import (
	"bytes"
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
			// A zero-length file holds no data, so unlinking it destroys
			// nothing an operator could miss. This is the shape a half-created
			// socket or a `> ipc.sock` shell redirect leaves behind, and
			// refusing here would wedge boot over an empty file.
			name: "zero-length regular file is reclaimable",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
		{
			// The fail-open branch this replaced: "a regular file cannot have
			// a listener, therefore it is junk" also describes a mistyped
			// ipc.socket_path pointed at state.db, which now lives in the same
			// derived data dir one tab-completion away.
			name: "non-empty regular file is refused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				if err := os.WriteFile(p, []byte("junk"), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: true,
		},
		{
			// A directory is a non-socket inode just like a regular file, so
			// connect() refuses on it and the staleness test read that as "the
			// owner is gone". os.Remove then rmdir's an empty one without a
			// word. Nothing here can be listening, but nothing here is ours to
			// delete either.
			name: "empty directory is refused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: true,
		},
		{
			name: "directory with contents is refused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(p, "state.db"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: true,
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
			// there". The real-world instance is EACCES from a 0700 parent
			// directory owned by another user, hiding that user's live daemon
			// socket — but mode bits cannot be used to stage it, because root
			// traverses any directory and this suite runs as root often
			// enough (containers, CI images) that the case would silently
			// stop running there.
			//
			// ENOTDIR reaches the identical branch with no permissions
			// involved: the guard only asks whether Lstat failed with
			// something other than ENOENT, never which errno it was. A
			// regular file standing where a parent directory should be — the
			// same tab-completion slip that puts the socket path on state.db
			// — produces exactly that, on every uid.
			name: "unstattable path is refused rather than assumed stale",
			setup: func(t *testing.T, dir string) string {
				notDir := filepath.Join(dir, "state.db")
				if err := os.WriteFile(notDir, []byte("junk"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(notDir, "i.sock")
			},
			wantErr: true,
		},
		{
			// Same rule one layer down: the inode is classifiable but the dial
			// fails for a reason that is not proof of deadness, so liveness is
			// unknown and the path stays. Motivating case again a permission
			// one — connect() denied on another user's live socket — and again
			// unstageable with mode bits, since root dials it regardless.
			//
			// A self-referential symlink lands in the same fallthrough for
			// every uid: symlinks are dialed rather than resolved here (see
			// the dangling-symlink case), and connect() answers ELOOP, which
			// is neither "connection refused" nor "does not exist" and so must
			// not license the unlink.
			name: "undialable path is refused rather than assumed stale",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "i.sock")
				if err := os.Symlink(p, p); err != nil {
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
			// Not a socket, but empty: nothing can be listening on it and
			// nothing is lost by unlinking it, so the path must be reclaimed.
			// (A *non-empty* file is refused instead — see
			// TestStartIPC_DoesNotDeleteNonSocketFile.)
			name: "zero-length file at the socket path is reclaimed",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
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

// TestStartIPC_DoesNotDeleteNonSocketInode extends the data-loss guard past
// regular files. The regular-file check exists because Linux answers connect()
// on a non-socket inode with ECONNREFUSED, which the staleness test reads as
// "the owner is gone, unlink it" — but a *directory* is a non-socket inode too
// and is not Mode().IsRegular(), so it fell through to the dial and was
// classified stale. startIPC then handed it to removeSocketFile, which rmdir'd
// an empty directory outright and failed on a non-empty one with a message
// about removing a "socket file" that named neither the mistake nor the knob
// that caused it.
//
// The assertion that matters is not "startIPC returned an error" — an
// implementation that deletes the directory and then complains would pass
// that, and that deletion is the entire failure mode this guard exists to
// prevent. It is that the inode, and anything inside it, is still there
// afterwards.
func TestStartIPC_DoesNotDeleteNonSocketInode(t *testing.T) {
	tests := []struct {
		name string
		// setup creates the inode at the socket path.
		setup func(t *testing.T, path string)
		// verify asserts it survived startIPC untouched.
		verify func(t *testing.T, path string)
	}{
		{
			// The silent case: os.Remove happily rmdir's an empty directory, so
			// the operator lost the directory and then got a healthy daemon
			// listening where it used to be.
			name: "empty directory",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			verify: func(t *testing.T, path string) {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("the directory at the socket path was deleted: %v", err)
				}
				if !info.IsDir() {
					t.Fatalf("the directory at the socket path was replaced by %s", info.Mode().Type())
				}
			},
		},
		{
			// ipc.socket_path aimed one component short — at the data dir
			// itself, say — which is a directory full of things that matter.
			name: "directory with contents",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "state.db"), []byte("SQLite format 3\x00"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			verify: func(t *testing.T, path string) {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("the directory at the socket path was deleted: %v", err)
				}
				if !info.IsDir() {
					t.Fatalf("the directory at the socket path was replaced by %s", info.Mode().Type())
				}
				got, err := os.ReadFile(filepath.Join(path, "state.db"))
				if err != nil {
					t.Fatalf("the file inside the directory was deleted: %v", err)
				}
				if !bytes.Equal(got, []byte("SQLite format 3\x00")) {
					t.Errorf("file inside the directory changed: got %q", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Short dir name: sockaddr_un caps the path near 100 bytes.
			path := filepath.Join(t.TempDir(), "i.sock")
			tt.setup(t, path)

			startIPCExpectingRefusal(t, path)

			tt.verify(t, path)
		})
	}
}

// startIPCExpectingRefusal runs the real startIPC against path and asserts it
// declined to take it over, with a message an operator can act on. Shared with
// the unix-only inode cases (FIFOs, device nodes) in the sibling file.
//
// It deliberately does not assert what is left on disk: that is the caller's
// job, because "returned an error" is satisfied by an implementation that
// deletes the path first and complains afterwards.
func startIPCExpectingRefusal(t *testing.T, path string) {
	t.Helper()

	s := newTestServerForT(t)
	s.cfg.IPC.SocketPath = path

	// A refusal returns immediately; only the success path blocks in Serve,
	// and reaching it here would itself be the bug.
	errCh := make(chan error, 1)
	go func() { errCh <- s.startIPC() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("startIPC() = nil; want a refusal to reuse a non-socket path")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q does not name the socket path %q", err, path)
		}
		// The operator has to learn which knob to correct; "remove socket
		// file: directory not empty" does not say that.
		if !strings.Contains(err.Error(), "ipc.socket_path") {
			t.Errorf("error %q does not point at ipc.socket_path", err)
		}
	case <-time.After(10 * time.Second):
		// The pre-fix shape of this failure: startIPC removed the inode, bound
		// a socket over it and blocked in Serve().
		info, statErr := os.Lstat(path)
		t.Fatalf("startIPC neither refused nor returned within 10s "+
			"(it bound the path instead); path is now %v (stat err %v)", info.Mode(), statErr)
	}
}

// TestStartIPC_DoesNotDeleteNonSocketFile pins the data-loss half of the
// guard. The original rule was "a regular file cannot have a listener bound to
// it, therefore it is junk, therefore unlink it" — but the same description
// fits an operator who typed the wrong path into ipc.socket_path or
// CREWSHIP_SOCKET_PATH. state.db and crewship.db now live in the *same* derived
// data dir as the socket, one tab-completion away, so that mistake silently
// deleted a database at boot and then reported a healthy daemon listening
// where it used to be.
//
// The assertion that matters is not "startIPC returned an error" — a fix that
// deleted the file and then failed to listen would pass that. It is that the
// bytes are still on disk afterwards.
func TestStartIPC_DoesNotDeleteNonSocketFile(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
	}{
		{
			// The motivating accident: ipc.socket_path aimed at state.db.
			name:    "sqlite database",
			content: append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0xAB}, 64)...),
		},
		{
			name:    "text file",
			content: []byte("not a socket\n"),
		},
		{
			// One byte is enough to make the file non-disposable: we cannot
			// tell a truncated database from a scratch file, and the fail-closed
			// answer to "I cannot tell" is to keep it.
			name:    "single byte",
			content: []byte{'x'},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Short dir name: sockaddr_un caps the path near 100 bytes.
			path := filepath.Join(t.TempDir(), "i.sock")
			if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatal(err)
			}

			s := newTestServerForT(t)
			s.cfg.IPC.SocketPath = path

			// A refusal returns immediately; only the success path blocks in
			// Serve, and reaching it here would itself be the bug.
			errCh := make(chan error, 1)
			go func() { errCh <- s.startIPC() }()

			select {
			case err := <-errCh:
				if err == nil {
					t.Fatal("startIPC() = nil; want a refusal to reuse a non-socket path")
				}
				if !strings.Contains(err.Error(), path) {
					t.Errorf("error %q does not name the socket path %q", err, path)
				}
			case <-time.After(10 * time.Second):
				// The pre-fix shape of this failure: startIPC unlinked the
				// file, bound a socket over it and blocked in Serve(). Say
				// whether the bytes survived, so the output names the data
				// loss rather than just "timed out".
				survived := false
				if got, readErr := os.ReadFile(path); readErr == nil {
					survived = bytes.Equal(got, tt.content)
				}
				t.Fatalf("startIPC neither refused nor returned within 10s "+
					"(it bound the path instead); original file contents survived = %v", survived)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("the file at the socket path was deleted: %v", err)
			}
			if !bytes.Equal(got, tt.content) {
				t.Errorf("file contents changed: got %d bytes %q, want %d bytes %q",
					len(got), got, len(tt.content), tt.content)
			}
		})
	}
}
