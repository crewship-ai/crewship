package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

// The dispatcher already imposed this timeout. Persist the same deadline so
// observer loss or a host restart cannot reset it or manufacture a new run.
const pageRequestDispatchTimeout = 30 * time.Minute
const pageRequestObservationTTL = 90 * time.Second

var errPageRequestEvidenceInvalid = errors.New("confirmed work result has invalid typed evidence")

const pageRequestDeadlineError = "Request deadline elapsed; runtime outcome remains unconfirmed"

type pageRequestLifecycle struct {
	ExecutionState string  `json:"execution_state"`
	HeartbeatAt    *string `json:"heartbeat_at"`
	LastActivityAt *string `json:"last_activity_at"`
	DeadlineAt     *string `json:"deadline_at"`
	DeadlineSource string  `json:"deadline_source,omitempty"`
	RunStatus      string  `json:"run_status,omitempty"`
}

type pageRequestIdentity struct {
	Kind, ID, Workspace, Agent, Run, Status, Deadline, Updated, Error string
}

func pageRequestTable(kind string) string {
	if kind == "artifact" {
		return "page_artifact_requests"
	}
	if kind == "board" {
		return "page_board_requests"
	}
	panic("invalid page request kind")
}

func loadPageRequestIdentity(ctx context.Context, q pageAuthQueryer, kind, id string) (pageRequestIdentity, error) {
	out := pageRequestIdentity{Kind: kind, ID: id}
	var query string
	if kind == "artifact" {
		query = `SELECT p.workspace_id,COALESCE(r.agent_id,''),COALESCE(r.run_id,''),r.status,r.deadline_at,r.updated_at,r.error FROM page_artifact_requests r JOIN pages p ON p.id=r.page_id WHERE r.id=?`
	} else if kind == "board" {
		query = `SELECT workspace_id,COALESCE(agent_id,''),COALESCE(run_id,''),status,deadline_at,updated_at,error FROM page_board_requests WHERE id=?`
	} else {
		return out, errors.New("invalid page request kind")
	}
	err := q.QueryRowContext(ctx, query, id).Scan(&out.Workspace, &out.Agent, &out.Run, &out.Status, &out.Deadline, &out.Updated, &out.Error)
	return out, err
}

func parsePageRequestTime(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, raw) // tsformat:allow: parsing durable timestamps; all SQL writes use tsformat.Format
}

func pageRequestDeadline(job pageRequestIdentity) (time.Time, string, error) {
	if job.Deadline != "" {
		at, err := parsePageRequestTime(job.Deadline)
		return at, "recorded", err
	}
	if job.Status != "running" {
		return time.Time{}, "", nil
	}
	// Existing running rows last changed at admission. This is a conservative
	// legacy bound, explicitly distinguished from a recorded exact deadline.
	admitted, err := parsePageRequestTime(job.Updated)
	return admitted.Add(pageRequestDispatchTimeout), "legacy_derived", err
}

