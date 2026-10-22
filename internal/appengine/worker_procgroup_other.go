//go:build windows

package appengine

import "os/exec"

// The offline worker runs on Linux only; this keeps the package building for
// the Windows CLI and kills at least the engine process itself.
func ownProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
