//go:build !windows

package apple

import (
	"os/exec"
	"syscall"
)

// Process-group control, split by platform: syscall.Setpgid, syscall.Getpgid
// and syscall.Kill do not exist on Windows, and the repo cross-compiles the CLI
// for it. This provider only ever runs on macOS, but its package still has to
// build everywhere.

// ownProcessGroup puts the command in its own group so a timeout can take the
// whole tree down. Killing only the CLI leaves its helpers holding the pipes,
// which is how a wedged call stayed stuck after its deadline fired.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the whole group, falling back to the process alone.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return cmd.Process.Kill()
}
