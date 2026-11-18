//go:build windows

package runtimestage

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func agentCLINoFollowFlag() int { return 0 }

// Windows mode bits encode the DOS read-only attribute, not NTFS custody.
// Initial path checks are structural; opened handles must pass the DACL check.
func safeAgentCLIFile(info os.FileInfo, immutable bool) bool {
	return info != nil && info.Mode().IsRegular() && (!immutable || info.Mode().Perm()&0222 == 0)
}

func safeAgentCLIHandle(file *os.File, info os.FileInfo, immutable bool) bool {
	if !safeAgentCLIFile(info, immutable) {
		return false
	}
	handle := windows.Handle(file.Fd())
	var metadata windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &metadata) != nil || metadata.NumberOfLinks != 1 || metadata.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return false
	}
	return agentCLIWindowsHandleCustody(handle, false, false)
}

func safeAgentCLIDirectoryAt(path string, info os.FileInfo, private bool) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var metadata windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &metadata) != nil || metadata.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || metadata.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return false
	}
	return agentCLIWindowsHandleCustody(handle, true, private)
}

func agentCLIWindowsHandleCustody(handle windows.Handle, directory, private bool) bool {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	trusted := []*windows.SID{user.User.Sid}
	for _, name := range []string{"S-1-5-18", "S-1-5-32-544"} {
		sid, err := windows.StringToSid(name)
		if err != nil {
			return false
		}
		trusted = append(trusted, sid)
	}
	// Windows-protected system ancestors may be owned by TrustedInstaller.
	// Resolve the local service SID through the OS instead of embedding a guess.
	if sid, _, _, err := windows.LookupSID("", "NT SERVICE\\TrustedInstaller"); err == nil {
		trusted = append(trusted, sid)
	}
	return agentCLIWindowsDescriptorCustody(descriptor, trusted, directory, private)
}

// Conservatively reject unevaluated ACE kinds and any effective modification
// grant to another principal. Inherit-only ACEs do not apply to this object;
// each child is checked independently on its own security descriptor.
func agentCLIWindowsDescriptorCustody(descriptor *windows.SECURITY_DESCRIPTOR, trusted []*windows.SID, directory, private bool) bool {
	if descriptor == nil {
		return false
	}
	defer runtime.KeepAlive(descriptor)
	owner, _, err := descriptor.Owner()
	if err != nil {
		return false
	}
	isTrusted := func(sid *windows.SID) bool {
		for _, candidate := range trusted {
			if windows.EqualSid(candidate, sid) {
				return true
			}
		}
		return false
	}
	if !isTrusted(owner) {
		return false
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return false
	}
	mask := windows.ACCESS_MASK(windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA | windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.MAXIMUM_ALLOWED)
	if directory {
		// Creating a different child does not replace an existing trusted child.
		// Deleting children does, even when those children are read-only.
		// FILE_DELETE_CHILD (WinNT.h); x/sys names only the file aliases.
		mask |= 0x40
		if private {
			mask |= windows.FILE_LIST_DIRECTORY | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_TRAVERSE | windows.FILE_READ_DATA | windows.FILE_READ_EA | windows.GENERIC_READ | windows.GENERIC_EXECUTE
		}
	} else {
		mask |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA
	}
	for index := uint16(0); index < acl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		// Let the OS bounds-check/index the ACL instead of walking its private
		// layout or assuming every ACE uses the ordinary allowed-ACE layout.
		if windows.GetAce(acl, uint32(index), &ace) != nil || ace == nil {
			return false
		}
		header := (*windows.ACE_HEADER)(unsafe.Pointer(ace))
		if header.AceSize < uint16(unsafe.Sizeof(windows.ACE_HEADER{})) {
			return false
		}
		if header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if header.AceSize < 16 {
				return false
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !sid.IsValid() || sid.Len() > int(header.AceSize)-8 {
				return false
			}
			if ace.Mask&mask != 0 && !isTrusted(sid) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
