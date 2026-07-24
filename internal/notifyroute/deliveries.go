package notifyroute

import (
	"context"
	"database/sql"
	"fmt"
)

// Delivery is one row of the notification_deliveries outbox/log (v154).
type Delivery struct {
	ID          string
	WorkspaceID string
	ChannelID   string
	UserID      string
	Category    string
	DedupKey    string
	SourceKind  string
	SourceID    string
	Title       string
	Status      string // pending | sent | failed | dropped_pref | dropped_rate
	Error       string
	Attempts    int
	CreatedAt   string
	UpdatedAt   string
	SentAt      string
}

// Delivery statuses.
const (
	StatusPending     = "pending"
	StatusSent        = "sent"
	StatusFailed      = "failed"
	StatusDroppedPref = "dropped_pref"
	StatusDroppedRate = "dropped_rate"
)

// DeliveryStore is the persistence layer for notification_deliveries.
type DeliveryStore struct {
	db *sql.DB
}

func NewDeliveryStore(db *sql.DB) *DeliveryStore { return &DeliveryStore{db: db} }

// InsertPending writes a new outbox row with status='pending'. The
// (channel_id, dedup_key) UNIQUE index makes this INSERT OR IGNORE — a
// re-fired source event (retried hook, duplicate inbox write) coalesces
// into the existing row instead of creating a sibling. Returns
// (id, created bool, err); created=false means the row already existed
// (coalesced) and the caller should NOT attempt delivery again.
func (s *DeliveryStore) InsertPending(ctx context.Context, d Delivery) (id string, created bool, err error) {
	id = generateID("del")
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO notification_deliveries
		    (id, workspace_id, channel_id, user_id, category, dedup_key, source_kind, source_id, title, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')`,
		id, d.WorkspaceID, d.ChannelID, nullStr(d.UserID), d.Category, d.DedupKey,
		nullStr(d.SourceKind), nullStr(d.SourceID), nullStr(d.Title))
	if err != nil {
		return "", false, fmt.Errorf("notifyroute: insert delivery: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Coalesced: look up the existing row's id so callers that log it
		// still have something to point at.
		var existing string
		_ = s.db.QueryRowContext(ctx,
			`SELECT id FROM notification_deliveries WHERE channel_id = ? AND dedup_key = ?`,
			d.ChannelID, d.DedupKey).Scan(&existing)
		return existing, false, nil
	}
	return id, true, nil
}

// InsertDropped writes a terminal dropped_pref/dropped_rate row directly
// (no pending state — the router decided NOT to attempt delivery). Same
// coalescing semantics as InsertPending.
func (s *DeliveryStore) InsertDropped(ctx context.Context, d Delivery, status string) error {
	id := generateID("del")
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO notification_deliveries
		    (id, workspace_id, channel_id, user_id, category, dedup_key, source_kind, source_id, title, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, d.WorkspaceID, d.ChannelID, nullStr(d.UserID), d.Category, d.DedupKey,
		nullStr(d.SourceKind), nullStr(d.SourceID), nullStr(d.Title), status)
	if err != nil {
		return fmt.Errorf("notifyroute: insert dropped delivery: %w", err)
	}
	return nil
}

// MarkSent flips a pending row to sent.
func (s *DeliveryStore) MarkSent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_deliveries
		SET status = 'sent', attempts = attempts + 1,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		    sent_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("notifyroute: mark delivery sent: %w", err)
	}
	return nil
}

// MarkFailed flips a pending row to failed, recording the error.
func (s *DeliveryStore) MarkFailed(ctx context.Context, id, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_deliveries
		SET status = 'failed', attempts = attempts + 1, error = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, errMsg, id)
	if err != nil {
		return fmt.Errorf("notifyroute: mark delivery failed: %w", err)
	}
	return nil
}

// ListFilter narrows List's result set. Zero-value fields are unfiltered.
type ListFilter struct {
	Status    string
	ChannelID string
	Category  string
	Limit     int
}

// List returns a workspace's delivery-log rows, newest first — backs
// GET /api/v1/notification-deliveries. Read-only, no secrets involved
// (the log never stores a channel's URL/token, only ids).
func (s *DeliveryStore) List(ctx context.Context, workspaceID string, f ListFilter) ([]Delivery, error) {
	q := `
SELECT id, workspace_id, channel_id, COALESCE(user_id,''), category, dedup_key,
       COALESCE(source_kind,''), COALESCE(source_id,''), COALESCE(title,''),
       status, COALESCE(error,''), attempts, created_at, updated_at, COALESCE(sent_at,'')
FROM notification_deliveries
WHERE workspace_id = ?`
	args := []any{workspaceID}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.ChannelID != "" {
		q += ` AND channel_id = ?`
		args = append(args, f.ChannelID)
	}
	if f.Category != "" {
		q += ` AND category = ?`
		args = append(args, f.Category)
	}
	q += ` ORDER BY created_at DESC`
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q += ` LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("notifyroute: list deliveries: %w", err)
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.WorkspaceID, &d.ChannelID, &d.UserID, &d.Category, &d.DedupKey,
			&d.SourceKind, &d.SourceID, &d.Title, &d.Status, &d.Error, &d.Attempts,
			&d.CreatedAt, &d.UpdatedAt, &d.SentAt); err != nil {
			return nil, fmt.Errorf("notifyroute: scan delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
