package api

// One-shot authoring dump (#862, PRD #848 Pillar 4). An LLM (Claude Code via
// CLI, or an in-container agent via the routine MCP server) shouldn't have to
// piece the authoring palette together from ~8 commands. Capabilities returns
// ONE bundle: the routine DSL schema + the crew's resolved devcontainer
// capabilities + connected integrations WITH their enabled tool names + agent
// slugs + the runtimes an author can actually use — enough to write a
// `routine validate`-clean DSL on the first try.

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/schemas"
)

type integrationCap struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	// Tools are the explicitly-enabled tool bindings (mcp_tool_bindings,
	// enabled != 0). A connected integration with no bindings yet renders an
	// empty list — its tools work by provider default; the dashboard's
	// tools/refresh materialises bindings once a user opens the tool picker.
	Tools []string `json:"tools"`
}

type agentCap struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type codeRuntimeCap struct {
	// Wired runtimes have a CodeRunner in this build — safe to use.
	Wired []string `json:"wired"`
	// ReservedUnwired are legal names with no runner yet — an author naming
	// one gets a "no wired runner" error, so they're listed as "not yet".
	ReservedUnwired []string `json:"reserved_unwired"`
}

type runtimeCap struct {
	Code codeRuntimeCap `json:"code"`
	// ScriptInterpreters is the extension→interpreter inference table for
	// `type: script` steps (script.interpreter overrides it).
	ScriptInterpreters map[string]string `json:"script_interpreters"`
}

type crewCapabilitiesResponse struct {
	CrewID       string           `json:"crew_id"`
	CrewSlug     string           `json:"crew_slug"`
	Container    *CrewResources   `json:"container"`
	Integrations []integrationCap `json:"integrations"`
	Agents       []agentCap       `json:"agents"`
	Runtimes     runtimeCap       `json:"runtimes"`
	// Schema is the routine DSL JSON schema, embedded so the whole authoring
	// contract travels in one response (nested object, not a string).
	Schema json.RawMessage `json:"schema"`
}

// Capabilities GET /api/v1/crews/{crewId}/capabilities
func (h *CrewHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Resolve + isolation-check the crew (and grab its slug) in one query.
	var crewSlug string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&crewSlug)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	if err != nil {
		h.logger.Error("capabilities: resolve crew", "error", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Container resources (datastores + installed CLI tools).
	container, err := ResolveCrewResources(r.Context(), h.db, crewID)
	if err != nil {
		h.logger.Error("capabilities: resolve resources", "error", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	integrations, err := h.crewIntegrationCaps(r, crewID)
	if err != nil {
		h.logger.Error("capabilities: integrations", "error", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	agents, err := h.crewAgentCaps(r, crewID)
	if err != nil {
		h.logger.Error("capabilities: agents", "error", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, crewCapabilitiesResponse{
		CrewID:       crewID,
		CrewSlug:     crewSlug,
		Container:    container,
		Integrations: integrations,
		Agents:       agents,
		Runtimes: runtimeCap{
			Code: codeRuntimeCap{
				Wired:           pipeline.WiredCodeRuntimes(),
				ReservedUnwired: pipeline.ReservedCodeRuntimes(),
			},
			ScriptInterpreters: pipeline.ScriptInterpreterExtensions(),
		},
		Schema: json.RawMessage(schemas.RoutineV1),
	})
}

// crewIntegrationCaps lists the crew's enabled integrations with each one's
// explicitly-enabled tool names.
func (h *CrewHandler) crewIntegrationCaps(r *http.Request, crewID string) ([]integrationCap, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, display_name
		FROM crew_mcp_servers
		WHERE crew_id = ? AND deleted_at IS NULL AND enabled = 1
		ORDER BY name ASC`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []integrationCap{}
	for rows.Next() {
		var c integrationCap
		var display sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &display); err != nil {
			return nil, err
		}
		c.DisplayName = display.String
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Per-integration enabled tool names. N+1 is fine — a crew has a handful
	// of integrations, and this is an author-time dump, not a hot path.
	for i := range out {
		tools, err := h.enabledToolNames(r, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Tools = tools
	}
	return out, nil
}

// enabledToolNames returns the enabled tool bindings for a crew-scoped MCP
// server (mirrors ListCrewIntegrationTools' query, filtered to enabled).
func (h *CrewHandler) enabledToolNames(r *http.Request, serverID string) ([]string, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT tool_name
		FROM mcp_tool_bindings
		WHERE mcp_server_id = ? AND mcp_server_scope = 'crew' AND enabled != 0
		ORDER BY tool_name ASC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// crewAgentCaps lists the crew's agent slugs (what a routine's agent_run steps
// reference).
func (h *CrewHandler) crewAgentCaps(r *http.Request, crewID string) ([]agentCap, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT slug, name
		FROM agents
		WHERE crew_id = ? AND deleted_at IS NULL
		ORDER BY slug ASC`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []agentCap{}
	for rows.Next() {
		var a agentCap
		if err := rows.Scan(&a.Slug, &a.Name); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
