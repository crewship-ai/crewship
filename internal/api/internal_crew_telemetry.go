package api

import (
	"math"
	"net/http"
	"sync"

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
	type statsTarget struct {
		crew, slot  int
		containerID string
	}
	var targets []statsTarget
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
		result[i].Containers = make([]containerEntry, len(found))
		for j, container := range found {
			result[i].Containers[j] = containerEntry{Name: container.Name, Kind: container.Kind, Status: container.State}
			// Only a running container has usage to report; asking about a
			// stopped one is a daemon round-trip whose answer is already known.
			if container.State == "running" {
				targets = append(targets, statsTarget{crew: i, slot: j, containerID: container.ID})
			}
		}
	}
	// Usage is read through a bounded pool, not serially: docker's stats call
	// collects two samples a second apart, so a serial pass costs about a
	// second per running container and a fleet of eight or more eats the
	// caller's whole snapshot budget. Same shape as
	// crew_container_inventory.go:Containers — the goroutines are joined by
	// the wg.Wait() below, before anything is written, so they are
	// request-scoped fan-out rather than detached work (the spawn-site entry
	// in unregisteredSpawnSites says exactly this, and
	// TestWorkspaceCrewTelemetry_StatsAreConcurrentAndJoinedBeforeReturn
	// holds it up). The bound keeps a wide fleet from opening a burst of
	// daemon connections at once.
	var wg sync.WaitGroup
	sem := make(chan struct{}, statsFanout)
	for _, target := range targets {
		wg.Add(1)
		go func(t statsTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entry := &result[t.crew].Containers[t.slot]
			entry.CPUPercent, entry.MemoryMB = telemetryUsage(h.container.ContainerStats(r.Context(), t.containerID))
		}(target)
	}
	wg.Wait()
	if err := r.Context().Err(); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"crews": result})
}

// telemetryUsage turns one stats sample into the payload's nullable fields.
// A failed, missing or non-finite sample leaves the field null instead of
// reporting a zero, and one bad reading does not take the other down with
// it — the rest of the fleet snapshot keeps whatever it collected.
func telemetryUsage(metrics *provider.ContainerMetrics, err error) (cpu *float64, memory *int) {
	if err != nil || metrics == nil {
		return nil, nil
	}
	if !math.IsNaN(metrics.CPUPercent) && !math.IsInf(metrics.CPUPercent, 0) && metrics.CPUPercent >= 0 {
		v := math.Round(metrics.CPUPercent*10) / 10
		cpu = &v
	}
	if metrics.MemoryUsed >= 0 {
		m := int(metrics.MemoryUsed / bytesPerMiB)
		memory = &m
	}
	return cpu, memory
}
