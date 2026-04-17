//go:build windows

package hooks

import "os/exec"

// configureProcessGroup is a no-op on Windows — the codebase doesn't
// target Windows agents in production, and Windows job objects need
// different wiring than POSIX process groups. Keeping the symbol so
// shell.go builds on the cross-platform path.
func configureProcessGroup(_ *exec.Cmd) {}
