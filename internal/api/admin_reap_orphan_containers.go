package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// OrphanContainerHandler detects — and, with ?apply=true, reaps — crew
// containers left ORPHANED by a server restart that rotated the internal-token
// master (#1385). Such a container survives the restart holding a crew-bound
// token minted under the OLD master; the new process rejects it forever
// ("invalid crew-bound token"), so its credential sync is silently broken and
// it spams the log every reap interval. #1387's fix (a) makes the master STABLE
// going forward, but any container that outlived the deploy that first rotates
// the master is orphaned once and never self-heals — this handler is the
// operator lever the issue calls for (`crewship admin reap-orphan-containers`).
//
// Detection is positive and fail-SAFE: for each of the workspace's crews it
// probes the running container's sidecar /health for the token fingerprint it
// advertises (#1385 token_fp) and compares it against the fingerprint of the
// crew token the server would mint TODAY. Only a definite non-empty mismatch
// is an orphan — an unreachable/pre-#1385/crew-less sidecar (empty fingerprint)
// or an unconfigured master is NEVER classified as orphaned, so a reap can only
// ever remove a container it positively proved is holding a stale token. A
// healthy container is left untouched. Reaping stops+removes the container; the
// next dispatch to that crew recreates it fresh and it re-mints a valid token.
//
// Workspace-scoped (like the crew-runtime pruner, unlike the instance-wide
// legacy pruner): the derivation needs each crew's workspace, and an admin acts
// within their own workspace. Admin-only. A nil provider (non-docker) or a
// provider that can't enumerate crew containers 503s rather than lie.
type OrphanContainerHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// ctr is the live container provider (nil on a non-docker runtime → 503).
	// lookup is its optional crew-container enumeration capability; without it
	// the endpoint can't map a crew to its running container, so it 503s too.
	ctr    provider.ContainerProvider
	lookup provider.CrewContainerLookup
	// master is the internal-token master. Empty means internal auth is
	// unconfigured — the server can't derive an expected fingerprint, so every
	// container is (fail-safe) reported healthy and nothing is ever reaped.
	master string
}

func NewOrphanContainerHandler(db *sql.DB, logger *slog.Logger, ctr provider.ContainerProvider, master string) *OrphanContainerHandler {
	h := &OrphanContainerHandler{db: db, logger: logger, ctr: ctr, master: master}
	if lk, ok := ctr.(provider.CrewContainerLookup); ok {
		h.lookup = lk
	}
	return h
}

type orphanContainer struct {
	CrewID      string `json:"crew_id"`
	Slug        string `json:"slug"`
	ContainerID string `json:"container_id"`
	// Reaped is true only when apply=true AND the stop+remove succeeded.
	Reaped bool `json:"reaped"`
}

type reapOrphanResponse struct {
	// Orphans is every crew container proven to hold a stale (rotated-master)
	// token. In dry-run (the default) Reaped is false on each; with apply=true
	// it reflects whether the reap succeeded.
	Orphans []orphanContainer `json:"orphans"`
	Count   int               `json:"count"`
	// Applied echoes whether this call actually reaped (apply=true) or only
	// reported (dry-run), so the CLI can phrase its output correctly.
	Applied bool `json:"applied"`
}

// crewRefsWithWorkspace enumerates the workspace's live crews. workspaceID is
// carried back on each so the per-crew token derivation binds the correct
// (workspace, crew) tuple — even though the query is already workspace-scoped,
// keeping it explicit avoids a silent mis-derivation if the scope ever widens.
type crewRefWS struct {
	provider.CrewRef
	WorkspaceID string
}

func (h *OrphanContainerHandler) workspaceCrews(ctx context.Context, workspaceID string) ([]crewRefWS, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, slug, workspace_id FROM crews WHERE workspace_id = ? AND deleted_at IS NULL`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var crews []crewRefWS
	for rows.Next() {
		var c crewRefWS
		if err := rows.Scan(&c.ID, &c.Slug, &c.WorkspaceID); err != nil {
			return nil, err
		}
		crews = append(crews, c)
	}
	return crews, rows.Err()
}

// Reap detects orphaned crew containers and, when the request carries
// ?apply=true, stops+removes them so the next dispatch re-mints a valid token.
// Admin-only. Dry-run by default. 503 when docker (or crew-container
// enumeration) isn't available. On a mid-reap failure it still returns 200 with
// the per-container Reaped flags so the operator can see exactly what happened
// (a stop/remove that failed leaves Reaped=false on that entry and is logged).
func (h *OrphanContainerHandler) Reap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !canRole(RoleFromContext(ctx), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	workspaceID := WorkspaceIDFromContext(ctx)
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if h.ctr == nil || h.lookup == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "reap-orphan-containers unavailable: docker not configured",
		})
		return
	}

	apply := r.URL.Query().Get("apply") == "true"

	crews, err := h.workspaceCrews(ctx, workspaceID)
	if err != nil {
		h.logger.Error("reap orphan: list crews", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	orphans := make([]orphanContainer, 0)
	for _, c := range crews {
		containerID, running, err := h.lookup.FindCrewContainer(ctx, c.ID, c.Slug)
		if err != nil {
			// A transport error talking to the runtime for ONE crew must not
			// abort the whole sweep — log and move on (the rest may still be
			// classifiable).
			h.logger.Warn("reap orphan: find crew container", "crew_id", c.ID, "error", err)
			continue
		}
		if containerID == "" || !running {
			continue
		}

		reportedFP := orchestrator.SidecarTokenFP(ctx, h.ctr, containerID)
		expectedFP := internaltoken.Fingerprint(internaltoken.DeriveCrewToken(h.master, c.WorkspaceID, c.ID))
		if !orchestrator.SidecarTokenOrphaned(reportedFP, expectedFP) {
			continue
		}

		entry := orphanContainer{CrewID: c.ID, Slug: c.Slug, ContainerID: containerID}
		if apply {
			// Stop then remove — mirrors normal crew teardown. Best-effort per
			// container: a failure is logged and Reaped stays false so the
			// operator sees it wasn't cleared. Recreation happens lazily on the
			// crew's next dispatch (EnsureCrewRuntime).
			if serr := h.ctr.StopCrewRuntime(ctx, containerID); serr != nil {
				h.logger.Warn("reap orphan: stop container", "crew_id", c.ID, "container_id", containerID, "error", serr)
			}
			if rerr := h.ctr.RemoveCrewRuntime(ctx, containerID); rerr != nil {
				h.logger.Warn("reap orphan: remove container", "crew_id", c.ID, "container_id", containerID, "error", rerr)
			} else {
				entry.Reaped = true
				h.logger.Info("reap orphan: reaped stale-token container",
					"crew_id", c.ID, "slug", c.Slug, "container_id", containerID)
			}
		}
		orphans = append(orphans, entry)
	}

	if apply {
		h.logger.Info("reap orphan: sweep complete", "workspace", workspaceID, "orphans", len(orphans))
	}
	writeJSON(w, http.StatusOK, reapOrphanResponse{Orphans: orphans, Count: len(orphans), Applied: apply})
}
