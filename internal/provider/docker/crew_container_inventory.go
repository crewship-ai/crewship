package docker

// Live crew container inventory: "which containers does this crew have on the
// daemon right now, and what state are they in" — the crew's own agent runtime
// plus its sidecars, in one pass. Backs GET /api/v1/crews/{crewId}/containers,
// `crewship crew containers`, and the crew bottom panel's Docker tab (#1697).
//
// ListCrewServices answers the narrower sidecar-only question and cannot
// answer this one: the crew runtime container carries no crewship.svc label,
// so it is invisible to that listing by construction — which is exactly why
// the Docker tab had nothing to show.

import (
	"context"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/client"
)

// matchCrewContainer classifies a listed container as one of crewID's, and
// says which kind it is.
//
// Sidecars and label-carrying crew runtimes are matched on the
// crewship.crew-id label exactly, for the tenancy reason sidecarMatchesCrew
// documents: crew slugs are UNIQUE(workspace_id, slug) while one daemon serves
// every workspace, so any slug- or name-prefix match can show one tenant
// another tenant's containers (#1732).
//
// The name check is a deliberate, NARROWER fallback and not a relaxation of
// that rule. The crew runtime container only started carrying ownership labels
// when they were added to createCrewContainer; a container created before that
// and still running today has none, and a crew whose only container predates
// the labels would otherwise report an empty inventory — the same silent
// nothing this endpoint exists to remove. It is an EQUALITY check against the
// one name CrewContainerName derives from the crew's globally-unique id, never
// a prefix match, so it cannot reach a different crew's container.
func matchCrewContainer(labels map[string]string, names []string, crewID, crewContainerName string) (string, bool) {
	if crewID == "" {
		return "", false
	}
	if sidecarMatchesCrew(labels, crewID, sidecarKind) {
		return provider.CrewContainerKindSidecar, true
	}
	if sidecarMatchesCrew(labels, crewID, crewRuntimeKind) {
		return provider.CrewContainerKindCrew, true
	}
	// A container that names ANOTHER crew is never adopted by the name
	// fallback: a wrong ownership label is a conflict, not a gap.
	//
	// A container carrying THIS crew's id but no kind label still reaches the
	// name check below — that combination is exactly what the labels looked
	// like before crewship.kind existed, so rejecting it here would re-open
	// the gap the fallback is for.
	if id := labels[crewCrewIDLabel]; id != "" && id != crewID {
		return "", false
	}
	if crewContainerName == "" {
		return "", false
	}
	for _, n := range names {
		if strings.TrimPrefix(n, "/") == crewContainerName {
			return provider.CrewContainerKindCrew, true
		}
	}
	return "", false
}

// containerDisplayName returns the container's runtime name with docker's
// leading "/" stripped. Falls back to the short id, because a row with no
// name at all is a row an operator cannot act on.
func containerDisplayName(names []string, id string) string {
	for _, n := range names {
		if trimmed := strings.TrimPrefix(n, "/"); trimmed != "" {
			return trimmed
		}
	}
	return provider.ShortID(id)
}

// ListCrewContainers enumerates every container belonging to a crew. Docker
// has no server-side label filter wired here, so — like ListCrewServices and
// FindCrewContainer before it — this lists ALL containers (All: true, so
// stopped ones are included) and filters in Go. Read-only: never starts, stops
// or removes anything.
//
// State comes from ContainerStatus (an inspect call) rather than the list
// Summary's own State field, so a row reports through the exact same
// running/stopped/creating/error vocabulary as the crew's container-status
// endpoint and the service inventory, and never disagrees with either. A
// failed inspect degrades to the Summary's state rather than failing the whole
// listing — one unreadable container must not blank the panel.
func (p *Provider) ListCrewContainers(ctx context.Context, crewID, crewSlug string) ([]provider.CrewContainerInfo, error) {
	if crewID == "" {
		return nil, fmt.Errorf("docker: ListCrewContainers requires a crew id (slug %q) — "+
			"crew containers are identified by the globally-unique crew id, not by the per-workspace slug", crewSlug)
	}

	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	// Only derivable when the crew has a slug; the name is id+slug shaped.
	crewContainerName := ""
	if crewSlug != "" {
		crewContainerName = p.CrewContainerName(crewID, crewSlug)
	}

	out := []provider.CrewContainerInfo{}
	for _, c := range listResult.Items {
		kind, matched := matchCrewContainer(c.Labels, c.Names, crewID, crewContainerName)
		if !matched {
			continue
		}

		state := string(c.State)
		if st, statusErr := p.ContainerStatus(ctx, c.ID); statusErr == nil && st != nil {
			state = st.State
		}

		out = append(out, provider.CrewContainerInfo{
			ID:    c.ID,
			Name:  containerDisplayName(c.Names, c.ID),
			Image: c.Image,
			Kind:  kind,
			State: state,
		})
	}
	return out, nil
}
