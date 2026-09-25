//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// openNoFollow opens path read-only without following a final-component
// symlink, mirroring internal/sidecar's openNoFollow contract for the seed
// auth file. Windows has no O_NOFOLLOW; the equivalent is opening with
// FILE_FLAG_OPEN_REPARSE_POINT, which opens the reparse point itself
// instead of its target, and then rejecting any handle that is a reparse
// point. Like the Unix variant this is race-free: the attribute check
// runs on the already-open handle.
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

// authFilePermOK mirrors the unix variant's owner-only check as far as
// Windows allows. Go surfaces Windows permissions as 0444 or 0666 (the
// read-only bit), both with group/other bits set, so the unix test would
// reject every regular file. The real access control is the file's ACL;
// a profile-scoped auth.json inherits the profile's protection, and a full
// ACL audit is out of scope for the seed bootstrap (same precedent as
// internal/sidecar's windows openNoFollow, which checks reparse points
// only).
func authFilePermOK(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
