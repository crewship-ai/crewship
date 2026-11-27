//go:build !windows

package craftengine

import "syscall"

// killSelf ends the process the way an out-of-memory killer does.
func killSelf() { _ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL) }
