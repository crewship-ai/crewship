//go:build unix

package testutil

import (
	"os"
	"syscall"
)

const sharedTemplateLockSupported = true

// lockFile takes an exclusive flock on path, creating it if needed. With wait
// false it returns an error instead of blocking when another process holds it.
// The returned func releases the lock and closes the file.
func lockFile(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
