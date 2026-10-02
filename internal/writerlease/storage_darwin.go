//go:build darwin

package writerlease

import "golang.org/x/sys/unix"

func supportedStorage(path string) error {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return err
	}
	if fs.Flags&unix.MNT_LOCAL == 0 {
		return ErrUnsupportedStorage
	}
	return nil
}
