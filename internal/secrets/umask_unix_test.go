//go:build !windows

package secrets

import "syscall"

func syscallUmask(mask int) int { return syscall.Umask(mask) }
