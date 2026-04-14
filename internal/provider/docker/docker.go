package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var _ provider.ContainerProvider = (*Provider)(nil)
var _ provider.InteractiveExecProvider = (*Provider)(nil)
var _ provider.VolumeManager = (*Provider)(nil)

// Config holds Docker provider configuration for container creation and runtime selection.
type Config struct {
	RuntimeImage    string
	DefaultRuntime  string // "runc" | "runsc" (gVisor) | "kata-runtime" | "sysbox-runc"
	Network         string
	OutputBasePath  string
	ContainerPrefix string // Container name prefix (e.g. "crewship" -> "crewship-team-{slug}"). Allows multi-instance isolation.
}

// DetectResult contains info about the detected container runtime.
type DetectResult struct {
	Runtime string // "docker" | "podman" | "colima" | "orbstack" | "rancher" | "nerdctl"
	Socket  string // socket path used
	Version string // server version string
}

// Provider implements provider.ContainerProvider using the Docker API.
// It auto-detects the container runtime (Docker, Podman, Colima, OrbStack, etc.)
// and manages crew containers with security isolation (non-root, cap-drop ALL).
type Provider struct {
	client   *client.Client
	cfg      Config
	logger   *slog.Logger
	detected DetectResult
}

// socketCandidate is a socket path + label for auto-detection.
type socketCandidate struct {
	path    string
	runtime string
}

// candidateSockets returns Docker-API-compatible sockets to try, in priority order.
// Covers Docker Desktop, Colima, OrbStack, Rancher Desktop, Podman (rootless/root), and nerdctl.
func candidateSockets() []socketCandidate {
	home, _ := os.UserHomeDir()
	uid := strconv.Itoa(os.Getuid())

	candidates := []socketCandidate{
		// Docker Desktop / Engine defaults
		{"/var/run/docker.sock", "docker"},
		// Colima (macOS)
		{filepath.Join(home, ".colima", "default", "docker.sock"), "colima"},
		// OrbStack (macOS)
		{filepath.Join(home, ".orbstack", "run", "docker.sock"), "orbstack"},
		// Rancher Desktop (macOS/Linux)
		{filepath.Join(home, ".rd", "docker.sock"), "rancher"},
		// Docker Desktop (macOS new path)
		{filepath.Join(home, ".docker", "run", "docker.sock"), "docker"},
		// Podman rootless
		{filepath.Join("/run/user", uid, "podman", "podman.sock"), "podman"},
		// Podman machine (macOS)
		{filepath.Join(home, ".local", "share", "containers", "podman", "machine", "podman.sock"), "podman"},
		// Podman root
		{"/run/podman/podman.sock", "podman"},
		// containerd/nerdctl
		{"/run/containerd/containerd.sock", "nerdctl"},
	}

	return candidates
}

// socketPingTimeout is the per-socket timeout for the Docker Ping call.
// Short enough that multiple failing sockets don't block the overall detection.
const socketPingTimeout = 1500 * time.Millisecond

// Detect probes for a Docker-API-compatible socket and returns info about
// the detected runtime. It checks DOCKER_HOST first, then iterates candidate
// sockets (Docker, Colima, OrbStack, Rancher, Podman, nerdctl). The ctx
// parameter is used as an outer deadline; each socket gets its own short timeout.
func Detect(ctx context.Context) (*DetectResult, error) {
	// If DOCKER_HOST is set, use that directly.
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("docker client (DOCKER_HOST=%s): %w", host, err)
		}
		defer cli.Close()
		info, err := cli.Ping(ctx)
		if err != nil {
			return nil, fmt.Errorf("docker ping (DOCKER_HOST=%s): %w", host, err)
		}
		rt := "docker"
		if strings.Contains(info.APIVersion, "libpod") {
			rt = "podman"
		}
		sv, _ := cli.ServerVersion(ctx)
		ver := sv.Version
		// Podman masquerades as Docker -- check server components
		for _, comp := range sv.Components {
			if strings.EqualFold(comp.Name, "Podman Engine") {
				rt = "podman"
				ver = comp.Version
			}
		}
		return &DetectResult{Runtime: rt, Socket: host, Version: ver}, nil
	}

	// Try candidate sockets in order, using a short per-socket timeout so a
	// hung daemon (socket file exists but daemon unresponsive) doesn't block
	// the entire detection for the full outer context deadline.
	for _, c := range candidateSockets() {
		if _, err := os.Stat(c.path); err != nil {
			continue
		}
		cli, err := client.NewClientWithOpts(
			client.WithHost("unix://"+c.path),
			client.WithAPIVersionNegotiation(),
		)
		if err != nil {
			continue
		}

		// Per-socket timeout: bail quickly if daemon is unresponsive.
		pingCtx, cancel := context.WithTimeout(ctx, socketPingTimeout)
		_, pingErr := cli.Ping(pingCtx)
		cancel()
		if pingErr != nil {
			cli.Close()
			continue
		}

		sv, _ := cli.ServerVersion(ctx)
		ver := sv.Version
		rt := c.runtime
		// Podman masquerades as Docker -- check server components
		for _, comp := range sv.Components {
			if strings.EqualFold(comp.Name, "Podman Engine") {
				rt = "podman"
				ver = comp.Version
			}
		}
		cli.Close()
		return &DetectResult{Runtime: rt, Socket: c.path, Version: ver}, nil
	}

	return nil, fmt.Errorf("no Docker-compatible runtime found (tried Docker, Podman, Colima, OrbStack, Rancher Desktop)")
}

