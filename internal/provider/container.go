package provider

import (
	"context"
	"io"
	"time"
)

// CrewConfig describes the resource requirements and network policy for a
// crew's container runtime.
type CrewConfig struct {
	ID       string
	Slug     string
	MemoryMB int
	CPUs     float64
	// Passed through for orchestrator/sidecar layer; not consumed by providers directly yet.
	NetworkMode    string   // "free" (default) or "restricted"
	AllowedDomains []string // domains allowed when NetworkMode is "restricted"
	TTLHours       int      // auto-stop after idle period; 0 = no TTL
	Image          string   // custom runtime image; empty = provider default
	CachedImage    string   // provisioned Docker image tag; empty = use Image or default
	// ContainerEnv is extra env vars from devcontainer.json containerEnv.
	// CREWSHIP_* keys are reserved for platform-managed vars and silently
	// skipped. Providers merge these into the container's Env at create time.
	ContainerEnv map[string]string

	// Runtime requirements bubbled up from devcontainer features. Applied to
	// the HostConfig at create time. Critical for features like DinD which
	// need Privileged + a docker.sock bind mount.
	Privileged  bool
	Init        bool
	CapAdd      []string
	SecurityOpt []string
	ExtraMounts []CrewMount

	// PostStartCommands are shell commands that run in the container on every
	// start / restart, not only first create. Concatenation of feature-level
	// postStartCommand hooks (install-order) followed by the root-level
	// devcontainer.json postStartCommand. Providers that run these must
	// execute as UID 1001:1001 (the agent user) with stdout/stderr captured
	// for debugging. A failing post-start command logs a warning but does
	// not prevent the container from coming up — agents may recover via
	// retry. Intentionally excluded from the provisioning hash; mutating the
	// list does not invalidate the cached image.
	PostStartCommands []string

	// InitHookEnabled opts the crew into auto-executing /crew/init.sh on every
	// container start. /crew is a persistent bind mount on the host, so an
	// agent (UID 1001) with write access can stash a script there that will
	// run as 1001 on every restart — a persistence backdoor that survives
	// docker rm -f, crew restart, even sidecar reinstall. Default false:
	// the soft-promotion path now requires explicit operator opt-in per
	// crew via the manifest. Operators who do flip the bit accept the
	// trust model that everything in /crew/init.sh is code they wrote
	// or audited.
	InitHookEnabled bool

	// Services are sidecar containers (Redis, Postgres, etc.) the
	// provider should start alongside the crew's runtime, on the
	// same network, so the agent can reach them by Service.Name.
	// Empty / nil means "no sidecars", which is the historical
	// default. Providers that don't yet support sidecars should
	// log + ignore — the orchestrator surfaces a warning to the
	// user through the manifest-apply path.
	Services []CrewService
}

// CrewService is one sidecar container declaration. Mirrors the
// manifest's Service shape but lives in provider/ to avoid a cyclic
// dependency between provider and manifest packages — the API layer
// translates from the on-disk JSON into this struct before invoking
// the provider.
type CrewService struct {
	Name        string
	Image       string
	Command     []string
	Env         map[string]string // literal env vars (already resolved)
	Ports       []string          // "5432" or "5432/tcp"
	Volumes     []CrewServiceVolume
	Healthcheck *CrewServiceHealthcheck
}

// CrewServiceVolume names a per-crew named volume and where it
// mounts inside the sidecar.
type CrewServiceVolume struct {
	Name  string
	Mount string
}

// CrewServiceHealthcheck mirrors docker's healthcheck shape so the
// provider can wait for HEALTHY before reporting the crew ready.
type CrewServiceHealthcheck struct {
	Test        []string
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	StartPeriod time.Duration
}

// CrewMount declares an additional bind or volume mount to apply to the crew
// runtime, typically sourced from a devcontainer feature's metadata.
type CrewMount struct {
	Source string // host path (bind) or volume name (volume)
	Target string // path inside the container
	Type   string // "bind" (default) or "volume"
}

// ExecConfig describes a non-interactive command to execute inside a container.
type ExecConfig struct {
	ContainerID string
	Cmd         []string
	Env         []string
	WorkingDir  string
	User        string
}

// ExecResult holds the exec ID and output stream from a container exec command.
type ExecResult struct {
	ExecID string
	Reader io.ReadCloser
}

