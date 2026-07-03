//go:build !unix

package diskusage

import "errors"

// rawUsage is unsupported off Unix. The server targets Linux/macOS; the stub
// keeps the package buildable on other platforms (e.g. Windows CI) with a
// clear error the caller surfaces as "disk stats unavailable" rather than a
// build break.
func rawUsage(string) (total, freeAll, avail uint64, err error) {
	return 0, 0, 0, errors.New("disk usage not supported on this platform")
}
