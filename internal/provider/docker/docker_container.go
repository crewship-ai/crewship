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
	"github.com/docker/docker/api/types/volume"
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

// migrateLegacyCrewResources auto-migrates pre-C1 (slug-only) crew Docker
// resources to the id-scoped C1 naming so provisioning survives an upgrade
// across the C1 boundary instead of wedging and forcing a destructive
// nuke+reseed.
//
// C1 (2026-06 audit) re-keyed crew resources from "<prefix>-{team,home,tools}-<slug>"
// to also include the globally-unique crew id
// ("<prefix>-{team,home,tools}-<slug>-<id>"). The dangerous state is a surviving
// legacy resource sitting next to the freshly-named id-scoped one: a stale
// legacy container would dual-mount the crew's bind mounts, and stranded
// home/tools volumes would orphan the agent's persistent home (~/.ssh, tooling).
// On the first provision after upgrade this function reconciles both:
//
//   - Legacy container "<prefix>-team-<slug>": stopped (short timeout) and
//     force-removed. It is an ephemeral runtime — its persistent state lives in
//     the home/tools volumes, not the container — so removing it is safe and
//     unblocks provisioning.
//   - Legacy volumes "<prefix>-{home,tools}-<slug>": their *data* is copied into
//     the new id-scoped volume via a short-lived helper container before the
//     legacy volume is pruned. The copy is fail-safe — the legacy volume is
//     never removed unless its data was successfully copied. If the target
//     id-scoped volume already exists we do NOT clobber it: the legacy volume is
//     left in place as a (warned) orphan the operator can prune manually.
//
// No-op on a fresh post-C1 daemon. If the daemon can't enumerate volumes the
// function fails closed (returns an error) rather than proceeding: skipping the
// check would let EnsureCrewRuntime create empty id-scoped volumes that strand an
// unmigrated legacy home behind an authoritative-looking target.
func (p *Provider) migrateLegacyCrewResources(ctx context.Context, id, slug, image string) error {
	if slug == "" {
		return nil
	}

	// Serialize by *legacy slug*, not crew id: the legacy resources reconciled
	// below are slug-scoped, so two crews sharing a slug (distinct ids) would
	// otherwise both observe the same legacy volumes and race to copy/claim the
	// same ambiguous data despite the "first crew to provision claims it" policy.
	// The caller already holds the id-scoped crew lock; this nested slug lock is
	// deadlock-free because crew-id locks are independent and acquired first.
	mu := p.lockForMigration(slug)
	mu.Lock()
	defer mu.Unlock()

	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	legacyContainer := prefix + "-team-" + slug

	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers (legacy C1 migration): %w", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+legacyContainer {
				p.logger.Info("removing legacy slug-scoped crew container (C1 migration)",
					"container", legacyContainer, "container_id", shortID(c.ID))
				timeout := 10
				_ = p.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
				if rmErr := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); rmErr != nil {
					return fmt.Errorf("remove legacy slug-scoped container %q (C1 migration): %w", legacyContainer, rmErr)
				}
			}
		}
	}

	// Fail closed if volumes can't be enumerated. We can't tell whether a legacy
	// home/tools volume is sitting here unmigrated, and EnsureCrewRuntime would
	// go on to create fresh empty id-scoped volumes — stranding the agent's
	// persistent home (~/.ssh, tooling) behind an authoritative-looking empty
	// target that future provisions won't re-migrate. Blocking provisioning until
	// the daemon can list volumes again is the data-preserving choice; a transient
	// list failure resolves on the next attempt.
	list, err := p.client.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return fmt.Errorf("list volumes (legacy C1 migration): %w; "+
			"legacy data was NOT removed and provisioning is paused so no empty id-scoped "+
			"volumes strand it — retry once the daemon can enumerate volumes again", err)
	}
	existing := make(map[string]bool, len(list.Volumes))
	for _, vol := range list.Volumes {
		if vol != nil {
			existing[vol.Name] = true
		}
	}

	for _, role := range []string{"home", "tools"} {
		legacy := prefix + "-" + role + "-" + slug
		target := p.crewResourceName(role, id, slug)
		if !existing[legacy] {
			continue // already migrated / never existed
		}
		if existing[target] {
			// Both names exist — do not clobber the id-scoped data. Leave the
			// legacy volume in place; an operator can prune it once they confirm
			// it is stale.
			p.logger.Warn("legacy slug-scoped volume orphaned (C1 migration): target id-scoped volume already exists, leaving legacy in place — operator may prune it",
				"legacy_volume", legacy, "target_volume", target)
			continue
		}
		if err := p.migrateLegacyVolume(ctx, legacy, target, image); err != nil {
			return err
		}
	}
	return nil
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

