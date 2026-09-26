//go:build windows

package main

import (
	"fmt"
	"os"
	"unsafe"

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

// authFilePermOK enforces the owner-only contract the unix variant reads
// off the mode bits. Windows mode bits cannot express 0600 (Go reports
// 0444 or 0666), so the real access control is the file's DACL: read
// access is allowed only for the file's owner, SYSTEM and the built-in
// Administrators group — every other principal (a specific other user, a
// custom group, Everyone, …) refuses. The check runs on the already-open
// handle's security descriptor, so it cannot race with a path swap. A
// missing or NULL DACL (no restrictions at all) refuses as well.
func authFilePermOK(f *os.File, info os.FileInfo) bool {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false
	}
	systemSID, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return false
	}
	adminsSID, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		return false
	}
	const aclHeaderSize = 8
	const readMask = windows.FILE_GENERIC_READ | windows.GENERIC_READ | windows.GENERIC_ALL | windows.MAXIMUM_ALLOWED
	offset := uintptr(unsafe.Pointer(dacl)) + aclHeaderSize
	for i := uint16(0); i < dacl.AceCount; i++ {
		header := (*windows.ACE_HEADER)(unsafe.Pointer(offset))
		if header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE {
			ace := (*windows.ACCESS_ALLOWED_ACE)(unsafe.Pointer(offset))
			if ace.Mask&readMask != 0 {
				sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
				if !windows.EqualSid(sid, owner) && !windows.EqualSid(sid, systemSID) && !windows.EqualSid(sid, adminsSID) {
					return false
				}
			}
		}
		offset += uintptr(header.AceSize)
	}
	return true
}
