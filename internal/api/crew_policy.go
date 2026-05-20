package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/policy"
)

// CrewPolicyHandler exposes PR-B F2 per-crew autonomy + behavior_mode
// state over HTTP. CRUD is intentionally minimal — operators set the
// policy once per crew (with rare flips), so the wire surface is GET
// current state + PUT new state + workspace-scoped list. Patches
// land via the same PUT to keep the audit trail (set_by_user_id +
// set_at + reason) atomic with the value change.
type CrewPolicyHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	resolver *policy.Resolver
	journal  journal.Emitter
}

// NewCrewPolicyHandler builds a handler bound to the shared policy
// resolver — invalidating the resolver after a PUT means the next
// Resolve in any subsystem (memory write gate, skill creation HITL,
// etc.) sees the new state without waiting for the 10s TTL.
func NewCrewPolicyHandler(db *sql.DB, resolver *policy.Resolver, logger *slog.Logger) *CrewPolicyHandler {
	return &CrewPolicyHandler{
		db:       db,
		logger:   logger,
		resolver: resolver,
		journal:  noopEmitter{},
	}
}

// SetJournal wires the journal emitter so policy.changed events
// land in the workspace audit feed alongside other crew mutations.
func (h *CrewPolicyHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// crewPolicyResponse is the wire shape returned by GET and the
// success envelope of PUT.
type crewPolicyResponse struct {
	CrewID        string `json:"crew_id"`
	AutonomyLevel string `json:"autonomy_level"`
	BehaviorMode  string `json:"behavior_mode"`
	SetByUserID   string `json:"set_by_user_id,omitempty"`
	SetAt         string `json:"set_at,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// crewPolicyUpdateBody is the wire shape accepted by PUT. Reason is
// optional for guided/trusted transitions but mandatory for any
// move to autonomy_level=full (enforced at validation, not
// signature).
type crewPolicyUpdateBody struct {
	AutonomyLevel string `json:"autonomy_level"`
	BehaviorMode  string `json:"behavior_mode"`
	Reason        string `json:"reason"`
}

// Get returns the current policy for a crew. Defaults (guided/warn)
// are baked into the DB by v98, so this is always a single SELECT.
func (h *CrewPolicyHandler) Get(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew id required")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !h.crewBelongsToWorkspace(r, crewID, workspaceID) {
		replyError(w, http.StatusNotFound, "crew not found")
		return
	}
	p, err := h.resolver.Resolve(r.Context(), crewID)
	if err != nil {
		h.logger.Error("policy.Get: resolve", "crew_id", crewID, "error", err)
		replyError(w, http.StatusInternalServerError, "resolve policy")
		return
	}
	writeJSON(w, http.StatusOK, marshalPolicy(p))
}

// Put updates the policy for a crew. Validation is the same closed-
// set check the resolver does on load, plus the forbidden
// (full × block) combination, plus the "reason required for full"
// rule documented in PRD §6 F2. On success, invalidates the
// resolver cache so the next Resolve sees the new state immediately.
func (h *CrewPolicyHandler) Put(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew id required")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !h.crewBelongsToWorkspace(r, crewID, workspaceID) {
		replyError(w, http.StatusNotFound, "crew not found")
		return
	}

	var body crewPolicyUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.AutonomyLevel = strings.TrimSpace(body.AutonomyLevel)
	body.BehaviorMode = strings.TrimSpace(body.BehaviorMode)
	body.Reason = strings.TrimSpace(body.Reason)

	newPolicy := policy.Policy{
		CrewID:        crewID,
		AutonomyLevel: policy.AutonomyLevel(body.AutonomyLevel),
		BehaviorMode:  policy.BehaviorMode(body.BehaviorMode),
	}
	if err := newPolicy.Validate(); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	// PRD §6 F2 documents: --reason required for full. Strict
	// transitions also feel important enough to require a reason
	// (operator likely flipping back to prod-safe defaults — record
	// why) but not enforced here so the API stays minimal.
	if newPolicy.AutonomyLevel == policy.AutonomyFull && body.Reason == "" {
		replyError(w, http.StatusBadRequest, "reason is required when setting autonomy_level=full")
		return
	}

	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE crews
		    SET autonomy_level = ?,
		        behavior_mode = ?,
		        autonomy_set_by_user_id = ?,
		        autonomy_set_at = ?,
		        autonomy_reason = ?
		  WHERE id = ?`,
		body.AutonomyLevel, body.BehaviorMode,
		nullIfEmpty(userID), now, nullIfEmpty(body.Reason),
		crewID,
	); err != nil {
		h.logger.Error("policy.Put: update", "crew_id", crewID, "error", err)
		replyError(w, http.StatusInternalServerError, "update policy")
		return
	}

	// Drop the cache so the next Resolve fetches fresh state —
	// without this, subsystems consulting the resolver would see
	// stale values for up to 10 seconds after a PATCH.
	h.resolver.Invalidate(crewID)

	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        "policy.changed",
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     userID,
		Summary:     "crew policy updated: " + body.AutonomyLevel + "/" + body.BehaviorMode,
		Payload: map[string]any{
			"crew_id":        crewID,
			"autonomy_level": body.AutonomyLevel,
			"behavior_mode":  body.BehaviorMode,
			"reason":         body.Reason,
		},
		Refs: map[string]any{"crew_id": crewID},
	}); jerr != nil {
		h.logger.Warn("policy.Put: journal emit failed", "error", jerr, "crew_id", crewID)
	}

	resp := crewPolicyResponse{
		CrewID:        crewID,
		AutonomyLevel: body.AutonomyLevel,
		BehaviorMode:  body.BehaviorMode,
		SetByUserID:   userID,
		SetAt:         now,
		Reason:        body.Reason,
	}
	writeJSON(w, http.StatusOK, resp)
}

// List returns the policy for every crew in the workspace. Used by
// `crewship policy list` to render the overview table.
func (h *CrewPolicyHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, autonomy_level, behavior_mode,
		       autonomy_set_by_user_id, autonomy_set_at, autonomy_reason
		FROM crews
		WHERE workspace_id = ? AND deleted_at IS NULL
		ORDER BY name COLLATE NOCASE`,
		workspaceID,
	)
	if err != nil {
		h.logger.Error("policy.List: query", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "list policies")
		return
	}
	defer rows.Close()

	out := []crewPolicyResponse{}
	for rows.Next() {
		var (
			id, lvl, mode        string
			setBy, setAt, reason sql.NullString
		)
		if err := rows.Scan(&id, &lvl, &mode, &setBy, &setAt, &reason); err != nil {
			h.logger.Warn("policy.List: scan", "error", err)
			continue
		}
		out = append(out, crewPolicyResponse{
			CrewID:        id,
			AutonomyLevel: lvl,
			BehaviorMode:  mode,
			SetByUserID:   setBy.String,
			SetAt:         setAt.String,
			Reason:        reason.String,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CrewPolicyHandler) crewBelongsToWorkspace(r *http.Request, crewID, workspaceID string) bool {
	var got string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		h.logger.Error("policy: crew lookup", "crew_id", crewID, "error", err)
		return false
	}
	return true
}

func marshalPolicy(p policy.Policy) crewPolicyResponse {
	resp := crewPolicyResponse{
		CrewID:        p.CrewID,
		AutonomyLevel: string(p.AutonomyLevel),
		BehaviorMode:  string(p.BehaviorMode),
		SetByUserID:   p.SetByUserID,
		Reason:        p.Reason,
	}
	if !p.SetAt.IsZero() {
		resp.SetAt = p.SetAt.Format(time.RFC3339)
	}
	return resp
}
