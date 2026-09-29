//go:build windows

package diskusage

import "golang.org/x/sys/windows"

// rawUsage returns (total, free-including-quota, available-to-caller) for the
// volume holding path, via GetDiskFreeSpaceExW. Windows has no root-reserved
// blocks, so freeAll and avail differ only under a per-user disk quota —
// the same meaning statfs's Bfree/Bavail have on Unix.
func rawUsage(path string) (total, freeAll, avail uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &freeAll); err != nil {
		return 0, 0, 0, err
	}
	return total, freeAll, avail, nil
}