// migrateLegacyVolume copies all data from a legacy slug-scoped volume into a
// fresh id-scoped target volume, then prunes the legacy volume. It is fail-safe:
// the legacy volume is removed ONLY after the copy completes with exit 0. Any
// failure (empty image, VolumeCreate, helper create/start/wait, non-zero copy
// exit) returns an actionable error and leaves the legacy volume untouched.
// Data loss is the cardinal sin here.
func (p *Provider) migrateLegacyVolume(ctx context.Context, legacy, target, image string) error {
	failSafe := func(stage string, cause error) error {
		return fmt.Errorf("C1 migration of legacy slug-scoped volume %q into %q failed at %s: %w; "+
			"the legacy volume was NOT removed — its data is intact. Migrate it manually or prune it once confirmed stale (dev: nuke + reseed)",
			legacy, target, stage, cause)
	}

	if image == "" {
		return fmt.Errorf("cannot migrate legacy slug-scoped volume %q into %q: no runtime image resolved to run the copy helper; "+
			"the legacy volume was NOT removed — its data is intact. Migrate it manually or prune it once confirmed stale (dev: nuke + reseed)",
			legacy, target)
	}

	// copySucceeded gates the target-volume rollback defer below. The legacy
	// volume is removed ONLY after a verified-good copy, so if any step between
	// here and that point fails we must also remove the half-written target —
	// otherwise the next provision sees an existing target (line ~"existing[target]"
	// orphan check), treats the partial copy as authoritative, and skips migration,
	// silently stranding the legacy data behind a corrupt half-copy.
	copySucceeded := false
	if _, err := p.client.VolumeCreate(ctx, volume.CreateOptions{
		Name:   target,
		Labels: map[string]string{"managed-by": "crewship"},
	}); err != nil {
		return failSafe("create target volume", err)
	}
	defer func() {
		if copySucceeded {
			return
		}
		// Roll back the partially-written target on any failure. Use a fresh
		// context: ctx may already be cancelled (e.g. wait timeout) and we still
		// must clean up. Best-effort — a failure here only leaves a benign empty
		// volume the orphan check will later flag, and the legacy data is intact.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.client.VolumeRemove(cleanupCtx, target, true); err != nil {
			p.logger.Warn("C1 migration failed and could not remove the incomplete target volume — operator should prune it before the next provision",
				"legacy_volume", legacy, "target_volume", target, "error", err)
		}
	}()

	// Short-lived helper: cp -a /from/. /to/ with the legacy volume mounted
	// read-only at /from and the target at /to. No network and an explicit
	// root user with only the three capabilities cp -a needs to faithfully
	// reproduce the legacy home: CHOWN (restore 1001:1001 ownership on the
	// fresh root-owned target volume), DAC_OVERRIDE (read 0700 dirs like
	// ~/.ssh and write the target regardless of mode) and FOWNER (preserve
	// permission bits on files it doesn't own). Without an explicit user the
	// helper would inherit the image's UID 1001 and fail to write the
	// root-owned target; without these caps cp -a under CapDrop ALL silently
	// drops ownership, landing the agent's home as root and breaking the agent.
	helperCfg := &container.Config{
		Image:      image,
		User:       "0:0",
		Entrypoint: []string{"sh", "-c", "cp -a /from/. /to/ 2>/dev/null"},
	}
	helperHost := &container.HostConfig{
		NetworkMode: "none",
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"},
		Mounts: []mount.Mount{
			{Type: mount.TypeVolume, Source: legacy, Target: "/from", ReadOnly: true},
			{Type: mount.TypeVolume, Source: target, Target: "/to"},
		},
	}
	created, err := p.client.ContainerCreate(ctx, helperCfg, helperHost, nil, nil, "")
	if err != nil {
		return failSafe("create copy helper", err)
	}
	// Always clean up the helper, success or failure.
	defer func() { _ = p.client.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true}) }()

	if err := p.client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return failSafe("start copy helper", err)
	}

	// ContainerWait returns two channels; the copy finishes quickly, so a short
	// timeout protects against a wedged daemon. We picked ContainerWait over
	// polling ContainerInspect because the SDK decodes a clean exit code from
	// /containers/{id}/wait, which is also straightforward to fake in tests.
	waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second)
	defer waitCancel()
	statusCh, errCh := p.client.ContainerWait(waitCtx, created.ID, container.WaitConditionNotRunning)
	select {
	case status, ok := <-statusCh:
		// ContainerWait closes statusCh (zero-value status, ok == false) on a
		// wait failure while delivering the real cause on errCh. select can pick
		// this ready-but-closed case first, so a closed channel must be treated
		// as failure — otherwise a wait error masquerades as StatusCode 0 and we
		// would go on to remove the legacy volume after a copy that never ran.
		if !ok {
			return failSafe("wait for copy helper", fmt.Errorf("wait channel closed before a status was delivered"))
		}
		if status.StatusCode != 0 {
			return failSafe("copy helper exit", fmt.Errorf("helper exited with status %d", status.StatusCode))
		}
	case werr := <-errCh:
		return failSafe("wait for copy helper", werr)
	case <-waitCtx.Done():
		return failSafe("wait for copy helper", waitCtx.Err())
	}
	// The copy completed with exit 0: the target volume is authoritative now, so
	// suppress the rollback defer before we prune the legacy source.
	copySucceeded = true

	if err := p.client.VolumeRemove(ctx, legacy, true); err != nil {
		// The data was copied successfully; failing to prune the legacy volume
		// is non-fatal (it becomes a benign orphan). Warn and continue.
		p.logger.Warn("C1 migration copied volume data but failed to prune legacy volume — operator may prune it manually",
			"legacy_volume", legacy, "target_volume", target, "error", err)
		return nil
	}
	p.logger.Info("migrated legacy slug-scoped crew volume (C1 migration)",
		"legacy_volume", legacy, "target_volume", target,
		"note", "if this crew's slug was shared across workspaces before C1, the migrated data may be ambiguous — the first crew to provision claims it")
	return nil
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
	//
	// Resolved BEFORE the C1 migration below so the migration's copy-helper has
	// a concrete image to run.
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

	// C1 migration (audit 2026-06): reconcile any surviving pre-C1, slug-only
	// resources for this crew before provisioning the id-scoped runtime.
	// Silently creating fresh "<slug>-<id>" names alongside a legacy
	// "crewship-team-<slug>" container would point two runtimes at the same crew
	// bind mounts, and fresh empty home/tools volumes would strand the agent's
	// persistent home. Rather than fail and force a destructive nuke+reseed, this
	// drops the (ephemeral) legacy container and copies legacy volume data into
	// the id-scoped volumes — hard-failing only, fail-safe, when a copy can't
	// complete (the legacy volume is never removed unless its data was copied).
	if err := p.migrateLegacyCrewResources(ctx, team.ID, team.Slug, desiredImage); err != nil {
		return "", err
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
