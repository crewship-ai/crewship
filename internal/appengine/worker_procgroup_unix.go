//go:build !windows

package appengine

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup makes the engine the leader of a new process group so every
// descendant can be killed with it.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the whole group the engine led. The group id equals
// the leader's pid for its lifetime, including after it exited.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
