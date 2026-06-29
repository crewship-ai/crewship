package docker

// Container lifecycle + runtime-state methods. Split from docker.go so
// that file can focus on provider construction, runtime detection,
// network/image/volume helpers and the exec surface. All methods are
// on *Provider — pure file move, no signature changes.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/safepath"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
)

// FindCrewContainer is a non-mutating lookup for an existing crew
// container by slug. Returns ("", false, nil) when none is found. Used
// by Server.Start to re-register containers that survived a crewshipd
// restart with the stats collector.
func (p *Provider) FindCrewContainer(ctx context.Context, id, slug string) (string, bool, error) {
	containerName := p.CrewContainerName(id, slug)
	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", false, fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+containerName {
				return c.ID, c.State == "running", nil
			}
		}
	}
	return "", false, nil
}

// legacyCrewNames returns the three pre-C1 (slug-only) resource names for a
// crew slug: container, home volume, tools volume. Centralised so the
// provisioning guard, detection, and prune never disagree about which names are
// legacy (a future naming change touches one place, not three).
func (p *Provider) legacyCrewNames(slug string) (container, home, tools string) {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	return prefix + "-team-" + slug, prefix + "-home-" + slug, prefix + "-tools-" + slug
}

// legacyNameSets computes, for the given crews, the set of pre-C1 slug-only
// resource names to TARGET and the set of live id-scoped names to PROTECT.
// Because slugs may contain hyphens, a crew whose slug equals another crew's
// "<slug>-<id>" string would make a legacy key collide with that crew's LIVE
// id-scoped resource; subtracting the protected set guarantees detection never
// false-positives and prune never removes a live crew's container/volumes.
func (p *Provider) legacyNameSets(crews []provider.CrewRef) map[string]bool {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	legacy := make(map[string]bool, len(crews)*3)
	protected := make(map[string]bool, len(crews)*3)
	for _, c := range crews {
		if c.Slug == "" {
			continue
		}
		legacy[prefix+"-team-"+c.Slug] = true
		legacy[prefix+"-home-"+c.Slug] = true
		legacy[prefix+"-tools-"+c.Slug] = true
		if c.ID != "" {
			protected[prefix+"-team-"+c.Slug+"-"+c.ID] = true
			protected[prefix+"-home-"+c.Slug+"-"+c.ID] = true
			protected[prefix+"-tools-"+c.Slug+"-"+c.ID] = true
		}
	}
	for name := range protected {
		delete(legacy, name)
	}
	return legacy
}

// checkNoLegacyCrewResources fails when pre-C1 (slug-only) container or
// home/tools volumes for slug still exist on the daemon. C1 (2026-06 audit)
// re-keyed crew resources from "<prefix>-{team,home,tools}-<slug>" to also
// include the globally-unique crew id; provisioning the id-scoped runtime
// alongside a surviving legacy one would dual-mount the crew's bind mounts and
// orphan its persistent volumes. No-op on a fresh post-C1 daemon. The returned
// error wraps provider.ErrLegacyCrewResource so chatbridge can surface a safe,
// actionable message instead of echoing the raw error to the end user.
func (p *Provider) checkNoLegacyCrewResources(ctx context.Context, slug string) error {
	if slug == "" {
		return nil
	}
	legacyContainer, legacyHome, legacyTools := p.legacyCrewNames(slug)

	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers (legacy C1 check): %w", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+legacyContainer {
				return fmt.Errorf("legacy slug-scoped container %q exists from before the C1 naming change; run 'crewship admin prune-legacy' before this crew can provision its id-scoped runtime: %w", legacyContainer, provider.ErrLegacyCrewResource)
			}
		}
	}

	// Volume check is best-effort: the container guard above already blocks the
	// dangerous dual-runtime case. If the daemon can't list volumes, log and
	// proceed rather than wedge all provisioning.
	list, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		p.logger.Warn("legacy C1 volume check skipped: list volumes failed", "error", err)
		return nil
	}
	for _, vol := range list.Volumes {
		if vol == nil {
			continue
		}
		if vol.Name == legacyHome || vol.Name == legacyTools {
			return fmt.Errorf("legacy slug-scoped volume %q exists from before the C1 naming change; run 'crewship admin prune-legacy' before provisioning: %w", vol.Name, provider.ErrLegacyCrewResource)
		}
	}
	return nil
}

