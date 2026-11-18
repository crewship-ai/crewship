//go:build !windows

package runtimestage

import (
	"os"
	"syscall"
)

func agentCLINoFollowFlag() int { return syscall.O_NOFOLLOW }

// Host ownership is necessary but not a crew isolation boundary. Unlike the
// managed-launch pilot, the distribution path supports server UID1001; the
// provider must prove the tree is absent from writable crew mounts.
func safeAgentCLIOwner(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == 0 || st.Uid == uint32(os.Geteuid()))
}

func safeAgentCLIFile(info os.FileInfo, immutable bool) bool {
	if !safeAgentCLIOwner(info) || !info.Mode().IsRegular() {
		return false
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Nlink != 1 || info.Mode().Perm()&0022 != 0 {
		return false
	}
	if immutable {
		return info.Mode().Perm() == 0555
	}
	return info.Mode().Perm()&0111 != 0
}

func safeAgentCLIDirectory(info os.FileInfo) bool {
	if !safeAgentCLIOwner(info) || !info.IsDir() {
		return false
	}
	st := info.Sys().(*syscall.Stat_t)
	return (info.Mode().Perm()&0002 == 0 || info.Mode()&os.ModeSticky != 0) &&
		(info.Mode().Perm()&0020 == 0 || st.Gid == 0 || st.Gid == uint32(os.Getegid()))
}
