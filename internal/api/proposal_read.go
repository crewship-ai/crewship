package api

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/crewship-ai/crewship/internal/memory"
)

// Staged proposals live in a crew's memory tree, which the crew itself can
// write. Every host-side read of a staged entry therefore goes through the
// memory package's no-follow readers, the same ones the listing uses.

// isNoFollowRefusal reports whether a failed no-follow read of path was a
// refusal of the entry (a symlink, a symlinked parent, or a non-regular
// file) rather than an I/O failure. The error alone does not always say:
// os.Root reports a link whose target leaves the root as an escape, not as
// ELOOP. The Lstat only classifies the failure; it never reads content.
func isNoFollowRefusal(path string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ELOOP) ||
		errors.Is(err, os.ErrPermission) ||
		strings.Contains(err.Error(), "not a regular file") {
		return true
	}
	info, lerr := os.Lstat(path)
	return lerr == nil && !info.Mode().IsRegular()
}

// openProposalFileNoFollow opens a staged proposal or canonical learned file
// for reading. With a storage root every path component below it is pinned
// and symlinks are refused; without one (tests, or a handler that was never
// given its root) the final component is still opened without following a
// link. Either way the result is a regular file or an error.
func openProposalFileNoFollow(storageRoot, path string) (*os.File, error) {
	if storageRoot != "" {
		return memory.OpenFileNoFollowRoot(storageRoot, path)
	}
	f, err := memory.OpenNoFollow(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	return f, nil
}
