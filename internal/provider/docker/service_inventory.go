package docker

// Live service inventory: "what sidecars are actually running for this
// crew right now" read straight from Docker, as opposed to the DB's
// crews.services_json snapshot of what was last configured (which can
// drift — an operator-stopped or OOM-killed sidecar still reads
// "configured" there). Backs GET /api/v1/crews/{crewId}/services.

import (
	"context"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/client"
)

// crewServiceNamePrefix returns "<namePrefix>-svc-<crewSlug>-" — the
// name segment that identifies a container as one of the crew's
// sidecars (mirrors sidecarContainerName). The trailing "-" is
// load-bearing: it's stripped off each matching container's name to
// recover the plain service name the manifest declared.
func (p *Provider) crewServiceNamePrefix(crewSlug string) string {
	return p.namePrefix() + "-svc-" + crewSlug + "-"
}

// matchCrewServiceName returns the service name (prefix stripped) if
// any of the container's names carries the crew's sidecar prefix.
func matchCrewServiceName(names []string, prefix string) (string, bool) {
	for _, n := range names {
		trimmed := strings.TrimPrefix(n, "/")
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimPrefix(trimmed, prefix), true
		}
	}
	return "", false
}

// ListCrewServices enumerates a crew's live sidecar containers. Docker
// has no server-side "name starts with" filter, so — like
// FindCrewContainer and ensureSidecar before it — this lists ALL
// containers (All: true, so stopped sidecars are included) and filters
// by name prefix in Go. Read-only: never starts, stops, or removes
// anything.
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
	prefix := p.crewServiceNamePrefix(crewSlug)

	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	out := []provider.CrewServiceStatus{}
	for _, c := range listResult.Items {
		name, matched := matchCrewServiceName(c.Names, prefix)
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
