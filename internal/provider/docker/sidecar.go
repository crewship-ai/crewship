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
// Naming convention: <prefix>-svc-<crew_slug>-<crew_id>-<service_name>,
// built through crewResourceName so it matches CrewContainerName's
// scheme exactly. The globally-unique crew id is load-bearing, not
// decoration: `crews` is UNIQUE(workspace_id, slug), so a slug names
// a crew only WITHIN a workspace while one crewshipd serves every
// workspace against one daemon. Keyed on the slug alone (as this was
// until #1732) two tenants who both call a crew `data-crew` resolved
// to ONE sidecar container and ONE data volume. The crew slug, crew
// id and service name are all DNS-label-validated upstream, so the
// resulting docker name is always container-name-safe.
//
// Ownership is stamped in labels, and labels — never name prefixes —
// are what the stop/remove/list paths match on:
//
//	crewship.crew-id  the globally-unique crew id   (the authority)
//	crewship.crew     the crew slug                 (human-readable)
//	crewship.kind     "sidecar" | "sidecar-volume"
//	crewship.svc      the manifest service name
//
// See sidecarMatchesCrew for why a name prefix is not usable here.
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
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

// sidecarSpecHashLabel stores a digest of the full desired spec on
// every sidecar container we create, so on next EnsureCrewServices
// we can detect drift in any field (command, env, ports, volumes,
// healthcheck) — not just image. Image is checked separately so the
// error path can name "image drift" specifically.
const sidecarSpecHashLabel = "crewship.svc.spec_hash"

// Ownership labels stamped on every sidecar container and every sidecar
// volume. crewCrewIDLabel is the one that carries tenancy: it holds the
// globally-unique crew id, and it is the only value the stop / remove /
// list paths are allowed to match on (#1732). crewCrewLabel keeps the slug
// for operators reading `docker ps`, exactly as the agent-runtime container
// does — it is a display value, never an identity check.
const (
	crewCrewIDLabel     = "crewship.crew-id"
	crewCrewLabel       = "crewship.crew"
	crewKindLabel       = "crewship.kind"
	sidecarSvcLabel     = "crewship.svc"
	sidecarVolNameLabel = "crewship.svc.volume"

	// crewRuntimeKind is the crewship.kind value the crew's own agent
	// runtime container carries (see the Labels block in
	// docker_container.go's assembleCrewSpec). Named here, next to the
	// label keys and the sidecar kinds, so the discovery paths match a
	// constant rather than re-spelling the string.
	crewRuntimeKind   = "crew"
	sidecarKind       = "sidecar"
	sidecarVolumeKind = "sidecar-volume"
)

// Default resource caps applied to every crew service (sidecar) container
// (audit F6). Sidecars run untrusted, tenant-declared images on the shared
// host, so an uncapped one is a host-wide DoS vector. These are deliberately
// smaller than the agent-runtime caps (8 GiB / 2 CPU) — typical service
// images (redis, postgres, mariadb, mongo, rabbitmq, nats, qdrant) sit well
// under 2 GiB / 1 CPU at steady state — while still bounding a runaway.
const (
	sidecarMemoryBytes = int64(2048) * 1024 * 1024 // 2 GiB
	sidecarNanoCPUs    = int64(1 * 1e9)            // 1.0 CPU
)

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
func volumeListOptions() client.VolumeListOptions {
	return client.VolumeListOptions{}
}

// sidecarContainerName returns the docker container name for one sidecar:
// "<prefix>-svc-<slug>-<crew_id>-<service>". Built on crewResourceName so it
// carries the globally-unique crew id the same way CrewContainerName does
// (audit C1 / #1732) — two workspaces with an identically-slugged crew get
// distinct containers.
func (p *Provider) sidecarContainerName(crewID, crewSlug, serviceName string) string {
	return p.crewResourceName("svc", crewID, crewSlug) + "-" + serviceName
}

// sidecarVolumeName returns the per-crew docker volume name for a service's
// named volume: "<prefix>-svc-<slug>-<crew_id>-vol-<volume>". Two crews that
// declare `pg-data` get distinct volumes even when they share a slug across
// workspaces — sidecars never share state across crews or tenants.
func (p *Provider) sidecarVolumeName(crewID, crewSlug, volumeName string) string {
	return p.crewResourceName("svc", crewID, crewSlug) + "-vol-" + volumeName
}

// legacySidecarContainerName / legacySidecarVolumeName reproduce the pre-#1732
// slug-only names. They exist for exactly one purpose: the one-time migration
// in migrateLegacySidecarResources, which is the only thing allowed to look a
// sidecar up by a name that carries no crew id.
func (p *Provider) legacySidecarContainerName(crewSlug, serviceName string) string {
	return p.namePrefix() + "-svc-" + crewSlug + "-" + serviceName
}

func (p *Provider) legacySidecarVolumeName(crewSlug, volumeName string) string {
	return p.namePrefix() + "-svc-" + crewSlug + "-vol-" + volumeName
}

