//go:build unix

package diskusage

import "syscall"

// rawUsage returns (total, avail-to-unprivileged) bytes for the filesystem
// containing path, via statfs(2). Works on Linux and macOS — the block-count
// fields are uint64 on both; Bsize widens cleanly (uint32 on darwin) under
// the uint64 cast.
func rawUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Blocks) * bsize, uint64(st.Bavail) * bsize, nil
}
