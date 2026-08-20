//go:build !windows

package apple

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

// TestKillProcessGroup_ReachesDescendantsOfAReapedChild is #2030's regression
// test for this provider's copy of the helper.
//
// runCLIWithin's window is narrower than the builder's — only the timeout path
// reaches killProcessGroup, and cmd.Run() reaps the child itself — but it is
// the same window: exec's watchCtx calls Cancel from its own goroutine while
// Run is waiting, so a CLI call that exits just as its deadline fires can be
// reaped before the kill looks its group up. On Darwin getpgid(2) then answers
// ESRCH for the unreaped zombie (proc_find excludes exited processes), the
// fallback re-kills the corpse, and the helpers that inherited the stdout and
// stderr pipes keep them open — so Run blocks on its copying goroutines and
// the timeout that was supposed to end the wedge never returns.
//
// Reaping the child before the kill makes that deterministic on Linux, where
// getpgid answers ESRCH for a gone pid too.
func TestKillProcessGroup_ReachesDescendantsOfAReapedChild(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "container")
	// A helper that inherits stdout and outlives the CLI that started it.
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
	pgid := cmd.Process.Pid // Setpgid makes the child its own group leader
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

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

	if err := cmd.Wait(); err != nil {
		t.Fatalf("the fake CLI should exit cleanly: %v", err)
	}
	if _, err := syscall.Getpgid(cmd.Process.Pid); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("precondition: getpgid on the reaped child should be ESRCH, got %v", err)
	}

	// The error is checked after the descendant, deliberately: whether the
	// helpers died is the property that matters, and reporting the return
	// value first would hide it behind "os: process already finished".
	killErr := killProcessGroup(cmd)

	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err = r.Read(buf)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stdout is still held after killProcessGroup (which returned %v): read %q, err %v — the descendant outlived the kill",
			killErr, buf[:n], err)
	}
	// An empty group is "already done", not a failure: exec.Cmd calls this as
	// its Cancel, and any other error there is wrapped around the command's own
	// result.
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		t.Errorf("killProcessGroup: %v", killErr)
	}
}
