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
	"syscall"
	"time"
)

var _ provider.ContainerProvider = (*Provider)(nil)
var _ provider.InteractiveExecProvider = (*Provider)(nil)
var _ provider.VolumeManager = (*Provider)(nil)

// agentContainerUser is the user Apple crew containers are created to run their
// init process (and therefore the agent) as. Single-sourced here so the create
// path (EnsureCrewRuntime) and ContainerUser (used by keeper to run injected
// commands as the same user, #1060) can never drift apart.
const agentContainerUser = "1001:1001"

// ContainerUser returns the container's configured run-as user. Apple crew
// containers are created with a fixed --user, so this reports that value; it
// lets keeper run credential-injected commands as the agent user rather than a
// separate hardcoded constant at the call site (#1060).
func (p *Provider) ContainerUser(_ context.Context, _ string) (string, error) {
	return agentContainerUser, nil
}

// Config holds Apple Container provider configuration.
type Config struct {
	RuntimeImage    string
	Network         string
	OutputBasePath  string
	ContainerPrefix string

	// SidecarBinaryPath is the host path to crewship-sidecar, bind-mounted
	// read-only into crew containers at /usr/local/bin/crewship-sidecar. It is
	// how the in-container egress proxy that enforces `restricted` network
	// mode gets there at all; without it the mode is reported and not
	// enforced (#1648). Required — buildCreateArgs refuses to build a create
	// invocation without it.
	SidecarBinaryPath string

	// EntrypointPath is the host path to entrypoint.sh, bind-mounted
	// read-only at /usr/local/bin/entrypoint.sh and forced as the container's
	// entrypoint so a user-provided base image runs it instead of its own
	// CMD. Required, as above.
	EntrypointPath string

	// Admission holds a container start until the host can afford it (#1668).
	// nil disables admission control, which is the pre-#1668 behaviour.
	//
	// Note what this does and does not do on this provider's platform. Apple
	// Containers run on macOS, which publishes neither /proc/meminfo nor
	// /proc/pressure/memory, so the HOST MEMORY leg of the gate is inactive
	// there and says so on the status surface. The concurrency bound and the
	// stagger still apply: they need no kernel file, and each Apple container
	// is a full lightweight VM, which is if anything a stronger argument for
	// not starting twenty of them at once.
	Admission provider.AdmissionGate
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
	// mounts records each container's bind mounts (container path → host path)
	// so CopyToContainer can write through them; see hostPathFor.
	mountsMu sync.RWMutex
	mounts   map[string]map[string]string
}

type execEntry struct {
	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
}

// containerJSON is the structure returned by `container inspect`.
// Apple Container CLI uses nested "configuration" and "networks" array.
type containerJSON struct {
	// Both the lifecycle state and the networks live INSIDE status in the CLI
	// payload. Declaring `status` as a string and `networks` at the top level
	// meant inspect failed to decode at all — and where it was tolerated, the
	// gateway address it exists to read came back empty, so HostAddress was
	// never learned from a real container (#1779).
	Status struct {
		State    string `json:"state"` // "running", "stopped", "created"
		Networks []struct {
			IPv4Address string `json:"ipv4Address"` // e.g. "192.168.67.4/24"
			IPv4Gateway string `json:"ipv4Gateway"` // e.g. "192.168.67.1"
			Hostname    string `json:"hostname"`
		} `json:"networks"`
	} `json:"status"`
	Configuration struct {
		ID    string `json:"id"`
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
	} `json:"configuration"`
}

// State is the container's lifecycle state, or "" when the payload carried none.
func (c containerJSON) State() string { return c.Status.State }

// GatewayIP is the first network's IPv4 gateway — the address the host is
// reachable at from inside the container. Empty when unknown.
func (c containerJSON) GatewayIP() string {
	if len(c.Status.Networks) == 0 {
		return ""
	}
	return c.Status.Networks[0].IPv4Gateway
}

// containerListEntry is one item from `container list --all --format json`.
// The Apple Container CLI nests the container ID inside "configuration".
type containerListEntry struct {
	// Status is an OBJECT in the CLI payload — networks, startedDate and the
	// actual lifecycle under `state`. It was declared as a string, so decoding
	// the whole list failed with a type error and findContainer reported "not
	// found" for a container that was plainly running; EnsureCrewRuntime then
	// tried to create it again and got "container already exists" (#1779).
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Configuration struct {
		ID    string `json:"id"`
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
	} `json:"configuration"`
}

// State is the container's lifecycle state ("running", "stopped", …), or "" if
// the payload carried none.
func (e containerListEntry) State() string { return e.Status.State }

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

