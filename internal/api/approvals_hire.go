package api

// Staged-hire projection into the approvals surface (#1209).
//
// Guided-autonomy ephemeral hires stage the agent row in
// PENDING_REVIEW with a blocking inbox waitpoint — a fourth HITL
// lifecycle that, before this file, was invisible to `crewship
// approvals list` / the dashboard approvals queue and decidable only
// via `crewship hire approve`. An operator watching the normal
// approval surfaces never saw it.
//
// This file closes the gap as a READ-MODEL projection, deliberately
// not a data migration: pending hires are projected live from the
// agents table into the approvals list/get responses as synthetic
// rows (kind=agent_hire, id = the agent id), and an approve decision
// on such a row delegates to the exact same approveStagedHire state
// machine the /agents/{id}/approve-hire endpoint uses. Nothing is
// written to approvals_queue, so there is no second source of truth
// and no schema change.
//
// Two deliberate asymmetries with queue-backed approvals:
//
//   - No decision history: an approved hire flips to IDLE and simply
//     leaves the projection (there is no persisted "approved" row to
//     show under ?status=approved). The journal/audit trail
//     (agent.hire_approved) remains the history surface.
//   - No deny: a staged hire has no deny lifecycle today — it ghosts
//     when its TTL expires. Deny returns a 409 explaining that,
//     rather than smuggling a new state machine into this fix.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

// stagedHireApprover is the slice of *AgentHandler the approvals
// handler needs to delegate an approve decision to the hire state
// machine. Wired by the router via SetHireApprover; nil-tolerant
// (Decide degrades to a hint pointing at `crewship hire approve`).
type stagedHireApprover interface {
	approveStagedHire(ctx context.Context, workspaceID, agentID, userID string) (string, string, error)
}

// SetHireApprover wires the agents handler so approvals Decide can
// delegate staged-hire approvals (#1209).
func (h *ApprovalsHandler) SetHireApprover(a stagedHireApprover) { h.hire = a }

// stagedHireSelect is the shared projection query. One row per
// ephemeral agent parked in PENDING_REVIEW in the workspace; the
// subquery pulls the hiring user off the blocking inbox waitpoint the
// Hire handler wrote (best-effort — an empty requested_by beats
// dropping the row).
const stagedHireSelect = `
	SELECT a.id, COALESCE(a.crew_id, ''), a.name, COALESCE(a.hire_reason, ''),
	       a.created_at, COALESCE(a.expires_at, ''),
	       COALESCE((SELECT i.sender_id FROM inbox_items i
	                 WHERE i.kind = 'waitpoint' AND i.source_id = a.id AND i.blocking = 1
	                 ORDER BY i.created_at DESC LIMIT 1), '')
	FROM agents a
	WHERE a.workspace_id = ? AND a.status = 'PENDING_REVIEW'
	  AND a.ephemeral = 1 AND a.deleted_at IS NULL`

// scanStagedHire builds the synthetic approvals row for one staged
// hire. Shape-compatible with queue rows on the wire (same struct),
// discriminated by kind=agent_hire plus the payload marker.
func scanStagedHire(workspaceID string, scan func(dest ...any) error) (harbormaster.Request, error) {
	var (
		id, crewID, name, hireReason, createdAt, expiresAt, requestedBy string
	)
	if err := scan(&id, &crewID, &name, &hireReason, &createdAt, &expiresAt, &requestedBy); err != nil {
		return harbormaster.Request{}, err
	}
	reason := hireReason
	if reason == "" {
		reason = "ephemeral hire pending review: " + name
	}
	req := harbormaster.Request{
		ID:          id,
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		AgentID:     id,
		RequestedBy: requestedBy,
		Kind:        harbormaster.KindAgentHire,
		Reason:      reason,
		Status:      harbormaster.StatusPending,
		Payload: map[string]any{
			"type":       "hire",
			"agent_name": name,
			"decide_via": "crewship hire approve " + id,
		},
	}
	if t, ok := parseHireTime(createdAt); ok {
		req.CreatedAt = t
	}
	if expiresAt != "" {
		req.Payload["expires_at"] = expiresAt
		if t, ok := parseHireTime(expiresAt); ok {
			req.TimeoutAt = &t
		}
	}
	return req, nil
}

