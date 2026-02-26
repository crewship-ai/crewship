package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

var _ provider.ContainerProvider = (*Provider)(nil)

// Config holds Apple Container provider configuration.
type Config struct {
	RuntimeImage    string
	Network         string
	OutputBasePath  string
	ContainerPrefix string
}

// Provider implements provider.ContainerProvider using the Apple Container CLI.
// Apple Containers run each container as a lightweight VM on macOS (Tahoe+).
// Since there is no Go SDK, all operations shell out to the `container` CLI.
type Provider struct {
	cfg    Config
	logger *slog.Logger
	hostIP string

	mu       sync.RWMutex
	execSeq  atomic.Int64
	execs    map[string]*execEntry
	done     chan struct{}
}

type execEntry struct {
	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
}

// containerJSON is the structure returned by `container inspect`.
// Apple Container CLI uses nested "configuration" and "networks" array.
type containerJSON struct {
	Status        string `json:"status"` // "running", "stopped", "created"
	Configuration struct {
		ID    string `json:"id"`
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
	} `json:"configuration"`
	Networks []struct {
		IPv4Address string `json:"ipv4Address"` // e.g. "192.168.67.4/24"
		IPv4Gateway string `json:"ipv4Gateway"` // e.g. "192.168.67.1"
		Hostname    string `json:"hostname"`
	} `json:"networks"`
}

// containerListEntry is one item from `container list --all --format json`.
// The Apple Container CLI nests the container ID inside "configuration".
type containerListEntry struct {
	Status        string `json:"status"`
	Configuration struct {
		ID    string `json:"id"`
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
	} `json:"configuration"`
}

// New creates an Apple Container Provider. It verifies the `container` CLI
// is available and the system service is running.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}

	detected, err := Detect(ctx)
	if err != nil {
		return nil, fmt.Errorf("apple container runtime: %w", err)
	}

	p := &Provider{
		cfg:    cfg,
		logger: logger,
		hostIP: detected.HostIP,
		execs:  make(map[string]*execEntry),
		done:   make(chan struct{}),
	}

	logger.Info("apple container runtime detected",
		"version", detected.Version,
		"host_ip", detected.HostIP,
	)

	if cfg.Network != "" {
		if err := p.ensureNetwork(ctx, cfg.Network); err != nil {
			logger.Warn("failed to create apple container network", "network", cfg.Network, "error", err)
		}
	}

	go p.gcExecs()

	return p, nil
}

// HostAddress returns the IP address that containers should use to reach the host.
// Apple Containers run in dedicated VMs so they need the host's actual IP.
func (p *Provider) HostAddress() string {
	return p.hostIP
}

func (p *Provider) ensureNetwork(ctx context.Context, name string) error {
	out, err := runCLI(ctx, "network", "list", "--format", "json")
	if err != nil {
		// Network commands require macOS 26+; non-fatal if unavailable
		p.logger.Debug("network list not available", "error", err)
		return nil
	}

	var networks []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &networks); err != nil {
		return nil
	}
	for _, n := range networks {
		if n.Name == name {
			return nil
		}
	}

	_, err = runCLI(ctx, "network", "create", name)
	if err != nil {
		// Ignore "already exists" errors (race or stale list cache)
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("create network %s: %w", name, err)
	}
	p.logger.Info("created apple container network", "network", name)
	return nil
}

func (p *Provider) ensureImage(ctx context.Context, ref string) error {
	out, err := runCLI(ctx, "image", "list", "--format", "json")
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}

	var images []struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(out, &images); err != nil {
		return fmt.Errorf("parse image list: %w", err)
	}
	for _, img := range images {
		if img.Reference == ref {
			return nil
		}
		// Match without docker.io/library/ prefix normalization
		if strings.TrimPrefix(img.Reference, "docker.io/library/") == ref {
			return nil
		}
	}

	p.logger.Info("pulling agent runtime image", "image", ref)
	// Apple Container CLI uses --scheme auto which picks HTTP for localhost
	_, err = runCLI(ctx, "image", "pull", ref)
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	p.logger.Info("agent runtime image pulled", "image", ref)
	return nil
}

