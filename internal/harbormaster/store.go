package harbormaster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// defaultTimeoutSecs is the fallback for Request.TimeoutSecs. One hour is
// long enough for an operator to notice a Slack/UI ping and short enough
// that a forgotten request doesn't block an agent overnight.
const defaultTimeoutSecs = 3600

// timeFmt is the storage format for the TEXT timestamp columns. Matches
// the journal package so cross-table comparisons stay sortable.
const timeFmt = "2006-01-02T15:04:05.000Z"

// Enqueue writes a new pending row to approvals_queue and emits the
// matching journal entry. Returns the generated request ID. The journal
// emit is best-effort; a failure there is logged via the emitter and does
// not roll back the DB insert (the audit row is recoverable from the queue
// state, but a queued-but-unannounced approval would be invisible).
func Enqueue(ctx context.Context, db *sql.DB, j journal.Emitter, req Request) (string, error) {
	if req.WorkspaceID == "" {
		return "", errors.New("harbormaster: workspace_id required")
	}
	if req.RequestedBy == "" {
		return "", errors.New("harbormaster: requested_by required")
	}
	if req.Kind == "" {
		return "", errors.New("harbormaster: kind required")
	}
	if req.Reason == "" {
		return "", errors.New("harbormaster: reason required")
	}

	if req.ID == "" {
		req.ID = newRequestID()
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	if req.TimeoutAt == nil {
		secs := req.TimeoutSecs
		if secs <= 0 {
			secs = defaultTimeoutSecs
		}
		t := req.CreatedAt.Add(time.Duration(secs) * time.Second)
		req.TimeoutAt = &t
	}
	if req.Status == "" {
		req.Status = StatusPending
	}

	payload, err := encodeJSON(req.Payload)
	if err != nil {
		return "", fmt.Errorf("harbormaster: marshal payload: %w", err)
	}

	const insertSQL = `INSERT INTO approvals_queue
		(id, workspace_id, crew_id, agent_id, mission_id, requested_by, kind, reason,
		 payload, status, timeout_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = db.ExecContext(ctx, insertSQL,
		req.ID,
		req.WorkspaceID,
		nullable(req.CrewID),
		nullable(req.AgentID),
		nullable(req.MissionID),
		req.RequestedBy,
		string(req.Kind),
		req.Reason,
		payload,
		string(req.Status),
		req.TimeoutAt.UTC().Format(timeFmt),
		req.CreatedAt.UTC().Format(timeFmt),
	)
	if err != nil {
		return "", fmt.Errorf("harbormaster: insert approval: %w", err)
	}

	if j != nil {
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			Type:        journal.EntryApprovalRequest,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorAgent,
			ActorID:     req.RequestedBy,
			Summary:     fmt.Sprintf("approval requested: %s — %s", req.Kind, req.Reason),
			Payload:     map[string]any{"approval_id": req.ID, "kind": string(req.Kind)},
			Refs:        map[string]any{"approval_id": req.ID},
		})
	}

	return req.ID, nil
}

// Decide moves a pending row to approved/denied. The status check happens
// inside the same UPDATE so two concurrent deciders can't both win — the
// second sees rowsAffected == 0 and gets ErrNotPending.
//
// ErrNotPending is also returned when the row exists but is already
// approved/denied/timed out; the caller should treat that as a no-op.
var (
	ErrNotPending = errors.New("harbormaster: approval not pending")
	ErrNotFound   = errors.New("harbormaster: approval not found")
	ErrBadStatus  = errors.New("harbormaster: invalid decision status")
)

// Decide flips a pending request to approved or denied. The status arg
// must be StatusApproved or StatusDenied; anything else is rejected with
// ErrBadStatus so callers can't accidentally write StatusPending or
// StatusCancelled through this path.
// Decide flips a pending approval to approved/denied and emits the matching
// journal entry. workspaceID is load-bearing for tenant isolation — without
// it in the UPDATE predicate, a caller who learns another workspace's
// approval ID could decide it cross-tenant. Callers MUST pass the
// workspace they resolved from auth context, not one derived from the row.
func Decide(ctx context.Context, db *sql.DB, j journal.Emitter, workspaceID, id string, status Status, decidedBy, comment string) error {
	if status != StatusApproved && status != StatusDenied {
		return ErrBadStatus
	}
	if id == "" {
		return ErrNotFound
	}

	now := time.Now().UTC()
	const updateSQL = `UPDATE approvals_queue
		SET status = ?, decided_by = ?, decided_at = ?, decision_comment = ?
		WHERE id = ? AND workspace_id = ? AND status = 'pending'`
	res, err := db.ExecContext(ctx, updateSQL,
		string(status),
		nullable(decidedBy),
		now.Format(timeFmt),
		nullable(comment),
		id,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("harbormaster: update decision: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("harbormaster: rows affected: %w", err)
	}
	if n == 0 {
		// Distinguish missing vs. not-pending so the caller can render
		// the right error to the operator. The Get below is scoped to
		// the caller's workspace, so a cross-tenant ID looks identical
		// to a nonexistent one (ErrNotFound) — no existence leak.
		row, err := Get(ctx, db, workspaceID, id)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		return ErrNotPending
	}

	// Reload so the journal entry carries the canonical scope. Scoped
	// to the caller's workspace, matching the UPDATE above.
	row, err := Get(ctx, db, workspaceID, id)
	if err != nil || row == nil {
		// Decision succeeded; failing the audit emit shouldn't error out.
		return nil
	}

	if j != nil {
		entryType := journal.EntryApprovalGranted
		if status == StatusDenied {
			entryType = journal.EntryApprovalDenied
		}
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: row.WorkspaceID,
			CrewID:      row.CrewID,
			AgentID:     row.AgentID,
			MissionID:   row.MissionID,
			Type:        entryType,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     decidedBy,
			Summary:     fmt.Sprintf("approval %s by %s", status, decidedBy),
			Payload: map[string]any{
				"approval_id": row.ID,
				"kind":        string(row.Kind),
				"comment":     comment,
			},
			Refs: map[string]any{"approval_id": row.ID},
		})
	}
	return nil
}

// Cancel withdraws a still-pending request. Used when the agent that
// requested approval terminates / aborts before a human responds. Cancel
// is a no-op on already-resolved requests and returns ErrNotPending so
// the caller can log loudly if that wasn't expected.
func Cancel(ctx context.Context, db *sql.DB, j journal.Emitter, id, reason string) error {
	if id == "" {
		return ErrNotFound
	}
	now := time.Now().UTC()
	const updateSQL = `UPDATE approvals_queue
		SET status = 'cancelled', decided_at = ?, decision_comment = ?
		WHERE id = ? AND status = 'pending'`
	res, err := db.ExecContext(ctx, updateSQL, now.Format(timeFmt), nullable(reason), id)
	if err != nil {
		return fmt.Errorf("harbormaster: cancel: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("harbormaster: rows affected: %w", err)
	}
	if n == 0 {
		row, err := Get(ctx, db, "", id)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		return ErrNotPending
	}
	if j != nil {
		row, _ := Get(ctx, db, "", id)
		if row != nil {
			_, _ = j.Emit(ctx, journal.Entry{
				WorkspaceID: row.WorkspaceID,
				CrewID:      row.CrewID,
				AgentID:     row.AgentID,
				MissionID:   row.MissionID,
				Type:        journal.EntryApprovalDenied,
				Severity:    journal.SeverityNotice,
				ActorType:   journal.ActorAgent,
				ActorID:     row.RequestedBy,
				Summary:     fmt.Sprintf("approval cancelled: %s", reason),
				Payload:     map[string]any{"approval_id": row.ID, "cancelled": true, "reason": reason},
				Refs:        map[string]any{"approval_id": row.ID},
			})
		}
	}
	return nil
}

// SweepTimeouts moves any pending row whose timeout_at is in the past to
// 'timeout' and emits one EntryApprovalTimeout per row. Designed to be
// called from a 30s ticker; safe to invoke concurrently because the
// UPDATE is conditional on status='pending'.
func SweepTimeouts(ctx context.Context, db *sql.DB, j journal.Emitter) (int, error) {
	now := time.Now().UTC().Format(timeFmt)

	// First snapshot the soon-to-be-timed-out IDs so the journal entries
	// know which scope to emit under. Doing the SELECT before the UPDATE
	// gives us a small race (a human could approve in between) but the
	// UPDATE's status='pending' guard is the source of truth — we just
	// emit a stale audit entry, which is preferable to skipping audit.
	const selectSQL = `SELECT id, workspace_id, crew_id, agent_id, mission_id,
			requested_by, kind, reason
		FROM approvals_queue
		WHERE status = 'pending' AND timeout_at IS NOT NULL AND timeout_at <= ?`
	rows, err := db.QueryContext(ctx, selectSQL, now)
	if err != nil {
		return 0, fmt.Errorf("harbormaster: sweep select: %w", err)
	}
	type stale struct {
		id, ws, crew, agent, mission, requestedBy, reason string
		kind                                              Kind
	}
	var pending []stale
	for rows.Next() {
		var (
			s                              stale
			crew, agent, mission, kindStr  sql.NullString
		)
		if err := rows.Scan(&s.id, &s.ws, &crew, &agent, &mission, &s.requestedBy, &kindStr, &s.reason); err != nil {
			rows.Close()
			return 0, fmt.Errorf("harbormaster: sweep scan: %w", err)
		}
		s.crew = crew.String
		s.agent = agent.String
		s.mission = mission.String
		s.kind = Kind(kindStr.String)
		pending = append(pending, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if len(pending) == 0 {
		return 0, nil
	}

	const updateSQL = `UPDATE approvals_queue
		SET status = 'timeout', decided_at = ?
		WHERE status = 'pending' AND timeout_at IS NOT NULL AND timeout_at <= ?`
	res, err := db.ExecContext(ctx, updateSQL, now, now)
	if err != nil {
		return 0, fmt.Errorf("harbormaster: sweep update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("harbormaster: sweep rows: %w", err)
	}

	if j != nil {
		for _, s := range pending {
			_, _ = j.Emit(ctx, journal.Entry{
				WorkspaceID: s.ws,
				CrewID:      s.crew,
				AgentID:     s.agent,
				MissionID:   s.mission,
				Type:        journal.EntryApprovalTimeout,
				Severity:    journal.SeverityWarn,
				ActorType:   journal.ActorSystem,
				ActorID:     "harbormaster",
				Summary:     fmt.Sprintf("approval timed out: %s", s.reason),
				Payload:     map[string]any{"approval_id": s.id, "kind": string(s.kind)},
				Refs:        map[string]any{"approval_id": s.id},
			})
		}
	}

	return int(n), nil
}

// List returns approvals for a workspace, optionally filtered by status.
// Newest-first. The cap is enforced server-side so a buggy caller can't
// pull the entire table by passing limit=MaxInt.
func List(ctx context.Context, db *sql.DB, workspaceID string, statusFilter Status, limit int) ([]Request, error) {
	if workspaceID == "" {
		return nil, errors.New("harbormaster: workspace_id required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		q    string
		args []any
	)
	if statusFilter == "" {
		q = `SELECT id, workspace_id, crew_id, agent_id, mission_id, requested_by, kind, reason,
				payload, status, decided_by, decided_at, decision_comment, timeout_at, created_at
			FROM approvals_queue WHERE workspace_id = ? ORDER BY created_at DESC LIMIT ?`
		args = []any{workspaceID, limit}
	} else {
		q = `SELECT id, workspace_id, crew_id, agent_id, mission_id, requested_by, kind, reason,
				payload, status, decided_by, decided_at, decision_comment, timeout_at, created_at
			FROM approvals_queue WHERE workspace_id = ? AND status = ?
			ORDER BY created_at DESC LIMIT ?`
		args = []any{workspaceID, string(statusFilter), limit}
	}

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("harbormaster: list: %w", err)
	}
	defer rows.Close()

	out := make([]Request, 0, limit)
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Get fetches a single request. If workspaceID is non-empty the row is
// scoped to that workspace so an API caller can't peek into another
// tenant's queue by guessing IDs.
func Get(ctx context.Context, db *sql.DB, workspaceID, id string) (*Request, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	var (
		row *sql.Row
	)
	if workspaceID == "" {
		row = db.QueryRowContext(ctx, `SELECT id, workspace_id, crew_id, agent_id, mission_id,
				requested_by, kind, reason, payload, status, decided_by, decided_at,
				decision_comment, timeout_at, created_at
			FROM approvals_queue WHERE id = ?`, id)
	} else {
		row = db.QueryRowContext(ctx, `SELECT id, workspace_id, crew_id, agent_id, mission_id,
				requested_by, kind, reason, payload, status, decided_by, decided_at,
				decision_comment, timeout_at, created_at
			FROM approvals_queue WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	}
	req, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &req, nil
}