// parseHireTime tolerates the timestamp layouts that reach the agents
// table (RFC3339 from the Go handlers, sqlite datetime() from older
// seeds/migrations).
func parseHireTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// listStagedHires projects all pending hires in the workspace, newest
// first, capped at limit.
func (h *ApprovalsHandler) listStagedHires(ctx context.Context, workspaceID string, limit int) ([]harbormaster.Request, error) {
	rows, err := h.db.QueryContext(ctx, stagedHireSelect+` ORDER BY a.created_at DESC LIMIT ?`,
		workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []harbormaster.Request
	for rows.Next() {
		req, err := scanStagedHire(workspaceID, rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// getStagedHire projects a single pending hire by agent id, workspace
// scoped. Returns (nil, nil) when the id is not a pending hire here —
// including approved/ghosted hires, which drop out of the projection.
func (h *ApprovalsHandler) getStagedHire(ctx context.Context, workspaceID, agentID string) (*harbormaster.Request, error) {
	row := h.db.QueryRowContext(ctx, stagedHireSelect+` AND a.id = ?`, workspaceID, agentID)
	req, err := scanStagedHire(workspaceID, row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// decideStagedHire is Decide's fallback once the id turned out not to
// be an approvals_queue row. The caller has already enforced the
// OWNER/ADMIN gate — strictly stronger than the MANAGER+ bar on the
// native /approve-hire endpoint, so delegation can't widen access.
//
//   - unknown id (or a hire in another workspace) → 404, exactly what
//     Decide returned for these ids before #1209.
//   - deny → 409: a staged hire has no deny lifecycle (it ghosts on
//     TTL); refusing loudly beats inventing a second state machine.
//   - approve → delegate to the same approveStagedHire core the
//     /agents/{id}/approve-hire endpoint runs.
func (h *ApprovalsHandler) decideStagedHire(w http.ResponseWriter, r *http.Request, workspaceID, id string, status harbormaster.Status, userID string) {
	hireRow, err := h.getStagedHire(r.Context(), workspaceID, id)
	if err != nil {
		h.logger.Error("approvals decide: staged hire lookup", "err", err)
		replyError(w, http.StatusInternalServerError, "decide failed")
		return
	}
	if hireRow == nil {
		replyError(w, http.StatusNotFound, "not found")
		return
	}
	if status == harbormaster.StatusDenied {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "staged hires cannot be denied from the approvals surface",
			"reason": "an unapproved hire ghosts when its TTL expires; approve it here or with `crewship hire approve " + id + "`",
		})
		return
	}
	if h.hire == nil {
		// Router misconfiguration (SetHireApprover never called) —
		// degrade to the native flow instead of a misleading 404/500.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "hire approval is not wired on this approvals surface",
			"reason": "decide this waitpoint with `crewship hire approve " + id + "`",
		})
		return
	}
	_, _, aerr := h.hire.approveStagedHire(r.Context(), workspaceID, id, userID)
	var conflict *stagedHireConflict
	switch {
	case errors.Is(aerr, errStagedHireNotFound):
		// Raced a TTL sweep / delete between the projection read and
		// the approve — same observable as "never existed".
		replyError(w, http.StatusNotFound, "not found")
		return
	case errors.As(aerr, &conflict):
		writeJSON(w, http.StatusConflict, conflict.resp)
		return
	case aerr != nil:
		h.logger.Error("approvals decide: staged hire approve", "err", aerr)
		replyError(w, http.StatusInternalServerError, "decide failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(status), "decided_by": userID})
}

// mergeApprovalRows interleaves queue rows and staged-hire rows,
// newest first, capped at limit. Both inputs are already sorted; a
// single stable sort keeps this simple and the row counts tiny.
func mergeApprovalRows(queue, hires []harbormaster.Request, limit int) []harbormaster.Request {
	merged := make([]harbormaster.Request, 0, len(queue)+len(hires))
	merged = append(merged, queue...)
	merged = append(merged, hires...)
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].CreatedAt.After(merged[j].CreatedAt)
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}
