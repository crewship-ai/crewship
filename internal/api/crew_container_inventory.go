package api

// Live crew container inventory: "which containers does this crew have right
// now, and what are they doing" — the crew's agent runtime plus its sidecars,
// read straight from the container runtime. Backs
// GET /api/v1/crews/{crewId}/containers, the `crewship crew containers` CLI
// command, and the crew bottom panel's Docker tab.
//
// The Docker tab existed for months with nothing behind it: it read
// `containers` off GET /api/v1/system/runtime, which is the HOST runtime
// inventory (#1690) and has never carried a containers field, so the tab
// rendered "No containers running." on every crew forever (#1697). These are
// per-crew facts and this is where they live; /system/runtime stays the
// answer to "what runtimes does this host have", which is a different
// question.
//
// Sibling of crew_service_inventory.go — same soft-failure convention (no
// provider wired, or a provider without the capability, answers 200 with an
// empty list), one wider question.

import (
	"database/sql"
	"math"
	"net/http"

	"github.com/crewship-ai/crewship/internal/provider"
)

// bytesPerMiB is the divisor used to report container memory. Docker reports
// bytes; every Crewship surface that shows container memory (crew limits,
// resource drift) speaks MiB, so the conversion happens once, here.
const bytesPerMiB = 1024 * 1024

// crewContainerEntry is one of the crew's containers as the API reports it.
//
// Every optional number is a POINTER on purpose. The tab renders an absent
// figure as "—", and an absent figure is not a zero one: a stopped container
// has no CPU reading, and a runtime that cannot report stats (apple-container)
// has none either. Serialising those as 0 would draw a container sitting idle
// at 0.0% when nothing measured it — the same class of quiet lie as the empty
// list this endpoint replaces.
type crewContainerEntry struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// Kind is "crew" (the agent runtime) or "sidecar" (a declared service),
	// so a client can tell the crew's own container from its dependencies
	// without parsing the name.
	Kind string `json:"kind"`
	// Status is the live state vocabulary — "running" | "stopped" |
	// "creating" | "error" — the same one container-status and the service
	// inventory report, never docker's human "Up 2 hours" string.
	Status     string   `json:"status"`
	CPUPercent *float64 `json:"cpu_percent"`
	MemoryMB   *int     `json:"memory_mb"`
	// AgentCount is the number of agents that run inside this container, so
	// it is stamped on the crew runtime row and left null for a sidecar —
	// no agent runs in a postgres container, and reporting the crew's count
	// there would read as one.
	AgentCount *int `json:"agent_count"`
}

type crewContainersResponse struct {
	Containers []crewContainerEntry `json:"containers"`
}

// Containers GET /api/v1/crews/{crewId}/containers
//
// Answers with the crew's LIVE containers — the agent runtime and every
// sidecar — with state, CPU and memory read from the container runtime, and
// the crew's agent count on the runtime row.
//
// Deliberately soft on missing capability rather than a hard failure: no
// container provider wired (tests, --no-docker) or a provider that doesn't
// implement CrewContainerLister (apple-container today) both answer 200 with
// an empty list, matching crew_service_inventory.go. Only crew-not-found
// (workspace scoping), a missing id, or a genuine daemon-list failure are
// hard failures.
//
// Per-container stats are best-effort within a successful listing: a stats
// call that fails leaves cpu_percent/memory_mb null on that row rather than
// failing the request. Knowing a container exists and is running is the
// answer the caller came for; its CPU reading is not worth losing that over.
func (h *CrewHandler) Containers(w http.ResponseWriter, r *http.Request) {
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
		replyInternalError(w, h.logger, "containers: resolve crew", err)
		return
	}

	lister, ok := h.container.(provider.CrewContainerLister)
	if h.container == nil || !ok {
		writeJSON(w, http.StatusOK, crewContainersResponse{Containers: []crewContainerEntry{}})
		return
	}

	// crewID comes from the workspace-scoped SELECT above, so the listing is
	// fenced to this workspace's crew even when another workspace has a crew
	// with the same slug (#1732).
	live, err := lister.ListCrewContainers(r.Context(), crewID, crewSlug)
	if err != nil {
		replyInternalError(w, h.logger, "containers: list crew containers", err)
		return
	}

	// One count for the whole response, and only if there is a runtime row to
	// put it on.
	var agentCount *int
	for _, c := range live {
		if c.Kind == provider.CrewContainerKindCrew {
			agentCount = h.crewAgentCount(r, crewID)
			break
		}
	}

	out := make([]crewContainerEntry, 0, len(live))
	for _, c := range live {
		entry := crewContainerEntry{
			Name:   c.Name,
			Image:  c.Image,
			Kind:   c.Kind,
			Status: c.State,
		}
		if c.Kind == provider.CrewContainerKindCrew {
			entry.AgentCount = agentCount
		}
		// Only a running container has usage to report, and asking about a
		// stopped one is a daemon round-trip whose answer is already known.
		if c.State == "running" {
			cpu, mem := h.containerUsage(r, c.ID)
			entry.CPUPercent, entry.MemoryMB = cpu, mem
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, crewContainersResponse{Containers: out})
}

// crewAgentCount returns how many agents the crew has, or nil when the count
// could not be read — an unknown count is reported as unknown rather than as
// zero, for the same reason the metric pointers exist.
func (h *CrewHandler) crewAgentCount(r *http.Request, crewID string) *int {
	var n int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM agents WHERE crew_id = ? AND deleted_at IS NULL`,
		crewID).Scan(&n); err != nil {
		h.logger.Warn("containers: agent count", "crew_id", crewID, "error", err)
		return nil
	}
	return &n
}

// containerUsage reads one container's CPU and memory, or (nil, nil) when the
// runtime cannot say. Providers that do not implement stats (apple-container)
// return an error here, and a container that exited between the listing and
// this call does too; neither is a reason to fail the inventory.
func (h *CrewHandler) containerUsage(r *http.Request, containerID string) (*float64, *int) {
	metrics, err := h.container.ContainerStats(r.Context(), containerID)
	if err != nil || metrics == nil {
		if err != nil {
			h.logger.Debug("containers: stats unavailable", "container_id", provider.ShortID(containerID), "error", err)
		}
		return nil, nil
	}
	// One decimal is what the UI renders; carrying float noise ("3.4666666%")
	// into the payload would make every client round it the same way.
	cpu := math.Round(metrics.CPUPercent*10) / 10
	mem := int(metrics.MemoryUsed / bytesPerMiB)
	return &cpu, &mem
}