// HasLegacyCrewResources reports whether any pre-C1 (slug-only) container or
// home/tools volume exists for any of crews. Read-only, detection-only
// counterpart to PruneLegacyCrewResources — `crewship doctor` reads it (via the
// admin legacy-resources endpoint) to warn an operator before an agent run
// fails. Lists the daemon ONCE and matches against that single snapshot; live
// id-scoped resources are excluded so a slug/id collision can't false-positive.
// Satisfies provider.LegacyResourceDetector.
func (p *Provider) HasLegacyCrewResources(ctx context.Context, crews []provider.CrewRef) (bool, error) {
	legacy := p.legacyNameSets(crews)
	if len(legacy) == 0 {
		return false, nil
	}

	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return false, fmt.Errorf("list containers (legacy C1 detect): %w", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if legacy[strings.TrimPrefix(name, "/")] {
				return true, nil
			}
		}
	}

	list, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return false, fmt.Errorf("list volumes (legacy C1 detect): %w", err)
	}
	for _, vol := range list.Volumes {
		if vol != nil && legacy[vol.Name] {
			return true, nil
		}
	}
	return false, nil
}

// PruneLegacyCrewResources removes pre-C1 slug-only container/volumes for the
// given crews and returns the names removed. Satisfies
// provider.LegacyResourcePruner. The daemon is enumerated ONCE (not per crew);
// live id-scoped resources are excluded so a slug/id collision can never delete
// an active crew's data. Containers are removed before volumes (a surviving
// legacy container would hold them). Per-resource removal failures are logged
// and skipped — a volume still in use must not wedge the rest. A transport
// failure listing the daemon is returned WITH whatever was removed so far so
// the caller can surface the partial result rather than a silent no-op.
func (p *Provider) PruneLegacyCrewResources(ctx context.Context, crews []provider.CrewRef) ([]string, error) {
	legacy := p.legacyNameSets(crews)
	removed := []string{}
	if len(legacy) == 0 {
		return removed, nil
	}

	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return removed, fmt.Errorf("list containers (legacy C1 prune): %w", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			n := strings.TrimPrefix(name, "/")
			if legacy[n] {
				if rmErr := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); rmErr != nil {
					p.logger.Warn("legacy C1 container remove failed", "container", n, "error", rmErr)
				} else {
					removed = append(removed, n)
				}
			}
		}
	}

	list, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return removed, fmt.Errorf("list volumes (legacy C1 prune): %w", err)
	}
	for _, vol := range list.Volumes {
		if vol != nil && legacy[vol.Name] {
			if rmErr := p.client.VolumeRemove(ctx, vol.Name, true); rmErr != nil {
				p.logger.Warn("legacy C1 volume remove failed", "volume", vol.Name, "error", rmErr)
			} else {
				removed = append(removed, vol.Name)
			}
		}
	}
	return removed, nil
}

