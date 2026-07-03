package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

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

	startOnce sync.Once
	stopCh    chan struct{}
	stopped   chan struct{}
}

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
}

// tick selects due rows and fires each. Single instance, no leader election —
// same assumption as the pipeline scheduler.
func (d *RecurringIssueDispatcher) tick(ctx context.Context) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, workspace_id, crew_id, title, description, priority,
		       project_id, milestone_id, assignee_type, assignee_id, labels_json, cron_expression
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
			&r.projectID, &r.milestoneID, &r.assigneeType, &r.assigneeID, &r.labelsJSON, &r.cronExpr); err != nil {
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
		return
	}
	if err := tx.Commit(); err != nil {
		d.logger.Error("recurring issue dispatcher: commit", "id", row.id, "error", err)
		return
	}

	d.logger.Info("recurring issue fired", "id", row.id, "issue_id", issueID, "identifier", identifier, "crew_id", row.crewID)
	if d.hub != nil {
		broadcastWorkspaceEvent(d.hub, row.workspaceID, "issue.created",
			map[string]string{"id": issueID, "identifier": identifier, "title": row.title})
	}
}
