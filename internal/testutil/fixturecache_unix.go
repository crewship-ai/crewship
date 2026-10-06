//go:build !windows

package testutil

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockFixtureCache(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

func fixtureProcessDead(pid int) bool {
	return errors.Is(unix.Kill(pid, 0), unix.ESRCH)
}

func fixtureRootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0o077 == 0
}
