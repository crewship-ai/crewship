//go:build unix

package diskusage

import "syscall"

// rawUsage returns, in bytes, (total, free-including-reserved, available-to-
// unprivileged) for the filesystem containing path, via statfs(2). Bfree and
// Bavail differ on filesystems that reserve blocks for root (ext4 ~5%);
// exposing both lets Usage match df's Used/Use% math. Works on Linux and
// macOS — the block-count fields are uint64 on both; Bsize widens cleanly
// (uint32 on darwin) under the uint64 cast.
func rawUsage(path string) (total, freeAll, avail uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Blocks) * bsize, uint64(st.Bfree) * bsize, uint64(st.Bavail) * bsize, nil
}
