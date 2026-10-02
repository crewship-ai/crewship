//go:build linux

package writerlease

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lock(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrHeld
		}
		return err
	}
}
