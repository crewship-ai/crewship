package api

// Crew delete → sidecar teardown (#1709), and the tenancy hazard it runs into.
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
// # Why this refuses to run sometimes
//
// Both teardown primitives are keyed on the crew SLUG alone: containers are
// matched by the label `crewship.crew=<slug>` and volumes by the name prefix
// `<prefix>-svc-<slug>-vol-`. Neither carries the crew id — and `crews` is
// UNIQUE(workspace_id, slug), so a slug identifies a crew only WITHIN a
// workspace.
//
// Two workspaces with a crew slugged `data-crew` therefore share one namespace
// on the daemon: deleting one would force-remove the other's live Postgres and
// delete its data directory. The prefix match widens it further — deleting
// `data` reaches `data-vol-x`'s volumes, because `…-svc-data-vol-` is a prefix
// of `…-svc-data-vol-x-vol-…`.
//
// The real fix is the one audit C1 already applied to crew containers:
// CrewContainerName embeds the crew id "so two tenants with an identically-named
// crew never collide on a shared daemon". The sidecar names and labels need the
// same treatment, in internal/provider/docker — including a migration for the
// sidecars already created under slug-only names. That is not this package's to
// make, and it is reported rather than attempted here.
//
// Until it lands, this refuses to destroy what it cannot prove it owns: when
// another live crew shares the slug (or extends its volume prefix), the crew is
// still deleted, the sidecars are LEFT RUNNING, and the caller is told exactly
// that. Leaking a container is recoverable by hand; deleting another tenant's
// database is not.
//
// The crew's own RUNTIME container is deliberately NOT removed here: it has an
// idle TTL and a reaper, so it stops on its own. Sidecars have neither, which is
// exactly why they need this.

import (
	"context"
	"database/sql"
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
	if crewSlug == "" {
		// Unreachable by construction — Delete loads the slug in the same query
		// that proves the crew exists, and refuses to delete without it. Kept as
		// a guard because a slug-less teardown call would sweep by the prefix
		// "<prefix>-svc--vol-", i.e. nothing, silently.
		return sidecarTeardownResult{
			Status: teardownSkipped,
			Reason: "the crew's slug could not be resolved, and the sidecar teardown is keyed on it",
		}
	}

	sp, canRemove := h.container.(provider.SidecarProvider)
	vr, canRemoveVolumes := h.container.(provider.ServiceVolumeRemover)
	if !canRemove && !canRemoveVolumes {
		return sidecarTeardownResult{Status: teardownUnsupported}
	}

	// The tenancy check, before anything is destroyed.
	claimants, err := crewsSharingSidecarNamespace(ctx, h.db, crewID, crewSlug)
	if err != nil {
		h.logger.Warn("crew delete: could not check the sidecar namespace; leaving sidecars in place",
			"crew_id", crewID, "crew_slug", crewSlug, "error", err)
		return sidecarTeardownResult{
			Status: teardownSkipped,
			Reason: fmt.Sprintf("could not check whether other crews share the sidecar namespace for %q "+
				"(%v), so the sidecars were left running rather than risk removing another crew's", crewSlug, err),
		}
	}
	if len(claimants) > 0 {
		h.logger.Warn("crew delete: sidecar namespace is shared; leaving sidecars in place",
			"crew_id", crewID, "crew_slug", crewSlug, "shared_with", claimants)
		return sidecarTeardownResult{
			Status: teardownSkipped,
			Reason: fmt.Sprintf("the sidecar containers and volumes for %q are named by slug alone, with no "+
				"crew id, and %d other live crew(s) share that namespace (%v) — removing them would remove "+
				"those crews' containers and data volumes too. Nothing was removed; delete the sidecars for "+
				"%q by hand once no other crew uses that slug.",
				crewSlug, len(claimants), claimants, crewSlug),
		}
	}

	// WithoutCancel: the HTTP request may be finished (or the client gone) by
	// the time docker answers, and a half-torn-down crew — containers removed,
	// volumes left — is the worst of the three possible outcomes.
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), crewSidecarTeardownTimeout)
	defer cancel()

	var firstErr error
	if canRemove {
		if err := sp.RemoveCrewServices(tctx, crewSlug); err != nil {
			h.logger.Warn("crew delete: remove sidecar containers",
				"crew_id", crewID, "crew_slug", crewSlug, "error", err)
			firstErr = err
			// Volumes are still attempted: docker refuses volumes that are
			// referenced, so at worst this second call is a no-op that logs.
		}
	}
	if canRemoveVolumes {
		if err := vr.RemoveCrewServiceVolumes(tctx, crewSlug); err != nil {
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

// crewsSharingSidecarNamespace returns the slugs of OTHER live crews whose
// sidecar containers or volumes the slug-keyed teardown would also remove.
//
// Two ways to collide, both consequences of the sidecars carrying no crew id:
//
//   - an identical slug in another workspace (`crews` is UNIQUE(workspace_id,
//     slug), so this is legal and common — every dev slot seeds the same names);
//   - a slug that EXTENDS this one past the volume infix, because volumes are
//     swept by the name prefix `<prefix>-svc-<slug>-vol-`: deleting `data`
//     matches `data-vol-x`'s `<prefix>-svc-data-vol-x-vol-<name>`.
//
// Soft-deleted crews are deliberately not counted: their sidecars are orphans
// that nothing else will ever clean up, so sweeping them is the point.
func crewsSharingSidecarNamespace(ctx context.Context, db *sql.DB, crewID, crewSlug string) ([]string, error) {
	if db == nil || crewSlug == "" {
		return nil, nil
	}
	// Slugs are kebab-case (validated on write), so they carry no LIKE
	// wildcards; the pattern is still built from a bound parameter rather than
	// interpolated into the statement.
	rows, err := db.QueryContext(ctx, `
		SELECT slug FROM crews
		WHERE id != ?
		  AND deleted_at IS NULL
		  AND (slug = ? OR slug LIKE ? || '-vol%')`,
		crewID, crewSlug, crewSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
