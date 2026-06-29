package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/provider"
)

var _ provider.ContainerProvider = (*Provider)(nil)
var _ provider.InteractiveExecProvider = (*Provider)(nil)
var _ provider.VolumeManager = (*Provider)(nil)

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

	mu      sync.RWMutex
	execSeq atomic.Int64
	execs   map[string]*execEntry
	done    chan struct{}
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
	p.mu.RLock()
	defer p.mu.RUnlock()
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

// CrewContainerName returns the container name for a crew. It folds in the
// globally-unique crew id (not the per-workspace slug alone) so two tenants
// with an identically-named crew never collide on a shared host (audit C1).
func (p *Provider) CrewContainerName(id, slug string) string {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	parts := []string{prefix, "team"}
	if slug != "" {
		parts = append(parts, slug)
	}
	if id != "" {
		parts = append(parts, id)
	}
	return strings.Join(parts, "-")
}

// EnsureCrewRuntime creates or starts an Apple Container for the given crew.

func (p *Provider) findContainer(ctx context.Context, name string) (*containerListEntry, error) {
	out, err := runCLI(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}

	var containers []containerListEntry
	if err := json.Unmarshal(out, &containers); err != nil {
		return nil, fmt.Errorf("parse container list: %w", err)
	}

	// In Apple Containers, configuration.id IS the container name (set via --name on create).
	// There is no separate "name" field in the CLI output.
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
	_, err := runCLI(ctx, "stop", "--time", "10", containerID)
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

	var state string
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

// ContainerStats is not supported on Apple Containers and always returns an error.

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
