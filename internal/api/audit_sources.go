package api

import (
	"math"
	"net/http"
)

// A workspace keeps more than one audit trail, and they are separate on
// purpose:
//
//   - audit_logs           — workspace state changes (this page's default)
//   - crew_audit_log       — cross-crew dispatch, messages, shared files
//   - credential_audit     — which secret was used, revealed or rotated
//   - keeper_request_events— the gatekeeper's APPEND-ONLY decision ledger
//
// Merging them into one table would cost the keeper ledger the append-only
// guarantee it exists for, and would force four different event shapes into
// one schema. So the storage stays split and only the READING is unified:
// each source is projected into the same auditResponse the workspace stream
// already returns, and the page gains a switch instead of the operator
// gaining three more places to look.
const (
	auditSourceWorkspace   = "workspace"
	auditSourceCrews       = "crews"
	auditSourceCredentials = "credentials"
	auditSourceKeeper      = "keeper"
)

// auditSourceQuery is one stream projected onto the common row shape.
//
// Each carries its own workspace scoping: crew_audit_log has the column
// directly, credential_audit reaches it through the credential, and the
// keeper ledger's column is nullable, so a row with no workspace is excluded
// rather than shown to everyone.
// The list/count strings stop at the workspace predicate so the date
// range can be appended as a bound clause before ORDER BY; `ts` names
// the qualified timestamp column that range applies to, which differs
// per source (created_at, occurred_at, recorded_at).
type auditSourceQuery struct{ list, count, ts, order string }

var auditSourceQueries = map[string]auditSourceQuery{
	auditSourceWorkspace: {}, // handled by the main List path

	auditSourceCrews: {
		list: `
			SELECT c.id, c.workspace_id, NULL, c.action, 'CREW',
			       COALESCE(c.from_crew_id, c.to_crew_id), c.details, NULL, NULL, c.created_at,
			       NULL, NULL,
			       (SELECT name FROM crews WHERE id = COALESCE(c.from_crew_id, c.to_crew_id))
			FROM crew_audit_log c
			WHERE c.workspace_id = ?`,
		count: `SELECT COUNT(*) FROM crew_audit_log c WHERE c.workspace_id = ?`,
		ts:    "c.created_at",
		order: "ORDER BY c.created_at DESC",
	},

	auditSourceCredentials: {
		list: `
			SELECT ca.id, cr.workspace_id, NULL, ca.event_type, 'CREDENTIAL',
			       ca.credential_id, ca.metadata_json, ca.ip_address, NULL, ca.occurred_at,
			       NULL, NULL,
			       cr.name
			FROM credential_audit ca
			JOIN credentials cr ON cr.id = ca.credential_id
			WHERE cr.workspace_id = ?`,
		count: `
			SELECT COUNT(*) FROM credential_audit ca
			JOIN credentials cr ON cr.id = ca.credential_id
			WHERE cr.workspace_id = ?`,
		ts:    "ca.occurred_at",
		order: "ORDER BY ca.occurred_at DESC",
	},

	auditSourceKeeper: {
		list: `
			SELECT k.id, k.workspace_id, k.actor_id, k.state, 'KEEPER_REQUEST',
			       k.request_id, k.reason, NULL, NULL, k.recorded_at,
			       NULL, NULL,
			       COALESCE(k.command, k.intent, k.request_type)
			FROM keeper_request_events k
			WHERE k.workspace_id = ?`,
		count: `SELECT COUNT(*) FROM keeper_request_events k WHERE k.workspace_id = ?`,
		ts:    "k.recorded_at",
		order: "ORDER BY k.recorded_at DESC",
	},
}

// listAlternateSource answers GET /api/v1/audit?source=… for everything other
// than the workspace stream.
//
// The filters the workspace stream supports (action, entity_type, user, free
// text) are deliberately NOT applied here: each source has its own vocabulary
// — a keeper state is not an action verb, a credential event has no user —
// and silently ignoring a filter the caller passed would be worse than not
// offering it. The date range and pagination, which mean the same thing
// everywhere, do apply.
func (h *AuditHandler) listAlternateSource(
	w http.ResponseWriter, r *http.Request, source, workspaceID string,
	page, limit, offset int, dateFrom, dateTo string,
) {
	q := auditSourceQueries[source]

	listQuery, countQuery := q.list, q.count
	args := []any{workspaceID}
	countArgs := []any{workspaceID}
	// Same semantics as the workspace stream: inclusive on both ends,
	// compared as stored strings.
	if dateFrom != "" {
		clause := " AND " + q.ts + " >= ?"
		listQuery += clause
		countQuery += clause
		args = append(args, dateFrom)
		countArgs = append(countArgs, dateFrom)
	}
	if dateTo != "" {
		clause := " AND " + q.ts + " <= ?"
		listQuery += clause
		countQuery += clause
		args = append(args, dateTo)
		countArgs = append(countArgs, dateTo)
	}
	listQuery += " " + q.order + " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), listQuery, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list audit source "+source, err)
		return
	}
	defer rows.Close()

	result := []auditResponse{}
	for rows.Next() {
		var a auditResponse
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.UserID, &a.Action,
			&a.EntityType, &a.EntityID, &a.Metadata, &a.IPAddress,
			&a.UserAgent, &a.CreatedAt, &a.UserEmail, &a.UserName, &a.EntityName); err != nil {
			replyInternalError(w, h.logger, "scan audit source "+source, err)
			return
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (audit source "+source+")", err)
		return
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(), countQuery, countArgs...).Scan(&total); err != nil {
		replyInternalError(w, h.logger, "count audit source "+source, err)
		return
	}

	writeJSON(w, http.StatusOK, auditListResponse{
		Data: result,
		Pagination: pagination{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: int(math.Ceil(float64(total) / float64(limit))),
		},
	})
}
