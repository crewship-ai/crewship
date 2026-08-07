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

// killProcessGroup signals the whole group, falling back to the process alone
// when its group cannot be determined.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
