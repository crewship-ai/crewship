//go:build linux

package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/writerlease"
	"golang.org/x/sys/unix"
)

// An exchange leaves the old generation at the scratch name. The enclosing
// private directory protects both incomplete plaintext and the retained copy.
type forceStage struct {
	dir, previous, target, container string
	scratch                          *os.Root
	parent, holder                   *os.File
	old, next                        *os.File
	anchors                          []*os.File
	mount                            uint64
	published                        bool
}

func beginForceStage(ctx context.Context, target string) (*forceStage, error) {
	return prepareForceStage(ctx, target, cloneForceTree)
}

func prepareForceStage(ctx context.Context, target string, clone func(context.Context, string, string, uint64) error) (_ *forceStage, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(target)
	if target == parent {
		return nil, fmt.Errorf("cannot replace filesystem root")
	}
	if err := trustForceParent(parent); err != nil {
		return nil, err
	}
	s := &forceStage{target: target}
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	// Serialize competing recoveries even when the destination does not exist.
	s.parent, err = writerlease.GenerationLock(parent, true)
	if err != nil {
		return nil, err
	}
	s.mount, err = forceMount(parent)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case !info.IsDir():
		return nil, fmt.Errorf("recovery target is not a real directory: %s", target)
	default:
		s.old, err = writerlease.GenerationLock(target, true)
		if err != nil {
			return nil, err
		}
		// Also catch database writers using an alias in another directory or a
		// binary predating directory leases. Never create an absent database.
		for _, rel := range []string{"crewship.db", "run/installation.lock"} {
			path := filepath.Join(target, rel)
			if rel != "crewship.db" {
				run, err := os.Lstat(filepath.Dir(path))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if !run.IsDir() {
					return nil, fmt.Errorf("unsafe installation run directory")
				}
			}
			fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if errors.Is(e, unix.ENOENT) {
				continue
			}
			if e != nil {
				return nil, e
			}
			f := os.NewFile(uintptr(fd), path)
			s.anchors = append(s.anchors, f)
			if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
				if errors.Is(e, unix.EWOULDBLOCK) {
					return nil, writerlease.ErrHeld
				}
				return nil, e
			}
		}
	}
	s.container, err = os.MkdirTemp(parent, ".crewship-recovery-*")
	if err != nil {
		return nil, err
	}
	s.scratch, err = os.OpenRoot(s.container)
	if err != nil {
		return nil, err
	}
	s.holder, err = s.scratch.Open(".")
	if err != nil {
		return nil, err
	}
	s.dir = filepath.Join(s.container, "generation")
	if s.old != nil {
		s.previous = s.dir
		err = clone(ctx, target, s.dir, s.mount)
	} else {
		err = os.Mkdir(s.dir, 0700)
	}
	if err != nil {
		return nil, fmt.Errorf("stage recovery generation: %w", err)
	}
	return s, nil
}

// Recovery helpers open paths below the private staging directory. Validate
// from / down so each name is protected before looking up the next component.
// A sticky ancestor (such as /tmp) protects the next trusted owner's entry.
// The immediate parent also controls the target name, which may be absent.
func trustForceParent(parent string) error {
	path := "/"
	for component := range strings.SplitSeq(parent, "/") {
		path = filepath.Join(path, component)
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return fmt.Errorf("untrusted recovery directory: %s", path)
		}
		if st.Mode&0022 != 0 && (path == parent || st.Mode&unix.S_ISVTX == 0) {
			return fmt.Errorf("recovery directory is writable by group or others: %s", path)
		}
	}
	return nil
}

func verifyForceName(path string, f *os.File) error {
	locked, err := f.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(locked, named) {
		return writerlease.ErrLost
	}
	return nil
}

