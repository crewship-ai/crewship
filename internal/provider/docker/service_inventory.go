package docker

// Live service inventory: "what sidecars are actually running for this
// crew right now" read straight from Docker, as opposed to the DB's
// crews.services_json snapshot of what was last configured (which can
// drift — an operator-stopped or OOM-killed sidecar still reads
// "configured" there). Backs GET /api/v1/crews/{crewId}/services.

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/client"
)

// matchCrewService reports whether a listed container is one of crewSlug's
// sidecars and, if so, returns the manifest service name.
//
// It keys off the exact per-container labels every sidecar is stamped with
// at create time (crewship.crew / crewship.kind / crewship.svc — see
// sidecarContainerName's cfg.Labels), NOT the container name. This is the
// same authoritative match StopCrewServices and RemoveCrewServices use.
//
// A name-prefix match ("<prefix>-svc-<slug>-") is UNSAFE here: crew slugs
// are DNS-label-shaped and may contain hyphens, so crew "alpha"'s prefix
// "crewship-svc-alpha-" also prefixes crew "alpha-foo"'s container
// "crewship-svc-alpha-foo-redis". Since slugs are only workspace-unique
// while the docker daemon is shared instance-wide, that boundary confusion
// is a cross-tenant information disclosure. Exact-label match has no such
// ambiguity: crewship.crew == "alpha" never equals "alpha-foo".
func matchCrewService(labels map[string]string, crewSlug string) (string, bool) {
	if labels["crewship.crew"] != crewSlug || labels["crewship.kind"] != "sidecar" {
		return "", false
	}
	svc := labels["crewship.svc"]
	if svc == "" {
		return "", false
	}
	return svc, true
}

// ListCrewServices enumerates a crew's live sidecar containers. Docker
// has no server-side label filter wired here, so — like
// FindCrewContainer and ensureSidecar before it — this lists ALL
// containers (All: true, so stopped sidecars are included) and filters
// by the crewship.crew label in Go. Read-only: never starts, stops, or
// removes anything.
//
// State comes from ContainerStatus (an inspect call) rather than the
// list Summary's own State field, so a service row reports through the
// exact same running/stopped/creating/error vocabulary as the crew's
// own container-status endpoint and never disagrees with it. Ports
// come from the list Summary, which already reflects the container's
// exposed ports.
func (p *Provider) ListCrewServices(ctx context.Context, crewSlug string) ([]provider.CrewServiceStatus, error) {
	if crewSlug == "" {
		return nil, fmt.Errorf("docker: ListCrewServices requires a crew slug")
	}

	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	out := []provider.CrewServiceStatus{}
	for _, c := range listResult.Items {
		name, matched := matchCrewService(c.Labels, crewSlug)
		if !matched {
			continue
		}

		state := string(c.State)
		if st, statusErr := p.ContainerStatus(ctx, c.ID); statusErr == nil {
			state = st.State
		}

		ports := make([]string, 0, len(c.Ports))
		for _, pt := range c.Ports {
			if pt.Type == "" {
				ports = append(ports, fmt.Sprintf("%d", pt.PrivatePort))
				continue
			}
			ports = append(ports, fmt.Sprintf("%d/%s", pt.PrivatePort, pt.Type))
		}

		out = append(out, provider.CrewServiceStatus{
			Name:   name,
			Image:  c.Image,
			Status: c.Status,
			State:  state,
			Ports:  ports,
		})
	}
	return out, nil
}