func readPageRequestLifecycle(ctx context.Context, q pageAuthQueryer, job pageRequestIdentity, now time.Time) (pageRequestLifecycle, error) {
	out := pageRequestLifecycle{ExecutionState: "unknown"}
	if job.Error == pageRequestDeadlineError {
		out.ExecutionState = "deadline_exceeded"
	}
	deadline, source, err := pageRequestDeadline(job)
	if err != nil {
		return out, err
	}
	if !deadline.IsZero() {
		raw := tsformat.Format(deadline)
		out.DeadlineAt = &raw
		out.DeadlineSource = source
	}
	if !deadline.IsZero() && !now.Before(deadline) {
		out.ExecutionState = "deadline_exceeded"
	}
	if job.Run == "" {
		if !deadline.IsZero() && !now.Before(deadline) {
			out.ExecutionState = "deadline_exceeded"
		}
		return out, nil
	}
	var workWS, workAgent, workState, workStatus, heartbeat, lease, phase string
	var itemGeneration, attemptGeneration int64
	var result, ended sql.NullString
	err = q.QueryRowContext(ctx, `SELECT w.workspace_id,COALESCE(w.agent_id,''),w.state,w.generation,a.generation,a.run_status,a.run_result_json,a.heartbeat_at,a.lease_expires_at,a.runtime_phase,a.ended_at FROM work_attempts a JOIN work_items w ON w.id=a.work_id WHERE a.run_id=?`, job.Run).Scan(&workWS, &workAgent, &workState, &itemGeneration, &attemptGeneration, &workStatus, &result, &heartbeat, &lease, &phase, &ended)
	owned := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if owned && (workWS != job.Workspace || workAgent != job.Agent) {
		return out, nil
	}
	var activity sql.NullString
	if err = q.QueryRowContext(ctx, `SELECT MAX(ts) FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=? AND entry_type IN ('run.started','exec.output_chunk','exec.command','llm.call','run.agent_span')`, job.Workspace, job.Agent, job.Run).Scan(&activity); err != nil {
		return out, err
	}
	if activity.Valid {
		at, parseErr := parsePageRequestTime(activity.String)
		if parseErr == nil && !at.After(now.Add(5*time.Second)) {
			raw := tsformat.Format(at)
			out.LastActivityAt = &raw
			if now.Sub(at) <= pageRequestObservationTTL {
				out.ExecutionState = "awaiting_confirmation"
			}
		}
	}
	if owned {
		if workState == "needs_reconciliation" || itemGeneration != attemptGeneration || (ended.Valid && workStatus == "") {
			out.ExecutionState = "unknown"
		}
		at, parseErr := parsePageRequestTime(heartbeat)
		if parseErr == nil && !at.After(now.Add(5*time.Second)) {
			raw := tsformat.Format(at)
			out.HeartbeatAt = &raw
			expires, leaseErr := parsePageRequestTime(lease)
			if !ended.Valid && itemGeneration == attemptGeneration && (workState == "running" || workState == "starting") && workState != "needs_reconciliation" && leaseErr == nil && expires.After(now) && now.Sub(at) <= pageRequestObservationTTL {
				out.ExecutionState = "observed"
			}
		}
		// An owned attempt without a confirmed result must never fall back to a
		// premature legacy journal terminal, including after a lease was parked.
		if workStatus != "" && result.Valid {
			var confirmed work.RunResult
			raw := strings.TrimSpace(result.String)
			if !strings.HasPrefix(raw, "{") || json.Unmarshal([]byte(raw), &confirmed) != nil {
				out.ExecutionState = "unknown"
				if !deadline.IsZero() && !now.Before(deadline) {
					out.ExecutionState = "deadline_exceeded"
				}
				return out, errPageRequestEvidenceInvalid
			}
			switch workStatus {
			case "COMPLETED", "FAILED", "CANCELLED", "TIMEOUT":
				out.RunStatus = workStatus
				out.ExecutionState = "observed"
			}
		}
	} else {
		var event, payload string
		err = q.QueryRowContext(ctx, `SELECT entry_type,COALESCE(payload,'{}') FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop') ORDER BY ts DESC,id DESC LIMIT 1`, job.Workspace, job.Agent, job.Run).Scan(&event, &payload)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if err == nil {
			var terminal struct {
				Error string `json:"error_message"`
			}
			if json.Unmarshal([]byte(payload), &terminal) == nil && !strings.Contains(terminal.Error, orchestrator.ErrHostCaptureUnconfirmed.Error()) && !strings.Contains(terminal.Error, orchestrator.ErrDetachedStillRunning.Error()) {
				switch event {
				case "run.completed":
					out.RunStatus = "COMPLETED"
				case "run.failed":
					out.RunStatus = "FAILED"
				case "run.cancelled", "run.recovered_stop":
					out.RunStatus = "CANCELLED"
				case "run.timeout":
					out.RunStatus = "TIMEOUT"
				}
				out.ExecutionState = "observed"
			}
		}
	}
	if out.RunStatus == "" && !deadline.IsZero() && !now.Before(deadline) {
		out.ExecutionState = "deadline_exceeded"
	}
	return out, nil
}

