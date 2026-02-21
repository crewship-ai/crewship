package docker

import (
	"context"
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
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var _ provider.ContainerProvider = (*Provider)(nil)

// Config holds Docker provider configuration for container creation and runtime selection.
type Config struct {
	RuntimeImage   string
	DefaultRuntime string // "runc" | "runsc" (gVisor) | "kata-runtime" | "sysbox-runc"
	Network        string
	OutputBasePath string
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
	_, _, err := p.client.ImageInspectWithRaw(ctx, ref)
	if err == nil {
		return nil
	}
	if !client.IsErrNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", ref, err)
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

// EnsureCrewRuntime creates or starts a Docker container for the given crew.
// It applies security isolation (non-root UID, cap-drop ALL, read-only rootfs)
// and resource limits (memory, CPU, PID). Returns the container ID.
func (p *Provider) EnsureCrewRuntime(ctx context.Context, team provider.CrewConfig) (string, error) {
	// Ensure network exists (auto-recreate if deleted at runtime)
	if p.cfg.Network != "" {
		if err := p.ensureNetwork(ctx, p.cfg.Network); err != nil {
			return "", fmt.Errorf("ensure network: %w", err)
		}
	}

	containerName := "crewship-team-" + team.Slug

	// Check if container already exists
	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+containerName {
				if c.State == "running" {
					return c.ID, nil
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

	if err := p.ensureImage(ctx, p.cfg.RuntimeImage); err != nil {
		return "", fmt.Errorf("ensure image: %w", err)
	}

	outputPath := filepath.Join(p.cfg.OutputBasePath, team.ID)
	if err := os.MkdirAll(outputPath, 0750); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	workspacePath := filepath.Join(p.cfg.OutputBasePath, "workspaces", team.ID)
	if err := os.MkdirAll(workspacePath, 0750); err != nil {
		return "", fmt.Errorf("create workspace dir: %w", err)
	}
	// Best-effort chown so container user (1001:1001) can write
	if err := os.Chown(workspacePath, 1001, 1001); err != nil {
		p.logger.Debug("chown workspace (non-fatal)", "path", workspacePath, "error", err)
	}

	pidsLimit := int64(200)
	resp, err := p.client.ContainerCreate(ctx,
		&container.Config{
			Image: p.cfg.RuntimeImage,
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
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: workspacePath, Target: "/workspace"},
				{Type: mount.TypeBind, Source: outputPath, Target: "/output"},
			},
			Tmpfs: map[string]string{
				"/tmp":        "rw,size=500m",
				"/home/agent": "rw,size=100m,uid=1001,gid=1001",
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

// Close releases the Docker API client connection.
func (p *Provider) Close() error {
	return p.client.Close()
}
