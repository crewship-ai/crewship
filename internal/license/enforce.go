package license

import (
	"context"
	"database/sql"
	"fmt"
)

// CheckCrewLimit verifies the workspace hasn't exceeded its crew limit.
func (l *License) CheckCrewLimit(ctx context.Context, db *sql.DB, workspaceID string) error {
	max := l.MaxCrews()
	if max <= 0 {
		return nil
	}

	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL",
		workspaceID,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("count crews: %w", err)
	}

	if count >= max {
		return &LimitError{
			Resource: "crews",
			Current:  count,
			Maximum:  max,
			Edition:  l.Edition(),
		}
	}
	return nil
}

// CheckAgentLimit verifies a crew hasn't exceeded its agent limit.
func (l *License) CheckAgentLimit(ctx context.Context, db *sql.DB, crewID string) error {
	max := l.MaxAgentsPerCrew()
	if max <= 0 {
		return nil
	}

	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agents WHERE crew_id = ? AND deleted_at IS NULL",
		crewID,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("count agents: %w", err)
	}

	if count >= max {
		return &LimitError{
			Resource: "agents per crew",
			Current:  count,
			Maximum:  max,
			Edition:  l.Edition(),
		}
	}
	return nil
}

// CheckMemberLimit verifies the workspace hasn't exceeded its member limit.
func (l *License) CheckMemberLimit(ctx context.Context, db *sql.DB, workspaceID string) error {
	max := l.MaxMembers()
	if max <= 0 {
		return nil
	}

	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ?",
		workspaceID,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("count members: %w", err)
	}

	if count >= max {
		return &LimitError{
			Resource: "workspace members",
			Current:  count,
			Maximum:  max,
			Edition:  l.Edition(),
		}
	}
	return nil
}

// LimitError is returned when a license limit is exceeded.
type LimitError struct {
	Resource string
	Current  int
	Maximum  int
	Edition  Edition
}

// Error returns a human-readable message describing which license limit was exceeded.
func (e *LimitError) Error() string {
	return fmt.Sprintf(
		"license limit reached: %d/%d %s (%s edition). Upgrade your license for higher limits.",
		e.Current, e.Maximum, e.Resource, e.Edition,
	)
}

// IsLimitError checks if an error is a LimitError.
func IsLimitError(err error) bool {
	_, ok := err.(*LimitError)
	return ok
}
