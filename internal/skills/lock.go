package skills

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// LockedError is returned by every autonomous write path that would change
// the content of a skill a person has locked. The message names the lock so
// the caller (agent, sidecar, operator reading a journal) knows why and how
// to proceed: a person must unlock the skill first.
type LockedError struct {
	Slug string
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("skill %q is locked against rewrites; an instance administrator "+
		"must unlock it (crewship skill unlock %s) before its content can change", e.Slug, e.Slug)
}

// IsLocked reports whether err is, or wraps, a *LockedError.
func IsLocked(err error) bool {
	var le *LockedError
	return errors.As(err, &le)
}

// CheckUnlocked returns a *LockedError when the skill with this slug exists
// and is locked. A missing skill is not locked. Callers that go on to write
// must still guard the write itself (WHERE locked = 0) because a lock can be
// taken between this check and the write.
func CheckUnlocked(ctx context.Context, db *sql.DB, slug string) error {
	var locked int
	err := db.QueryRowContext(ctx, `SELECT locked FROM skills WHERE slug = ? AND locked = 1 LIMIT 1`, slug).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check skill lock: %w", err)
	}
	return &LockedError{Slug: slug}
}
