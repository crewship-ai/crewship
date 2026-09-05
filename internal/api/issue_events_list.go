package api

// issue_events_list.go — GET .../issues/{identifier}/events?after_seq= (§14.1,
// work package B11, #2368). The B1 event log (internal/missionactivity,
// mission_activity.seq) already gives every row on an issue a monotonic,
// per-mission cursor; this is the first reader that exposes it directly,
// rather than through ListActivity's created_at-ordered, unpaginated,
// non-cursorable top-50.
//
// Why this exists alongside ListActivity: F43 (PRD-ISSUES-AND-ROUTINES-
// 2026.md §2.6) — ws.Hub.dispatch sends non-blocking and drops a frame
// silently when a client's buffer is full. The realtime allowlist (F32)
// only guards which TYPES a client will accept; it says nothing about
// whether a given frame ever arrived. A board that missed a frame under
// load has no way to notice, let alone recover, without a cursor to ask
// "what happened after the last thing I saw" — this endpoint is that
// question, and hooks/use-issue-events-resync.ts (the client gap detector)
// is the thing that asks it.
//
// after_seq=0 (the default) returns the issue's entire event history in
// seq order — a cold-start client backfilling before it starts trusting
// live frames. A client that has seen up to seq N passes after_seq=N and
// gets exactly what it missed.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

// maxIssueEventsPage bounds one response to a fixed, non-user-controlled
// size. after_seq is a cursor, not a page size — a caller cannot ask for
// an unbounded slice by choice of that parameter, and CodeQL's "allocation
// size from user input" class never applies here because nothing sizes an
// allocation off afterSeq itself.
const maxIssueEventsPage = 500

// issueEventDTO is one mission_activity row as the ordered event log reads
// it — the same columns missionactivity.Emit writes, minus id-internal
// bookkeeping the client has no use for.
type issueEventDTO struct {
	ID          string  `json:"id"`
	MissionID   string  `json:"mission_id"`
	Seq         int     `json:"seq"`
	ActorType   string  `json:"actor_type"`
	ActorID     string  `json:"actor_id"`
	ActorName   *string `json:"actor_name,omitempty"`
	Action      string  `json:"action"`
	Details     *string `json:"details"`
	PayloadJSON *string `json:"payload_json,omitempty"`
	SourceKind  *string `json:"source_kind,omitempty"`
	SourceID    *string `json:"source_id,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

// issueEventsResponse wraps the ordered page with the mission's current
// high-water mark. latest_seq lets a client's resync tell "caught up" (an
// empty page whose latest_seq matches what it already has) from "there is
// more — page again with the last seq in this response" without a second,
// speculative round trip.
type issueEventsResponse struct {
	Events    []issueEventDTO `json:"events"`
	AfterSeq  int             `json:"after_seq"`
	LatestSeq int             `json:"latest_seq"`
}

// ListEvents — GET /api/v1/crews/{crewId}/issues/{identifier}/events?after_seq=N
func (h *IssueHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "resolve issue for events", err)
		return
	}

	afterSeq := 0
	if raw := r.URL.Query().Get("after_seq"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 0 {
			writeProblem(w, r, http.StatusBadRequest, "after_seq must be a non-negative integer")
			return
		}
		afterSeq = n
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.mission_id, a.seq, a.actor_type, a.actor_id, a.action, a.details,
		       a.payload_json, a.source_kind, a.source_id, a.created_at,
		       CASE
		           WHEN a.actor_type = 'user' THEN (SELECT full_name FROM users WHERE id = a.actor_id)
		           WHEN a.actor_type = 'agent' THEN (SELECT name FROM agents WHERE id = a.actor_id)
		           ELSE NULL
		       END AS actor_name
		FROM mission_activity a
		WHERE a.mission_id = ? AND a.seq IS NOT NULL AND a.seq > ?
		ORDER BY a.seq ASC
		LIMIT ?`, missionID, afterSeq, maxIssueEventsPage)
	if err != nil {
		internalError(w, r, h.logger, "list issue events", err)
		return
	}
	defer rows.Close()

	events := []issueEventDTO{}
	for rows.Next() {
		var e issueEventDTO
		var details, payload, sourceKind, sourceID, actorName sql.NullString
		if err := rows.Scan(&e.ID, &e.MissionID, &e.Seq, &e.ActorType, &e.ActorID, &e.Action,
			&details, &payload, &sourceKind, &sourceID, &e.CreatedAt, &actorName); err != nil {
			internalError(w, r, h.logger, "scan issue event", err)
			return
		}
		if details.Valid {
			e.Details = &details.String
		}
		if payload.Valid {
			e.PayloadJSON = &payload.String
		}
		if sourceKind.Valid {
			e.SourceKind = &sourceKind.String
		}
		if sourceID.Valid {
			e.SourceID = &sourceID.String
		}
		if actorName.Valid {
			e.ActorName = &actorName.String
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (issue events)", err)
		return
	}

	var latestSeq sql.NullInt64
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT MAX(seq) FROM mission_activity WHERE mission_id = ?`, missionID,
	).Scan(&latestSeq); err != nil {
		internalError(w, r, h.logger, "latest seq for issue events", err)
		return
	}

	writeJSON(w, http.StatusOK, issueEventsResponse{
		Events:    events,
		AfterSeq:  afterSeq,
		LatestSeq: int(latestSeq.Int64),
	})
}
