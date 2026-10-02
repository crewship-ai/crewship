//go:build linux

package writerlease

import "golang.org/x/sys/unix"

func supportedStorage(path string) error {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return err
	}
	if !supportedLinuxFilesystem(uint64(fs.Type)) {
		return ErrUnsupportedStorage
	}
	return nil
}

// Fail closed for unknown, FUSE and network filesystems. Block-backed shared
// disks mounted simultaneously remain unsupported: flock is a local contract.
func supportedLinuxFilesystem(kind uint64) bool {
	switch kind {
	case 0xEF53, 0x58465342, 0x9123683E, 0x01021994, 0x794C7630, 0x2FC12FC1, 0xF2F52010:
		return true // ext2/3/4, XFS, Btrfs, tmpfs, overlay, ZFS, F2FS
	default:
		return false
	}
}
