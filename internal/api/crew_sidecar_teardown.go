package api

// Crew delete → sidecar teardown (#1709), and the tenancy hazard it used to
// run into.
//
// internal/provider/docker/sidecar.go has implemented the full lifecycle —
// ensure, stop, remove, remove-volumes — since sidecars landed. Only two of the
// four had a production caller: EnsureCrewServices (chat only, which is #1708)
// and StopCrewServices (the internal container-stop route). RemoveCrewServices
// and RemoveCrewServiceVolumes had none at all, so `crewship crew delete`
// removed the crew's rows and left its postgres running and its named data
// volumes on disk — permanently, since a soft-deleted crew is never started
// again and nothing else sweeps them.
//
// # Why this used to refuse to run
//
// Both teardown primitives were keyed on the crew SLUG alone: containers were
// matched by the label `crewship.crew=<slug>` and volumes by the name prefix
// `<prefix>-svc-<slug>-vol-`. Neither carried the crew id — and `crews` is
// UNIQUE(workspace_id, slug), so a slug identifies a crew only WITHIN a
// workspace. Two workspaces with a crew slugged `data-crew` shared one
// namespace on the daemon, so deleting one force-removed the other's live
// Postgres and deleted its data directory. This function answered that by
// refusing to destroy what it could not prove it owned, and saying so.
//
// #1732 fixed the naming instead, the way audit C1 fixed it for crew
// containers: the sidecar container name, the volume name and the
// `crewship.crew-id` label all carry the globally-unique crew id, and the
// provider selects by that label — exact equality, never a name prefix. A
// teardown can no longer reach another crew's sidecars, in any workspace, so
// the refusal is gone: it now only withheld cleanup from crews that were never
// at risk.
//
// One residual case is deliberately NOT swept: sidecars created before #1732
// carry no crew id anywhere, so nothing on the daemon can prove which crew owns
// one. They are re-keyed automatically the first time the crew's services
// start (migrateLegacySidecarResources). A crew deleted before it is ever
// started again leaves them behind; leaking a container and a volume is
// recoverable by hand, deleting a different tenant's database is not.
//
// The crew's own RUNTIME container is deliberately NOT removed here: it has an
// idle TTL and a reaper, so it stops on its own. Sidecars have neither, which is
// exactly why they need this.

import (
	"context"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// crewSidecarTeardownTimeout bounds the daemon calls a delete makes. A wedged
// container runtime must not hold a crew delete open indefinitely; the DB rows
// are already gone by the time this runs and the response is honest either way.
const crewSidecarTeardownTimeout = 30 * time.Second

// Teardown outcomes, as reported to the caller. Every crew delete carries one:
// the operator has just answered a prompt saying the sidecar volumes will be
// deleted, so "nothing said" is not an acceptable answer to what happened.
const (
	teardownRemoved       = "removed"        // containers + volumes gone
	teardownNotConfigured = "not_configured" // no container runtime wired
	teardownUnsupported   = "unsupported"    // provider has no sidecar capability
	teardownSkipped       = "skipped"        // refused — see Reason
	teardownFailed        = "failed"         // attempted, daemon said no — see Reason
)

// sidecarTeardownResult is what the delete response says about the crew's
// sidecars. Reason is written for an operator with nothing else in front of
// them: what was left behind, and why.
type sidecarTeardownResult struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// removeCrewSidecars force-removes the crew's sidecar containers and then their
// named volumes, unless doing so would reach another crew's.
//
// Never fatal to the delete: the crew IS deleted by the time this runs, so
// reporting a 500 because docker was unreachable would leave the caller with a
// crew that is gone from every read surface and an error saying it is not. The
// outcome travels in the response body instead.
func (h *CrewHandler) removeCrewSidecars(ctx context.Context, crewID, crewSlug string) sidecarTeardownResult {
	if h.container == nil {
		return sidecarTeardownResult{Status: teardownNotConfigured}
	}
	if crewID == "" {
		// Unreachable by construction — Delete resolves the crew by id. Kept as
		// a guard because the provider selects sidecars by an EXACT crew-id
		// label match, and an id-less call would match nothing, silently.
		return sidecarTeardownResult{
			Status: teardownSkipped,
			Reason: "the crew's id could not be resolved, and the sidecar teardown is keyed on it",
		}
	}

	sp, canRemove := h.container.(provider.SidecarProvider)
	vr, canRemoveVolumes := h.container.(provider.ServiceVolumeRemover)
	if !canRemove && !canRemoveVolumes {
		return sidecarTeardownResult{Status: teardownUnsupported}
	}

	// WithoutCancel: the HTTP request may be finished (or the client gone) by
	// the time docker answers, and a half-torn-down crew — containers removed,
	// volumes left — is the worst of the three possible outcomes.
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), crewSidecarTeardownTimeout)
	defer cancel()

	var firstErr error
	if canRemove {
		if err := sp.RemoveCrewServices(tctx, crewID, crewSlug); err != nil {
			h.logger.Warn("crew delete: remove sidecar containers",
				"crew_id", crewID, "crew_slug", crewSlug, "error", err)
			firstErr = err
			// Volumes are still attempted: docker refuses volumes that are
			// referenced, so at worst this second call is a no-op that logs.
		}
	}
	if canRemoveVolumes {
		if err := vr.RemoveCrewServiceVolumes(tctx, crewID, crewSlug); err != nil {
			h.logger.Warn("crew delete: remove sidecar volumes",
				"crew_id", crewID, "crew_slug", crewSlug, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return sidecarTeardownResult{
			Status: teardownFailed,
			Reason: fmt.Sprintf("the container runtime refused part of the teardown (%v); some sidecar "+
				"containers or volumes for %q may still exist", firstErr, crewSlug),
		}
	}
	return sidecarTeardownResult{Status: teardownRemoved}
}