// parseImageListNames extracts every name the local image store knows.
//
// `container image list --format json` carries no top-level "reference": the
// name lives in the descriptor annotations and `id` is the digest. Decoding
// into {Reference string} read every entry as "", so no local image was ever
// recognised and ensureImage always fell through to a pull — invisible while
// the only image came from a registry, fatal once a crew ran its own
// provisioned image, which exists locally and nowhere else (#1779).
//
// Both the fully qualified name and its docker.io/library-stripped form are
// recorded, so `alpine:3.20` matches an entry annotated
// `docker.io/library/alpine:3.20`.
func parseImageListNames(raw []byte) (map[string]bool, error) {
	var images []struct {
		Configuration struct {
			Descriptor struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"descriptor"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(raw, &images); err != nil {
		return nil, fmt.Errorf("parse image list: %w", err)
	}

	names := make(map[string]bool, len(images)*2)
	for _, img := range images {
		for _, key := range []string{
			"com.apple.containerization.image.name",
			"io.containerd.image.name",
		} {
			n := strings.TrimSpace(img.Configuration.Descriptor.Annotations[key])
			if n == "" {
				continue
			}
			names[n] = true
			names[strings.TrimPrefix(n, "docker.io/library/")] = true
		}
	}
	return names, nil
}

// ensureImage makes ref available locally, pulling only when it is not already
// in the store. A crew's provisioned image is built on this host and is in no
// registry, so a pull is not a fallback for it — it is a guaranteed failure.
func (p *Provider) ensureImage(ctx context.Context, ref string) error {
	out, err := runCLI(ctx, "image", "list", "--format", "json")
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	names, err := parseImageListNames(out)
	if err != nil {
		return err
	}
	if names[ref] || names[strings.TrimPrefix(ref, "docker.io/library/")] {
		return nil
	}

	p.logger.Info("pulling agent runtime image", "image", ref)
	// Apple Container CLI uses --scheme auto which picks HTTP for localhost
	if _, err = runCLI(ctx, "image", "pull", ref); err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	p.logger.Info("agent runtime image pulled", "image", ref)
	return nil
}

// namePrefix returns the configured container-name prefix
// (Config.ContainerPrefix), falling back to the "crewship" default when
// unset.
func (p *Provider) namePrefix() string {
	if p.cfg.ContainerPrefix != "" {
		return p.cfg.ContainerPrefix
	}
	return "crewship"
}

// CrewContainerName returns the container name for a crew. It folds in the
// globally-unique crew id (not the per-workspace slug alone) so two tenants
// with an identically-named crew never collide on a shared host (audit C1).
func (p *Provider) CrewContainerName(id, slug string) string {
	parts := []string{p.namePrefix(), "team"}
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
		return fmt.Errorf("stop crew runtime %s: %w", provider.ShortID(containerID), err)
	}
	return nil
}

// RemoveCrewRuntime forcefully removes a crew container.
func (p *Provider) RemoveCrewRuntime(ctx context.Context, containerID string) error {
	_, err := runCLI(ctx, "delete", "--force", containerID)
	if err != nil {
		return fmt.Errorf("remove crew runtime %s: %w", provider.ShortID(containerID), err)
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
	switch strings.ToLower(info.State()) {
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

// defaultCLITimeout bounds a single `container` invocation.
//
// Apple's CLI can wedge: a `container create` was observed sitting at 6+
// minutes with an argument vector that completes in seconds by hand, and the
// image builder showed the same shape after finishing its work. runCLI passed
// the caller's context straight through, and most callers have no deadline —
// so one wedged call took the whole crew start with it, indefinitely.
//
// Generous rather than tight: creating a container from a multi-GB devcontainer
// image genuinely takes tens of seconds, and unpacking one for the first time
// can take minutes. The point is not to be strict, it is that there is a
// ceiling at all — an operator can act on "timed out after 5m", never on a
// spinner (#1779).
const defaultCLITimeout = 5 * time.Minute

func runCLI(ctx context.Context, args ...string) ([]byte, error) {
	return runCLIWithin(ctx, defaultCLITimeout, args...)
}

// runCLIWithin runs one `container` invocation under its own deadline.
//
// On timeout the whole process group is signalled, not just the CLI: Apple's
// CLI spawns helpers, and killing the parent alone leaves them holding their
// pipes — which is exactly how the build side stayed stuck after its own
// watchdog fired.
func runCLIWithin(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if timeout <= 0 {
		timeout = defaultCLITimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "container", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
			return syscall.Kill(-pgid, syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.Bytes(), fmt.Errorf("container %s: timed out after %s (stderr: %s)",
				strings.Join(args, " "), timeout, stderr.String())
		}
		return stdout.Bytes(), fmt.Errorf("container %s: %w (stderr: %s)", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// gcExecs periodically cleans up finished exec entries.

// crewImage picks the image a crew's container is created from:
// CachedImage > Image > the provider default.
//
// This provider used to run p.cfg.RuntimeImage unconditionally, which meant
// provisioning built a crew an image with its devcontainer features baked in
// and then never ran it — on macOS the mise toolchains, github-cli and python
// a crew asked for were all installed into an image nothing started (#1779).
// Same chain as the Docker provider (docker_container.go), so "which image is
// my crew running" has one answer per crew rather than one per provider.
func (p *Provider) crewImage(team provider.CrewConfig) string {
	if team.CachedImage != "" {
		return team.CachedImage
	}
	if team.Image != "" {
		return team.Image
	}
	return p.cfg.RuntimeImage
}
