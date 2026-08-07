//go:build windows

package apple

import "os/exec"

// Windows has no POSIX process groups and no Apple `container` runtime. This
// file exists so the package cross-compiles for the CLI build.

func ownProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