// EnsureCrewRuntime creates or starts a Docker container for the given crew.
// It applies security isolation (non-root UID, cap-drop ALL, read-only rootfs)
// and resource limits (memory, CPU, PID). Returns the container ID.
//
// Calls are serialized per crew_id via Provider.crewLocks. A burst of
// concurrent assignments to the same crew (e.g. 8 issues dispatched at
// once) used to race between the "list → not found" and "create" steps,
// with N-1 callers failing with `Conflict: name already in use`. The
// per-crew mutex makes the second caller see the freshly-created
// container and reuse it instead.
func (p *Provider) EnsureCrewRuntime(ctx context.Context, team provider.CrewConfig) (string, error) {
	// crew_id/slug end up as filesystem path components below — validate
	// before any filepath.Join so a malformed ID can't reach the bind
	// mount layer (which would let an attacker who controls the DB pin
	// container output at /etc, /root, etc.).
	if _, err := safepath.ValidateComponent(team.ID); err != nil {
		return "", fmt.Errorf("crew id not safe for path: %w", err)
	}
	if team.Slug != "" {
		if _, err := safepath.ValidateComponent(team.Slug); err != nil {
			return "", fmt.Errorf("crew slug not safe for path: %w", err)
		}
	}

	mu := p.lockForCrew(team.ID)
	mu.Lock()
	defer mu.Unlock()

	p.logger.Debug("EnsureCrewRuntime", "crew_id", team.ID, "crew_slug", team.Slug)
	// Ensure network exists (auto-recreate if deleted at runtime)
	if p.cfg.Network != "" {
		p.logger.Debug("ensuring network", "network", p.cfg.Network)
		if err := p.ensureNetwork(ctx, p.cfg.Network); err != nil {
			return "", fmt.Errorf("ensure network: %w", err)
		}
	}

	containerName := p.CrewContainerName(team.ID, team.Slug)

	// C1 migration guard (CodeRabbit): refuse to provision the new id-scoped
	// runtime while pre-C1, slug-only resources for this crew still exist on
	// the daemon. Silently creating fresh "<slug>-<id>" names alongside a
	// surviving legacy "crewship-team-<slug>" container would point two
	// runtimes at the same crew bind mounts, and fresh empty home/tools volumes
	// would strand the agent's persistent home (~/.ssh, tooling). Fail loudly
	// so an operator migrates/prunes the legacy resources first.
	if err := p.checkNoLegacyCrewResources(ctx, team.Slug); err != nil {
		return "", err
	}

	// Compute the image we WANT to run, mirroring the
	// CachedImage > Image > default chain used at create time
	// below. Lifted earlier so the existing-container loop can
	// notice when the manifest has been provisioned to a new
	// image tag (post-feature-add rebuild, for example) and
	// rebuild the container instead of silently reusing the
	// stale one. Pre-fix the loop short-circuited on State=running
	// without checking Config.Image, so the operator had to
	// `docker rm -f <name>` by hand after every devcontainer edit.
	// callerSpecifiedImage distinguishes "caller wants THIS image" from "caller
	// passed no image, fall back to the runtime default". The image-drift
	// recreate below must only fire in the former case: a bare-config caller
	// (e.g. the assignment path's GetOrCreateContainer, which passes only
	// ID+Slug) would otherwise resolve desiredImage to the default and tear
	// down a perfectly good provisioned container out from under a concurrent
	// run — killing that run with exit 137 and thrashing the container.
	desiredImage := p.cfg.RuntimeImage
	callerSpecifiedImage := false
	if team.Image != "" {
		desiredImage = team.Image
		callerSpecifiedImage = true
	}
	if team.CachedImage != "" {
		desiredImage = team.CachedImage
		callerSpecifiedImage = true
	}

	p.logger.Debug("listing containers")
	// Check if container already exists
	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	p.logger.Debug("containers listed", "count", len(containers))
	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+containerName {
				// Check if container has /crew mount; if not, recreate it.
				inspect, inspErr := p.client.ContainerInspect(ctx, c.ID)
				if inspErr != nil {
					return "", fmt.Errorf("inspect existing container %s: %w", containerName, inspErr)
				}
				// Image-drift check before the mount checks: if a
				// re-provision produced a new image tag, the running
				// container is stale by definition (its filesystem
				// reflects the OLD provisioned image). Tear it down
				// and fall through to create-new with the new tag.
				if callerSpecifiedImage && inspect.Config != nil && desiredImage != "" && inspect.Config.Image != desiredImage {
					p.logger.Info("recreating container (image drift)",
						"container", containerName,
						"running_image", inspect.Config.Image,
						"desired_image", desiredImage,
					)
					timeout := 10
					_ = p.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
					_ = p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
					break // fall through to create new container
				}
				// Check required mounts: /crew, /home/agent (volume), /opt/crew-tools (volume).
				requiredMounts := map[string]bool{"/crew": false, "/home/agent": false, "/opt/crew-tools": false}
				for _, m := range inspect.Mounts {
					if _, ok := requiredMounts[m.Destination]; ok {
						requiredMounts[m.Destination] = true
					}
				}
				needsRecreate := false
				for dest, found := range requiredMounts {
					if !found {
						needsRecreate = true
						p.logger.Info("missing mount, will recreate container", "mount", dest, "container", containerName)
						break
					}
				}
				if needsRecreate {
					p.logger.Info("recreating container (missing required mounts)", "container", containerName)
					timeout := 10
					_ = p.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
					_ = p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
					break // fall through to create new container
				}
				if c.State == "running" {
					return c.ID, nil
				}
				// Verify bind-mount directories still exist (macOS /tmp is wiped on reboot).
				bindMountDirs := []string{
					filepath.Join(p.cfg.OutputBasePath, "workspaces", team.ID),
					filepath.Join(p.cfg.OutputBasePath, team.ID),
					filepath.Join(p.cfg.OutputBasePath, "crews", team.ID),
					filepath.Join(p.cfg.OutputBasePath, "secrets", team.ID),
				}
				bindsMissing := false
				for _, d := range bindMountDirs {
					if _, statErr := os.Stat(d); os.IsNotExist(statErr) {
						bindsMissing = true
						break
					}
				}
				if bindsMissing {
					p.logger.Info("bind-mount dirs missing, recreating container", "container", containerName)
					timeout := 10
					_ = p.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
					_ = p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
					break // fall through to create new container
				}
				if err := p.client.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
					return "", fmt.Errorf("start existing container: %w", err)
				}
				// Note: postStartCommand runs ONCE when the container is
				// freshly created (see the create-path call below). On warm
				// restart of a stopped container, the hooks already ran at
				// create time and the changes were persisted to the container
				// filesystem — re-running them would cause issues for
				// non-idempotent commands (e.g. "mysql start" would hit port
				// conflicts, "mkdir /foo" would fail on EEXIST).
				//
				// This is a deliberate deviation from the devcontainer spec's
				// "run on every start" semantics, but matches what most
				// template authors actually want. If a future use case needs
				// ephemeral hooks that re-run on each restart, add a
				// per-feature opt-in flag rather than flipping this default.
				return c.ID, nil
			}
		}
	}

	runtime := p.cfg.DefaultRuntime
	if runtime == "" {
		runtime = "runc"
	}
	if v := os.Getenv("CREWSHIP_RUNTIME"); v != "" {
		runtime = v
	}

	// Last-resort defaults. The real value should arrive from
	// crews.container_memory_mb via chatbridge.resolver, but every call
	// site that *also* hits this path must survive — 512 MiB caused
	// Docker OOM-kill (exit 137) on real agent runs.
	// Guard against any non-positive value (including a stray -1 / -N
	// from a future "unset sentinel" convention) so we never pass a
	// negative limit to the Docker daemon, which rejects it outright.
	memoryMB := team.MemoryMB
	if memoryMB <= 0 {
		memoryMB = 8192
	}
	cpus := team.CPUs
	if cpus <= 0 {
		cpus = 2.0
	}

	// Image selection chain was already resolved into desiredImage
	// at the top of EnsureCrewRuntime so the existing-container
	// loop could detect drift. Alias here keeps the create-path
	// reads readable without recomputing.
	runtimeImage := desiredImage

	p.logger.Debug("ensuring image", "image", runtimeImage)
	if err := p.ensureImage(ctx, runtimeImage); err != nil {
		return "", fmt.Errorf("ensure image: %w", err)
	}

	p.logger.Debug("image ok, creating dirs")
	outputPath := filepath.Join(p.cfg.OutputBasePath, team.ID)
	workspacePath := filepath.Join(p.cfg.OutputBasePath, "workspaces", team.ID)
	crewPath := filepath.Join(p.cfg.OutputBasePath, "crews", team.ID)
	secretsPath := filepath.Join(p.cfg.OutputBasePath, "secrets", team.ID)

	allDirs := []string{
		outputPath,
		workspacePath,
		crewPath,
		filepath.Join(crewPath, "shared"),
		filepath.Join(crewPath, "agents"),
		secretsPath,
		filepath.Join(secretsPath, "shared"),
	}
	for _, dir := range allDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	// Ensure persistent named volumes for home directory and crew tools.
	if team.Slug != "" {
		if err := p.ensureVolume(ctx, p.homeVolumeName(team.ID, team.Slug)); err != nil {
			return "", err
		}
		if err := p.ensureVolume(ctx, p.toolsVolumeName(team.ID, team.Slug)); err != nil {
			return "", err
		}
	}

	// Fix ownership for container user (1001:1001). The host process may not
	// run as root, so os.Chown can fail. In that case we use a short-lived
	// Docker container (running as root) to chown the bind-mount paths.
	needsDockerChown := false
	for _, dir := range allDirs {
		if err := os.Chown(dir, 1001, 1001); err != nil {
			needsDockerChown = true
			break
		}
	}
	if needsDockerChown {
		chownCmd := buildChownInitCmd(allDirs, crewPath)
		var mounts []mount.Mount
		for _, dir := range allDirs {
			mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: dir, Target: "/mnt" + dir})
		}
		initResp, initErr := p.client.ContainerCreate(ctx,
			&container.Config{
				Image:      runtimeImage,
				User:       "0:0",
				Entrypoint: []string{"sh", "-c", chownCmd},
			},
			&container.HostConfig{Mounts: mounts},
			nil, nil, "")
		if initErr == nil {
			_ = p.client.ContainerStart(ctx, initResp.ID, container.StartOptions{})
			// ContainerWait returns two channels; drain one of them (or
			// cancel) so we do not leak a goroutine inside the docker client
			// when the wait completes. A short timeout keeps us from hanging
			// indefinitely on a wedged daemon.
			waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
			statusCh, waitErrCh := p.client.ContainerWait(waitCtx, initResp.ID, container.WaitConditionNotRunning)
			select {
			case <-statusCh:
			case werr := <-waitErrCh:
				if werr != nil {
					p.logger.Debug("init container wait returned error", "error", werr)
				}
			case <-waitCtx.Done():
				p.logger.Debug("init container wait timed out", "error", waitCtx.Err())
			}
			waitCancel()
			_ = p.client.ContainerRemove(ctx, initResp.ID, container.RemoveOptions{})
			p.logger.Debug("init container fixed bind-mount ownership")
		} else {
			p.logger.Warn("init container chown failed, falling back to 0777", "error", initErr)
			for _, dir := range allDirs {
				os.Chmod(dir, 0777) //nolint:errcheck
			}
		}
	}

	pidsLimit := int64(200)
	p.logger.Debug("calling ContainerCreate", "image", runtimeImage, "name", containerName)
	env := []string{
		"CREWSHIP_CREW_ID=" + team.ID,
	}
	// Merge devcontainer containerEnv (from runtime config) if present.
	// These come from the committed cached image's devcontainer_config,
	// passed through from crew config. CREWSHIP_-prefixed keys are reserved
	// for platform-managed vars and silently skipped.
	if team.ContainerEnv != nil {
		for k, v := range team.ContainerEnv {
			if strings.HasPrefix(k, "CREWSHIP_") {
				continue
			}
			env = append(env, k+"="+v)
		}
	}
	// Expand ${VAR} references in env values against the image's default
	// ENV. Devcontainer features sometimes emit literals like
	// "PATH=/usr/local/cargo/bin:${PATH}" expecting the runtime to do
	// shell expansion at container start. Without this, Docker stores the
	// literal "${PATH}" string and the runtime PATH ends up missing
	// /usr/bin / /bin entirely (mkdir / touch / etc. all become exit 127).
	if imgEnv, err := imageEnvMap(ctx, p.client, runtimeImage); err == nil {
		env = expandContainerEnv(env, imgEnv)
	} else {
		needsExpansion := false
		for _, e := range env {
			if eq := strings.IndexByte(e, '='); eq > 0 && strings.Contains(e[eq+1:], "${") {
				needsExpansion = true
				break
			}
		}
		if needsExpansion {
			return "", fmt.Errorf("inspect image env for containerEnv expansion (%s): %w", runtimeImage, err)
		}
		p.logger.Warn("could not inspect image for env expansion — passing containerEnv literally",
			"image", runtimeImage, "error", err)
	}
	containerCfg := &container.Config{
		Image: runtimeImage,
		User:  "1001:1001",
		Env:   env,
		Healthcheck: &container.HealthConfig{
			Test:     []string{"CMD-SHELL", "test -f /workspace/.ready"},
			Interval: 30_000_000_000,
			Timeout:  5_000_000_000,
			Retries:  3,
		},
	}
	// Force the bind-mounted entrypoint.sh so custom base images (debian,
	// ubuntu) use our init script instead of their default (typically
	// /bin/sh). Clear Cmd because the entrypoint ends with `exec sleep
	// infinity` — no CMD needed. Paths are guaranteed non-empty by
	// buildMounts below (it errors out otherwise).
	containerCfg.Entrypoint = []string{"/usr/local/bin/entrypoint.sh"}
	containerCfg.Cmd = nil
	crewMounts, err := p.buildMounts(team.ID, team.Slug, workspacePath, outputPath, crewPath, secretsPath)
	if err != nil {
		return "", err
	}
	// Apply feature-declared mounts (e.g. DinD needs /var/run/docker.sock).
	// Feature metadata is user-controlled via devcontainer.json, so each
	// mount source is validated against an allowlist to prevent malicious
	// features from exposing arbitrary host paths (e.g. "/").
	for _, m := range team.ExtraMounts {
		if !devcontainer.IsAllowedMountSource(m.Source) {
			p.logger.Warn("rejecting feature-declared mount with unsafe source",
				"crew_id", team.ID,
				"source", m.Source,
				"target", m.Target,
			)
			continue
		}
		mt := mount.TypeBind
		if strings.EqualFold(m.Type, "volume") {
			mt = mount.TypeVolume
		}
		crewMounts = append(crewMounts, mount.Mount{
			Type:   mt,
			Source: m.Source,
			Target: m.Target,
		})
	}

	// Build base HostConfig. Privileged features (DinD etc.) require
	// dropping the default no-new-privileges and relaxing capability drops.
	securityOpt := []string{"no-new-privileges"}
	// NET_RAW used to be added unconditionally — it lets a process open
	// AF_PACKET sockets, which is a DNS-tunneling exfil primitive (carry
	// stolen secrets out via base64-encoded subdomain lookups against an
	// attacker DNS server, even when the egress allowlist blocks every
	// other domain). Removed from the default set; features that
	// genuinely need ICMP / raw sockets (network debugging utilities)
	// can opt in via team.CapAdd, which the devcontainer features parser
	// restricts to an explicit allowlist (NET_BIND_SERVICE today; add
	// NET_RAW to the allowlist there if a real use case appears).
	capAdd := []string{}
	readonlyRoot := true
	if team.Privileged {
		// Privileged mode implies the security restrictions we normally
		// enforce are unnecessary (and actually incompatible — dockerd
		// can't run under no-new-privileges with ReadOnlyRootFS).
		securityOpt = nil
		readonlyRoot = false
	}
	// Additional feature-declared capabilities/security opts are appended
	// after the defaults; Docker dedupes capAdd server-side.
	capAdd = append(capAdd, team.CapAdd...)
	securityOpt = append(securityOpt, team.SecurityOpt...)

	hostConfig := &container.HostConfig{
		Runtime:        runtime,
		ReadonlyRootfs: readonlyRoot,
		Privileged:     team.Privileged,
		Init:           boolPtrIf(team.Init),
		SecurityOpt:    securityOpt,
		CapDrop:        []string{"ALL"},
		CapAdd:         capAdd,
		// ExtraHosts makes host.docker.internal resolve to the Docker host
		// on both macOS and Linux, enabling containers to reach crewshipd
		// for assignment IPC calls via the sidecar.
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
		Resources: container.Resources{
			Memory:    int64(memoryMB) * 1024 * 1024,
			NanoCPUs:  int64(cpus * 1e9),
			PidsLimit: &pidsLimit,
		},
		Mounts: crewMounts,
		Tmpfs: map[string]string{
			"/tmp": "rw,size=500m",
		},
		NetworkMode: container.NetworkMode(p.cfg.Network),
	}
	resp, err := p.client.ContainerCreate(ctx,
		containerCfg,
		hostConfig,
		&dockernetwork.NetworkingConfig{},
		nil,
		containerName,
	)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if err := p.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("container start: %w", err)
	}

	p.logger.Info("crew container started",
		"crew_id", team.ID,
		"container_id", resp.ID[:12],
		"runtime", runtime,
	)

	// Sanity-check the bind-mounted sidecar on any BYOI crew (user-provided
	// base image, with or without a cached derivative). Runs the binary with
	// --version so an ABI mismatch (musl base vs. glibc sidecar) surfaces as
	// a clear error instead of a cryptic runtime crash once the agent starts.
	//
	// Previously scoped to `team.CachedImage == ""` only, meaning a cached
	// image originally built from a musl base would silently ship a broken
	// sidecar. Now fires whenever team.Image is set.
	if team.Image != "" {
		// Wrapped in an inline func so the WithTimeout's cancel is
		// released as soon as the sanity check returns, rather than
		// leaking until EnsureCrewRuntime itself returns (which may be
		// much later — post-start hooks, etc.).
		if err := func() error {
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			execCfg := container.ExecOptions{
				Cmd:          []string{"/usr/local/bin/crewship-sidecar", "--version"},
				User:         "0:0",
				AttachStdout: true,
				AttachStderr: true,
			}
			ex, execErr := p.client.ContainerExecCreate(checkCtx, resp.ID, execCfg)
			if execErr != nil {
				return nil
			}
			if startErr := p.client.ContainerExecStart(checkCtx, ex.ID, container.ExecStartOptions{}); startErr != nil {
				return nil
			}
			// Poll exit code briefly.
			for i := 0; i < 20; i++ {
				inspect, ierr := p.client.ContainerExecInspect(checkCtx, ex.ID)
				if ierr != nil {
					return nil
				}
				if !inspect.Running {
					if inspect.ExitCode != 0 {
						p.logger.Error("sidecar binary incompatible with container libc — " +
							"host-built sidecar expects glibc; Alpine/musl bases must use a glibc-compatible image")
						return fmt.Errorf("sidecar sanity check failed (exit %d) — custom base image %q is likely musl-based or missing glibc symbols; use a glibc base (debian, ubuntu, mcr devcontainers)", inspect.ExitCode, team.Image)
					}
					return nil
				}
				time.Sleep(50 * time.Millisecond)
			}
			return nil
		}(); err != nil {
			return "", err
		}
	}

	// Run postStartCommand hooks. The `/crew/init.sh` soft-promotion path
	// is OPT-IN per crew (team.InitHookEnabled). When disabled (default),
	// the auto-exec is skipped entirely — even a present and executable
	// init.sh script is ignored. When enabled, it runs FIRST as UID 1001.
	//
	// Why opt-in: /crew/init.sh sits on a persistent bind mount on the
	// host that survives container removal, sidecar reinstall, and
	// docker rm -f. An agent with write access to /crew (which every
	// agent has — it's the legitimate shared workspace) could stash a
	// reverse-shell or exfil command there, and the next operator restart
	// would auto-execute it as 1001. The default no-exec policy removes
	// this persistence vector; operators who want the soft-promotion
	// behaviour set init_hook_enabled=true on the crew manifest, which
	// is a deliberate trust statement that everything in init.sh is
	// code they wrote or audited.
	var hooks []string
	if team.InitHookEnabled {
		hooks = append(hooks, "[ -x /crew/init.sh ] && /crew/init.sh; true")
	} else {
		// Log a one-line breadcrumb when a script exists but the hook is
		// disabled — helps an operator who recently flipped the flag off
		// understand why their script stopped running. The exec just
		// stats the file; no execution.
		hooks = append(hooks,
			`if [ -e /crew/init.sh ]; then echo "crewship: /crew/init.sh present but init_hook_enabled=false on crew config — skipping execution" >&2; fi`)
	}
	hooks = append(hooks, team.PostStartCommands...)
	p.runPostStartCommands(ctx, resp.ID, hooks)

	return resp.ID, nil
}

