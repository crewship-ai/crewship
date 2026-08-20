//go:build !windows

package devcontainer

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestKillProcessGroup_ReachesDescendantsOfAReapedChild pins #2030 — a
// macOS-only production hang — to the Linux runners that gate every PR.
//
// On a Mac the state below arises on its own. exec.CommandContext's default
// Cancel (Process.Kill, the direct child only) and the build watchdog's group
// kill both wake on the same cancellation, so when exec's wins the child can
// already have exited by the time the group kill asks which group it is in.
// Nothing has reaped it yet — Build only reaches cmd.Wait() after the scanner
// drains — so it is a zombie, and XNU's getpgid(2) goes through proc_find(),
// which deliberately excludes exited processes (hence proc_find_zombref) and
// answers ESRCH. Linux's getpgid answers for zombies, which is why this never
// failed here.
//
// Reaping the child first reproduces that state deterministically on any
// platform: once the pid is gone, Linux answers ESRCH too. A killer that looks
// the group up at kill time then falls back to re-killing the corpse and
// orphans the descendant holding stdout — which is the production hang, where
// the scanner blocks on a pipe nobody will ever close. A killer that signals
// the group the child was started in kills it.
func TestKillProcessGroup_ReachesDescendantsOfAReapedChild(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "container")
	// The shape of the real thing: the CLI backgrounds a helper that inherits
	// stdout and then exits itself, leaving the write end of the pipe held by
	// something exec.Cmd knows nothing about.
	script := "#!/bin/sh\n( echo holding; sleep 60 ) &\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	cmd := exec.Command(fake) // #nosec G204 — path is this test's temp dir
	cmd.Stdout = w
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Under Setpgid the child leads its own group, so the group id is its pid.
	pgid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	// Drop this process's copy of the write end. From here the descendant is
	// the only thing keeping the pipe open, exactly as the CLI's helpers are
	// during a real build.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	if err := r.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "holding\n" {
		t.Fatalf("the descendant never took stdout: read %q, err %v", buf[:n], err)
	}

	// Reap the direct child, putting its pid beyond getpgid's reach — the
	// Linux-observable equivalent of Darwin's unreaped zombie.
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the fake CLI should exit cleanly: %v", err)
	}
	if _, err := syscall.Getpgid(cmd.Process.Pid); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("precondition: getpgid on the reaped child should be ESRCH, got %v", err)
	}

	killProcessGroup(cmd)

	// With the group signalled the descendant dies, the last write end closes
	// and the read returns EOF. If the kill reached only the corpse, this
	// blocks until the deadline — the production hang, where the build's
	// scanner never returns and Build leaks the goroutine, the pipe and the
	// process tree indefinitely.
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err = r.Read(buf)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stdout is still held after killProcessGroup: read %q, err %v — the descendant outlived the kill", buf[:n], err)
	}
}
