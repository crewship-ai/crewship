//go:build linux

package writerlease

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// GenerationLock fences replacement of a directory while a server opens or
// owns files inside it. Both sides lock its inode, so publication can fence the
// old and new names across a directory exchange without creating lock files.
func GenerationLock(dir string, exclusive bool) (*os.File, error) {
	if err := supportedStorage(dir); err != nil {
		return nil, err
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	flag := unix.LOCK_SH
	if exclusive {
		flag = unix.LOCK_EX
	}
	if err = unix.Flock(int(f.Fd()), flag|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		var named os.FileInfo
		named, err = os.Stat(dir)
		if err == nil && (!info.IsDir() || !os.SameFile(info, named)) {
			err = ErrLost
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func lockGeneration(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if filepath.Base(path) == "installation.lock" && filepath.Base(dir) == "run" {
		dir = filepath.Dir(dir)
	}
	return GenerationLock(dir, false)
}
