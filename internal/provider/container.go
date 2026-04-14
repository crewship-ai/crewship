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
