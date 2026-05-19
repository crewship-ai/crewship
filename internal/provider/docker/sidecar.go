package docker

// Sidecar (per-crew service) container management. Sidecars are the
// Redis / Postgres / MySQL / etc. containers a crew can declare in
// its services_json column; the docker provider starts them
// alongside the agent runtime so the agent can reach them via the
// crew bridge network by Service.Name.
//
// Lifecycle:
//
//   EnsureCrewServices       — start (or reattach to) every declared
//                              sidecar; idempotent on warm restart.
//   StopCrewServices         — graceful stop for crew shutdown.
//   RemoveCrewServices       — force-remove containers (volumes
//                              preserved unless RemoveCrewVolumes
//                              is also called).
//
// Naming convention: <prefix>-svc-<crew_slug>-<service_name>. The
// crew slug + service name pair are both DNS-label-validated at
// the API layer, so the resulting docker name is always
// container-name-safe.
//
// Network model: every sidecar attaches to the same configured
// network as the agent (p.cfg.Network). The container is registered
// with an alias matching the service name, so DNS lookups inside
// the agent container resolve `redis:6379`, `postgres:5432` etc.
// without any host-port publish.
//
// Image pulls are unconditional + best-effort: if the registry is
// unreachable and we already have a local copy, we proceed; if
// neither is true the EnsureCrewServices call fails loudly. This
// mirrors the agent-image policy in ensureImage above.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"

	"github.com/crewship-ai/crewship/internal/provider"
)

// sidecarSpecHashLabel stores a digest of the full desired spec on
// every sidecar container we create, so on next EnsureCrewServices
// we can detect drift in any field (command, env, ports, volumes,
// healthcheck) — not just image. Image is checked separately so the
// error path can name "image drift" specifically.
const sidecarSpecHashLabel = "crewship.svc.spec_hash"

// computeSidecarSpecHash returns a SHA-256 of the fields that, when
// changed, require recreating the container. Image is intentionally
// excluded because it's checked + reported separately upstream.
// Maps are walked in sorted key order so the digest is stable
// regardless of YAML key ordering or Go map iteration.
func computeSidecarSpecHash(svc *provider.CrewService) string {
	// Sort env + volumes for determinism. Slices keep their author
	// order — flipping the args list is a meaningful change.
	envKeys := make([]string, 0, len(svc.Env))
	for k := range svc.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	envPairs := make([][2]string, 0, len(envKeys))
	for _, k := range envKeys {
		envPairs = append(envPairs, [2]string{k, svc.Env[k]})
	}

	vols := append([]provider.CrewServiceVolume(nil), svc.Volumes...)
	sort.Slice(vols, func(i, j int) bool {
		if vols[i].Name != vols[j].Name {
			return vols[i].Name < vols[j].Name
		}
		return vols[i].Mount < vols[j].Mount
	})

	payload := struct {
		Command     []string
		Env         [][2]string
		Ports       []string
		Volumes     []provider.CrewServiceVolume
		Healthcheck *provider.CrewServiceHealthcheck
	}{
		Command:     svc.Command,
		Env:         envPairs,
		Ports:       svc.Ports,
		Volumes:     vols,
		Healthcheck: svc.Healthcheck,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		// Marshal failure on a struct with only strings, slices,
		// maps, and one pointer is unreachable — but a zero hash
		// would silently disable drift detection. Return a unique
		// sentinel so the next reconcile triggers a recreate
		// rather than masking the problem.
		return "marshal-err"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]) // 32 hex chars — short label, ample collision space
}

// readToDiscard drains a reader into io.Discard. Wrapper exists so
// sidecar.go doesn't pull the entire io package; matches the
// pattern docker.go uses for pull-stream draining.
func readToDiscard(r io.Reader) (int64, error) {
	return io.Copy(io.Discard, r)
}

// volumeListOptions returns the no-filter ListOptions used by the
// sidecar volume cleanup path. Centralised here so a future
// label-based filter change touches one site.
func volumeListOptions() volume.ListOptions {
	return volume.ListOptions{}
}

// sidecarContainerName returns the docker container name for one
// sidecar. Kept short enough (crew slug ≤50, svc name ≤32) to stay
// under docker's 64-char container-name limit even with a prefix.
func (p *Provider) sidecarContainerName(crewSlug, serviceName string) string {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	return prefix + "-svc-" + crewSlug + "-" + serviceName
}

