package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Writer-side half of the ownership contract in ownership.go (#4777).
//
// MemoryOwnershipCmd puts group 1002 and group write on the tree when a
// container starts. The agent, the sidecar and the host keep writing
// between starts, and the service units run with UMask=0022. A writer that
// relied on its create mode therefore left 0600 locks, 0644 files and 0755
// directories behind, and the next writer under a different uid was
// refused until the container restarted. Each writer now restores group
// write itself, through the descriptor it holds, when it writes into the
// shared tree.
//
// "The shared tree" is recognised by the setgid bit on the parent
// directory. MemoryOwnershipCmd sets it on every `.memory` directory and
// Linux propagates it to subdirectories.
//
// The change is additive only. It starts from the mode the entry actually
// got (so the process umask still decides read access) and adds group
// write where group read is already there, plus setgid on directories.
// It never adds group or other read. Lock sentinels are the one exception:
// they are empty, so they are made 0660 regardless of the umask.
const (
	// MemoryDirPerm is MemoryDirMode as an os.FileMode.
	MemoryDirPerm os.FileMode = os.ModeSetgid | 0o775
	// memoryLockPerm is the mode of a lock sentinel in the shared tree:
	// every writer must be able to open it O_RDWR to flock it. Sentinels
	// hold no data, so group read reveals nothing.
	memoryLockPerm os.FileMode = 0o660
)

// errDotDotPath refuses a directory path that still contains ".." after
// cleaning. Memory paths are built from validated IDs and slugs, so such a
// path is never legitimate here.
var errDotDotPath = errors.New("memory: refusing path containing \"..\"")

// inSharedTree reports whether dir is a setgid directory, the marker of a
// memory tree that several uids write to through group 1002. A path that
// still contains ".." after cleaning is never treated as shared.
func inSharedTree(dir string) bool {
	dir = filepath.Clean(dir)
	if strings.Contains(dir, "..") {
		return false
	}
	fi, err := os.Stat(dir)
	return err == nil && isSharedDir(fi)
}

func isSharedDir(fi os.FileInfo) bool {
	return fi != nil && fi.IsDir() && fi.Mode()&os.ModeSetgid != 0
}

// shareFile adds group write to f when it already has group read. f is a
// file this writer just created in the shared tree. Its current mode comes
// from fstat, so read access stays exactly what the umask allowed.
func shareFile(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	cur := fi.Mode().Perm()
	if cur&0o040 == 0 || cur&0o020 != 0 {
		return nil
	}
	return f.Chmod(cur | 0o020)
}

// sharedDirTarget returns the mode a directory just created in the shared
// tree should get: its current mode plus setgid, plus group write when it
// has group read. ok is false when nothing should change, which includes
// the case where this process is not a member of the directory's group.
// There a chmod would make the kernel clear the setgid bit the directory
// inherited, and files below it would lose group 1002.
func sharedDirTarget(fi os.FileInfo) (os.FileMode, bool) {
	cur := fi.Mode() & (os.ModePerm | os.ModeSetgid)
	want := cur | os.ModeSetgid
	if cur&0o040 != 0 {
		want |= 0o020
	}
	if want == cur || !canKeepSetgid(fi) {
		return 0, false
	}
	return want, true
}

// shareDirFile applies sharedDirTarget to the directory open as f.
func shareDirFile(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if mode, ok := sharedDirTarget(fi); ok {
		return f.Chmod(mode)
	}
	return nil
}

// shareLockFile gives a lock sentinel in the shared tree mode 0660. It also
// repairs a legacy 0600 sentinel this process owns. A sentinel owned by
// another uid cannot be changed here (EPERM). The caller has already
// opened it read-write, so it has the access it needs, and that is not an
// error.
func shareLockFile(f *os.File, shared bool) {
	if !shared {
		return
	}
	if fi, err := f.Stat(); err == nil && fi.Mode().Perm() == memoryLockPerm {
		return
	}
	_ = f.Chmod(memoryLockPerm)
}

// MkdirAllShared is os.MkdirAll(dir, 0o775) for the memory tree. It
// creates one level at a time. Each directory this call creates under a
// setgid parent gets sharedDirTarget's mode (02775 under a 0022 umask),
// set through a descriptor opened on that directory. A directory that
// already exists, or that another writer creates concurrently, is left
// alone. Outside the shared tree it behaves like os.MkdirAll(dir, 0o775).
func MkdirAllShared(dir string) error {
	dir = filepath.Clean(dir)
	if strings.Contains(dir, "..") {
		return fmt.Errorf("%w: %s", errDotDotPath, dir)
	}
	if fi, err := os.Stat(dir); err == nil {
		if fi.IsDir() {
			return nil
		}
		return &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.ENOTDIR}
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := MkdirAllShared(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o775); err != nil {
		// Another writer won the race; it applies the contract itself.
		if fi, statErr := os.Stat(dir); statErr == nil && fi.IsDir() {
			return nil
		}
		return err
	}
	if !inSharedTree(parent) {
		return nil
	}
	return shareNewDir(dir)
}
