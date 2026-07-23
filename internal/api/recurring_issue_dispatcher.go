package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/leader"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/ws"
)

// scheduledFireIdempotencyKey derives the durable fire key
// sha256(kind‖id‖occurrence-bucket) that dedupes a single scheduled fire
// across replicas — the exact scheme #788 established for the pipeline
// scheduler. The bucket is the occurrence's next_run timestamp, so each
// occurrence gets a distinct key while retries of the same occurrence
// collide.
//
// TODO(#820): consolidate with pipeline.ScheduledFireIdempotencyKey once
// that sibling PR lands on main (it exports the identical scheme).
func scheduledFireIdempotencyKey(kind, id, bucket string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + id + "\x00" + bucket))
	return hex.EncodeToString(sum[:])
}

// RecurringIssueDispatcher fires due recurring_issues rows: it stamps a new
// issue from each due template and advances the schedule. Mirrors
// pipeline.PipelineScheduler's lifecycle (30s tick, single instance, no leader
// election) so cmd_start.go wires it the same way. Without this, recurring
// issues had full CRUD + a next_run column + an index but NO runtime consumer —
// created rows looked scheduled and never fired.
type RecurringIssueDispatcher struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger

	// leaderGate, when non-nil, gates each tick on holding the scheduler
	// lease so a multi-replica deploy fires each due row once. Nil = single
	// instance (always fire), the unchanged default (#1376).
	leaderGate leader.Gate

	startOnce sync.Once
	stopCh    chan struct{}
	stopped   chan struct{}
}

// SetLeaderGate attaches a leader-election gate so this dispatcher only fires
// while its replica holds the scheduler lease. Call before Start. Nil (the
// default) keeps single-instance behaviour.
func (d *RecurringIssueDispatcher) SetLeaderGate(g leader.Gate) { d.leaderGate = g }