// sidecarVolumeName returns the per-crew docker volume name for a
// service's named volume. Two crews that declare `pg-data` get
// distinct volumes — sidecars never share state across crews.
func (p *Provider) sidecarVolumeName(crewSlug, volumeName string) string {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	return prefix + "-svc-" + crewSlug + "-vol-" + volumeName
}

// EnsureCrewServices ensures every declared sidecar is running for
// the given crew. Idempotent: a sidecar that already exists with
// the matching image and config is reused; mismatching ones are
// stopped+recreated. Returns a map of service name → container ID
// for the orchestrator to log or expose downstream.
//
// Caller is responsible for invoking EnsureCrewServices BEFORE the
// agent runtime is exec'd into, so the agent's first DB call lands
// on a ready sidecar. The function blocks until either (a) all
// healthchecked sidecars report HEALTHY (b) we time out waiting
// (c) a sidecar fails to start. (a) is best-effort: not every
// upstream image declares a HEALTHCHECK, and we don't synthesise
// one — services without a healthcheck are considered ready as
// soon as the container reports running.
func (p *Provider) EnsureCrewServices(ctx context.Context, team provider.CrewConfig) (map[string]string, error) {
	if len(team.Services) == 0 {
		return nil, nil
	}
	if team.Slug == "" {
		return nil, fmt.Errorf("docker: EnsureCrewServices requires a crew slug")
	}

	// All sidecars share the agent's bridge network so DNS resolves
	// service names without exposing host ports. ensureNetwork is
	// the same call EnsureCrewRuntime already makes.
	if p.cfg.Network != "" {
		if err := p.ensureNetwork(ctx, p.cfg.Network); err != nil {
			return nil, fmt.Errorf("ensure network for services: %w", err)
		}
	}

	mu := p.lockForCrew(team.ID)
	mu.Lock()
	defer mu.Unlock()

	ids := make(map[string]string, len(team.Services))
	for i := range team.Services {
		svc := &team.Services[i]
		id, err := p.ensureSidecar(ctx, team.Slug, svc)
		if err != nil {
			return ids, fmt.Errorf("sidecar %q: %w", svc.Name, err)
		}
		ids[svc.Name] = id
	}

	// Wait for healthchecks (capped at 60s total across all
	// sidecars to keep the agent-start latency bounded). A failed
	// healthcheck now propagates as an error rather than a warning
	// so the agent never starts against a dependency the operator
	// declared a healthcheck for — silently proceeding would mask
	// half-broken setups that look fine until the first DB query
	// times out. Healthcheck-less services aren't gated (the
	// upstream image didn't declare one, we don't synthesise one).
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, svc := range team.Services {
		if svc.Healthcheck == nil {
			continue
		}
		if err := p.waitSidecarHealthy(waitCtx, ids[svc.Name]); err != nil {
			return ids, fmt.Errorf("sidecar %q not healthy: %w", svc.Name, err)
		}
	}
	return ids, nil
}

