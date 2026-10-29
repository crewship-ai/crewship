//go:build linux && amd64

// Package i386probe issues one i386 system call through int $0x80. Tests use
// it to prove the engine seccomp filter refuses foreign-architecture calls; it
// is not linked into the worker.
package i386probe

// Getpid calls i386 getpid (20) through int $0x80 and returns the raw result:
// the PID, or a negative errno.
func Getpid() int32
