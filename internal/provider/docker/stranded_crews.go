package docker

// Crew containers left behind on a daemon we no longer talk to (#1704).
//
// Switching the container runtime under a running instance — `colima start`
// taking the current context, DOCKER_HOST repointed, OrbStack stopped and
// Rancher Desktop started — does not stop anything. The previous daemon keeps
// running the crew container, under the same name, with the same credentials.
// Crewship then creates a second container with that name on the new daemon and
// reports only that one.
//
// The wasted container is the harmless half. /crew, /workspace and /output are
// bind-backed volumes onto HOST directories, so both containers have write
// access to the same host tree — and /crew/agents/<slug>/.memory is where agent
// memory lives. An agent inside the stranded container keeps mutating the
// memory the live crew reads back. Proven on 2026-08-02: a marker written from
// the OrbStack container was read out of the Colima one.
//
// Nothing caught it because every discovery path in this package — the orphan
// reaper, PruneCrewRuntimes, FindCrewContainer — is name-based off the crews
// table AND only ever asks the daemon currently in use. The crews row is fine;
// the daemon moved out from under it.
//
// What this file does: at startup, enumerate every OTHER Docker-API daemon
// reachable on this host (DetectAll already knows how), find the crew
// containers belonging to THIS instance on them, say so by name, and stop them
// — the write access is the defect, and a container that is stopped no longer
// has any. Stop, never remove: the container, its logs and its volumes stay put
// for whoever wants to look.
//
// Deliberately NOT here: a `container_runtime` provenance column on `crews`.
// Recording which daemon made a container is worth doing, but it answers a
// different question — "which daemon SHOULD have this?" — and it cannot find
// anything this cannot, because a stranded container is identified by being
// present on a daemon we are not using, not by disagreeing with a stored value.
// It is also a schema migration in a package outside this change. Follow-up.

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// strandedScanTimeout bounds the whole cross-daemon sweep. It runs at boot, in
// front of everything else the server wants to do, so it gets a hard ceiling:
// DetectAll probes concurrently at socketPingTimeout each, then one container
// list per daemon that answered. A sweep that times out reports what it found
// so far — a partial answer at boot beats a complete one that delays it.
const strandedScanTimeout = 10 * time.Second

// StrandedCrew is one crew container found on a daemon this provider is not
// using.
type StrandedCrew struct {
	Runtime string // the daemon's label: colima, orbstack, rancher, podman, docker
	Socket  string // where that daemon answers
	Host    string // the client URL for it, for the remediation command
	Name    string // container name — the same name the live container has
	ID      string
	CrewID  string // crewship.crew-id label, when the container carries one
	Running bool
}

// findStrandedCrews returns this instance's crew containers that live on some
// other reachable daemon.
//
// "This instance's" is by container-name prefix, which is what makes the result
// safe to act on: ContainerPrefix is the multi-instance isolation key
// (crewship-1-, crewship-2-, …), so a container carrying ours is one WE made.
// Two servers sharing a prefix across two daemons is not a configuration this
// can tell apart — but it is also not a configuration that works, because those
// two servers are already writing the same host crew directories, which is the
// very defect being fixed.
func (p *Provider) findStrandedCrews(ctx context.Context) []StrandedCrew {
	return p.findStrandedCrewsIn(ctx, DetectAll(ctx), listContainersAt)
}

