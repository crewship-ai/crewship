//go:build unix

package pagebuild

import (
	"os"
	"syscall"
)

const lockOpenNoFollow = syscall.O_NOFOLLOW

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