// ensureSidecar starts a single sidecar, reusing the existing
// container if its image AND full spec hash match. Any drift
// (image, command, env, ports, volumes, healthcheck) triggers a
// stop + remove + recreate so apply is true sync for sidecars,
// not just "fresh creates work."
func (p *Provider) ensureSidecar(ctx context.Context, crewSlug string, svc *provider.CrewService) (string, error) {
	name := p.sidecarContainerName(crewSlug, svc.Name)
	desiredHash := computeSidecarSpecHash(svc)

	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		var matched bool
		for _, n := range c.Names {
			if n == "/"+name {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		// Drift detection in two passes so the operator log gets
		// the most actionable reason. Image is the common case
		// (postgres:15 → postgres:16) and worth surfacing
		// specifically; everything else falls under "spec drift"
		// and the hash diff identifies it without enumerating
		// fields in the log message.
		drift := ""
		if c.Image != svc.Image {
			drift = fmt.Sprintf("image drift: %s → %s", c.Image, svc.Image)
		} else if c.Labels[sidecarSpecHashLabel] != desiredHash {
			drift = "spec drift (command/env/ports/volumes/healthcheck)"
		}
		if drift != "" {
			p.logger.Info("sidecar drift; recreating", "service", svc.Name, "reason", drift)
			timeout := 5
			if err := p.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil {
				// Stop can fail when the container is already
				// gone (race with another reconcile). Inspect
				// + skip-if-not-found would be cleaner, but
				// the subsequent Remove with Force handles the
				// happy path; only error out if Remove also
				// fails, since that's the load-bearing step.
				p.logger.Debug("sidecar stop returned error (may be already stopped)",
					"service", svc.Name, "error", err)
			}
			if err := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
				return "", fmt.Errorf("remove sidecar %q for recreate: %w", svc.Name, err)
			}
			break // fall through to create
		}
		if c.State != "running" {
			if err := p.client.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
				return "", fmt.Errorf("start existing sidecar: %w", err)
			}
		}
		return c.ID, nil
	}

	// Pull image (best-effort: tolerate offline + local copy).
	if err := p.pullSidecarImage(ctx, svc.Image); err != nil {
		return "", err
	}

	// Volumes: ensure each named volume exists before container
	// create so docker doesn't auto-create unowned anonymous
	// volumes that we then can't clean up.
	mounts := make([]mount.Mount, 0, len(svc.Volumes))
	for _, vol := range svc.Volumes {
		fullName := p.sidecarVolumeName(crewSlug, vol.Name)
		if err := p.ensureVolume(ctx, fullName); err != nil {
			return "", fmt.Errorf("ensure volume %q: %w", vol.Name, err)
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: fullName,
			Target: vol.Mount,
		})
	}

	// Env: map[string]string → docker's []string "KEY=VALUE" form.
	envSlice := make([]string, 0, len(svc.Env))
	for k, v := range svc.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	// Ports: container-internal only — never publish to the host.
	// Sidecars are crew-private; exposing them on the host would
	// leak DB ports across crews and tenants.
	exposed := nat.PortSet{}
	for _, p := range svc.Ports {
		port, err := nat.NewPort("tcp", strings.TrimSuffix(p, "/tcp"))
		if err == nil {
			exposed[port] = struct{}{}
		}
	}

	// Healthcheck from the manifest's shape → docker's.
	var hc *container.HealthConfig
	if svc.Healthcheck != nil {
		hc = &container.HealthConfig{
			Test:        svc.Healthcheck.Test,
			Interval:    svc.Healthcheck.Interval,
			Timeout:     svc.Healthcheck.Timeout,
			Retries:     svc.Healthcheck.Retries,
			StartPeriod: svc.Healthcheck.StartPeriod,
		}
	}

	cfg := &container.Config{
		Image:        svc.Image,
		Env:          envSlice,
		ExposedPorts: exposed,
		Labels: map[string]string{
			"managed-by":         "crewship",
			"crewship.crew":      crewSlug,
			"crewship.kind":      "sidecar",
			"crewship.svc":       svc.Name,
			sidecarSpecHashLabel: desiredHash,
		},
		Healthcheck: hc,
	}
	if len(svc.Command) > 0 {
		cfg.Cmd = strslice.StrSlice(svc.Command)
	}

	hostCfg := &container.HostConfig{
		Mounts:        mounts,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
	}

	// NetworkingConfig wires the sidecar to the crew bridge with a
	// DNS alias so `redis` resolves inside the agent container.
	var networkCfg *dockernetwork.NetworkingConfig
	if p.cfg.Network != "" {
		networkCfg = &dockernetwork.NetworkingConfig{
			EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
				p.cfg.Network: {Aliases: []string{svc.Name}},
			},
		}
	}

	created, err := p.client.ContainerCreate(ctx, cfg, hostCfg, networkCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("create sidecar: %w", err)
	}
	if err := p.client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start sidecar: %w", err)
	}
	p.logger.Info("sidecar started", "crew", crewSlug, "service", svc.Name, "container", created.ID, "image", svc.Image)
	return created.ID, nil
}

// pullSidecarImage pulls the image; tolerates registry outages when
// a local copy is already present. Mirrors ensureImage but without
// digest pinning — sidecar images use mutable tags by convention
// (redis:7-alpine, postgres:16) and operators bump them by editing
// services_json, not by digest reconciliation.
func (p *Provider) pullSidecarImage(ctx context.Context, ref string) error {
	_, inspectErr := p.client.ImageInspect(ctx, ref)
	localPresent := inspectErr == nil

	reader, err := p.client.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		if localPresent {
			p.logger.Warn("sidecar image pull failed; using local copy", "image", ref, "error", err)
			return nil
		}
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer reader.Close()
	// Drain the pull stream — docker holds the lock until EOF.
	if _, err := readToDiscard(reader); err != nil {
		return fmt.Errorf("drain pull %s: %w", ref, err)
	}
	return nil
}