// listContainersAt is the real per-daemon lister: a short-lived client against
// one endpoint. Injected into findStrandedCrewsIn so the sweep's logic can be
// tested without nine sockets and four daemons on the machine running the test.
func listContainersAt(ctx context.Context, host string) ([]container.Summary, error) {
	cli, err := client.New(client.WithHost(host))
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// findStrandedCrewsIn is the injectable core of findStrandedCrews.
func (p *Provider) findStrandedCrewsIn(
	ctx context.Context,
	all []DetectResult,
	list func(context.Context, string) ([]container.Summary, error),
) []StrandedCrew {
	prefix := p.namePrefix() + "-team-"
	var found []StrandedCrew
	for _, other := range all {
		if SameRuntimeEndpoint(other, p.detected) {
			continue
		}
		items, err := list(ctx, other.Host)
		if err != nil {
			// A daemon that answered a ping but not a list is a fact worth
			// having: it means this sweep cannot rule out stranded crews there.
			p.logger.Warn("could not list containers on another runtime while checking for stranded crews (#1704)",
				"runtime", other.Runtime, "socket", other.Socket, "error", err)
			continue
		}
		for _, c := range items {
			name := primaryContainerName(c.Names)
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			found = append(found, StrandedCrew{
				Runtime: other.Runtime,
				Socket:  other.Socket,
				Host:    other.Host,
				Name:    name,
				ID:      c.ID,
				CrewID:  c.Labels["crewship.crew-id"],
				Running: c.State == container.StateRunning,
			})
		}
	}
	return found
}

// primaryContainerName strips the Docker API's leading "/" from the first name
// of a container. Containers can carry several names (network aliases); the
// first is the one `docker ps` shows and the one CrewContainerName produced.
func primaryContainerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// strandedCrewPolicy reads CREWSHIP_STRANDED_CREWS. "report" reports without
// stopping anything; anything else (including unset) stops them.
//
// Stopping is the default because the failure it prevents is silent data
// corruption and the failure it can cause is a crew that has to be woken again.
// Those are not close. The escape hatch exists for the one legitimate reason to
// want the container left alone — an operator mid-investigation who wants to
// exec into it.
func strandedCrewPolicy() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CREWSHIP_STRANDED_CREWS")), "report") {
		return "report"
	}
	return "stop"
}

// reconcileStrandedCrews is the startup sweep: find crew containers of ours on
// other daemons, name them, and take away their write access.
func (p *Provider) reconcileStrandedCrews(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, strandedScanTimeout)
	defer cancel()
	p.actOnStrandedCrews(ctx, p.findStrandedCrews(ctx), strandedCrewPolicy(), p.stopStrandedCrew)
}

// actOnStrandedCrews is the injectable core of reconcileStrandedCrews: what to
// say and what to stop, given a list of stranded containers.
func (p *Provider) actOnStrandedCrews(
	ctx context.Context,
	stranded []StrandedCrew,
	policy string,
	stop func(context.Context, StrandedCrew) error,
) {
	for _, s := range stranded {
		if !s.Running {
			p.logger.Info("found a stopped crew container on another container runtime (#1704)",
				"runtime", s.Runtime, "socket", s.Socket, "container", s.Name, "crew_id", s.CrewID)
			continue
		}
		p.logger.Warn("a crew container from this instance is still RUNNING on a container runtime Crewship is no longer using: it holds the same bind-mounted host directories as the live crew, including /crew where agent memory lives, so an agent inside it can keep writing memory the live crew reads back (#1704)",
			"runtime", s.Runtime,
			"socket", s.Socket,
			"container", s.Name,
			"crew_id", s.CrewID,
			"in_use_runtime", p.detected.Runtime,
			"in_use_socket", p.detected.Socket,
			"inspect_it_with", "DOCKER_HOST="+s.Host+" docker inspect "+s.Name,
		)
		if policy == "report" {
			p.logger.Warn("leaving it running because CREWSHIP_STRANDED_CREWS=report — it keeps write access to live crew memory until you stop it",
				"container", s.Name, "stop_it_with", "DOCKER_HOST="+s.Host+" docker stop "+s.Name)
			continue
		}
		if err := stop(ctx, s); err != nil {
			p.logger.Error("could not stop the stranded crew container; it still has write access to live crew memory — stop it by hand (#1704)",
				"container", s.Name, "runtime", s.Runtime, "error", err,
				"stop_it_with", "DOCKER_HOST="+s.Host+" docker stop "+s.Name)
			continue
		}
		p.logger.Warn("stopped a crew container stranded on another container runtime; it is stopped, NOT removed, so its filesystem and volumes are still there to inspect (#1704)",
			"container", s.Name, "runtime", s.Runtime, "crew_id", s.CrewID)
	}
}

// stopStrandedCrew stops one container on the daemon it is stranded on.
// Stop and not remove — see the file comment.
func (p *Provider) stopStrandedCrew(ctx context.Context, s StrandedCrew) error {
	cli, err := client.New(client.WithHost(s.Host))
	if err != nil {
		return err
	}
	defer cli.Close()
	timeout := int(strandedStopGrace.Seconds())
	_, err = cli.ContainerStop(ctx, s.ID, client.ContainerStopOptions{Timeout: &timeout})
	return err
}

// strandedStopGrace is how long the stranded container gets to shut down before
// SIGKILL. Its PID 1 is `exec sleep infinity`, which does not handle SIGTERM,
// so this window is spent waiting rather than draining — keep it short.
const strandedStopGrace = 5 * time.Second
