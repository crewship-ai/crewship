package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// internalRelationTypes are the mission_relations relation_type values an
// agent may create. They mirror the public handler's set exactly — and the
// table's CHECK constraint, which is the real gate: anything else fails at
// INSERT time with a constraint error the caller would read as a 500.
var internalRelationTypes = map[string]bool{
	"blocks":       true,
	"blocked_by":   true,
	"relates_to":   true,
	"duplicate_of": true,
}

// relationTypeSubIssue is not a mission_relations row at all — it writes
// parent_issue_id on the SOURCE issue. It is the decomposition verb: an agent
// handed a large issue creates one child per piece and links each child
// sub_issue_of the parent, after which each child can be assigned its own
// agent.
//
// Only this direction exists. The inverse ("parent_of", which would write the
// TARGET's row) is deliberately absent: every other write on this handler is
// gated by the crew check on the SOURCE issue, so a verb that mutates the
// target would need a second, differently-scoped gate — a boundary that is
// easy to state and easy to get wrong. One direction keeps the invariant
// "the issue named in the path is the issue you mutate" true without exception.
const relationTypeSubIssue = "sub_issue_of"

// CreateRelation handles POST /api/v1/internal/issues/{identifier}/relations.
//
// This is the agent-facing twin of IssueHandler.CreateRelation. The public one
// derives its tenant from the JWT session and gates on the caller's RBAC role;
// this one has neither, so the scope comes entirely from the X-Internal-Token
// binding:
//
//   - assertInternalTokenWorkspace — the body's workspace_id must equal the
//     token's workspace (403 otherwise). requireInternal only sees the query.
//   - assertBoundCrewWorkspaceDB on the SOURCE issue's crew — a crew-bound
//     (crwv1) sidecar token may only mutate its own crew's issues (#1365, the
//     same boundary UpdateStatus and CreateComment carry).
//   - the target is looked up by identifier AND workspace, so a foreign-tenant
//     identifier is a plain 404 rather than a distinguishable refusal. A 403
//     there would confirm the identifier exists in some other tenant, which is
//     precisely the existence oracle DeleteRelation's comment warns about.
func (h *InternalIssueHandler) CreateRelation(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")

	var req struct {
		WorkspaceID      string `json:"workspace_id"`
		AgentID          string `json:"agent_id"`
		TargetIdentifier string `json:"target_identifier"`
		RelationType     string `json:"relation_type"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if req.TargetIdentifier == "" {
		writeProblem(w, r, http.StatusBadRequest, "target_identifier is required")
		return
	}
	if !internalRelationTypes[req.RelationType] && req.RelationType != relationTypeSubIssue {
		writeProblem(w, r, http.StatusBadRequest,
			"relation_type must be: blocks, blocked_by, relates_to, duplicate_of, sub_issue_of")
		return
	}
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}

	// Source: the issue named in the path. Located by identifier + workspace,
	// then held to the token's crew.
	var sourceID, sourceCrew string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, crew_id FROM missions WHERE identifier = ? AND workspace_id = ?`,
		ident, req.WorkspaceID).Scan(&sourceID, &sourceCrew)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "find issue for relation", err)
		return
	}
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &sourceCrew) {
		return
	}

	// Target: same workspace only. Deliberately NOT crew-gated — linking to a
	// sibling crew's issue is a legitimate cross-team signal ("we are blocked
	// on their work") and it does not write that issue's row. The one verb
	// that WOULD write it does not exist (see relationTypeSubIssue).
	var targetID string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND workspace_id = ?`,
		req.TargetIdentifier, req.WorkspaceID).Scan(&targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Target issue not found: "+req.TargetIdentifier)
			return
		}
		internalError(w, r, h.logger, "find target issue for relation", err)
		return
	}
	if sourceID == targetID {
		writeProblem(w, r, http.StatusBadRequest, "Cannot relate an issue to itself")
		return
	}

	// Actor for the audit trail — same convention as UpdateStatus: a real
	// agent when the sidecar forwarded one, "system" for a non-agent internal
	// caller (mission_activity's CHECK allows it; mission_comments' does not).
	actorType, actorID := "agent", req.AgentID
	if actorID == "" {
		actorType, actorID = "system", "system"
	}

	if req.RelationType == relationTypeSubIssue {
		// The hierarchy must stay a forest. Self-parenting is already refused
		// above; wouldCycleParent walks the target's ancestors so A → B → A
		// (two calls, trivially reachable for an agent decomposing a backlog
		// in a loop) is refused too. The public Update path calls the same
		// helper — an agent-only cycle rule would have the two endpoints
		// disagree about what the same graph may look like.
		switch err := wouldCycleParent(r.Context(), h.db, sourceID, targetID, req.WorkspaceID); {
		case errors.Is(err, errParentCycle):
			writeProblem(w, r, http.StatusBadRequest,
				"sub_issue_of would create a parent cycle")
			return
		case err != nil:
			internalError(w, r, h.logger, "check parent cycle", err)
			return
		}
		if _, err := h.db.ExecContext(r.Context(),
			`UPDATE missions SET parent_issue_id = ?, updated_at = datetime('now') WHERE id = ?`,
			targetID, sourceID); err != nil {
			internalError(w, r, h.logger, "set parent issue", err)
			return
		}
		h.logActivity(r.Context(), sourceID, actorType, actorID, "parent_changed", req.TargetIdentifier)
		broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "issue.updated",
			map[string]string{"id": sourceID, "identifier": ident})
		writeJSON(w, http.StatusCreated, map[string]string{
			"status":        "ok",
			"relation_type": relationTypeSubIssue,
			"parent_id":     targetID,
		})
		return
	}

	// blocked_by is stored as the inverse `blocks` row with the endpoints
	// swapped — same normalisation the public handler applies, so the two
	// entry points cannot produce two shapes of the same link.
	actualSource, actualTarget, actualType := sourceID, targetID, req.RelationType
	if req.RelationType == "blocked_by" {
		actualSource, actualTarget, actualType = targetID, sourceID, "blocks"
	}

	id := generateCUID()
	if _, err := h.db.ExecContext(r.Context(),
		`INSERT INTO mission_relations (id, source_id, target_id, relation_type) VALUES (?, ?, ?, ?)`,
		id, actualSource, actualTarget, actualType); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeProblem(w, r, http.StatusConflict, "Relation already exists")
			return
		}
		internalError(w, r, h.logger, "create relation", err)
		return
	}

	h.logActivity(r.Context(), sourceID, actorType, actorID,
		"relation_added", req.RelationType+" "+req.TargetIdentifier)
	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "issue.updated",
		map[string]string{"id": sourceID, "identifier": ident})

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":            id,
		"status":        "ok",
		"relation_type": actualType,
	})
}
