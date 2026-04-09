package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

type ProposalHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine *orchestrator.MissionEngine
	logger        *slog.Logger
}

func NewProposalHandler(db *sql.DB, hub *ws.Hub, me *orchestrator.MissionEngine, logger *slog.Logger) *ProposalHandler {
	return &ProposalHandler{db: db, hub: hub, missionEngine: me, logger: logger}
}

type proposalMission struct {
	CrewID      string         `json:"crew_id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Tasks       []proposalTask `json:"tasks"`
}

type proposalTask struct {
	Title           string   `json:"title"`
	Description     string   `json:"description,omitempty"`
	AssignedAgentID string   `json:"assigned_agent_id,omitempty"`
	TaskOrder       int      `json:"task_order"`
	DependsOn       []string `json:"depends_on,omitempty"`
	MaxIterations   *int     `json:"max_iterations,omitempty"`
}

type proposalResponse struct {
	ID           string              `json:"id"`
	WorkspaceID  string              `json:"workspace_id"`
	ProposedByID *string             `json:"proposed_by_id"`
	ProposerName *string             `json:"proposer_name,omitempty"`
	ProposerSlug *string             `json:"proposer_slug,omitempty"`
	Title        string              `json:"title"`
	Description  *string             `json:"description"`
	Plan         *string             `json:"plan"`
	Status       string              `json:"status"`
	Missions     []proposalMission   `json:"missions,omitempty"`
	MissionIDs   []string            `json:"mission_ids,omitempty"`
	ReviewedBy   *string             `json:"reviewed_by"`
	ReviewedAt   *string             `json:"reviewed_at"`
	ReviewNotes  *string             `json:"review_notes"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
}

