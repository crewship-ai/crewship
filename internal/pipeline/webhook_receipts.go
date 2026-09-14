package pipeline

// Retention for routine webhook receipts.
//
// A receipt is the record that one routine webhook delivery was accepted: its
// identity (workspace, webhook, sender's delivery id), the body it carried,
// and the run it produced. Its job is deduplication — a redelivery of the same
// identifier answers with the original run instead of executing again.
//
// The contract (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §6)
// keeps that promise for at least 30 days from acceptance and makes none past
// it. So a receipt carries an explicit expiry, and this sweeper removes the
// expired ones. What removal means is stated rather than implied: a delivery
// re-sent after its receipt is gone is NEW work and runs again. That is the
// contract's answer, not a leak — a dedup key kept forever would make the
// table the one thing in the system that never stops growing.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RoutineReceiptDedupWindow is how long a routine webhook receipt suppresses a
// redelivery of the same identifier. §6: at least 30 days from acceptance.
const RoutineReceiptDedupWindow = 30 * 24 * time.Hour

// RoutineReceiptDedupExpiry is when a receipt received at `receivedAt` stops
// deduplicating.
func RoutineReceiptDedupExpiry(receivedAt time.Time) time.Time {
	return receivedAt.Add(RoutineReceiptDedupWindow)
}

// routineReceiptSweepBatch bounds one DELETE so the sweeper never holds the
// single SQLite writer across a large backlog.
const routineReceiptSweepBatch = 500

// SweepRoutineWebhookReceipts deletes every receipt whose dedup window has
// closed, in bounded batches, and reports how many rows it removed.
//
// A receipt whose window closes exactly at `now` is kept: the boundary is
// "strictly before", so a redelivery arriving in the same instant as the
// sweep still finds its receipt. Receipts without an expiry (which the
// migration backfills, so there should be none) are never touched — an
// unknown age is not a licence to delete.
func SweepRoutineWebhookReceipts(ctx context.Context, db *sql.DB, now time.Time) (int64, error) {
	cutoff := tsformat.Format(now.UTC())
	var total int64
	for {
		res, err := db.ExecContext(ctx, `
			DELETE FROM routine_webhook_receipts
			WHERE id IN (
				SELECT id FROM routine_webhook_receipts
				WHERE dedup_expires_at != '' AND dedup_expires_at < ?
				ORDER BY dedup_expires_at, id
				LIMIT ?
			)`, cutoff, routineReceiptSweepBatch)
		if err != nil {
			return total, fmt.Errorf("sweep routine webhook receipts: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("sweep routine webhook receipts: rows affected: %w", err)
		}
		total += n
		if n < routineReceiptSweepBatch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// StartRoutineReceiptRetentionSweeper runs SweepRoutineWebhookReceipts every
// `interval`, with an immediate first sweep so a freshly started server does
// not wait a full day. Blocks; callers run it as a goroutine.
//
// Not leader-gated, matching StartRunRetentionSweeper and the other daily
// sweepers: the DELETE is idempotent, so on a multi-replica deployment the
// loser simply deletes zero rows (#1891 tracks gating the family).
func StartRoutineReceiptRetentionSweeper(ctx context.Context, db *sql.DB, logger *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	sweep := func() {
		n, err := SweepRoutineWebhookReceipts(ctx, db, time.Now())
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Warn("routine receipt retention sweeper: sweep failed", "error", err, "deleted", n)
		case n > 0:
			logger.Info("routine receipt retention sweeper: expired receipts removed", "deleted", n)
		}
	}
	sweep()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
