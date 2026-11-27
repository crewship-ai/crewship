//go:build !windows

package craftengine

import (
	"os"
	"syscall"
)

// privateDir accepts a directory owned by this process's user that group and others cannot use.
func privateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return errNotPrivate
	}
	return nil
}
