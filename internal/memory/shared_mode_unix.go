//go:build unix

package memory

import (
	"fmt"
	"os"
	"syscall"
)

// shareNewDir applies sharedDirTarget to dir, a directory just created,
// through a descriptor opened with O_NOFOLLOW|O_DIRECTORY. A symlink
// swapped in after the mkdir therefore cannot redirect the chmod.
func shareNewDir(dir string) error {
	d, err := openConfinedDir(dir)
	if err != nil {
		return fmt.Errorf("open shared dir %s: %w", dir, err)
	}
	defer func() { _ = d.close() }()
	if err := shareDirFile(d.file); err != nil {
		return fmt.Errorf("chmod shared dir %s: %w", dir, err)
	}
	return nil
}

// shareConfinedChild applies sharedDirTarget to child, a directory just
// created beneath parent, when parent is in the shared tree.
func shareConfinedChild(parent, child *confinedDir) error {
	fi, err := parent.file.Stat()
	if err != nil || !isSharedDir(fi) {
		return err
	}
	return shareDirFile(child.file)
}

// canKeepSetgid reports whether a chmod by this process keeps the setgid
// bit on fi. Linux clears S_ISGID when the caller is neither in the
// file's group nor privileged.
func canKeepSetgid(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if os.Geteuid() == 0 || int(st.Gid) == os.Getegid() {
		return true
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	for _, g := range groups {
		if g == int(st.Gid) {
			return true
		}
	}
	return false
}
