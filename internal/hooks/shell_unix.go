//go:build unix

package hooks

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand builds the host-shell invocation for a hook command line.
// POSIX hosts run it under `sh -c` — the contract hook authors have always
// had. nosemgrep: dangerous-exec-command — intentional; trust model
// documented in shell.go (hook config = owner-only, env sanitized, timeout
// bounded).
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command) // nosemgrep: dangerous-exec-command
}

// hookBaseEnv is the deliberately minimal, fixed environment hook commands
// run under — a pinned PATH rather than the daemon's own environment, so a
// hook can't inherit secrets the daemon holds.
func hookBaseEnv() []string {
	return []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
}

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