// New creates a Provider by auto-detecting the container runtime and
// establishing a Docker API client connection. Returns an error if no
// compatible runtime is found.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}

	detected, detectErr := Detect(ctx)
	if detectErr != nil {
		return nil, fmt.Errorf("container runtime: %w", detectErr)
	}

	// Build client options based on detected socket.
	var opts []client.Opt
	if os.Getenv("DOCKER_HOST") != "" {
		opts = append(opts, client.FromEnv)
	} else {
		opts = append(opts, client.WithHost("unix://"+detected.Socket))
	}
	opts = append(opts, client.WithAPIVersionNegotiation())

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	if _, err := cli.Ping(ctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("docker ping: %w", err)
	}

	p := &Provider{client: cli, cfg: cfg, logger: logger, detected: *detected}

	logger.Info("container runtime detected",
		"runtime", detected.Runtime,
		"version", detected.Version,
		"socket", detected.Socket,
	)

	if cfg.Network != "" {
		if err := p.ensureNetwork(ctx, cfg.Network); err != nil {
			logger.Warn("failed to create docker network", "network", cfg.Network, "error", err)
		}
	}

	return p, nil
}

// Detected returns info about the detected container runtime.
func (p *Provider) Detected() DetectResult {
	return p.detected
}

// ensureNetwork creates the Docker bridge network if it doesn't already exist.
func (p *Provider) ensureNetwork(ctx context.Context, name string) error {
	networks, err := p.client.NetworkList(ctx, dockernetwork.ListOptions{})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range networks {
		if n.Name == name {
			return nil
		}
	}
	_, err = p.client.NetworkCreate(ctx, name, dockernetwork.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		return fmt.Errorf("create network: %w", err)
	}
	p.logger.Info("created docker network", "network", name)
	return nil
}

// ensureImage pulls the agent runtime image if it is not already present locally.
func (p *Provider) ensureImage(ctx context.Context, ref string) error {
	// List ALL local images (no filter) and check manually.
	// Docker Desktop can block indefinitely when using reference filters
	// or ImageInspect on remote registry references (ghcr.io/...).
	imgs, err := p.client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if tag == ref {
				return nil
			}
		}
	}
	p.logger.Info("pulling agent runtime image", "image", ref)
	reader, err := p.client.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)
	p.logger.Info("agent runtime image pulled", "image", ref)
	return nil
}

// CrewContainerName returns the container name for a crew based on its slug and the configured prefix.
func (p *Provider) CrewContainerName(slug string) string {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	return prefix + "-team-" + slug
}

// homeVolumeName returns the Docker named volume name for a crew's persistent home directory.
func (p *Provider) homeVolumeName(slug string) string {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	return prefix + "-home-" + slug
}

// toolsVolumeName returns the Docker named volume name for a crew's persistent tools directory.
func (p *Provider) toolsVolumeName(slug string) string {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	return prefix + "-tools-" + slug
}

// buildMounts returns the full list of mounts for a crew container, including
// persistent bind mounts and named volumes for home/tools directories.
func (p *Provider) buildMounts(slug, workspacePath, outputPath, crewPath, secretsPath string) []mount.Mount {
	mounts := []mount.Mount{
		{Type: mount.TypeBind, Source: workspacePath, Target: "/workspace"},
		{Type: mount.TypeBind, Source: outputPath, Target: "/output"},
		{Type: mount.TypeBind, Source: crewPath, Target: "/crew"},
		{Type: mount.TypeBind, Source: secretsPath, Target: "/secrets"},
	}
	if slug != "" {
		mounts = append(mounts,
			mount.Mount{Type: mount.TypeVolume, Source: p.homeVolumeName(slug), Target: "/home/agent"},
			mount.Mount{Type: mount.TypeVolume, Source: p.toolsVolumeName(slug), Target: "/opt/crew-tools"},
		)
	}
	return mounts
}

// ensureVolume creates a Docker named volume if it doesn't already exist.
func (p *Provider) ensureVolume(ctx context.Context, name string) error {
	_, err := p.client.VolumeCreate(ctx, volume.CreateOptions{
		Name: name,
		Labels: map[string]string{
			"managed-by": "crewship",
		},
	})
	if err != nil {
		return fmt.Errorf("volume create %s: %w", name, err)
	}
	return nil
}

