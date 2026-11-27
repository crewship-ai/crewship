//go:build windows

package craftengine

import "os/exec"

func setWorkerProcAttr(*exec.Cmd) {}

func killWorkerProcess(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
