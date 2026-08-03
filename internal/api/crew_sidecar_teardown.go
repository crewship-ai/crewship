package api

// Crew delete → sidecar teardown (#1709).
//
// internal/provider/docker/sidecar.go has implemented the full lifecycle —
// ensure, stop, remove, remove-volumes — since sidecars landed. Only two of the
// four had a production caller: EnsureCrewServices (chat only, which is #1708)
// and StopCrewServices (the internal container-stop route). RemoveCrewServices
// and RemoveCrewServiceVolumes had none at all, so `crewship crew delete`
// removed the crew's rows and left its postgres running, its redis running, and
// its named data volumes on disk — permanently, since a soft-deleted crew is
// never started again and nothing else sweeps them. On a long-lived host that is
// a live, still-authenticated database per deleted crew, plus its disk.
//
// The crew's own RUNTIME container is deliberately NOT removed here: it has an
// idle TTL and a reaper, so it stops on its own. Sidecars have neither, which is
// exactly why they need this.

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// crewSidecarTeardownTimeout bounds the daemon calls a delete makes. A wedged
// container runtime must not hold a crew delete open indefinitely; the DB rows
// are already gone by the time this runs and the response is honest either way.
const crewSidecarTeardownTimeout = 30 * time.Second

// removeCrewSidecars force-removes the crew's sidecar containers and then their
// named volumes. Best-effort and never fatal to the delete: the crew IS deleted:
// reporting a 500 because docker was unreachable would leave the caller with a
// crew that is gone from every read surface and an error saying it is not.
//
// Both capabilities are optional (the apple-container provider implements
// neither) — a provider that cannot do it is skipped, not an error.
func (h *CrewHandler) removeCrewSidecars(ctx context.Context, crewID, crewSlug string) {
	if h.container == nil || crewSlug == "" {
		return
	}

	sp, canRemove := h.container.(provider.SidecarProvider)
	vr, canRemoveVolumes := h.container.(provider.ServiceVolumeRemover)
	if !canRemove && !canRemoveVolumes {
		return
	}

	// WithoutCancel: the HTTP request may be finished (or the client gone) by
	// the time docker answers, and a half-torn-down crew — containers removed,
	// volumes left — is the worst of the three possible outcomes.
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), crewSidecarTeardownTimeout)
	defer cancel()

	if canRemove {
		if err := sp.RemoveCrewServices(tctx, crewSlug); err != nil {
			h.logger.Warn("crew delete: remove sidecar containers",
				"crew_id", crewID, "crew_slug", crewSlug, "error", err)
			// Volumes are still attempted: docker refuses volumes that are
			// referenced, so at worst this second call is a no-op that logs.
		}
	}
	if canRemoveVolumes {
		if err := vr.RemoveCrewServiceVolumes(tctx, crewSlug); err != nil {
			h.logger.Warn("crew delete: remove sidecar volumes",
				"crew_id", crewID, "crew_slug", crewSlug, "error", err)
		}
	}
}
