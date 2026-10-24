//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package mooring

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const originBurnSupported = true

func openOriginBurnFile(root *os.Root, flags int) (*os.File, error) {
	return root.OpenFile(originBurnFilename, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
}
func lockOriginBurnFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
func originBurnSafeFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
