//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package mooring

import (
	"os"

	"golang.org/x/sys/unix"
)

func openOriginNodeLock(root *os.Root) (*os.File, error) {
	return root.OpenFile(originNodeLockName, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
}
