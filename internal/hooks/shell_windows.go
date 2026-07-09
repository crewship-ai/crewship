//go:build windows

package hooks

import (
	"context"
	"os"
	"os/exec"
)

// configureProcessGroup is a no-op on Windows — Windows job objects need
// different wiring than POSIX process groups; CommandContext's default
// kill covers the immediate cmd.exe child. Keeping the symbol so
// shell.go builds on the cross-platform path.
func configureProcessGroup(_ *exec.Cmd) {}

// shellCommand builds the host-shell invocation for a hook command line.
// Windows hosts (#946) run hooks under `cmd.exe /c` — the platform
// equivalent of `sh -c`. Hook authors targeting Windows write cmd syntax
// (documented in guides/hooks.mdx).
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	return exec.CommandContext(ctx, comspec, "/c", command) // nosemgrep: dangerous-exec-command
}

// hookBaseEnv mirrors the unix sanitized-environment posture with the
// minimal set cmd.exe and console tools need to function: a pinned
// System32 PATH plus SystemRoot/ComSpec (many Windows binaries fail to
// start without them). Nothing else from the daemon's environment leaks
// into hooks.
func hookBaseEnv() []string {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = systemRoot + `\System32\cmd.exe`
	}
	return []string{
		"PATH=" + systemRoot + `\System32;` + systemRoot,
		"SystemRoot=" + systemRoot,
		"ComSpec=" + comspec,
	}
}