// runPostStartCommands executes each post-start hook sequentially as the
// agent user (UID 1001). Per-command timeout is 60 s. Non-fatal: a failing
// hook is logged as a warning and we move on — agents can retry their own
// logic.
func (p *Provider) runPostStartCommands(ctx context.Context, containerID string, cmds []string) {
	if len(cmds) == 0 {
		return
	}
	for _, cmd := range cmds {
		runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		// strict mode — fail loud on first error, matches install.sh behavior.
		execCfg := container.ExecOptions{
			Cmd:          []string{"bash", "-lc", "set -e\n" + cmd},
			User:         "1001:1001",
			Env:          []string{"HOME=/home/agent", "USER=agent"},
			AttachStdout: true,
			AttachStderr: true,
		}
		ex, err := p.client.ContainerExecCreate(runCtx, containerID, execCfg)
		if err != nil {
			cancel()
			p.logger.Warn("postStartCommand exec create failed",
				"container", shortID(containerID), "cmd", cmd, "error", err)
			continue
		}
		if err := p.client.ContainerExecStart(runCtx, ex.ID, container.ExecStartOptions{}); err != nil {
			cancel()
			p.logger.Warn("postStartCommand exec start failed",
				"container", shortID(containerID), "cmd", cmd, "error", err)
			continue
		}
		// Poll exit code briefly; cap at ~60s total via runCtx timeout.
		for i := 0; i < 1200; i++ { // 1200 * 50ms = 60s
			inspect, ierr := p.client.ContainerExecInspect(runCtx, ex.ID)
			if ierr != nil {
				break
			}
			if !inspect.Running {
				if inspect.ExitCode != 0 {
					p.logger.Warn("postStartCommand exited non-zero",
						"container", shortID(containerID), "cmd", cmd, "exit_code", inspect.ExitCode)
				} else {
					p.logger.Debug("postStartCommand completed",
						"container", shortID(containerID), "cmd", cmd)
				}
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
	}
}

// shellQuote wraps s in single quotes for safe interpolation into a
// `sh -c "..."` command string. Inside single quotes the shell treats every
// character literally, so the only thing that needs special handling is an
// embedded single quote, expressed via the classic close-quote / escaped
// literal-quote / reopen-quote idiom. This neutralises spaces, ;, |, &,
// $(...), backticks, redirections and every other metacharacter. Crew IDs are
// server-generated CUIDs today, but quoting here makes the command robust if a
// path component ever becomes user-influenced.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildChownInitCmd assembles the root-owned init container's shell command
// that fixes bind-mount ownership for the container user (1001:1001) and then
// re-flips .memory subtrees to the sidecar group (1002). A shell is genuinely
// required: the command chains `find` invocations (with -name/-path globs) and
// `&&` / `;` sequencing. Every interpolated filesystem path is run through
// shellQuote so the command does exactly what it did before for a normal path,
// while no path component can inject shell syntax. See Issue #530 for the
// .memory ownership rationale.
func buildChownInitCmd(allDirs []string, crewPath string) string {
	mnt := func(p string) string { return shellQuote("/mnt" + p) }

	chownCmd := "chown -R 1001:1001"
	for _, dir := range allDirs {
		chownCmd += " " + mnt(dir)
	}
	chownCmd += ` && find ` + mnt(crewPath) + ` -name .memory -type d -exec chgrp -R 1002 {} +`
	chownCmd += ` ; find ` + mnt(crewPath) + ` -name .memory -type d -exec chmod 2775 {} +`
	chownCmd += ` ; find ` + mnt(crewPath) + ` -path '*/.memory/*' -type f -exec chmod g+rw {} +`
	return chownCmd
}

// shortID returns first 12 chars of a container ID, or the full string if shorter.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// StopCrewRuntime gracefully stops a crew container with a 30-second timeout.
func (p *Provider) StopCrewRuntime(ctx context.Context, containerID string) error {
	timeout := 30
	if err := p.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("stop crew runtime %s: %w", shortID(containerID), err)
	}
	return nil
}

// RemoveCrewRuntime forcefully removes a crew container.
func (p *Provider) RemoveCrewRuntime(ctx context.Context, containerID string) error {
	if err := p.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove crew runtime %s: %w", shortID(containerID), err)
	}
	return nil
}

