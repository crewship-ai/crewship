//go:build windows

package memory

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// openNoFollow opens path read-only without following a final-component
// symlink. See readRegularNoFollow (index.go) for the full threat-model
// rationale.
//
// Windows has no O_NOFOLLOW; the equivalent is opening with
// FILE_FLAG_OPEN_REPARSE_POINT, which opens the reparse point (symlink,
// junction, …) ITSELF instead of its target, and then rejecting any
// handle that is a reparse point. Like the Unix variant this is
// race-free: the attribute check runs on the already-open handle, so a
// symlink swapped in after our open cannot redirect the read.
//
// The Unix FIFO/O_NONBLOCK concern has no Windows analogue — named
// pipes live in the \\.\pipe\ namespace, not in directory trees — and
// the caller's IsRegular re-check still rejects any other special file.
func openNoFollow(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		_ = windows.CloseHandle(h)
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("symlink or reparse point rejected: %s", path)
	}
	return os.NewFile(uintptr(h), path), nil
}
