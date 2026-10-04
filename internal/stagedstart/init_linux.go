//go:build linux

package stagedstart

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Initialize uses directory FDs and never follows agent-controlled symlinks.
// All roots are exact provider mounts in an offline, read-only helper.
func Initialize(roots []string) error {
	if os.Getuid() != 0 || len(roots) == 0 || len(roots) > 64 {
		return errors.New("staged init: invalid roots")
	}
	for _, spec := range roots {
		role, root, ok := strings.Cut(spec, ":")
		if !ok || (role != "tree" && role != "crew" && role != "volume") {
			return errors.New("staged init: invalid role")
		}
		if !strings.HasPrefix(root, "/mnt/init/") || strings.Contains(root, "..") {
			return errors.New("staged init: invalid root")
		}
		fd, e := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return e
		}
		if role == "volume" {
			e = unix.Fchown(fd, 1001, 1001)
			if e == nil {
				e = unix.Fchmod(fd, 0755)
			}
		} else {
			e = ownDir(fd, 0, role == "crew", false, false)
		}
		unix.Close(fd)
		if e != nil {
			return e
		}
	}
	return nil
}
func ownDir(fd, depth int, allowMemory, memoryTree, memoryRoot bool) error {
	if depth > 128 {
		return errors.New("staged init: tree too deep")
	}
	gid := 1001
	if memoryTree {
		gid = 1002
	}
	if e := unix.Fchown(fd, 1001, gid); e != nil {
		return e
	}
	if memoryRoot {
		if e := unix.Fchmod(fd, 02775); e != nil {
			return e
		}
	}
	copyFD, e := unix.Dup(fd)
	if e != nil {
		return e
	}
	file := os.NewFile(uintptr(copyFD), "init-directory")
	defer file.Close()
	entries, e := file.ReadDir(-1)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		name := entry.Name()
		child, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e == nil {
			e = ownDir(child, depth+1, allowMemory, memoryTree || (allowMemory && name == ".memory"), allowMemory && name == ".memory")
			unix.Close(child)
			if e != nil {
				return e
			}
			continue
		}
		if e != unix.ENOTDIR && e != unix.ELOOP {
			return e
		}
		if e = unix.Fchownat(fd, name, 1001, gid, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return e
		}
		if memoryTree {
			var kind unix.Stat_t
			if e := unix.Fstatat(fd, name, &kind, unix.AT_SYMLINK_NOFOLLOW); e != nil {
				return e
			}
			if kind.Mode&unix.S_IFMT != unix.S_IFREG {
				continue
			}
			child, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if e == unix.ELOOP {
				continue
			}
			if e != nil {
				return e
			}
			var st unix.Stat_t
			e = unix.Fstat(child, &st)
			if e == nil && st.Mode&unix.S_IFMT == unix.S_IFREG {
				e = unix.Fchmod(child, st.Mode&07777|0060)
			}
			unix.Close(child)
			if e != nil {
				return e
			}
		}
	}
	return nil
}
