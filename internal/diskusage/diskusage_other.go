//go:build !unix && !windows

package diskusage

import "errors"

// rawUsage is unsupported off Unix and Windows (Plan 9, wasm). The stub keeps
// the package buildable there with a clear error the caller surfaces as
// "disk stats unavailable" rather than a build break.
func rawUsage(string) (total, freeAll, avail uint64, err error) {
	return 0, 0, 0, errors.New("disk usage not supported on this platform")
}
