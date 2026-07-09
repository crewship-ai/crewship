//go:build !windows

package memory

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a final-component
// symlink. See readRegularNoFollow (index.go) for the full threat-model
// rationale.
//
//   - O_NOFOLLOW makes the open syscall fail (with ELOOP) if path's
//     final component is a symlink.
//   - O_NONBLOCK keeps Open from hanging when an attacker swaps a
//     regular .md for a FIFO with no writer; the caller re-Stats and
//     rejects anything non-regular afterwards.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
