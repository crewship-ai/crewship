package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/work"
	"github.com/robfig/cron/v3"
)

var (
	ErrScheduleUnavailable = errors.New("scheduler: schedule unavailable in this workspace")
	ErrScheduleNotDue      = errors.New("scheduler: schedule is not due")
	ErrLegacyOccurrence    = errors.New("scheduler: occurrence reserved by legacy executor; drain or reconcile before migration")
)

// ScheduledInput is the immutable input for a scheduled agent work item.
// Credentials, policy and agent configuration must be resolved again at dispatch.
type ScheduledInput struct {
	Version    int    `json:"version"`
	AgentID    string `json:"agent_id"`
	Cron       string `json:"cron"`
	Prompt     string `json:"prompt"`
	Occurrence string `json:"occurrence"`
}

// AcceptDue bounds the scheduler's write transaction with the same dedicated
// SQLite handle used by webhook acceptance. A disk-pressure refusal leaves the
// persisted occurrence untouched; the caller may retry on the next tick.
// The receipt becomes visible only after Acceptor.Do has committed.
func AcceptDue(ctx context.Context, acceptor *work.Acceptor, disk *work.DiskGuard,
	workspaceID, agentID string, now time.Time, limits work.IngressLimits) (work.Receipt, error) {
	if acceptor == nil {
		return work.Receipt{}, errors.New("scheduler: durable acceptor is not configured")
	}
	if disk == nil || disk.Path == "" {
		return work.Receipt{}, errors.New("scheduler: disk guard is not configured")
	}
	if err := disk.Check(); err != nil {
		return work.Receipt{}, fmt.Errorf("scheduler: check disk before acceptance: %w", err)
	}
	var receipt work.Receipt
	err := acceptor.Do(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var acceptErr error
		receipt, acceptErr = AcceptDueTx(txCtx, tx, acceptor.Store(), workspaceID, agentID, now, limits)
		return acceptErr
	})
	if err != nil {
		return work.Receipt{}, err
	}
	return receipt, nil
}

