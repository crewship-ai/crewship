package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// lockSuffix is the sentinel filename suffix every per-subject writer in
// this package appends to the file it guards (see FileLock in writer.go).
const lockSuffix = ".lock"

// anyExists reports whether at least one of the given paths is present.
// Used by the erasure paths to decide whether there is anything to do at
// all — a subject with neither a file nor a stranded sentinel must not
// cause either to be created.
//
// Only a definite ENOENT counts as absent. Any other stat error (a
// permission problem on the directory, say) reports "present" so the
// caller goes on and fails loudly: an erasure that silently reports
// success because it could not look is the worse of the two outcomes.
func anyExists(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// removeUnderLock erases path AND its lock sentinel, holding the sentinel's
// lock across both. subsystem names the caller for the error text
// ("users", "peers").
//
// # Why the sentinel goes at all
//
// The sentinel persists on purpose on the write path: flock state is
// per-fd rather than per-inode, so a leftover zero-byte lockfile does not
// "stay locked" and saves a create on the next write. That argument is
// about writing. On an erasure it inverts — the row and the content are
// gone, so a file named `<subject-slug>.md.lock` becomes the only thing
// left, and the slug is recomputable by anyone with the workspace id and
// a user list. A per-subject marker that survives an erasure answers the
// question the erasure was asked to stop answering (#1701).
//
// # The unlink/unlock ordering
//
// We remove the sentinel INSIDE the critical section and unlock after —
// deliberately, not incidentally:
//
//   - A writer already blocked in flock() holds an open fd on that inode.
//     unlink(2) removes the NAME, not the inode, so that writer is still
//     correctly serialised against this delete and still wakes up holding
//     a valid lock. Nothing in flight is broken by the unlink.
//   - The only window is between the unlink and the unlock: a writer
//     ARRIVING in it creates a fresh sentinel and so does not exclude the
//     already-blocked one. Both writers write whole content through an
//     atomic rename, so the outcome is last-write-wins — exactly what two
//     serialised writers produce. A partial file is not reachable.
//   - Unlocking first and unlinking after would be strictly worse: the
//     blocked writer would proceed INTO its critical section and we would
//     then pull the sentinel out from under a live one, widening that same
//     window from one syscall to the width of a whole write.
//   - Leaving the sentinel is the bug.
//
// A write that races an erasure is unordered by definition — it either
// lands before and is erased, or lands after and resurrects a model for
// someone who asked to be forgotten. That is a caller-level concern
// (the API layer deletes the index row in the same request); this
// function's job is not to invent an ordering it cannot have.
//
// The sentinel is removed only when the guarded file went. If the erasure
// itself failed, the file is still there and the sentinel is not the last
// artefact — dropping it then would only remove the lock protecting a
// file that still exists.
func removeUnderLock(path, subsystem string) error {
	lk := NewFileLock(path + lockSuffix)
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("%s: lock: %w", subsystem, err)
	}
	var firstErr error
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		firstErr = fmt.Errorf("%s: remove: %w", subsystem, err)
	}
	if firstErr == nil {
		if err := os.Remove(path + lockSuffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			firstErr = fmt.Errorf("%s: remove lock sentinel: %w", subsystem, err)
		}
	}
	if err := lk.Unlock(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("%s: unlock: %w", subsystem, err)
	}
	return firstErr
}
