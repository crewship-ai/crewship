package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
	"github.com/crewship-ai/crewship/internal/servicelifecycle"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type serviceStateResponse struct {
	Name          string `json:"name"`
	ID            string `json:"id,omitempty"`
	DesiredState  string `json:"desired_state"`
	ObservedState string `json:"observed_state"`
	Version       int64  `json:"version"`
	LastError     string `json:"last_error,omitempty"`
}

func loadServiceDeclarations(ctx context.Context, db *sql.DB, crew, ws string) ([]serviceWire, error) {
	var body string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(services_json,'') FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, crew, ws).Scan(&body); err != nil {
		return nil, err
	}
	plain, err := serviceconfig.Open(body)
	if err != nil {
		return nil, err
	}
	if plain == "" {
		return nil, nil
	}
	var specs []serviceWire
	err = json.Unmarshal([]byte(plain), &specs)
	return specs, err
}

func (h *CrewHandler) ServiceStates(w http.ResponseWriter, r *http.Request) {
	crew, ws := r.PathValue("crewId"), WorkspaceIDFromContext(r.Context())
	specs, err := loadServiceDeclarations(r.Context(), h.db, crew, ws)
	if err == sql.ErrNoRows {
		replyError(w, 404, "Crew not found")
		return
	}
	if err != nil {
		internalError(w, r, h.logger, "load service declarations", err)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,service_name,desired_state,observed_state,version,last_error FROM service_runtime_intents WHERE crew_id=? ORDER BY service_name`, crew)
	if err != nil {
		internalError(w, r, h.logger, "load service states", err)
		return
	}
	defer rows.Close()
	states := map[string]serviceStateResponse{}
	for rows.Next() {
		var state serviceStateResponse
		if err := rows.Scan(&state.ID, &state.Name, &state.DesiredState, &state.ObservedState, &state.Version, &state.LastError); err != nil {
			internalError(w, r, h.logger, "read service state", err)
			return
		}
		states[state.Name] = state
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "read service states", err)
		return
	}
	out := []serviceStateResponse{}
	for _, spec := range specs {
		state, ok := states[spec.Name]
		if !ok {
			state = serviceStateResponse{Name: spec.Name, DesiredState: "on_demand", ObservedState: "unmanaged"}
		}
		out = append(out, state)
		delete(states, spec.Name)
	}
	for _, state := range states {
		out = append(out, state)
	}
	_, supported := h.container.(servicelifecycle.Runtime)
	writeJSON(w, 200, map[string]any{"services": out, "supported": supported})
}

func (h *CrewHandler) SetServiceState(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	var body struct {
		DesiredState    string `json:"desired_state"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if err := readJSON(r, &body); err != nil || body.ExpectedVersion == nil || *body.ExpectedVersion < 0 || (body.DesiredState != "running" && body.DesiredState != "stopped") {
		replyError(w, 400, "desired_state and a nonnegative expected_version are required")
		return
	}
	crew, ws, name := r.PathValue("crewId"), WorkspaceIDFromContext(r.Context()), r.PathValue("serviceName")
	specs, err := loadServiceDeclarations(r.Context(), h.db, crew, ws)
	if err == sql.ErrNoRows {
		replyError(w, 404, "Crew not found")
		return
	}
	if err != nil {
		internalError(w, r, h.logger, "load services", err)
		return
	}
	if _, ok := h.container.(servicelifecycle.Runtime); !ok {
		replyError(w, 409, "Runtime does not support managed services")
		return
	}
	declared := false
	for _, spec := range specs {
		if spec.Name == name {
			declared = true
			break
		}
	}
	if !declared && (body.DesiredState != "stopped" || *body.ExpectedVersion == 0) {
		replyError(w, 404, "Service not declared")
		return
	}
	now := tsformat.Format(time.Now())
	var result sql.Result
	if *body.ExpectedVersion == 0 {
		result, err = h.db.ExecContext(r.Context(), `INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,updated_at) VALUES (?,?,?,?,?) ON CONFLICT(crew_id,service_name) DO NOTHING`, generateCUID(), crew, name, body.DesiredState, now)
	} else {
		result, err = h.db.ExecContext(r.Context(), `UPDATE service_runtime_intents SET desired_state=?,version=version+1,observed_state='pending',last_error='',next_attempt_at='',updated_at=? WHERE crew_id=? AND service_name=? AND version=?`, body.DesiredState, now, crew, name, *body.ExpectedVersion)
	}
	if err != nil {
		internalError(w, r, h.logger, "update service intent", err)
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "verify service intent", err)
		return
	}
	if n != 1 {
		replyError(w, 409, "Service changed; reload before retrying")
		return
	}
	auditFromRequest(r, h.db, "crew_service.intent", "CREW", crew, map[string]any{"service": name, "desired_state": body.DesiredState, "version": *body.ExpectedVersion + 1})
	writeJSON(w, http.StatusAccepted, map[string]any{"desired_state": body.DesiredState, "version": *body.ExpectedVersion + 1, "observed_state": "pending"})
}