// ContainerStatus reports the current state and uptime of a crew's container.
type ContainerStatus struct {
	ID     string
	State  string // "creating", "running", "idle", "stopped", "error"
	Uptime string
}

// ContainerMetrics holds point-in-time resource usage metrics for a container
// including CPU, memory, network I/O, and process count.
type ContainerMetrics struct {
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryUsed  int64     `json:"memory_used_bytes"`
	MemoryLimit int64     `json:"memory_limit_bytes"`
	MemoryPct   float64   `json:"memory_percent"`
	NetRx       int64     `json:"net_rx_bytes"`
	NetTx       int64     `json:"net_tx_bytes"`
	PIDs        int       `json:"pids"`
	Timestamp   time.Time `json:"timestamp"`
}

// ContainerProvider defines the interface for managing crew container runtimes.
// Implementations include Docker and Apple Containers.
type ContainerProvider interface {
	EnsureCrewRuntime(ctx context.Context, team CrewConfig) (string, error)
	StopCrewRuntime(ctx context.Context, containerID string) error
	RemoveCrewRuntime(ctx context.Context, containerID string) error
	ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error)
	ContainerStats(ctx context.Context, containerID string) (*ContainerMetrics, error)
	Exec(ctx context.Context, cfg ExecConfig) (*ExecResult, error)
	ExecInspect(ctx context.Context, execID string) (bool, int, error)
	// CrewContainerName returns the container name for a given crew slug.
	CrewContainerName(slug string) string
	// CopyToContainer copies a tar archive into the container filesystem at dstPath.
	CopyToContainer(ctx context.Context, containerID string, dstPath string, content io.Reader) error
}

// HostAddressProvider is an optional interface that container providers can
// implement to advertise the hostname/IP that containers should use to reach
// the host machine. Docker uses "host.docker.internal"; Apple Containers use
// the host's actual IP since each container runs in its own VM.
type HostAddressProvider interface {
	HostAddress() string
}

// SidecarProvider is the optional capability for container providers
// that can start crew-scoped sidecar containers (Redis, Postgres,
// etc.). The docker provider implements it; the apple-container
// provider does not yet, and orchestrator callers that need
// sidecars must type-assert at the call site (graceful degradation:
// if the provider doesn't support sidecars, the manifest's
// `services:` block is ignored at runtime with a warning).
type SidecarProvider interface {
	EnsureCrewServices(ctx context.Context, team CrewConfig) (map[string]string, error)
	StopCrewServices(ctx context.Context, crewSlug string) error
	RemoveCrewServices(ctx context.Context, crewSlug string) error
}

// CrewContainerLookup is an optional interface that container providers
// can implement to expose a non-mutating "does a container for this crew
// already exist?" lookup. Used by Server.Start for boot-time rehydration:
// containers persist across `crewshipd` restarts, so the stats collector
// + listening-port scanner stay blind to them unless we re-register on
// startup. Providers that don't implement this just skip rehydration —
// existing crews start being tracked again the next time their crew is
// dispatched (i.e. the next EnsureCrewRuntime call).
type CrewContainerLookup interface {
	// FindCrewContainer returns the existing container ID for a crew
	// slug. `running` is false for stopped-but-present containers (so
	// the caller can decide whether to start it). When no container is
	// found, returns ("", false, nil) — only error path is for transport
	// failures talking to the runtime.
	FindCrewContainer(ctx context.Context, slug string) (containerID string, running bool, err error)
}

// VolumeManager is an optional interface for managing persistent volumes
// associated with crew containers (home directories, tool storage).
type VolumeManager interface {
	RemoveCrewVolumes(ctx context.Context, slug string) error
}

// InteractiveExecConfig configures an interactive (TTY) exec session.
type InteractiveExecConfig struct {
	ContainerID string
	Cmd         []string
	Env         []string
	WorkingDir  string
	User        string
	Rows        uint16
	Cols        uint16
}

// InteractiveExecResult holds the bidirectional connection to an interactive exec.
type InteractiveExecResult struct {
	ExecID string
	Conn   io.ReadWriteCloser // raw bidirectional PTY stream
}

// InteractiveExecProvider is an optional interface for providers that support
// interactive (TTY + stdin) exec sessions, used by the web terminal.
type InteractiveExecProvider interface {
	ExecInteractive(ctx context.Context, cfg InteractiveExecConfig) (*InteractiveExecResult, error)
	ExecResize(ctx context.Context, execID string, rows, cols uint16) error
}
