//go:build linux

package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/writerlease"
	"golang.org/x/sys/unix"
)

// An exchange leaves the old generation at the scratch name. The enclosing
// private directory protects both incomplete plaintext and the retained copy.
type forceStage struct {
	dir, previous, target, container string
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
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, err
	}
	if real != parent {
		return nil, fmt.Errorf("recovery parent contains a symlink: %s", parent)
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
	s.holder, err = os.Open(s.container)
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
	var err error
	if s.next == nil {
		s.next, err = writerlease.GenerationLock(s.dir, true)
		if err != nil {
			return false, err
		}
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
		named, e := os.Lstat(s.target)
		if e != nil {
			return false, e
		}
		locked, e := s.old.Stat()
		if e != nil {
			return false, e
		}
		if !os.SameFile(locked, named) {
			return false, writerlease.ErrLost
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

func (s *forceStage) close() {
	if s.container != "" && (!s.published || s.previous == "") {
		if err := os.RemoveAll(s.container); errors.Is(err, os.ErrPermission) {
			// A copied directory can intentionally be read-only. Only scratch
			// belonging to a failed transaction reaches this cleanup path.
			_ = filepath.WalkDir(s.container, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return os.Chmod(path, 0700)
				}
				return nil
			})
			_ = os.RemoveAll(s.container)
		}
	}
	for _, f := range append(s.anchors, s.next, s.old, s.holder, s.parent) {
		if f != nil {
			_ = f.Close()
		}
	}
}
