//go:build !windows

package devcontainer

import (
	"os/exec"
	"syscall"
)

// Process-group control, split by platform because syscall.Setpgid,
// syscall.Getpgid and syscall.Kill do not exist on Windows — and the repo
// cross-compiles the CLI for it (the `windows clionly` leg of CI). Apple's
// `container` runs only on macOS, but the package it lives in still has to
// build everywhere.

// ownProcessGroup puts the command in its own group so the whole tree can be
// signalled. Killing only the CLI leaves its helpers holding the pipes, which
// is how a wedged build stayed stuck even after its watchdog fired.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processGroupOf reports the group the command's child was placed in, or 0
// when it was never given one of its own.
//
// It derives the id from what ownProcessGroup asked the kernel for instead of
// looking it up, and that distinction is the whole of #2030. Setpgid with a
// zero Pgid makes the child its own group leader at fork, so pgid == pid for
// the rest of the pid's life — including the window where the child has exited
// and nothing has reaped it yet. syscall.Getpgid cannot answer for that window
// on Darwin: XNU's proc_find deliberately excludes exited processes (hence the
// separate proc_find_zombref), so it returns ESRCH where Linux still reports
// the group. A cancelled build landed in exactly that window, fell back to
// re-killing the corpse, and left the descendants that inherited stdout alive
// with nobody ever going to read them again.
func processGroupOf(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return 0
	}
	if cmd.SysProcAttr.Pgid != 0 {
		return cmd.SysProcAttr.Pgid
	}
	// Readable after Wait too: os clears Process.Pid only on an explicit
	// Release, which exec.Cmd never calls.
	return cmd.Process.Pid
}

// killProcessGroup signals the whole group, falling back to the process alone
// when the command was never put in a group of its own.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Guarded because a non-positive pgid would make this kill(-0), which is
	// every process in *this* process's group — the server included.
	if pgid := processGroupOf(cmd); pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
