package buildinfo

import (
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

// RequireEmptyProcessCapabilities checks kernel credentials of the actual
// exec thread. Mounted proc files and utilities in the image are not evidence.
func RequireEmptyProcessCapabilities() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	denied := errors.New("agent runtime probe requires empty Linux capabilities")
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var caps [2]unix.CapUserData
	if err := unix.Capget(&header, &caps[0]); err != nil {
		return err
	}
	// Ambient capabilities are a subset of both permitted and inheritable.
	if caps != [2]unix.CapUserData{} {
		return denied
	}
	// PR_CAPBSET_READ returns EINVAL at the running kernel's first unsupported
	// capability. CAP_SETFCAP is part of every kernel supporting capget v3.
	// Newer build headers may know capabilities that this kernel predates.
	for cap := uintptr(0); cap <= 64; cap++ {
		set, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, cap, 0, 0, 0)
		if err == unix.EINVAL && cap > unix.CAP_SETFCAP {
			return nil
		}
		if err != nil {
			return err
		}
		if set != 0 {
			return denied
		}
	}
	return denied
}
