//go:build !windows

package main

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a final-component
// symlink, mirroring internal/sidecar's openNoFollow contract for the seed
// auth file: O_NOFOLLOW fails the open (ELOOP) on a symlink and O_NONBLOCK
// keeps a swapped-in FIFO from hanging the read; the caller re-Stats the
// handle and rejects anything non-regular.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// authFilePermOK reports whether the handle's permission bits keep the auth
// file private to its owner. Unix mode bits are the real access control.
func authFilePermOK(_ *os.File, info os.FileInfo) bool {
	return info.Mode().Perm()&0077 == 0
}
