//go:build windows

package writerlease

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func lock(file *os.File) error {
	// A byte far beyond SQLite's data/locking region. Windows permits locking
	// beyond EOF; the offset neither extends nor changes the database.
	overlap := windows.Overlapped{OffsetHigh: 0x40000000}
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlap)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrHeld
	}
	return err
}

func supportedStorage(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		return err
	}
	buffer := make([]uint16, 32768)
	if err = windows.GetVolumePathName(name, &buffer[0], uint32(len(buffer))); err != nil {
		return err
	}
	if windows.GetDriveType(&buffer[0]) != windows.DRIVE_FIXED {
		return ErrUnsupportedStorage
	}
	return nil
}
