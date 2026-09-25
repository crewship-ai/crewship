package api

import (
	"math"
	"net/http"

	"github.com/crewship-ai/crewship/internal/provider"
)

// WorkspaceCrewTelemetry is a read-only, workspace-scoped fleet sample for
// in-crew routines. It reports absent metrics as null instead of zero.
func (h *InternalHandler) WorkspaceCrewTelemetry(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,name,slug FROM crews WHERE workspace_id=? AND deleted_at IS NULL AND COALESCE(kind,'') <> 'setup' ORDER BY name`, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "fleet telemetry: list crews", err)
		return
	}
	type crewInfo struct{ ID, Name, Slug string }
	crews := []crewInfo{}
	for rows.Next() {
		var crew crewInfo
		if err := rows.Scan(&crew.ID, &crew.Name, &crew.Slug); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "fleet telemetry: scan crew", err)
			return
		}
		crews = append(crews, crew)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		replyInternalError(w, h.logger, "fleet telemetry: crews", err)
		return
	}
	rows.Close()
	type containerEntry struct {
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Status     string   `json:"status"`
		CPUPercent *float64 `json:"cpu_percent"`
		MemoryMB   *int     `json:"memory_mb"`
	}
	type crewEntry struct {
		Name       string           `json:"name"`
		Slug       string           `json:"slug"`
		Containers []containerEntry `json:"containers"`
		Available  bool             `json:"available"`
	}
	result := make([]crewEntry, len(crews))
	lister, supported := h.container.(provider.CrewContainerLister)
	for i, crew := range crews {
		result[i] = crewEntry{Name: crew.Name, Slug: crew.Slug, Containers: []containerEntry{}}
		if !supported {
			continue
		}
		found, err := lister.ListCrewContainers(r.Context(), crew.ID, crew.Slug)
		if err != nil {
			continue
		}
		result[i].Available = true
		for _, container := range found {
			entry := containerEntry{Name: container.Name, Kind: container.Kind, Status: container.State}
			if container.State == "running" {
				metrics, err := h.container.ContainerStats(r.Context(), container.ID)
				if err == nil && metrics != nil {
					if !math.IsNaN(metrics.CPUPercent) && !math.IsInf(metrics.CPUPercent, 0) && metrics.CPUPercent >= 0 {
						cpu := math.Round(metrics.CPUPercent*10) / 10
						entry.CPUPercent = &cpu
					}
					if metrics.MemoryUsed >= 0 {
						memory := int(metrics.MemoryUsed / bytesPerMiB)
						entry.MemoryMB = &memory
					}
				}
			}
			result[i].Containers = append(result[i].Containers, entry)
		}
	}
	if err := r.Context().Err(); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"crews": result})
}