// EnsureCrewRuntime creates or starts an Apple Container for the given crew.
func (p *Provider) EnsureCrewRuntime(ctx context.Context, team provider.CrewConfig) (string, error) {
	p.logger.Debug("EnsureCrewRuntime", "crew_id", team.ID, "crew_slug", team.Slug)

	if p.cfg.Network != "" {
		if err := p.ensureNetwork(ctx, p.cfg.Network); err != nil {
			return "", fmt.Errorf("ensure network: %w", err)
		}
	}

	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	containerName := prefix + "-team-" + team.Slug

	// Check if container already exists
	existing, err := p.findContainer(ctx, containerName)
	if err == nil && existing != nil {
		if existing.Status == "running" {
			return existing.Configuration.ID, nil
		}
		// Start stopped container
		if _, err := runCLI(ctx, "start", existing.Configuration.ID); err != nil {
			return "", fmt.Errorf("start existing container: %w", err)
		}
		return existing.Configuration.ID, nil
	}

	// Set up resources
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

	// Create host directories for bind mounts
	outputPath := filepath.Join(p.cfg.OutputBasePath, team.ID)
	if err := os.MkdirAll(outputPath, 0750); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	workspacePath := filepath.Join(p.cfg.OutputBasePath, "workspaces", team.ID)
	if err := os.MkdirAll(workspacePath, 0750); err != nil {
		return "", fmt.Errorf("create workspace dir: %w", err)
	}
	if err := os.Chown(workspacePath, 1001, 1001); err != nil {
		p.logger.Debug("chown workspace (non-fatal)", "path", workspacePath, "error", err)
	}

	crewPath := filepath.Join(p.cfg.OutputBasePath, "crews", team.ID)
	for _, sub := range []string{"shared", "agents"} {
		if err := os.MkdirAll(filepath.Join(crewPath, sub), 0750); err != nil {
			return "", fmt.Errorf("create crew dir %s: %w", sub, err)
		}
	}
	for _, dir := range []string{crewPath, filepath.Join(crewPath, "shared"), filepath.Join(crewPath, "agents")} {
		if err := os.Chown(dir, 1001, 1001); err != nil {
			p.logger.Debug("chown crew dir (non-fatal)", "path", dir, "error", err)
		}
	}

	// Build create command
	// Apple Container CLI requires integer CPUs (no fractional)
	cpuInt := int(cpus)
	if cpuInt < 1 {
		cpuInt = 1
	}
	args := []string{
		"create",
		"--name", containerName,
		"--cpus", fmt.Sprintf("%d", cpuInt),
		"--memory", fmt.Sprintf("%dM", memoryMB),
		"--read-only",
		"--env", "CREWSHIP_CREW_ID=" + team.ID,
		"-v", workspacePath + ":/workspace",
		"-v", outputPath + ":/output",
		"-v", crewPath + ":/crew",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home/agent",
	}

	if p.cfg.Network != "" {
		args = append(args, "--network", p.cfg.Network)
	}

	// Apple Containers use --user for the init process user
	args = append(args, "--user", "1001:1001")

	// Image + entrypoint: keep container alive
	args = append(args, p.cfg.RuntimeImage, "sleep", "infinity")

	out, err := runCLI(ctx, args...)
	if err != nil {
		// Handle race condition: another goroutine created the container concurrently
		if strings.Contains(err.Error(), "already exists") {
			existing, findErr := p.findContainer(ctx, containerName)
			if findErr == nil && existing != nil {
				if existing.Status != "running" {
					if _, startErr := runCLI(ctx, "start", existing.Configuration.ID); startErr != nil {
						return "", fmt.Errorf("start existing container after race: %w", startErr)
					}
				}
				return existing.Configuration.ID, nil
			}
		}
		return "", fmt.Errorf("container create: %w (output: %s)", err, string(out))
	}

	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		containerID = containerName
	}

	// Start the container
	if _, err := runCLI(ctx, "start", containerID); err != nil {
		return "", fmt.Errorf("container start: %w", err)
	}

	// Get actual container ID from inspect
	info, err := p.inspectContainer(ctx, containerID)
	if err == nil && info.Configuration.ID != "" {
		containerID = info.Configuration.ID
		// Discover host IP from gateway if not already known
		if p.hostIP == "" && len(info.Networks) > 0 && info.Networks[0].IPv4Gateway != "" {
			p.hostIP = info.Networks[0].IPv4Gateway
		}
	}

	p.logger.Info("crew container started",
		"crew_id", team.ID,
		"container_id", shortID(containerID),
	)

	return containerID, nil
}