// ContainerStatus inspects a container and returns its current state (running/stopped/error).
func (p *Provider) ContainerStatus(ctx context.Context, containerID string) (*provider.ContainerStatus, error) {
	inspect, err := p.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("container inspect: %w", err)
	}

	state := "stopped"
	switch {
	case inspect.State.Running:
		state = "running"
	case inspect.State.Restarting:
		state = "creating"
	case inspect.State.Dead || inspect.State.OOMKilled:
		state = "error"
	}

	return &provider.ContainerStatus{
		ID:     containerID,
		State:  state,
		Uptime: inspect.State.StartedAt,
	}, nil
}

// ContainerStats returns CPU and memory usage metrics for a running container.
func (p *Provider) ContainerStats(ctx context.Context, containerID string) (*provider.ContainerMetrics, error) {
	resp, err := p.client.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, fmt.Errorf("container stats: %w", err)
	}
	defer resp.Body.Close()
	var stats container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}
	var cpuPct float64
	// Guard against uint64 counter wraparound
	if stats.CPUStats.CPUUsage.TotalUsage >= stats.PreCPUStats.CPUUsage.TotalUsage &&
		stats.CPUStats.SystemUsage >= stats.PreCPUStats.SystemUsage {
		cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
		sysDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
		if sysDelta > 0 && cpuDelta >= 0 {
			numCPUs := float64(stats.CPUStats.OnlineCPUs)
			if numCPUs == 0 {
				numCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
			}
			if numCPUs == 0 {
				numCPUs = 1
			}
			cpuPct = (cpuDelta / sysDelta) * numCPUs * 100.0
		}
	}
	memUsed := int64(stats.MemoryStats.Usage - stats.MemoryStats.Stats["cache"])
	if memUsed < 0 {
		memUsed = int64(stats.MemoryStats.Usage)
	}
	memLimit := int64(stats.MemoryStats.Limit)
	var memPct float64
	if memLimit > 0 {
		memPct = float64(memUsed) / float64(memLimit) * 100.0
	}
	var netRx, netTx int64
	for _, iface := range stats.Networks {
		netRx += int64(iface.RxBytes)
		netTx += int64(iface.TxBytes)
	}
	return &provider.ContainerMetrics{
		CPUPercent: cpuPct, MemoryUsed: memUsed, MemoryLimit: memLimit,
		MemoryPct: memPct, NetRx: netRx, NetTx: netTx,
		PIDs: int(stats.PidsStats.Current), Timestamp: time.Now().UTC(),
	}, nil
}
