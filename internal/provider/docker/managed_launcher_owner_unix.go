//go:build !windows

package docker

import (
	"os"
	"syscall"
)

func safeLauncherOwner(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == 0 || st.Uid == uint32(os.Geteuid())) && st.Uid != 1001 && st.Uid != 1002
}

func safeLauncherDirectory(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && safeLauncherOwner(info) &&
		(info.Mode().Perm()&0002 == 0 || info.Mode()&os.ModeSticky != 0) &&
		(info.Mode().Perm()&0020 == 0 || st.Gid == 0 || (st.Gid == uint32(os.Getegid()) && st.Gid != 1001 && st.Gid != 1002))
}
