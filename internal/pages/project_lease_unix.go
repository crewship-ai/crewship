//go:build !windows

package pages

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryProjectFileLock(file *os.File, exclusive bool) (bool, error) {
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	err := unix.Flock(int(file.Fd()), mode|unix.LOCK_NB)
	// A signal-interrupted lock attempt may be retried within the lease deadline.
	if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
