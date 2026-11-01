package pages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Bounds for the configurable pages-per-workspace cap. The floor keeps an
// administrator from locking every workspace out with a typo; the ceiling is
// far past what the library UI is built for.
const (
	MinPagesPerWorkspaceLimit = 10
	MaxPagesPerWorkspaceLimit = 10000
)

// ErrPageLimitValue marks a rejected cap value (out of range or not set).
var ErrPageLimitValue = errors.New("invalid page limit")

// LimitError is the one error every page-creating path returns (or renders)
// when the workspace is at its cap. Existing and Limit are the numbers the
// message states.
type LimitError struct {
	Existing int
	Limit    int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("this workspace has reached its page limit: it holds %d pages and the limit is %d. Nothing was created. Delete or archive pages you no longer need, or ask an instance administrator to raise the limit", e.Existing, e.Limit)
}

// querier is satisfied by *sql.DB, *sql.Tx and *sql.Conn.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Limit reads the configured cap. A missing row (a database that predates the
// setting) is the default.
func Limit(ctx context.Context, q querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT max_pages_per_workspace FROM instance_page_limits WHERE id=1`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultMaxPagesPerWorkspace, nil
	}
	return n, err
}

// CheckCap is the single page-cap check. It counts every page of the
// workspace (archived pages included: they still occupy the catalog) and
// returns a *LimitError when one more page would exceed the configured cap.
// Pass the creating transaction as q so the count and the insert agree.
func CheckCap(ctx context.Context, q querier, workspaceID string) error {
	limit, err := Limit(ctx, q)
	if err != nil {
		return err
	}
	var existing int
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE workspace_id=?`, workspaceID).Scan(&existing); err != nil {
		return err
	}
	if existing >= limit {
		return &LimitError{Existing: existing, Limit: limit}
	}
	return nil
}

// SetLimit changes the cap inside tx and returns the old and new values.
// Lowering it never removes a page; it only refuses new ones.
func SetLimit(ctx context.Context, tx *sql.Tx, value *int) (before, after int, err error) {
	if value == nil {
		return 0, 0, fmt.Errorf("%w: send max_pages_per_workspace", ErrPageLimitValue)
	}
	if *value < MinPagesPerWorkspaceLimit || *value > MaxPagesPerWorkspaceLimit {
		return 0, 0, fmt.Errorf("%w: max_pages_per_workspace must be between %d and %d", ErrPageLimitValue, MinPagesPerWorkspaceLimit, MaxPagesPerWorkspaceLimit)
	}
	if before, err = Limit(ctx, tx); err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_page_limits(id,max_pages_per_workspace) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET max_pages_per_workspace=excluded.max_pages_per_workspace`, *value); err != nil {
		return 0, 0, err
	}
	return before, *value, nil
}
