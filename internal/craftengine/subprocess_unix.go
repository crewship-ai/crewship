//go:build !windows

package craftengine

import (
	"os/exec"
	"syscall"
)

func setWorkerProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killWorkerProcess(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