// NewRecurringIssueDispatcher constructs the dispatcher. It needs the DB and a
// logger; hub is optional (nil in tests) for broadcast.
func NewRecurringIssueDispatcher(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *RecurringIssueDispatcher {
	return &RecurringIssueDispatcher{
		db:     db,
		hub:    hub,
		logger: logger,
		stopCh: make(chan struct{}),
	}
}

// Start launches the tick loop (idempotent). Fires one tick immediately so a
// newly-due row doesn't wait a full interval.
func (d *RecurringIssueDispatcher) Start(ctx context.Context) {
	d.startOnce.Do(func() {
		d.stopped = make(chan struct{})
		go d.run(ctx)
	})
}

// Stop signals the loop to exit and waits for it.
func (d *RecurringIssueDispatcher) Stop() {
	if d.stopped == nil {
		return
	}
	close(d.stopCh)
	<-d.stopped
}

func (d *RecurringIssueDispatcher) run(ctx context.Context) {
	defer close(d.stopped)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	d.tick(ctx)
	for {
		select {
		case <-d.stopCh:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			d.tick(ctx)
		}
	}
}

type recurringDueRow struct {
	id, workspaceID, crewID, title, cronExpr string
	description, priority                    sql.NullString
	projectID, milestoneID                   sql.NullString
	assigneeType, assigneeID                 sql.NullString
	labelsJSON                               sql.NullString
	createdBy                                sql.NullString
	// nextRunBucket is the row's current next_run value — the timestamp that
	// made it due. Stable across restart and distinct per occurrence, so it
	// is the occurrence bucket for the durable fire-idempotency key.
	nextRunBucket string
}

// tick selects due rows and fires each. Single instance, no leader election —
// same assumption as the pipeline scheduler.
func (d *RecurringIssueDispatcher) tick(ctx context.Context) {
	// Leader gate: only the lease holder fires on a multi-replica deploy, so
	// two replicas don't both stamp the same due recurring issue. Nil gate
	// (single-instance default) always passes.
	if d.leaderGate != nil && !d.leaderGate.IsLeader() {
		return
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, workspace_id, crew_id, title, description, priority,
		       project_id, milestone_id, assignee_type, assignee_id, labels_json, cron_expression,
		       created_by, next_run
		FROM recurring_issues
		WHERE enabled = 1 AND next_run IS NOT NULL AND next_run <= ?
		ORDER BY next_run ASC LIMIT 100`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		d.logger.Error("recurring issue dispatcher: list due", "error", err)
		return
	}
	defer rows.Close()

	var due []recurringDueRow
	for rows.Next() {
		var r recurringDueRow
		if err := rows.Scan(&r.id, &r.workspaceID, &r.crewID, &r.title, &r.description, &r.priority,
			&r.projectID, &r.milestoneID, &r.assigneeType, &r.assigneeID, &r.labelsJSON, &r.cronExpr,
			&r.createdBy, &r.nextRunBucket); err != nil {
			d.logger.Error("recurring issue dispatcher: scan", "error", err)
			return
		}
		due = append(due, r)
	}
	if err := rows.Err(); err != nil {
		d.logger.Error("recurring issue dispatcher: rows", "error", err)
		return
	}
	for _, r := range due {
		d.fireOne(ctx, r)
	}
}

func (d *RecurringIssueDispatcher) fireOne(ctx context.Context, row recurringDueRow) {
	// Compute the next occurrence up front (5-field cron, UTC — the table has no
	// timezone column, unlike pipeline_schedules). A bad expr disables the row
	// so a un-parseable cron can't spin the ticker forever.
	sched, err := cronParser.Parse(row.cronExpr)
	if err != nil {
		d.logger.Error("recurring issue dispatcher: bad cron, disabling", "id", row.id, "error", err)
		if _, derr := d.db.ExecContext(ctx,
			`UPDATE recurring_issues SET enabled = 0, updated_at = ? WHERE id = ?`,
			time.Now().UTC().Format(time.RFC3339), row.id); derr != nil {
			d.logger.Error("recurring issue dispatcher: disable bad-cron row", "id", row.id, "error", derr)
		}
		return
	}
	now := time.Now().UTC()
	nextRun := sched.Next(now).UTC().Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)

	// Durable fire-idempotency: reserve a per-occurrence key BEFORE inserting
	// so two replicas racing the same due row can't both create an issue. The
	// occurrence bucket is the row's current next_run (the timestamp that made
	// it due — stable across restart, distinct per occurrence). Reuse the
	// existing pipeline_run_idempotency table (no new migration); the row id
	// stands in for the synthetic run/pipeline labels.
	// releaseReservation frees the occurrence key. It MUST be called on any
	// failure path that returns WITHOUT advancing next_run — otherwise the
	// reservation persists for its 24h TTL while the row stays due, so every
	// subsequent tick re-derives the same key, sees isNew=false, and silently
	// drops the occurrence until the reservation expires. Only reserved==true
	// (this call created the row) releases; a duplicate must not delete the
	// owner's reservation.
	var reserved bool
	idemStore := pipeline.NewIdempotencyStore(d.db)
	idemKey := scheduledFireIdempotencyKey("recurring_issue", row.id, row.nextRunBucket)
	releaseReservation := func() {
		if !reserved || row.nextRunBucket == "" {
			return
		}
		if ferr := idemStore.Forget(ctx, row.workspaceID, idemKey); ferr != nil {
			d.logger.Warn("recurring issue dispatcher: release reservation failed", "id", row.id, "error", ferr)
		}
	}
	if row.nextRunBucket != "" {
		_, isNew, ierr := idemStore.LookupOrReserve(ctx, row.workspaceID, idemKey, row.id, row.id, 0)
		if ierr != nil {
			// Fail closed: another replica may own this fire, or the store is
			// unavailable. Skip without advancing — the owning replica advances
			// next_run, and a transient error retries on the next tick.
			if !errors.Is(ierr, sql.ErrNoRows) {
				d.logger.Warn("recurring issue dispatcher: idempotency reserve failed, skipping fire",
					"id", row.id, "error", ierr)
			}
			return
		}
		if !isNew {
			// Duplicate occurrence — the owning replica inserts and advances.
			// Don't insert, don't advance.
			return
		}
		reserved = true
	}

	var labels []string
	if row.labelsJSON.Valid && row.labelsJSON.String != "" {
		if err := json.Unmarshal([]byte(row.labelsJSON.String), &labels); err != nil {
			d.logger.Warn("recurring issue dispatcher: bad labels_json, ignoring", "id", row.id, "error", err)
		}
	}
	strPtr := func(ns sql.NullString) *string {
		if ns.Valid {
			return &ns.String
		}
		return nil
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		d.logger.Error("recurring issue dispatcher: begin tx", "id", row.id, "error", err)
		releaseReservation() // next_run not advanced — free the key so the next tick retries
		return
	}
	defer tx.Rollback() //nolint:errcheck

	issueID, identifier, cerr := insertIssueTx(ctx, tx, d.logger, issueSpec{
		WorkspaceID:  row.workspaceID,
		CrewID:       row.crewID,
		Title:        row.title,
		Description:  strPtr(row.description),
		Priority:     row.priority.String,
		AssigneeType: strPtr(row.assigneeType),
		AssigneeID:   strPtr(row.assigneeID),
		ProjectID:    strPtr(row.projectID),
		MilestoneID:  strPtr(row.milestoneID),
		Labels:       labels,
		AuthoredVia:  "recurring",
		// Attribute the fired issue to whoever set up the template (v129).
		// NULL when the template predates created_by capture; the response
		// layer then omits the creator rather than guessing.
		CreatedByUserID: row.createdBy.String,
	})
	if cerr != nil {
		// A persistent config error (e.g. crew has no LEAD agent) must not spin
		// the ticker: advance next_run past this occurrence and log loudly. The
		// occurrence is skipped, not silently retried forever.
		_ = tx.Rollback()
		d.logger.Warn("recurring issue dispatcher: create failed, advancing to next occurrence",
			"id", row.id, "crew_id", row.crewID, "error", cerr)
		if _, uerr := d.db.ExecContext(ctx,
			`UPDATE recurring_issues SET next_run = ?, updated_at = ? WHERE id = ?`,
			nextRun, nowStr, row.id); uerr != nil {
			d.logger.Error("recurring issue dispatcher: advance after failure", "id", row.id, "error", uerr)
		}
		return
	}

	// Advance the schedule in the SAME tx as the issue insert, so a crash can't
	// create the issue without recording the fire (double-fire) or vice versa.
	if _, uerr := tx.ExecContext(ctx,
		`UPDATE recurring_issues SET next_run = ?, last_run = ?, run_count = run_count + 1, updated_at = ? WHERE id = ?`,
		nextRun, nowStr, nowStr, row.id); uerr != nil {
		d.logger.Error("recurring issue dispatcher: advance schedule", "id", row.id, "error", uerr)
		// Roll back FIRST: an aborted statement still holds the write lock until
		// the tx closes, and releaseReservation writes on another connection —
		// forgetting before the rollback would deadlock on the held lock.
		_ = tx.Rollback()
		releaseReservation() // next_run not advanced — free the key so the next tick retries
		return
	}
	if err := tx.Commit(); err != nil {
		d.logger.Error("recurring issue dispatcher: commit", "id", row.id, "error", err)
		// A failed Commit finalizes the tx (connection released), so the key
		// can be freed safely — insert+advance rolled back, next_run unchanged.
		releaseReservation()
		return
	}

	d.logger.Info("recurring issue fired", "id", row.id, "issue_id", issueID, "identifier", identifier, "crew_id", row.crewID)
	if d.hub != nil {
		broadcastWorkspaceEvent(d.hub, row.workspaceID, "issue.created",
			map[string]string{"id": issueID, "identifier": identifier, "title": row.title})
	}
}
