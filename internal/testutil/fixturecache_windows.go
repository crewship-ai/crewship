//go:build windows

package testutil

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockFixtureCache(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 0xffffffff, 0xffffffff, &overlapped); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 0xffffffff, 0xffffffff, &overlapped)
		_ = f.Close()
	}, nil
}

func fixtureProcessDead(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code != 259 // STILL_ACTIVE
}

// Windows temp directories are scoped to the user; FileMode does not expose
// their ACL ownership. Unix validates both mode and owner.
func fixtureRootOwned(os.FileInfo) bool { return true }
