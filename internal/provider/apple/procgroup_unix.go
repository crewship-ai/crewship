//go:build !windows

package apple

import (
	"errors"
	"os"
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

// processGroupOf reports the group the command's child was placed in, or 0
// when it was never given one of its own.
//
// Derived from what ownProcessGroup asked the kernel for, never looked up:
// Setpgid with a zero Pgid makes the child its own group leader at fork, so
// pgid == pid for the rest of the pid's life. syscall.Getpgid at kill time
// asks the same question of a process that may already have exited, and on
// Darwin an unreaped zombie answers ESRCH — XNU's proc_find excludes exited
// processes — where Linux still reports the group. The kill then falls back to
// the corpse and the helpers holding the pipes are never signalled (#2030).
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
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Guarded because a non-positive pgid would make this kill(-0), which is
	// every process in *this* process's group — the server included.
	if pgid := processGroupOf(cmd); pgid > 0 {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				// The group is already empty. Reported as "already done" so
				// exec.Cmd, which calls this as its Cancel, does not wrap a
				// needless error around a command that simply finished.
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	return cmd.Process.Kill()
}
