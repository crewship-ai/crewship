//go:build !windows

package memory

import "syscall"

func syscallUmask(mask int) int { return syscall.Umask(mask) }
