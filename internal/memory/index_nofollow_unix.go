//go:build !windows

package memory

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a final-component
// symlink. See readRegularNoFollow (index.go) for the full threat-model
// rationale.
//
//   - O_NOFOLLOW makes the open syscall fail (with ELOOP) if path's
//     final component is a symlink.
//   - O_NONBLOCK keeps Open from hanging when an attacker swaps a
//     regular .md for a FIFO with no writer; the caller re-Stats and
//     rejects anything non-regular afterwards.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// openRootNoFollow opens name beneath root and refuses a final-component
// symlink. os.Root.OpenFile follows such a link when its target stays inside
// the root, even with O_NOFOLLOW (#4893), so the name is Lstat'ed first and
// the opened descriptor must be the same file. Swapping the entry for a link
// after the Lstat makes the open resolve elsewhere and the SameFile check
// fails; O_NOFOLLOW still refuses links that leave the root.
func openRootNoFollow(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.ELOOP}
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.ELOOP}
	}
	return f, nil
}