func forceMount(path string) (uint64, error) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &st); err != nil {
		return 0, err
	}
	if st.Attributes&(unix.STATX_ATTR_APPEND|unix.STATX_ATTR_IMMUTABLE|unix.STATX_ATTR_VERITY|unix.STATX_ATTR_ENCRYPTED) != 0 {
		return 0, fmt.Errorf("unsupported filesystem attributes on %s", path)
	}
	if st.Mask&unix.STATX_MNT_ID == 0 {
		return 0, fmt.Errorf("kernel cannot verify recovery mount boundary")
	}
	return st.Mnt_id, nil
}

func (s *forceStage) commit(ctx context.Context) (bool, error) {
	return s.publish(ctx, unix.Renameat2)
}

func (s *forceStage) publish(ctx context.Context, rename func(int, string, int, string, uint) error) (bool, error) {
	if s.published {
		return true, fmt.Errorf("generation already published")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	parent := filepath.Dir(s.target)
	if err := trustForceParent(parent); err != nil {
		return false, err
	}
	if err := verifyForceName(parent, s.parent); err != nil {
		return false, err
	}
	if err := verifyForceName(s.container, s.holder); err != nil {
		return false, err
	}
	var err error
	if s.next == nil {
		s.next, err = writerlease.GenerationLock(s.dir, true)
		if err != nil {
			return false, err
		}
	} else if err = verifyForceName(s.dir, s.next); err != nil {
		return false, err
	}
	original := ""
	if s.old != nil {
		original = s.target
	}
	var root unix.Stat_t
	if err = unix.Fstat(int(s.next.Fd()), &root); err != nil {
		return false, err
	}
	if err = syncForceTree(ctx, s.dir, original, s.mount, root.Uid, root.Gid); err != nil {
		return false, err
	}
	// Persist both pre-exchange names as well as the staged contents.
	if err = s.holder.Sync(); err != nil {
		return false, err
	}
	if err = s.parent.Sync(); err != nil {
		return false, err
	}
	flag := uint(unix.RENAME_NOREPLACE)
	if s.old != nil {
		if err = verifyForceName(s.target, s.old); err != nil {
			return false, err
		}
		flag = unix.RENAME_EXCHANGE
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = rename(int(s.holder.Fd()), "generation", int(s.parent.Fd()), filepath.Base(s.target), flag); err != nil {
		return false, fmt.Errorf("publish recovery generation: %w", err)
	}
	// From here even a directory-sync error must retain the old generation.
	s.published = true
	return true, errors.Join(s.parent.Sync(), s.holder.Sync())
}

// Remove the empty wrapper through its original parent even if that parent
// moved. If the parent is now shared, or the wrapper name was reused, leave
// the directory entry alone.
func (s *forceStage) removeScratchWrapper() {
	if s.holder == nil {
		return // An open failure left no handle with which to identify the wrapper.
	}
	parentFD := int(s.parent.Fd())
	var parent, named, held unix.Stat_t
	if unix.Fstat(parentFD, &parent) != nil || parent.Mode&0022 != 0 || (parent.Uid != 0 && parent.Uid != uint32(os.Geteuid())) {
		return
	}
	name := filepath.Base(s.container)
	if unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || unix.Fstat(int(s.holder.Fd()), &held) != nil {
		return
	}
	if named.Dev != held.Dev || named.Ino != held.Ino {
		return
	}
	_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
}

func (s *forceStage) close() {
	if s.container != "" && (!s.published || s.previous == "") {
		// Recursively clean only through the original private directory handle.
		// Its former pathname may now identify a different installation.
		if s.scratch != nil {
			if err := s.scratch.RemoveAll("generation"); errors.Is(err, os.ErrPermission) {
				_ = fs.WalkDir(s.scratch.FS(), "generation", func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return s.scratch.Chmod(path, 0700)
					}
					return nil
				})
				_ = s.scratch.RemoveAll("generation")
			}
		}
		s.removeScratchWrapper()
	}
	if s.scratch != nil {
		_ = s.scratch.Close()
	}
	for _, f := range append(s.anchors, s.next, s.old, s.holder, s.parent) {
		if f != nil {
			_ = f.Close()
		}
	}
}