// RemoveCrewVolumes removes persistent named volumes for a crew (home + tools).
func (p *Provider) RemoveCrewVolumes(ctx context.Context, slug string) error {
	for _, name := range []string{p.homeVolumeName(slug), p.toolsVolumeName(slug)} {
		if err := p.client.VolumeRemove(ctx, name, true); err != nil {
			p.logger.Warn("volume remove failed", "volume", name, "error", err)
		}
	}
	return nil
}

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
				Image:      p.cfg.RuntimeImage,
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
	resp, err := p.client.ContainerCreate(ctx,
		&container.Config{
			Image: runtimeImage,
			User:  "1001:1001",
			Env: []string{
				"CREWSHIP_CREW_ID=" + team.ID,
			},
			Healthcheck: &container.HealthConfig{
				Test:     []string{"CMD-SHELL", "test -f /workspace/.ready"},
				Interval: 30_000_000_000,
				Timeout:  5_000_000_000,
				Retries:  3,
			},
		},
		&container.HostConfig{
			Runtime:        runtime,
			ReadonlyRootfs: true,
			SecurityOpt:    []string{"no-new-privileges"},
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"NET_RAW"},
			// ExtraHosts makes host.docker.internal resolve to the Docker host
			// on both macOS and Linux, enabling containers to reach crewshipd
			// for assignment IPC calls via the sidecar.
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
			Resources: container.Resources{
				Memory:    int64(memoryMB) * 1024 * 1024,
				NanoCPUs:  int64(cpus * 1e9),
				PidsLimit: &pidsLimit,
			},
			Mounts: p.buildMounts(team.Slug, workspacePath, outputPath, crewPath, secretsPath),
			Tmpfs: map[string]string{
				"/tmp": "rw,size=500m",
			},
			NetworkMode: container.NetworkMode(p.cfg.Network),
		},
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

	return resp.ID, nil
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

// Exec runs a command inside a container via Docker exec. Returns a reader
// for the combined stdout/stderr stream.
func (p *Provider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	execCfg := container.ExecOptions{
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
		AttachStdout: true,
		AttachStderr: true,
		User:         cfg.User,
	}
	if execCfg.User == "" {
		execCfg.User = "1001:1001"
	}

	exec, err := p.client.ContainerExecCreate(ctx, cfg.ContainerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}

	resp, err := p.client.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		defer resp.Close()
		_, _ = stdcopy.StdCopy(pw, pw, resp.Reader)
	}()

	return &provider.ExecResult{
		ExecID: exec.ID,
		Reader: pr,
	}, nil
}

// ExecInspect checks if an exec process is still running and returns its exit code.
func (p *Provider) ExecInspect(ctx context.Context, execID string) (bool, int, error) {
	resp, err := p.client.ContainerExecInspect(ctx, execID)
	if err != nil {
		return false, 0, fmt.Errorf("exec inspect: %w", err)
	}
	return resp.Running, resp.ExitCode, nil
}

// ExecInteractive creates an interactive TTY exec session with bidirectional I/O.
// Unlike Exec(), this supports stdin and returns a raw connection for terminal use.
func (p *Provider) ExecInteractive(ctx context.Context, cfg provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	execCfg := container.ExecOptions{
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		User:         cfg.User,
	}
	if execCfg.User == "" {
		execCfg.User = "1001:1001"
	}

	exec, err := p.client.ContainerExecCreate(ctx, cfg.ContainerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec interactive create: %w", err)
	}

	resp, err := p.client.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, fmt.Errorf("exec interactive attach: %w", err)
	}

	// Set initial terminal size.
	if cfg.Rows > 0 && cfg.Cols > 0 {
		_ = p.client.ContainerExecResize(ctx, exec.ID, container.ResizeOptions{
			Height: uint(cfg.Rows),
			Width:  uint(cfg.Cols),
		})
	}

	return &provider.InteractiveExecResult{
		ExecID: exec.ID,
		Conn:   resp.Conn,
	}, nil
}

// ExecResize changes the terminal dimensions of a running interactive exec session.
func (p *Provider) ExecResize(ctx context.Context, execID string, rows, cols uint16) error {
	return p.client.ContainerExecResize(ctx, execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

// HostAddress returns the hostname that containers should use to reach the host.
// Docker injects "host.docker.internal" via ExtraHosts in container creation.
func (p *Provider) HostAddress() string {
	return "host.docker.internal"
}

// CopyToContainer copies a tar archive into the container filesystem at dstPath.
func (p *Provider) CopyToContainer(ctx context.Context, containerID string, dstPath string, content io.Reader) error {
	return p.client.CopyToContainer(ctx, containerID, dstPath, content, container.CopyToContainerOptions{})
}

// Close releases the Docker API client connection.
func (p *Provider) Close() error {
	return p.client.Close()
}
