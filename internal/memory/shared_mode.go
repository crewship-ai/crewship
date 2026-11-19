package memory

import (
	"os"
	"path/filepath"
)

// Writer-side half of the ownership contract in ownership.go (#4777).
//
// MemoryOwnershipCmd puts group 1002 and group write on the tree when a
// container starts, but the agent, the sidecar and the host keep writing
// between starts, and the service units run with UMask=0022. A writer that
// relied on its create mode therefore left 0600 locks, 0644 files and 0755
// directories behind, and the next writer under a different uid was refused
// until the container restarted. Each writer now restores group write
// itself, through the descriptor it created, whenever it writes into the
// shared tree.
//
// "The shared tree" is recognised by the setgid bit on the parent
// directory: MemoryOwnershipCmd sets it on every `.memory` directory and
// Linux propagates it to subdirectories, and no other directory Crewship
// writes to carries it. Outside such a directory nothing changes.
const (
	// MemoryDirPerm is MemoryDirMode as an os.FileMode.
	MemoryDirPerm os.FileMode = os.ModeSetgid | 0o775
	// memoryLockPerm is the mode of a lock sentinel in the shared tree:
	// every writer must be able to open it O_RDWR to flock it.
	memoryLockPerm os.FileMode = 0o660
)

// inSharedTree reports whether dir is a setgid directory, the marker of a
// memory tree that several uids write to through group 1002.
func inSharedTree(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && isSharedDir(fi)
}

func isSharedDir(fi os.FileInfo) bool {
	return fi != nil && fi.IsDir() && fi.Mode()&os.ModeSetgid != 0
}

// sharedFilePerm adds group write to perm for a file in the shared tree.
// A file its writer made deliberately private (no group read, e.g. the
// 0600 quarantine copies) stays private.
func sharedFilePerm(shared bool, perm os.FileMode) os.FileMode {
	if shared && perm&0o040 != 0 {
		return perm | 0o020
	}
	return perm
}

// shareLockFile gives a lock sentinel in the shared tree mode 0660. It also
// repairs a legacy 0600 sentinel this process owns. A sentinel owned by
// another uid cannot be changed here (EPERM); having opened it read-write
// already, the caller has the access it needs, so that is not an error.
func shareLockFile(f *os.File, shared bool) {
	if !shared {
		return
	}
	if fi, err := f.Stat(); err == nil && fi.Mode().Perm() == memoryLockPerm {
		return
	}
	_ = f.Chmod(memoryLockPerm)
}

// MkdirAllShared is os.MkdirAll(dir, 0o775) for the memory tree: every
// directory it creates under a setgid parent is set to MemoryDirPerm
// (02775), because the umask strips the group write the mode asked for.
// Directories that already existed are left alone. Outside the shared tree
// it behaves exactly like os.MkdirAll(dir, 0o775) under the process umask.
func MkdirAllShared(dir string) error {
	dir = filepath.Clean(dir)
	var created []string
	for d := dir; ; {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		created = append(created, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return err
	}
	for i := len(created) - 1; i >= 0; i-- {
		d := created[i]
		if !inSharedTree(filepath.Dir(d)) {
			continue
		}
		if err := chmodDirNoFollow(d, MemoryDirPerm); err != nil {
			return err
		}
	}
	return nil
}
