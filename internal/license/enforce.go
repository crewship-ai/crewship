package license

import (
	"context"
	"database/sql"
	"fmt"
)

// v0.1 ships fully Apache-2.0 with no edition gating, so all enforcement
// returns nil. The package is preserved as a kostra (skeleton) so v0.2
// can re-enable tiered limits without re-introducing types and call
// sites; flip the early-return to a real count check when that lands.

// CheckCrewLimit is a no-op in v0.1 — see file-level note.
func (l *License) CheckCrewLimit(_ context.Context, _ *sql.DB, _ string) error {
	return nil
}

// CheckAgentLimit is a no-op in v0.1 — see file-level note.
func (l *License) CheckAgentLimit(_ context.Context, _ *sql.DB, _ string) error {
	return nil
}

// CheckMemberLimit is a no-op in v0.1 — see file-level note.
func (l *License) CheckMemberLimit(_ context.Context, _ *sql.DB, _ string) error {
	return nil
}

// LimitError is returned when a license limit is exceeded. v0.1 never
// constructs one, but the type and IsLimitError() are kept so the 402
// handlers in agents_create.go / workspaces_membership.go don't need
// to change shape.
type LimitError struct {
	Resource string
	Current  int
	Maximum  int
	Edition  Edition
}

// Error returns a human-readable message describing which license limit was exceeded.
func (e *LimitError) Error() string {
	return fmt.Sprintf(
		"license limit reached: %d/%d %s (%s edition).",
		e.Current, e.Maximum, e.Resource, e.Edition,
	)
}

// IsLimitError checks if an error is a LimitError.
func IsLimitError(err error) bool {
	_, ok := err.(*LimitError)
	return ok
}
