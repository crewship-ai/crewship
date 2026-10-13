//go:build unix

package runoutput

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func configureHostHelper(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func validateHostDirectory(dir string) error {
	if os.Geteuid() == 1001 || os.Geteuid() == 1002 {
		return errors.New("host capture requires a distinct trusted host UID")
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("invalid protected host directory")
	}
	// Reject symlink components; checking only the leaf would let a workload
	// accessible alias redirect a nominally protected spool into its workspace.
	for part := dir; ; part = filepath.Dir(part) {
		info, err := os.Lstat(part)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("host spool contains non-directory or symlink")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
			return errors.New("host spool ancestor has untrusted ownership")
		}
		if info.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return errors.New("host spool ancestor is writable by another identity")
		}
		if part == dir {
			if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
				return errors.New("host spool must be private and owned by controller UID")
			}
		}
		if part == filepath.Dir(part) {
			break
		}
	}
	return nil
}

func lockHostReplay(ctx context.Context, dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "replay.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
