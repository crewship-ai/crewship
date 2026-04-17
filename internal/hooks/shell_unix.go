//go:build unix

package hooks

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup makes the shell child live in its own process
// group so cmd.Cancel can signal the entire subtree on timeout, not
// just the immediate sh child. Linux unit tests that spawn
// `sh -c 'sleep 5'` with a 1s timeout hang without this because SIGKILL
// to sh leaves sleep running and the inherited stdout pipe open.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID sends to the whole process group. Ignore the
		// error — the process may have already exited between the
		// deadline trigger and this call.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil
	}
}
