package work

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

// StartRetentionSweeper runs the work delivery retention policy at startup and
// then periodically. It blocks until ctx is cancelled; callers start it in a
// goroutine after migrations. Sweep itself keeps write transactions batched.
func StartRetentionSweeper(ctx context.Context, store *Store, logger *slog.Logger, interval time.Duration) {
	if logger == nil {
		logger = slog.Default()
	}
	if store == nil {
		logger.Error("work retention sweeper has no store")
		return
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	run := func() {
		// Never delete under an instance backup's consistent copy: the
		// whole sweep is a writer in the quiet window's barrier, and it
		// yields between batches (Sweep) so a backup's drain never waits
		// on it.
		if ctx.Err() != nil {
			return
		}
		wr, err := quiesce.EnterWait(ctx)
		if err != nil {
			return
		}
		defer wr.Leave()
		res, err := store.Sweep(wr.Context(), DefaultRetentionPolicy())
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				logger.Warn("work retention sweep failed", "error", err)
			}
			return
		}
		logger.Info("work retention sweep complete",
			"raw_bodies_expired", res.RawBodiesExpired,
			"raw_body_bytes_freed", res.RawBodyBytesFreed,
			"deliveries_removed", res.DeliveriesRemoved,
			"raw_bodies_retained", res.RawBodiesRetained,
			"batches", res.Batches)
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
