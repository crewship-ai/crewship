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
//
// INTERNAL-ONLY: the empty-workspaceID branch intentionally skips the
// scope check so cross-workspace internal sweepers (SweepTimeouts,
// admin audit tooling) can resolve rows without pre-knowing the owning
// workspace. External handlers MUST always pass a non-empty
// workspaceID — the request-scoped Decide / Cancel / Get paths already
// fail closed on empty input. Never thread a user-controlled value
// into the workspaceID argument without a pre-check.
func Get(ctx context.Context, db *sql.DB, workspaceID, id string) (*Request, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	var (
		row *sql.Row
	)
	if workspaceID == "" {
		// Unscoped path — see doc comment. Reached only from internal
		// sweepers that need cross-workspace visibility.
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
		req                                                            Request
		crew, agent, mission, decidedBy, decidedAt, comment, timeoutAt sql.NullString
		payloadStr, kindStr, statusStr, createdAt                      string
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