// List handles GET /api/v1/mission-proposals
func (h *ProposalHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	status := r.URL.Query().Get("status")

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	query := `
		SELECT p.id, p.workspace_id, p.proposed_by_id, p.title, p.description,
		       p.plan, p.status, p.missions_json, p.reviewed_by, p.reviewed_at,
		       p.review_notes, p.created_at, p.updated_at,
		       a.name, a.slug
		FROM mission_proposals p
		LEFT JOIN agents a ON a.id = p.proposed_by_id
		WHERE p.workspace_id = ?`
	args := []interface{}{wsID}

	if status != "" {
		query += " AND p.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY p.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list proposals", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []proposalResponse
	for rows.Next() {
		var p proposalResponse
		var missionsJSON *string
		if err := rows.Scan(
			&p.ID, &p.WorkspaceID, &p.ProposedByID, &p.Title, &p.Description,
			&p.Plan, &p.Status, &missionsJSON, &p.ReviewedBy, &p.ReviewedAt,
			&p.ReviewNotes, &p.CreatedAt, &p.UpdatedAt,
			&p.ProposerName, &p.ProposerSlug,
		); err != nil {
			h.logger.Error("scan proposal", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if missionsJSON != nil {
			_ = json.Unmarshal([]byte(*missionsJSON), &p.Missions)
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate proposals", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if result == nil {
		result = []proposalResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/mission-proposals/{proposalId}
func (h *ProposalHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	proposalID := r.PathValue("proposalId")

	p, err := h.loadProposal(r.Context(), wsID, proposalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Proposal not found")
			return
		}
		h.logger.Error("get proposal", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Load created mission IDs if approved
	if p.Status == "APPROVED" {
		p.MissionIDs = h.loadProposalMissionIDs(r.Context(), proposalID)
	}

	writeJSON(w, http.StatusOK, p)
}

// Create handles POST /api/v1/mission-proposals
func (h *ProposalHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Insufficient permissions")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Title        string            `json:"title"`
		Description  string            `json:"description"`
		Plan         string            `json:"plan"`
		ProposedByID string            `json:"proposed_by_id"`
		Missions     []proposalMission `json:"missions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}
	if len(req.Missions) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "at least one mission is required")
		return
	}

	// Validate each mission's crew exists in this workspace
	for i, m := range req.Missions {
		if m.CrewID == "" {
			writeProblem(w, r, http.StatusBadRequest, fmt.Sprintf("missions[%d].crew_id is required", i))
			return
		}
		if err := crewExists(r.Context(), h.db, m.CrewID, wsID); err != nil {
			writeProblem(w, r, http.StatusBadRequest, fmt.Sprintf("missions[%d].crew_id %q not found", i, m.CrewID))
			return
		}
		if m.Title == "" {
			writeProblem(w, r, http.StatusBadRequest, fmt.Sprintf("missions[%d].title is required", i))
			return
		}
	}

	missionsJSON, err := json.Marshal(req.Missions)
	if err != nil {
		h.logger.Error("marshal missions", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	var proposedByID *string
	if req.ProposedByID != "" {
		proposedByID = &req.ProposedByID
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_proposals (id, workspace_id, proposed_by_id, title, description, plan, status, missions_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?, ?, ?)`,
		id, wsID, proposedByID, req.Title, nilIfEmpty(req.Description), nilIfEmpty(req.Plan),
		string(missionsJSON), now, now)
	if err != nil {
		h.logger.Error("create proposal", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	h.broadcastProposalEvent(wsID, "proposal_created", id)

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "PENDING"})
}

// Approve handles POST /api/v1/mission-proposals/{proposalId}/approve
func (h *ProposalHandler) Approve(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeProblem(w, r, http.StatusForbidden, "Insufficient permissions")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	proposalID := r.PathValue("proposalId")

	var req struct {
		ReviewedBy  string `json:"reviewed_by"`
		ReviewNotes string `json:"review_notes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	now := time.Now().UTC().Format(time.RFC3339)

	// Atomic claim: only one approve can succeed (WHERE status = 'PENDING')
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE mission_proposals SET status = 'APPROVED', reviewed_by = ?, reviewed_at = ?, review_notes = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING'`,
		nilIfEmpty(req.ReviewedBy), now, nilIfEmpty(req.ReviewNotes), now, proposalID, wsID)
	if err != nil {
		h.logger.Error("approve proposal claim", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Either not found or already processed
		p, loadErr := h.loadProposal(r.Context(), wsID, proposalID)
		if loadErr != nil {
			writeProblem(w, r, http.StatusNotFound, "Proposal not found")
			return
		}
		writeProblem(w, r, http.StatusConflict, fmt.Sprintf("proposal is %s, only PENDING proposals can be approved", p.Status))
		return
	}

	// Load proposal to get missions JSON
	p, err := h.loadProposal(r.Context(), wsID, proposalID)
	if err != nil {
		h.logger.Error("load proposal after claim", "error", err)
		if _, rbErr := h.db.ExecContext(r.Context(),
			`UPDATE mission_proposals SET status = 'PENDING', reviewed_by = NULL, reviewed_at = NULL, review_notes = NULL, updated_at = ? WHERE id = ?`,
			now, proposalID); rbErr != nil {
			h.logger.Error("rollback proposal to PENDING after failed reload", "proposalID", proposalID, "error", rbErr)
		}
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Create missions from the proposal
	missionIDs, err := h.createMissionsFromProposal(r.Context(), wsID, proposalID, p.Missions)
	if err != nil {
		h.logger.Error("create missions from proposal", "error", err)
		// Rollback the approval since mission creation failed
		if _, rbErr := h.db.ExecContext(r.Context(), `UPDATE mission_proposals SET status = 'PENDING', reviewed_by = NULL, reviewed_at = NULL, review_notes = NULL, updated_at = ? WHERE id = ?`, now, proposalID); rbErr != nil {
			h.logger.Error("rollback proposal approval", "proposalID", proposalID, "error", rbErr)
		}
		writeProblem(w, r, http.StatusInternalServerError, "Failed to create missions from proposal")
		return
	}

	h.broadcastProposalEvent(wsID, "proposal_approved", proposalID)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":          proposalID,
		"status":      "APPROVED",
		"mission_ids": missionIDs,
	})
}

// Reject handles POST /api/v1/mission-proposals/{proposalId}/reject
func (h *ProposalHandler) Reject(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeProblem(w, r, http.StatusForbidden, "Insufficient permissions")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	proposalID := r.PathValue("proposalId")

	var req struct {
		ReviewedBy  string `json:"reviewed_by"`
		ReviewNotes string `json:"review_notes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	now := time.Now().UTC().Format(time.RFC3339)

	// Atomic claim: only one reject can succeed (WHERE status = 'PENDING')
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE mission_proposals SET status = 'REJECTED', reviewed_by = ?, reviewed_at = ?, review_notes = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING'`,
		nilIfEmpty(req.ReviewedBy), now, nilIfEmpty(req.ReviewNotes), now, proposalID, wsID)
	if err != nil {
		h.logger.Error("reject proposal", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		p, loadErr := h.loadProposal(r.Context(), wsID, proposalID)
		if loadErr != nil {
			writeProblem(w, r, http.StatusNotFound, "Proposal not found")
			return
		}
		writeProblem(w, r, http.StatusConflict, fmt.Sprintf("proposal is %s, only PENDING proposals can be rejected", p.Status))
		return
	}

	h.broadcastProposalEvent(wsID, "proposal_rejected", proposalID)

	writeJSON(w, http.StatusOK, map[string]string{"id": proposalID, "status": "REJECTED"})
}

// Delete handles DELETE /api/v1/mission-proposals/{proposalId}
func (h *ProposalHandler) Delete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeProblem(w, r, http.StatusForbidden, "Insufficient permissions")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	proposalID := r.PathValue("proposalId")

	res, err := h.db.ExecContext(r.Context(), `
		DELETE FROM mission_proposals WHERE id = ? AND workspace_id = ? AND status = 'PENDING'`,
		proposalID, wsID)
	if err != nil {
		h.logger.Error("delete proposal", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Proposal not found or already processed")
		return
	}

	h.broadcastProposalEvent(wsID, "proposal_deleted", proposalID)
	w.WriteHeader(http.StatusNoContent)
}

// createMissionsFromProposal creates missions and tasks in the DB from proposal JSON.
// It is a package-level helper shared by ProposalHandler and captain tool executors.
func createMissionsFromProposal(ctx context.Context, db *sql.DB, wsID, proposalID string, missions []proposalMission) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC().Format(time.RFC3339)
	var missionIDs []string

	for _, pm := range missions {
		var leadAgentID string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL LIMIT 1`,
			pm.CrewID).Scan(&leadAgentID)
		if err != nil {
			return nil, fmt.Errorf("no LEAD agent in crew %s: %w", pm.CrewID, err)
		}

		missionID := generateCUID()
		traceID := "mission-" + generateCUID()

		_, err = tx.ExecContext(ctx, `
			INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description,
			                      status, scope, proposal_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', 'workspace', ?, ?, ?)`,
			missionID, wsID, pm.CrewID, leadAgentID, traceID, pm.Title,
			nilIfEmpty(pm.Description), proposalID, now, now)
		if err != nil {
			return nil, fmt.Errorf("insert mission %q: %w", pm.Title, err)
		}

		chatID := missionID
		_, err = tx.ExecContext(ctx, `
			INSERT INTO chats (id, workspace_id, agent_id, title, mode, status, started_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
			chatID, wsID, leadAgentID, "Mission: "+pm.Title, now, now, now)
		if err != nil {
			return nil, fmt.Errorf("insert chat for mission %q: %w", pm.Title, err)
		}

		for _, t := range pm.Tasks {
			taskID := generateCUID()
			depsJSON := "[]"
			if len(t.DependsOn) > 0 {
				if b, err := json.Marshal(t.DependsOn); err == nil {
					depsJSON = string(b)
				}
			}

			var assignedAgentID *string
			if t.AssignedAgentID != "" {
				assignedAgentID = &t.AssignedAgentID
			}

			taskStatus := "PENDING"
			if len(t.DependsOn) > 0 {
				taskStatus = "BLOCKED"
			}

			_, err = tx.ExecContext(ctx, `
				INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description,
				                           status, task_order, depends_on, max_iterations, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				taskID, missionID, assignedAgentID, t.Title,
				nilIfEmpty(t.Description), taskStatus, t.TaskOrder, depsJSON, t.MaxIterations, now, now)
			if err != nil {
				return nil, fmt.Errorf("insert task %q: %w", t.Title, err)
			}
		}

		missionIDs = append(missionIDs, missionID)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return missionIDs, nil
}

// createMissionsFromProposal on ProposalHandler delegates to the package-level helper.
func (h *ProposalHandler) createMissionsFromProposal(ctx context.Context, wsID, proposalID string, missions []proposalMission) ([]string, error) {
	return createMissionsFromProposal(ctx, h.db, wsID, proposalID, missions)
}

func (h *ProposalHandler) loadProposal(ctx context.Context, wsID, proposalID string) (*proposalResponse, error) {
	var p proposalResponse
	var missionsJSON *string
	err := h.db.QueryRowContext(ctx, `
		SELECT p.id, p.workspace_id, p.proposed_by_id, p.title, p.description,
		       p.plan, p.status, p.missions_json, p.reviewed_by, p.reviewed_at,
		       p.review_notes, p.created_at, p.updated_at,
		       a.name, a.slug
		FROM mission_proposals p
		LEFT JOIN agents a ON a.id = p.proposed_by_id
		WHERE p.id = ? AND p.workspace_id = ?`,
		proposalID, wsID).Scan(
		&p.ID, &p.WorkspaceID, &p.ProposedByID, &p.Title, &p.Description,
		&p.Plan, &p.Status, &missionsJSON, &p.ReviewedBy, &p.ReviewedAt,
		&p.ReviewNotes, &p.CreatedAt, &p.UpdatedAt,
		&p.ProposerName, &p.ProposerSlug,
	)
	if err != nil {
		return nil, err
	}
	if missionsJSON != nil {
		_ = json.Unmarshal([]byte(*missionsJSON), &p.Missions)
	}
	return &p, nil
}

func (h *ProposalHandler) loadProposalMissionIDs(ctx context.Context, proposalID string) []string {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id FROM missions WHERE proposal_id = ? ORDER BY created_at`, proposalID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *ProposalHandler) broadcastProposalEvent(wsID, eventType, proposalID string) {
	if h.hub == nil {
		return
	}
	channel := "workspace:" + wsID
	h.hub.Broadcast(channel, ws.ServerMessage{
		Type:    eventType,
		Channel: channel,
		Payload: map[string]string{"proposal_id": proposalID},
	})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