// rowScanner is the common surface of *sql.Row and *sql.Rows so scanRequest
// can serve both Get and List.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRequest(r rowScanner) (Request, error) {
	var (
		req                                                               Request
		crew, agent, mission, decidedBy, decidedAt, comment, timeoutAt    sql.NullString
		payloadStr, kindStr, statusStr, createdAt                         string
	)
	if err := r.Scan(
		&req.ID,
		&req.WorkspaceID,
		&crew,
		&agent,
		&mission,
		&req.RequestedBy,
		&kindStr,
		&req.Reason,
		&payloadStr,
		&statusStr,
		&decidedBy,
		&decidedAt,
		&comment,
		&timeoutAt,
		&createdAt,
	); err != nil {
		return Request{}, err
	}
	req.CrewID = crew.String
	req.AgentID = agent.String
	req.MissionID = mission.String
	req.Kind = Kind(kindStr)
	req.Status = Status(statusStr)
	req.DecidedBy = decidedBy.String
	req.DecisionComment = comment.String
	if t, err := parseTime(createdAt); err == nil {
		req.CreatedAt = t
	}
	if decidedAt.Valid {
		if t, err := parseTime(decidedAt.String); err == nil {
			req.DecidedAt = &t
		}
	}
	if timeoutAt.Valid {
		if t, err := parseTime(timeoutAt.String); err == nil {
			req.TimeoutAt = &t
		}
	}
	if payloadStr != "" && payloadStr != "{}" {
		_ = json.Unmarshal([]byte(payloadStr), &req.Payload)
	}
	return req, nil
}

func encodeJSON(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{
		timeFmt,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("harbormaster: unparseable timestamp %q", s)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// newRequestID returns a short collision-resistant identifier for a queue
// row. 64 bits of entropy is enough for the queue's expected scale; the
// "ap_" prefix matches the journal's "j_" pattern so logs stay greppable.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "ap_" + hex.EncodeToString(b[:])
}
