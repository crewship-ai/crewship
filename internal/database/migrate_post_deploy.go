package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Post-deployment migrations — the batched lane.
//
// Measured on this schema (migrate_scaling_test.go): rewriting journal_entries
// costs ~59µs per row across the three migrations that do it. Linear, which is
// the good news; but linear in a customer's data means one minute of upgrade
// downtime at a million rows and ten at ten million, for a server that is
// otherwise ready to serve. Downtime that scales with how successfully someone
// has used the product is not acceptable.
//
// So a migration marked postDeploy is skipped at boot and run here instead,
// after the server is already answering requests, one batch per transaction.
//
// What this buys and what it costs:
//
//   - Buys: the boot is not held hostage by row count, and the work is
//     resumable — a restart mid-backfill re-enters where it left off rather
//     than starting over or, worse, half-applying.
//   - Costs: the change is NOT applied when the new code starts serving. The
//     running code must tolerate the half-migrated state for as long as the
//     backfill takes. That is a real constraint on the code, not a detail;
//     migrations/post_deploy/README.md is the contract.
//
// Batching is by statement, not by row: the SQL in a post-deploy migration is
// expected to be shaped so that repeated execution converges — the canonical
// form being `UPDATE t SET col = … WHERE col IS NULL LIMIT <batch>`. The runner
// re-runs the statement until it stops changing rows, committing each pass, so
// no single transaction holds the write lock for the whole table.

// PostDeployBatchSize is how many rows a single pass of a post-deploy
// migration should touch. Small enough that the write lock is held briefly,
// large enough that the per-transaction overhead stays irrelevant.
const PostDeployBatchSize = 500

// postDeployPassLimit bounds how many passes one migration may take before the
// runner gives up and says so. At the default batch size this allows 5 million
// rows, which is far past anything real; the point is that a migration whose
// WHERE clause never stops matching gets reported instead of looping until the
// process is killed.
const postDeployPassLimit = 10000

// PostDeployStatus describes one deferred migration's state.
type PostDeployStatus struct {
	Version int
	Name    string
	Applied bool
}

// PostDeployPending returns the declared post-deployment migrations that this
// database has not recorded yet, in order.
func PostDeployPending(ctx context.Context, db *sql.DB) ([]PostDeployStatus, error) {
	declared := pendingPostDeployDeclared()
	if len(declared) == 0 {
		return nil, nil
	}

	var out []PostDeployStatus
	for _, m := range declared {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM _migrations WHERE version = ?`, m.version).Scan(&name)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out = append(out, PostDeployStatus{Version: m.version, Name: m.name, Applied: false})
		case err != nil:
			return nil, fmt.Errorf("check post-deploy migration %d: %w", m.version, err)
		default:
			// Present. The name is verified by Migrate's collision guard, so a
			// disagreement here has already been reported with better context.
			out = append(out, PostDeployStatus{Version: m.version, Name: m.name, Applied: true})
		}
	}
	return out, nil
}

// RunPostDeployMigrations applies every pending post-deployment migration, in
// order, batching each one. Intended to be called once from a goroutine after
// the server starts serving.
//
// It is deliberately sequential and deliberately unhurried: correctness and a
// short lock hold matter, throughput does not. A context cancellation (server
// shutting down) stops cleanly between batches — the completed batches stay
// committed and the next start resumes, which is exactly why the SQL has to be
// re-runnable.
func RunPostDeployMigrations(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if migrationRegistryErr != nil {
		return fmt.Errorf("migration registry is invalid: %w", migrationRegistryErr)
	}

	pending, err := PostDeployPending(ctx, db)
	if err != nil {
		return err
	}

	byVersion := make(map[int]migration, len(pending))
	for _, m := range pendingPostDeployDeclared() {
		byVersion[m.version] = m
	}

	for _, p := range pending {
		if p.Applied {
			continue
		}
		m := byVersion[p.Version]
		if err := runOnePostDeploy(ctx, db, m, logger); err != nil {
			// Reported, not retried in a loop: a failing post-deploy migration
			// needs a human, and hammering it would just fill the log. The
			// server keeps serving, which is the entire point of this lane.
			return fmt.Errorf("post-deployment migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

func runOnePostDeploy(ctx context.Context, db *sql.DB, m migration, logger *slog.Logger) error {
	logger.Info("starting post-deployment migration", "version", m.version, "name", m.name)
	started := time.Now()

	var passes int
	var touched int64
	for {
		if err := ctx.Err(); err != nil {
			// Shutting down. Batches so far are committed; the next start
			// picks up where this left off.
			logger.Info("post-deployment migration interrupted, will resume on next start",
				"version", m.version, "name", m.name, "passes", passes, "rows", touched)
			return nil
		}

		n, err := postDeployPass(ctx, db, m)
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
		passes++
		touched += n

		if passes >= postDeployPassLimit {
			return fmt.Errorf(
				"stopped after %d passes having changed %d rows and still matching more — "+
					"the statement is not converging, which usually means its WHERE clause does "+
					"not exclude the rows it has already handled (see migrations/post_deploy/README.md)",
				passes, touched)
		}
		// Every pass logs at DEBUG; INFO every 20 keeps a long backfill
		// visible without drowning the log.
		if passes%20 == 0 {
			logger.Info("post-deployment migration progress",
				"version", m.version, "name", m.name, "passes", passes, "rows", touched,
				"elapsed", time.Since(started).Round(time.Second))
		} else {
			logger.Debug("post-deployment migration batch",
				"version", m.version, "name", m.name, "pass", passes, "rows", n)
		}
	}

	// Record it only now, in its own transaction. Until this row exists the
	// migration is pending, which is what makes an interrupted run resumable.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO _migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	logger.Info("post-deployment migration complete",
		"version", m.version, "name", m.name,
		"passes", passes, "rows", touched,
		"duration", time.Since(started).Round(time.Millisecond))
	return nil
}

// postDeployPass runs the migration's SQL once, in its own transaction, and
// reports how many rows it changed. Zero means there is nothing left to do.
func postDeployPass(ctx context.Context, db *sql.DB, m migration) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, m.sql)
	if err != nil {
		return 0, fmt.Errorf("batch: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// Some statements do not report a count. Treating that as "nothing
		// left" would silently stop a backfill after one pass, so it is an
		// error: a post-deploy migration has to be countable to be batchable.
		return 0, fmt.Errorf("batch affected-row count unavailable (%w) — a post-deployment "+
			"migration must be a single row-changing statement so the runner can tell when "+
			"it is finished", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit batch: %w", err)
	}
	return n, nil
}