// This settles the JOB only. It never updates a run, releases runtime capacity,
// resumes execution, or manufactures a successful result from a process exit.
func (h *PageHandler) settlePageRequest(ctx context.Context, kind, id, unboundFailure string, now time.Time) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	table := pageRequestTable(kind)
	if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET updated_at=updated_at WHERE id=?`, id); err != nil {
		return err
	}
	job, err := loadPageRequestIdentity(ctx, tx, kind, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.Status != "running" && job.Status != "queued" {
		return nil
	}
	next, message := "", ""
	if job.Run == "" && unboundFailure != "" {
		next, message = "failed", unboundFailure
	} else if job.Status == "running" || job.Status == "queued" {
		deadline, _, deadlineErr := pageRequestDeadline(job)
		if deadlineErr != nil {
			if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET status='failed',error='Request deadline metadata is invalid; runtime outcome remains unconfirmed',updated_at=? WHERE id=? AND status=?`, tsformat.Format(now), id, job.Status); err != nil {
				return err
			}
			return tx.Commit()
		}
		expired := !deadline.IsZero() && !now.Before(deadline)
		observation, err := readPageRequestLifecycle(ctx, tx, job, now)
		if err != nil && !expired {
			return err
		}
		if err != nil {
			observation = pageRequestLifecycle{ExecutionState: "deadline_exceeded"}
		}

		switch observation.RunStatus {
		case "COMPLETED":
			next, message = "failed", "Agent execution completed without committing the requested result"
		case "FAILED", "TIMEOUT":
			next, message = "failed", "Agent execution ended without committing the requested result; inspect its chat"
		case "CANCELLED":
			next, message = "cancelled", "Agent execution was confirmed cancelled before committing the requested result"
		default:
			if observation.ExecutionState == "deadline_exceeded" {
				next, message = "failed", pageRequestDeadlineError
			}
		}
	}
	if next == "" {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET status=?,error=?,updated_at=? WHERE id=? AND status=?`, next, message, tsformat.Format(now), id, job.Status); err != nil {
		return err
	}
	if err = h.pageRequestNoticeTx(ctx, tx, kind, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (h *PageHandler) finishPageRequestDispatch(ctx context.Context, kind, id, message string) {
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.settlePageRequest(clean, kind, id, message, time.Now()); err != nil {
		h.logger.Warn("page request outcome remains unconfirmed", "request_kind", kind, "request_id", id, "error", err)
	}
}

func (h *PageHandler) initializePageRequestLifecycles(ctx context.Context) {
	for _, kind := range []string{"artifact", "board"} {
		table := pageRequestTable(kind)
		_, err := h.db.ExecContext(ctx, `UPDATE `+table+` SET status='failed',error='Server restarted before confirmed run admission; review before creating a new request',updated_at=? WHERE id IN (SELECT id FROM `+table+` WHERE status='running' AND COALESCE(run_id,'')='' ORDER BY id LIMIT 100)`, tsformat.Format(time.Now()))
		if err != nil {
			h.logger.Warn("initialize page request lifecycle", "request_kind", kind, "error", err)
		}
	}
}

// Independent of the two blocking dispatch workers. Cursoring prevents a quiet
// bound run from starving later jobs; every sweep touches at most 100 rows.
func (h *PageHandler) reconcilePageRequests(ctx context.Context, after string, now time.Time) (string, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT kind,id FROM (SELECT 'artifact' AS kind,id FROM page_artifact_requests WHERE status='running' OR(status='queued' AND deadline_at<>'') UNION ALL SELECT 'board' AS kind,id FROM page_board_requests WHERE status='running' OR(status='queued' AND deadline_at<>'')) WHERE kind||':'||id>? ORDER BY kind,id LIMIT 100`, after)
	if err != nil {
		return after, err
	}
	var jobs []struct{ kind, id string }
	for rows.Next() {
		var job struct{ kind, id string }
		if err = rows.Scan(&job.kind, &job.id); err != nil {
			rows.Close()
			return after, err
		}
		jobs = append(jobs, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return after, err
	}
	var problems []error
	for _, job := range jobs {
		if err = h.settlePageRequest(ctx, job.kind, job.id, "", now); err != nil {
			problems = append(problems, fmt.Errorf("reconcile %s request: %w", job.kind, err))
		}
	}

	if len(jobs) < 100 {
		return "", errors.Join(problems...)
	}
	last := jobs[len(jobs)-1]
	return last.kind + ":" + last.id, errors.Join(problems...)
}

// All publication paths call this through their transaction-aware reader.
// A committed result remains exactly retryable after its admission deadline.
func pageRequestWithinDeadline(deadline, updated string, now time.Time) bool {
	at, _, err := pageRequestDeadline(pageRequestIdentity{Status: "running", Deadline: deadline, Updated: updated})
	return err == nil && !at.IsZero() && now.Before(at)
}
