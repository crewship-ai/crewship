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

// isNoFollowRefusal reports whether a failed no-follow read was a refusal of
// the entry (a symlink, a symlinked or non-directory parent, a path outside the
// storage root, or a non-regular file) rather than an I/O failure. It decides
// from the error alone and never touches the path again: a second lookup by
// name would be exactly the path-following access this file exists to avoid.
// os.Root reports a link whose target leaves the root as an escape ("path
// escapes from parent"), not as ELOOP, so that text is matched as well.
func isNoFollowRefusal(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "not a regular file") ||
		strings.Contains(msg, "path escapes from parent")
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
