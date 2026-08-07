//go:build windows

package devcontainer

import "os/exec"

// Windows has no process groups in the POSIX sense, and no Apple `container`
// runtime either — this file exists so the package cross-compiles for the CLI
// build, not because the builder runs here.

func ownProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