// waitSidecarHealthy polls container inspect until Health.Status
// is "healthy" or the context expires. Returns the last status
// when the context expires so the caller can log meaningfully.
func (p *Provider) waitSidecarHealthy(ctx context.Context, containerID string) error {
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for healthy")
		case <-tick.C:
			inspect, err := p.client.ContainerInspect(ctx, containerID)
			if err != nil {
				continue // transient — keep polling
			}
			if inspect.State == nil {
				continue
			}
			if inspect.State.Health == nil {
				// Container is running but has no healthcheck
				// configured at the docker level (e.g. spec said
				// otherwise but docker didn't apply it). Treat
				// "running" as ready and move on.
				if inspect.State.Running {
					return nil
				}
				continue
			}
			switch inspect.State.Health.Status {
			case "healthy":
				return nil
			case "unhealthy":
				return fmt.Errorf("sidecar reported unhealthy")
			}
		}
	}
}

// StopCrewServices stops every sidecar container belonging to the
// crew. Volumes are preserved.
//
// Per-container failures are logged AND aggregated into the
// returned error so the caller knows the operation was partial.
// We still attempt every container before returning — short-
// circuiting on the first failure would leave the rest of the
// crew's sidecars running, which is the worst outcome (the agent
// is gone but its dependents linger).
func (p *Provider) StopCrewServices(ctx context.Context, crewSlug string) error {
	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	timeout := 10
	var failures []error
	for _, c := range containers {
		if c.Labels["crewship.crew"] != crewSlug || c.Labels["crewship.kind"] != "sidecar" {
			continue
		}
		if c.State != "running" {
			continue
		}
		if err := p.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil {
			p.logger.Warn("stop sidecar failed", "container", c.ID, "error", err)
			failures = append(failures, fmt.Errorf("stop %s: %w", c.ID, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("stop %d sidecar(s) for crew %q: %w", len(failures), crewSlug, errors.Join(failures...))
	}
	return nil
}

// RemoveCrewServices force-removes every sidecar container for the
// crew. Volumes are NOT removed — call RemoveCrewServiceVolumes if
// you want a full teardown. Like StopCrewServices, attempts every
// container and aggregates failures.
func (p *Provider) RemoveCrewServices(ctx context.Context, crewSlug string) error {
	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	var failures []error
	for _, c := range containers {
		if c.Labels["crewship.crew"] != crewSlug || c.Labels["crewship.kind"] != "sidecar" {
			continue
		}
		if err := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
			p.logger.Warn("remove sidecar failed", "container", c.ID, "error", err)
			failures = append(failures, fmt.Errorf("remove %s: %w", c.ID, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("remove %d sidecar(s) for crew %q: %w", len(failures), crewSlug, errors.Join(failures...))
	}
	return nil
}

// RemoveCrewServiceVolumes removes every named volume created for
// the crew's sidecars. Call AFTER RemoveCrewServices so docker
// doesn't refuse with "volume in use". Per-volume failures are
// aggregated; the rest of the volumes are still attempted.
func (p *Provider) RemoveCrewServiceVolumes(ctx context.Context, crewSlug string) error {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	wantPrefix := prefix + "-svc-" + crewSlug + "-vol-"
	// List by filter is preferable but docker's volume list filter
	// API treats `label=managed-by=crewship` consistently; we list
	// all and filter by name prefix in code to keep this simple.
	list, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	var failures []error
	for _, vol := range list.Volumes {
		if vol == nil || !strings.HasPrefix(vol.Name, wantPrefix) {
			continue
		}
		if err := p.client.VolumeRemove(ctx, vol.Name, true); err != nil {
			p.logger.Warn("remove sidecar volume failed", "volume", vol.Name, "error", err)
			failures = append(failures, fmt.Errorf("remove %s: %w", vol.Name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("remove %d sidecar volume(s) for crew %q: %w", len(failures), crewSlug, errors.Join(failures...))
	}
	return nil
}