// AcceptDueTx prepares one scheduled occurrence in the caller's transaction.
// The caller must commit before sending a dispatcher hint and roll back on ANY
// error. Use the bounded work.Acceptor; do not perform runtime/IPC operations
// inside this transaction. This primitive is not yet wired to the cron producer.
//
// A delayed fire accepts the persisted due instant once, then schedules the next
// future instant. It does not enumerate missed intervals. Accepted work has no
// implicit expiry and waits for capacity instead of being dropped on a busy agent.
func AcceptDueTx(ctx context.Context, tx *sql.Tx, store *work.Store, workspaceID, agentID string, now time.Time, limits work.IngressLimits) (work.Receipt, error) {
	var expr, prompt, crewID, dueText string
	err := tx.QueryRowContext(ctx, `SELECT a.schedule_cron, COALESCE(a.schedule_prompt,''),
		COALESCE(a.crew_id,''), COALESCE(a.schedule_next_run,'') FROM agents a
		JOIN workspaces w ON w.id = a.workspace_id
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.workspace_id = ? AND a.deleted_at IS NULL AND w.deleted_at IS NULL
		AND (a.crew_id IS NULL OR (c.id IS NOT NULL AND c.deleted_at IS NULL AND c.workspace_id = a.workspace_id))
		AND a.schedule_enabled = 1 AND a.schedule_cron IS NOT NULL AND a.schedule_cron != ''`, agentID, workspaceID).
		Scan(&expr, &prompt, &crewID, &dueText)
	if errors.Is(err, sql.ErrNoRows) {
		return work.Receipt{}, ErrScheduleUnavailable
	}
	if err != nil {
		return work.Receipt{}, fmt.Errorf("scheduler: read due occurrence: %w", err)
	}
	due, err := time.Parse(time.RFC3339Nano, dueText)
	if err != nil {
		return work.Receipt{}, fmt.Errorf("scheduler: invalid persisted due instant: %w", err)
	}
	if due.After(now) {
		return work.Receipt{}, ErrScheduleNotDue
	}
	dueKey := due.UTC().Format(time.RFC3339Nano)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	plan, err := parser.Parse(expr)
	if err != nil {
		return work.Receipt{}, fmt.Errorf("scheduler: invalid stored cron: %w", err)
	}
	next := plan.Next(now).UTC()
	if next.IsZero() || !next.After(now) {
		return work.Receipt{}, errors.New("scheduler: cron has no next occurrence")
	}

	// The unique index is a second guard against a producer accidentally
	// accepting the same occurrence without the schedule cursor transaction.
	var receipt work.Receipt
	err = tx.QueryRowContext(ctx, `SELECT id, state FROM work_items
		WHERE workspace_id = ? AND agent_id = ? AND source = 'schedule'
		AND domain_kind = 'agent_run' AND source_ref = ? AND replay_of IS NULL`,
		workspaceID, agentID, dueKey).Scan(&receipt.WorkID, &receipt.State)
	if err == nil {
		receipt.Duplicate = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return work.Receipt{}, fmt.Errorf("scheduler: lookup occurrence: %w", err)
	} else {
		// An old executor may own the occurrence. Never start a new work item
		// beside it merely because it has no work_id. Production cutover must
		// drain the old executor; this is an additional migration guard.
		legacyKey := pipeline.ScheduledFireIdempotencyKey("agent-sched", agentID, due.UTC().Format(time.RFC3339))
		var expires string
		err := tx.QueryRowContext(ctx, `SELECT expires_at FROM pipeline_run_idempotency
			WHERE workspace_id = ? AND pipeline_id = ? AND idempotency_key = ?`, workspaceID, agentID, legacyKey).Scan(&expires)
		if err == nil {
			end, parseErr := time.Parse(time.RFC3339Nano, expires)
			if parseErr != nil || end.After(now) {
				return work.Receipt{}, ErrLegacyOccurrence
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return work.Receipt{}, fmt.Errorf("scheduler: lookup legacy owner: %w", err)
		}

		if prompt == "" {
			prompt = "This is a scheduled run. Execute your primary tasks."
		}
		input, err := json.Marshal(ScheduledInput{Version: 1, AgentID: agentID, Cron: expr, Prompt: prompt, Occurrence: dueKey})
		if err != nil {
			return work.Receipt{}, fmt.Errorf("scheduler: count pending scheduled work: %w", err)
		}
		// Schedules have no webhook delivery row. Count their per-agent backlog
		// explicitly, then apply the common workspace count/byte budget.
		cap := limits.EndpointNonTerminal
		if cap <= 0 {
			cap = work.DefaultEndpointNonTerminal
		}
		var count int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE workspace_id = ?
			AND agent_id = ? AND source = 'schedule' AND domain_kind = 'agent_run'
			AND terminal_at IS NULL`, workspaceID, agentID).Scan(&count)
		if err != nil {
			return work.Receipt{}, err
		}
		if count >= cap {
			return work.Receipt{}, work.ErrEndpointFull
		}
		if err := store.CheckIngressTx(ctx, tx, limits, work.IngressRequest{WorkspaceID: workspaceID, BodyBytes: int64(len(input))}); err != nil {
			return work.Receipt{}, fmt.Errorf("scheduler: check ingress capacity: %w", err)
		}
		hash := sha256.Sum256(input)
		identity := sha256.Sum256([]byte(workspaceID + "\x00" + agentID + "\x00" + dueKey))
		receipt, err = store.AcceptTx(ctx, tx, work.AcceptRequest{
			WorkspaceID: workspaceID, Source: work.SourceSchedule, SourceRef: dueKey,
			DomainKind: work.DomainAgentRun, DomainID: hex.EncodeToString(identity[:]),
			AgentID: agentID, CrewID: crewID, SessionID: "scheduled-" + hex.EncodeToString(identity[:]),
			Class: work.ClassBackground, AuthorizedScope: "agent-schedule",
			InputJSON: string(input), InputSHA256: hex.EncodeToString(hash[:]), EligibleAt: due,
		})
		if err != nil {
			return work.Receipt{}, fmt.Errorf("scheduler: accept due work: %w", err)
		}
	}
	// Advancing the cursor commits with the work and its event, never before.
	// Last-run records execution, so acceptance deliberately leaves it alone.
	_, err = tx.ExecContext(ctx, `UPDATE agents SET schedule_next_run = ? WHERE id = ? AND workspace_id = ?`,
		next.Format(time.RFC3339), agentID, workspaceID)
	if err != nil {
		return work.Receipt{}, fmt.Errorf("scheduler: advance occurrence: %w", err)
	}
	return receipt, nil
}
