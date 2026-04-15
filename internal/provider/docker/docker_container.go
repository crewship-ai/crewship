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
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
)

// EnsureCrewRuntime creates or starts a Docker container for the given crew.
// It applies security isolation (non-root UID, cap-drop ALL, read-only rootfs)
// and resource limits (memory, CPU, PID). Returns the container ID.
func (p *Provider) EnsureCrewRuntime(ctx context.Context, team provider.CrewConfig) (string, error) {
	p.logger.Debug("EnsureCrewRuntime", "crew_id", team.ID, "crew_slug", team.Slug)
	// Ensure network exists (auto-recreate if deleted at runtime)
	if p.cfg.Network != "" {
		p.logger.Debug("ensuring network", "network", p.cfg.Network)
		if err := p.ensureNetwork(ctx, p.cfg.Network); err != nil {
			return "", fmt.Errorf("ensure network: %w", err)
		}
	}

	containerName := p.CrewContainerName(team.Slug)

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

	memoryMB := team.MemoryMB
	if memoryMB == 0 {
		memoryMB = 512
	}
	cpus := team.CPUs
	if cpus == 0 {
		cpus = 1.0
	}

	// Image selection chain: CachedImage > Image > default RuntimeImage
	runtimeImage := p.cfg.RuntimeImage
	if team.Image != "" {
		runtimeImage = team.Image
	}
	if team.CachedImage != "" {
		runtimeImage = team.CachedImage
	}

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
		if err := p.ensureVolume(ctx, p.homeVolumeName(team.Slug)); err != nil {
			return "", err
		}
		if err := p.ensureVolume(ctx, p.toolsVolumeName(team.Slug)); err != nil {
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
		chownCmd := "chown -R 1001:1001"
		for _, dir := range allDirs {
			chownCmd += " /mnt" + dir
		}
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
			p.client.ContainerWait(ctx, initResp.ID, container.WaitConditionNotRunning)
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
	crewMounts, err := p.buildMounts(team.Slug, workspacePath, outputPath, crewPath, secretsPath)
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
	capAdd := []string{"NET_RAW"}
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
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		execCfg := container.ExecOptions{
			Cmd:          []string{"/usr/local/bin/crewship-sidecar", "--version"},
			User:         "0:0",
			AttachStdout: true,
			AttachStderr: true,
		}
		ex, execErr := p.client.ContainerExecCreate(checkCtx, resp.ID, execCfg)
		if execErr == nil {
			if startErr := p.client.ContainerExecStart(checkCtx, ex.ID, container.ExecStartOptions{}); startErr == nil {
				// Poll exit code briefly.
				for i := 0; i < 20; i++ {
					inspect, ierr := p.client.ContainerExecInspect(checkCtx, ex.ID)
					if ierr != nil {
						break
					}
					if !inspect.Running {
						if inspect.ExitCode != 0 {
							p.logger.Error("sidecar binary incompatible with container libc — " +
								"host-built sidecar expects glibc; Alpine/musl bases must use a glibc-compatible image")
							return "", fmt.Errorf("sidecar sanity check failed (exit %d) — custom base image %q is likely musl-based or missing glibc symbols; use a glibc base (debian, ubuntu, mcr devcontainers)", inspect.ExitCode, team.Image)
						}
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}
	}

	// Run postStartCommand hooks (feature-level + root-level, pre-merged by
	// the bridge resolver). Non-fatal — failures log warnings but do not
	// block the container from coming up.
	p.runPostStartCommands(ctx, resp.ID, team.PostStartCommands)

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
