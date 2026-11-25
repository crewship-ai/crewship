//go:build linux

package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Never follow a link or enter even a same-device bind mount. A symlink
// returns a nil file: callers copy or keep the link itself. A hard-linked
// file cannot be cloned independently without changing its alias semantics.
func openForceNode(path string, mount uint64) (*os.File, unix.Stat_t, error) {
	var stat unix.Stat_t
	id, err := forceMount(path)
	if err != nil {
		return nil, stat, err
	}
	if id != mount {
		return nil, stat, fmt.Errorf("recovery crosses a mount at %s", path)
	}
	if err = unix.Lstat(path, &stat); err != nil {
		return nil, stat, err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		// A link is copied as a link and never opened: there is no handle.
		return nil, stat, nil
	case unix.S_IFDIR:
	case unix.S_IFREG:
		if stat.Nlink != 1 {
			return nil, stat, fmt.Errorf("recovery cannot isolate hard-linked file %s", path)
		}
	default:
		return nil, stat, fmt.Errorf("unsupported recovery entry %s", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOATIME|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, stat, err
	}
	f := os.NewFile(uintptr(fd), path)
	var opened unix.Stat_t
	if err = unix.Fstat(fd, &opened); err != nil || opened.Dev != stat.Dev || opened.Ino != stat.Ino {
		f.Close()
		return nil, stat, errors.Join(err, fmt.Errorf("recovery entry changed: %s", path))
	}
	return f, opened, nil
}

// forceCopy collects what a clone leaves out. root is the source's top.
type forceCopy struct {
	root    string
	skipped []string
}

func cloneForceTree(ctx context.Context, src, dst string, mount uint64) error {
	return cloneForceNode(ctx, src, dst, mount, &forceCopy{root: src})
}

func cloneForceNode(ctx context.Context, src, dst string, mount uint64, fc *forceCopy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fc.root != src {
		// A socket belongs to a process, not to the data: after a crash the
		// stale one only blocks the next start. Leave it out and say so.
		var st unix.Stat_t
		if err := unix.Lstat(src, &st); err == nil && st.Mode&unix.S_IFMT == unix.S_IFSOCK {
			rel, _ := filepath.Rel(fc.root, src)
			fc.skipped = append(fc.skipped, rel)
			return nil
		}
	}
	in, st, err := openForceNode(src, mount)
	if err != nil {
		return err
	}
	if in == nil {
		return cloneForceLink(src, dst, &st)
	}
	defer in.Close()
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		if err = os.Mkdir(dst, 0700); err != nil {
			return err
		}
		if err = visitForceChildren(ctx, in, func(name string) error {
			return cloneForceNode(ctx, filepath.Join(src, name), filepath.Join(dst, name), mount, fc)
		}); err != nil {
			return err
		}
	} else if err := writeBlobDurable(ctx, dst, uploadContextReader{ctx: ctx, reader: in}, ""); err != nil {
		return err
	}

	// Chown can clear set-ID and capability bits. Set those only after the
	// ownership is correct, and restore ACLs after chmod's ACL mask update.
	if err = os.Chown(dst, int(st.Uid), int(st.Gid)); err != nil {
		return err
	}
	if err = unix.Chmod(dst, st.Mode&07777); err != nil {
		return err
	}
	if err = copyForceAttrs(src, dst); err != nil {
		return err
	}
	if err = unix.UtimesNanoAt(unix.AT_FDCWD, dst, []unix.Timespec{st.Atim, st.Mtim}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	return in.Sync()
}

// cloneForceLink copies a symlink as a link: the same target text, owner,
// attributes and times. It never reads through the link, so a link that
// points outside the installation, or nowhere, is kept as it is.
func cloneForceLink(src, dst string, st *unix.Stat_t) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err = os.Symlink(target, dst); err != nil {
		return err
	}
	if err = unix.Fchownat(unix.AT_FDCWD, dst, int(st.Uid), int(st.Gid), unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if err = copyForceAttrs(src, dst); err != nil {
		return err
	}
	return unix.UtimesNanoAt(unix.AT_FDCWD, dst, []unix.Timespec{st.Atim, st.Mtim}, unix.AT_SYMLINK_NOFOLLOW)
}

const maxForceObstacles = 20

// scanForceTree lists every entry that stops a recovery copy BEFORE anything
// is copied: hard-linked files, special files other than sockets, and mount
// crossings. Links and sockets are not obstacles. It reads only (Lstat), so
// the operator sees the whole list at once, not one error per attempt.
func scanForceTree(ctx context.Context, root string, mount uint64) error {
	var found []string
	total := 0
	note := func(path, why string) {
		total++
		if len(found) < maxForceObstacles {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel+" ("+why+")")
		}
	}
	var walk func(path string) error
	walk = func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := forceMount(path)
		if err != nil {
			return err
		}
		if id != mount {
			note(path, "another mount")
			return nil
		}
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			return err
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK, unix.S_IFSOCK:
			return nil
		case unix.S_IFREG:
			if st.Nlink != 1 {
				note(path, "hard-linked file")
			}
			return nil
		case unix.S_IFDIR:
		default:
			note(path, "special file")
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := walk(filepath.Join(path, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return err
	}
	if total == 0 {
		return nil
	}
	more := ""
	if total > len(found) {
		more = fmt.Sprintf("; and %d more", total-len(found))
	}
	return fmt.Errorf("recovery cannot copy the current data directory: %d entr(y/ies) block it: %s%s", total, strings.Join(found, "; "), more)
}

func visitForceChildren(ctx context.Context, dir *os.File, visit func(string) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := dir.Readdirnames(128)
		for _, name := range names {
			if e := visit(name); e != nil {
				return e
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Recovery may run as root for an installation owned by a service UID. Files
// replaced by extraction retain the original owner; newly added descendants
// inherit ownership from the containing staged directory. Keep the access
// mode and ACL mask set by database.Open on the staged root (0700); the
// retained generation still has the operator's original directory permissions.
func syncForceTree(ctx context.Context, path, original string, mount uint64, uid, gid uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, st, err := openForceNode(path, mount)
	if err != nil {
		return err
	}
	if f != nil {
		defer f.Close()
	}
	if original != "" {
		var old unix.Stat_t
		switch e := unix.Lstat(original, &old); {
		case e == nil:
			uid, gid = old.Uid, old.Gid
		case errors.Is(e, unix.ENOENT), errors.Is(e, unix.ENOTDIR):
		default:
			return e
		}
	}
	if f == nil {
		// A symlink: change the link's own owner, never its target's.
		if st.Uid != uid || st.Gid != gid {
			return unix.Fchownat(unix.AT_FDCWD, path, int(uid), int(gid), unix.AT_SYMLINK_NOFOLLOW)
		}
		return nil
	}
	if st.Uid != uid || st.Gid != gid {
		if err = f.Chown(int(uid), int(gid)); err != nil {
			return err
		}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		if err = visitForceChildren(ctx, f, func(name string) error {
			old := ""
			if original != "" {
				old = filepath.Join(original, name)
			}
			return syncForceTree(ctx, filepath.Join(path, name), old, mount, uid, gid)
		}); err != nil {
			return err
		}
	}
	return f.Sync()
}

func forceAttrs(path string) (map[string][]byte, error) {
	// Linux bounds both a single value and the entire name list to 64 KiB.
	size, err := unix.Llistxattr(path, nil)
	if errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	attrs := map[string][]byte{}
	if size == 0 {
		return attrs, nil
	}
	buf := make([]byte, size)
	n, err := unix.Llistxattr(path, buf)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(strings.TrimSuffix(string(buf[:n]), "\x00"), "\x00") {
		size, err := unix.Lgetxattr(path, name, nil)
		if err != nil {
			return nil, err
		}
		value := make([]byte, size)
		n, err := unix.Lgetxattr(path, name, value)
		if err != nil {
			return nil, err
		}
		if n != size {
			return nil, fmt.Errorf("attribute %s changed during recovery", name)
		}
		attrs[name] = value
	}
	return attrs, nil
}

func copyForceAttrs(src, dst string) error {
	want, err := forceAttrs(src)
	if err != nil {
		return err
	}
	have, err := forceAttrs(dst)
	if err != nil {
		return err
	}
	for name := range have {
		if _, ok := want[name]; !ok {
			if err = unix.Lremovexattr(dst, name); err != nil {
				return err
			}
		}
	}
	for name, value := range want {
		if old, ok := have[name]; ok && bytes.Equal(value, old) {
			continue
		}
		if err = unix.Lsetxattr(dst, name, value, 0); err != nil {
			return err
		}
	}
	return nil
}