// sidecarContainerLabels / sidecarVolumeLabels are the single definition of a
// sidecar resource's ownership stamp. Keeping them in one place means the
// create path and the match path cannot drift apart.
func sidecarContainerLabels(crewID, crewSlug, serviceName, specHash string) map[string]string {
	return map[string]string{
		"managed-by":         "crewship",
		crewCrewIDLabel:      crewID,
		crewCrewLabel:        crewSlug,
		crewKindLabel:        sidecarKind,
		sidecarSvcLabel:      serviceName,
		sidecarSpecHashLabel: specHash,
	}
}

func sidecarVolumeLabels(crewID, crewSlug, serviceName, volumeName string) map[string]string {
	return map[string]string{
		crewCrewIDLabel:     crewID,
		crewCrewLabel:       crewSlug,
		crewKindLabel:       sidecarVolumeKind,
		sidecarSvcLabel:     serviceName,
		sidecarVolNameLabel: volumeName,
	}
}

// sidecarMatchesCrew reports whether a labelled docker object (container or
// volume) belongs to crewID's sidecars.
//
// It matches the crew ID label EXACTLY and nothing else. Two weaker matches
// were both live bugs:
//
//   - by `crewship.crew` (the slug): slugs are UNIQUE(workspace_id, slug), so
//     one workspace's teardown reached another workspace's live Postgres and
//     deleted its data volume (#1732).
//   - by name prefix: slugs are DNS-label-shaped and may contain hyphens, so
//     crew "data"'s volume prefix "<prefix>-svc-data-vol-" also prefixes crew
//     "data-vol-x"'s volumes. Adding the crew id narrows that but does not
//     close it — a slug may itself contain "-vol-". A label equality check has
//     no such ambiguity.
//
// An empty crewID never matches: a caller that could not resolve the crew id
// must sweep nothing, not everything.
func sidecarMatchesCrew(labels map[string]string, crewID, kind string) bool {
	if crewID == "" {
		return false
	}
	return labels[crewCrewIDLabel] == crewID && labels[crewKindLabel] == kind
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
	// Fail closed on a missing crew id rather than degrade to the pre-#1732
	// slug-only names: crewResourceName drops empty segments, so an id-less
	// call would silently rebuild the very namespace two tenants used to
	// share. Every caller has the id (it is the crew's primary key).
	if team.ID == "" {
		return nil, fmt.Errorf("docker: EnsureCrewServices requires a crew id — " +
			"sidecar containers and volumes are namespaced by it so two workspaces " +
			"with the same crew slug never share one")
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

	// Re-key any sidecars this crew created under the pre-#1732 slug-only
	// names before we go looking for the id-scoped ones. Silently creating a
	// fresh id-scoped postgres next to a still-running legacy one would leave
	// two containers claiming the same `postgres` DNS alias on the crew
	// bridge — the agent would round-robin between its real database and an
	// empty one. Same reasoning, and the same fail-safe copy, as the C1
	// migration in migrateLegacyCrewResources.
	if err := p.migrateLegacySidecarResources(ctx, team.ID, team.Slug, team.Services); err != nil {
		return nil, err
	}

	ids := make(map[string]string, len(team.Services))
	for i := range team.Services {
		svc := &team.Services[i]
		id, err := p.ensureSidecar(ctx, team.ID, team.Slug, svc)
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
func (p *Provider) ensureSidecar(ctx context.Context, crewID, crewSlug string, svc *provider.CrewService) (string, error) {
	name := p.sidecarContainerName(crewID, crewSlug, svc.Name)
	desiredHash := computeSidecarSpecHash(svc)

	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	for _, c := range listResult.Items {
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
			if _, err := p.client.ContainerStop(ctx, c.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
				// Stop can fail when the container is already
				// gone (race with another reconcile). Inspect
				// + skip-if-not-found would be cleaner, but
				// the subsequent Remove with Force handles the
				// happy path; only error out if Remove also
				// fails, since that's the load-bearing step.
				p.logger.Debug("sidecar stop returned error (may be already stopped)",
					"service", svc.Name, "error", err)
			}
			if _, err := p.client.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
				return "", fmt.Errorf("remove sidecar %q for recreate: %w", svc.Name, err)
			}
			break // fall through to create
		}
		if c.State != "running" {
			if _, err := p.client.ContainerStart(ctx, c.ID, client.ContainerStartOptions{}); err != nil {
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
		fullName := p.sidecarVolumeName(crewID, crewSlug, vol.Name)
		if err := p.ensureVolumeLabeled(ctx, fullName,
			sidecarVolumeLabels(crewID, crewSlug, svc.Name, vol.Name)); err != nil {
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
	exposed := dockernetwork.PortSet{}
	for _, p := range svc.Ports {
		// ParsePort handles "5432", "5432/tcp" and "5432/udp" directly
		// (bare ports default to tcp). No suffix munging: appending
		// "/tcp" to an already-qualified "53/udp" would produce the
		// malformed key "53/udp/tcp".
		port, err := dockernetwork.ParsePort(p)
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
		Labels:       sidecarContainerLabels(crewID, crewSlug, svc.Name, desiredHash),
		Healthcheck:  hc,
	}
	if len(svc.Command) > 0 {
		cfg.Cmd = svc.Command
	}

	// Audit H7 baseline hardening. Sidecars used to inherit Docker's
	// default HostConfig (no SecurityOpt, no resource caps), which the
	// audit flagged as a privilege-escalation + fork-bomb pivot path.
	//
	// What's safe across every common sidecar image (redis, postgres,
	// mariadb, mongo, rabbitmq, nats, qdrant, chromadb, ollama):
	//
	//   - no-new-privileges: disables setuid binary privilege escalation
	//     inside the container. No legitimate sidecar relies on setuid
	//     post-startup.
	//   - PidsLimit 512: caps process count per container. Generous (a
	//     postgres + autovacuum + walwriter stack sits under 30; redis
	//     under 5) while denying fork-bomb-style DoS.
	//
	// Capability dropping is intentionally NOT in this baseline -- some
	// images still need CHOWN/SETUID for entrypoint user-switching, and
	// the audit's design note (notes/sidecar-zero-hardening.md) calls
	// for a separate per-image test matrix before tightening further.
	//
	// Audit F6 (MED): the baseline above capped process count but left
	// Memory and NanoCPUs at Docker's default of *unlimited*. A single
	// crew's redis/postgres/etc. could therefore balloon and exhaust the
	// shared host's RAM/CPU, DoSing every co-resident tenant on the daemon.
	// Mirror the agent-runtime caps (docker_container.go) with a smaller
	// default that comfortably fits typical service images (redis,
	// postgres, mariadb, mongo, rabbitmq, nats, qdrant) while bounding a
	// runaway container.
	pidsLimit := int64(512)
	hostCfg := &container.HostConfig{
		Mounts:        mounts,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
		SecurityOpt:   []string{"no-new-privileges:true"},
		Resources: container.Resources{
			Memory:    sidecarMemoryBytes,
			NanoCPUs:  sidecarNanoCPUs,
			PidsLimit: &pidsLimit,
		},
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

	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: networkCfg,
		Name:             name,
	})
	if err != nil {
		return "", fmt.Errorf("create sidecar: %w", err)
	}
	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start sidecar: %w", err)
	}
	p.logger.Info("sidecar started", "crew", crewSlug, "crew_id", crewID,
		"service", svc.Name, "container", created.ID, "image", svc.Image)
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

	reader, err := p.client.ImagePull(ctx, ref, client.ImagePullOptions{})
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
			inspect, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
			if err != nil {
				continue // transient — keep polling
			}
			if inspect.Container.State == nil {
				continue
			}
			if inspect.Container.State.Health == nil {
				// Container is running but has no healthcheck
				// configured at the docker level (e.g. spec said
				// otherwise but docker didn't apply it). Treat
				// "running" as ready and move on.
				if inspect.Container.State.Running {
					return nil
				}
				continue
			}
			switch inspect.Container.State.Health.Status {
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
func (p *Provider) StopCrewServices(ctx context.Context, crewID, crewSlug string) error {
	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	timeout := 10
	var failures []error
	for _, c := range listResult.Items {
		if !sidecarMatchesCrew(c.Labels, crewID, sidecarKind) {
			continue
		}
		if c.State != "running" {
			continue
		}
		if _, err := p.client.ContainerStop(ctx, c.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
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
func (p *Provider) RemoveCrewServices(ctx context.Context, crewID, crewSlug string) error {
	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	var failures []error
	for _, c := range listResult.Items {
		if !sidecarMatchesCrew(c.Labels, crewID, sidecarKind) {
			continue
		}
		if _, err := p.client.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
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
//
// Volumes are selected by an EXACT match on the crew id label, never by
// name prefix. The prefix this used to sweep — "<prefix>-svc-<slug>-vol-" —
// was wrong twice over (#1732): it carried no crew id, so deleting one
// workspace's `data-crew` deleted another workspace's data directory; and
// being a prefix it also reached crew `data-vol-x`'s volumes when crew
// `data` was deleted. Adding the id to the name narrows the second problem
// but cannot close it, because a slug may itself contain "-vol-". Label
// equality closes both.
func (p *Provider) RemoveCrewServiceVolumes(ctx context.Context, crewID, crewSlug string) error {
	// List by filter is preferable but docker's volume list filter
	// API treats `label=managed-by=crewship` consistently; we list
	// all and filter by ownership label in code to keep this simple.
	volList, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	var failures []error
	for _, vol := range volList.Items {
		if !sidecarMatchesCrew(vol.Labels, crewID, sidecarVolumeKind) {
			continue
		}
		if _, err := p.client.VolumeRemove(ctx, vol.Name, client.VolumeRemoveOptions{Force: true}); err != nil {
			p.logger.Warn("remove sidecar volume failed", "volume", vol.Name, "error", err)
			failures = append(failures, fmt.Errorf("remove %s: %w", vol.Name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("remove %d sidecar volume(s) for crew %q: %w", len(failures), crewSlug, errors.Join(failures...))
	}
	return nil
}
