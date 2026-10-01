//go:build windows

package resourcelifecycle

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryExclusiveLock(file *os.File) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY | windows.LOCKFILE_EXCLUSIVE_LOCK)
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
