package database

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

type checkpointLogCapture struct {
	slog.Handler
	records  []slog.Record
	onRecord func(slog.Record)
}

func (h *checkpointLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *checkpointLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	if h.onRecord != nil {
		h.onRecord(r)
	}
	return nil
}

func TestCheckpointerRetriesClosedPoolWithRateLimitedWarnings(t *testing.T) {
	raw := migrationBoundaryDB(t)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logger := &checkpointLogCapture{}
	logger.onRecord = func(r slog.Record) {
		if r.Message == "wal checkpoint failed" {
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "consecutive_failures" && a.Value.Int64() == 100 {
					cancel()
				}
				return true
			})
		}
	}
	StartCheckpointer(ctx, &DB{DB: raw}, slog.New(logger), CheckpointerConfig{Interval: time.Millisecond})
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("checkpointer stopped retrying before warning 100")
	}
	var failures []int64
	finalFailure := false
	for _, r := range logger.records {
		if r.Message == "wal checkpoint failed" {
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "consecutive_failures" {
					failures = append(failures, a.Value.Int64())
				}
				return true
			})
		}
		if r.Message == "wal checkpointer: final truncate failed" {
			finalFailure = true
		}
	}
	if len(failures) != 2 || failures[0] != 1 || failures[1] != 100 {
		t.Fatalf("warnings were not rate limited: %v", failures)
	}
	if !finalFailure {
		t.Fatal("shutdown failed to report the final checkpoint error")
	}
}

func TestCheckpointerUsesDefaultLoggerOnImmediateShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw := migrationBoundaryDB(t)
	StartCheckpointer(ctx, &DB{DB: raw}, nil, CheckpointerConfig{})
}
