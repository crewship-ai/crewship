package api

// Live service inventory: "what sidecars are actually running for this
// crew RIGHT NOW", read straight from Docker via the container
// provider — as opposed to crews.services_json, which is only a
// snapshot of what was last *configured* and can drift (an operator
// stopping a sidecar by hand, or Docker OOM-killing one, still reads
// "configured" there). Backs GET /api/v1/crews/{crewId}/services and
// the `crewship crew services` CLI command.

import (
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/provider"
)

// crewServiceInventoryEntry is one live sidecar container, with its
// image mapped to a datastore type the same way the capabilities
// endpoint's container caps are (inferDatastoreType) — so the
// dashboard/CLI show one consistent vocabulary for "what kind of
// datastore is this" everywhere.
type crewServiceInventoryEntry struct {
	Name   string   `json:"name"`
	Image  string   `json:"image"`
	Type   string   `json:"type"` // "postgres" | "redis" | "mysql" | "mongodb" | "other"
	Status string   `json:"status"`
	Ports  []string `json:"ports"`
}

type crewServiceInventoryResponse struct {
	Services []crewServiceInventoryEntry `json:"services"`
}

// Services GET /api/v1/crews/{crewId}/services
//
// Answers with the crew's LIVE sidecar containers — status, ports, and
// inferred datastore type read straight from the container runtime.
// Deliberately soft on missing capability rather than a hard failure:
// no container provider wired (tests, --no-docker) or a provider that
// doesn't implement the optional ServiceLister capability
// (apple-container today) both answer 200 with an empty list, matching
// the SidecarProvider "unsupported → warn, don't error" convention
// documented on provider.ServiceLister. Only crew-not-found (workspace
// scoping), a missing id, or a genuine daemon-list failure are hard
// failures.
func (h *CrewHandler) Services(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	var crewSlug string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&crewSlug)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "services: resolve crew", err)
		return
	}

	lister, ok := h.container.(provider.ServiceLister)
	if h.container == nil || !ok {
		writeJSON(w, http.StatusOK, crewServiceInventoryResponse{Services: []crewServiceInventoryEntry{}})
		return
	}

	live, err := lister.ListCrewServices(r.Context(), crewSlug)
	if err != nil {
		replyInternalError(w, h.logger, "services: list crew services", err)
		return
	}

	out := make([]crewServiceInventoryEntry, 0, len(live))
	for _, svc := range live {
		ports := svc.Ports
		if ports == nil {
			ports = []string{}
		}
		out = append(out, crewServiceInventoryEntry{
			Name:   svc.Name,
			Image:  svc.Image,
			Type:   inferDatastoreType(svc.Image),
			Status: svc.State,
			Ports:  ports,
		})
	}
	writeJSON(w, http.StatusOK, crewServiceInventoryResponse{Services: out})
}
