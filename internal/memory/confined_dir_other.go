//go:build !unix && !windows

package memory

import "os"

type confinedDir struct {
	root *os.Root
}

func openConfinedDir(path string) (*confinedDir, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &confinedDir{root: root}, nil
}

func (d *confinedDir) openChild(name string) (*confinedDir, error) {
	root, err := d.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &confinedDir{root: root}, nil
}

func (d *confinedDir) mkdir(name string, perm os.FileMode) error {
	return d.root.Mkdir(name, perm)
}

func (d *confinedDir) kind(name string) (exists, symlink, dir bool, err error) {
	info, err := d.root.Lstat(name)
	if err != nil {
		return false, false, false, err
	}
	return true, info.Mode()&os.ModeSymlink != 0, info.IsDir(), nil
}

func (d *confinedDir) close() error {
	return d.root.Close()
}
