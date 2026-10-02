//go:build darwin

package writerlease

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Darwin uses one advisory lock space for flock and SQLite's fcntl locks.
// Keep ownership on the actual inode, but reserve one byte outside SQLite's
// database and locking regions. An OFD lock survives closing unrelated SQLite
// descriptors and is released only when this private descriptor closes.
// F_OFD_SETLK=90 is defined in Apple's bsd/sys/fcntl.h; x/sys does not expose it.
const darwinOFDSetLK = 90

func lock(file *os.File) error {
	region := unix.Flock_t{Type: unix.F_WRLCK, Whence: int16(os.SEEK_SET), Start: 1 << 62, Len: 1}
	for {
		err := unix.FcntlFlock(file.Fd(), darwinOFDSetLK, &region)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EACCES) {
			return ErrHeld
		}
		// Fail closed on unsupported kernels; never fall back to a process-scoped
		// F_SETLK lock, which SQLite descriptor closes could silently release.
		return err
	}
}
