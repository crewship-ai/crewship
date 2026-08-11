//go:build unix

package memory

import (
	"os"

	"golang.org/x/sys/unix"
)

type confinedDir struct {
	file *os.File
}

func openConfinedDir(path string) (*confinedDir, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &confinedDir{file: os.NewFile(uintptr(fd), path)}, nil
}

func (d *confinedDir) openChild(name string) (*confinedDir, error) {
	fd, err := unix.Openat(int(d.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &confinedDir{file: os.NewFile(uintptr(fd), name)}, nil
}

func (d *confinedDir) mkdir(name string, perm os.FileMode) error {
	return unix.Mkdirat(int(d.file.Fd()), name, uint32(perm.Perm()))
}

func (d *confinedDir) kind(name string) (exists, symlink, dir bool, err error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(d.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, false, false, err
	}
	mode := uint32(stat.Mode) & unix.S_IFMT
	return true, mode == unix.S_IFLNK, mode == unix.S_IFDIR, nil
}

func (d *confinedDir) close() error {
	return d.file.Close()
}
