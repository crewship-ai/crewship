//go:build unix

package documents

import (
	"os"
	"syscall"
)

func openServiceSecret(path string) (*os.File, error) {
	// No symlink following, and no blocking open if the path becomes a FIFO.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
