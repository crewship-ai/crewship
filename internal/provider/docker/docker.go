package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var _ provider.ContainerProvider = (*Provider)(nil)

type Config struct {
	RuntimeImage   string
	DefaultRuntime string // "runc" | "runsc"
	Network        string
	OutputBasePath string
}

type Provider struct {
	client *client.Client
	cfg    Config
	logger *slog.Logger
}

func New(cfg Config, logger *slog.Logger) (*Provider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	if _, err := cli.Ping(context.Background()); err != nil {
		cli.Close()
		return nil, fmt.Errorf("docker ping: %w", err)
	}

	if logger == nil {
		logger = slog.Default()
	}

	p := &Provider{client: cli, cfg: cfg, logger: logger}

	if cfg.Network != "" {
		if err := p.ensureNetwork(context.Background(), cfg.Network); err != nil {
			logger.Warn("failed to create docker network", "network", cfg.Network, "error", err)
		}
	}

	return p, nil
}

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
		Driver:   "bridge",
		Internal: true,
	})
	if err != nil {
		return fmt.Errorf("create network: %w", err)
	}
	p.logger.Info("created docker network", "network", name, "internal", true)
	return nil
}

func (p *Provider) ensureImage(ctx context.Context, ref string) error {
	_, _, err := p.client.ImageInspectWithRaw(ctx, ref)
	if err == nil {
		return nil
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

func (p *Provider) EnsureCrewRuntime(ctx context.Context, team provider.CrewConfig) (string, error) {
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
			Resources: container.Resources{
				Memory:    int64(memoryMB) * 1024 * 1024,
				NanoCPUs:  int64(cpus * 1e9),
				PidsLimit: &pidsLimit,
			},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: "workspace-" + team.ID, Target: "/workspace"},
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

func (p *Provider) StopCrewRuntime(ctx context.Context, containerID string) error {
	timeout := 30
	return p.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

func (p *Provider) RemoveCrewRuntime(ctx context.Context, containerID string) error {
	return p.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

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
		_, _ = stdcopy.StdCopy(pw, pw, resp.Reader)
	}()

	return &provider.ExecResult{
		ExecID: exec.ID,
		Reader: pr,
	}, nil
}

func (p *Provider) ExecInspect(ctx context.Context, execID string) (bool, int, error) {
	resp, err := p.client.ContainerExecInspect(ctx, execID)
	if err != nil {
		return false, 0, fmt.Errorf("exec inspect: %w", err)
	}
	return resp.Running, resp.ExitCode, nil
}

func (p *Provider) Close() error {
	return p.client.Close()
}