func (p *Provider) findContainer(ctx context.Context, name string) (*containerListEntry, error) {
	out, err := runCLI(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}

	var containers []containerListEntry
	if err := json.Unmarshal(out, &containers); err != nil {
		return nil, fmt.Errorf("parse container list: %w", err)
	}

	for _, c := range containers {
		if c.Configuration.ID == name {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("container %q not found", name)
}

func (p *Provider) inspectContainer(ctx context.Context, id string) (*containerJSON, error) {
	out, err := runCLI(ctx, "inspect", id)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", id, err)
	}

	// inspect may return an array or a single object
	var containers []containerJSON
	if err := json.Unmarshal(out, &containers); err != nil {
		var single containerJSON
		if err2 := json.Unmarshal(out, &single); err2 != nil {
			return nil, fmt.Errorf("parse inspect output: %w", err)
		}
		return &single, nil
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("empty inspect result for %s", id)
	}
	return &containers[0], nil
}

// StopCrewRuntime gracefully stops a crew container.
func (p *Provider) StopCrewRuntime(ctx context.Context, containerID string) error {
	_, err := runCLI(ctx, "stop", "--time", "30", containerID)
	if err != nil {
		return fmt.Errorf("stop crew runtime %s: %w", shortID(containerID), err)
	}
	return nil
}

// RemoveCrewRuntime forcefully removes a crew container.
func (p *Provider) RemoveCrewRuntime(ctx context.Context, containerID string) error {
	_, err := runCLI(ctx, "delete", "--force", containerID)
	if err != nil {
		return fmt.Errorf("remove crew runtime %s: %w", shortID(containerID), err)
	}
	return nil
}

// ContainerStatus inspects a container and returns its current state.
func (p *Provider) ContainerStatus(ctx context.Context, containerID string) (*provider.ContainerStatus, error) {
	info, err := p.inspectContainer(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("container inspect: %w", err)
	}

	state := "stopped"
	switch strings.ToLower(info.Status) {
	case "running":
		state = "running"
	case "created", "starting":
		state = "creating"
	case "stopped", "exited":
		state = "stopped"
	default:
		state = "error"
	}

	return &provider.ContainerStatus{
		ID:    containerID,
		State: state,
	}, nil
}

// Exec runs a command inside a container via the Apple Container CLI exec.
// It returns a reader for stdout/stderr and tracks the exec process for ExecInspect.
func (p *Provider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	args := []string{"exec"}

	for _, env := range cfg.Env {
		args = append(args, "--env", env)
	}
	if cfg.WorkingDir != "" {
		args = append(args, "--workdir", cfg.WorkingDir)
	}
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}

	args = append(args, cfg.ContainerID)
	args = append(args, cfg.Cmd...)

	cmd := exec.CommandContext(ctx, "container", args...)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, fmt.Errorf("exec start: %w", err)
	}

	execID := fmt.Sprintf("apple-exec-%d", p.execSeq.Add(1))

	entry := &execEntry{
		cmd:  cmd,
		done: make(chan struct{}),
	}

	p.mu.Lock()
	p.execs[execID] = entry
	p.mu.Unlock()

	go func() {
		defer pw.Close()
		err := cmd.Wait()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				entry.exitCode = exitErr.ExitCode()
			} else {
				entry.exitCode = -1
			}
		}
		close(entry.done)
	}()

	return &provider.ExecResult{
		ExecID: execID,
		Reader: pr,
	}, nil
}

// ExecInspect checks if an exec process is still running and returns its exit code.
func (p *Provider) ExecInspect(_ context.Context, execID string) (bool, int, error) {
	p.mu.RLock()
	entry, ok := p.execs[execID]
	p.mu.RUnlock()

	if !ok {
		return false, -1, fmt.Errorf("exec %s not found", execID)
	}

	select {
	case <-entry.done:
		return false, entry.exitCode, nil
	default:
		return true, 0, nil
	}
}

// Close stops the background gc goroutine and releases resources.
func (p *Provider) Close() error {
	close(p.done)
	return nil
}

// runCLI executes `container <args...>` and returns stdout bytes.
func runCLI(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "container", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("container %s: %w (stderr: %s)", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// gcExecs periodically cleans up finished exec entries.
func (p *Provider) gcExecs() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.mu.Lock()
			for id, entry := range p.execs {
				select {
				case <-entry.done:
					delete(p.execs, id)
				default:
				}
			}
			p.mu.Unlock()
		}
	}
}
