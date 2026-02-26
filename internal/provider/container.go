package provider

import (
	"context"
	"io"
)

type CrewConfig struct {
	ID       string
	Slug     string
	MemoryMB int
	CPUs     float64
}

type ExecConfig struct {
	ContainerID string
	Cmd         []string
	Env         []string
	WorkingDir  string
	User        string
}

type ExecResult struct {
	ExecID string
	Reader io.ReadCloser
}

type ContainerStatus struct {
	ID     string
	State  string // "creating", "running", "idle", "stopped", "error"
	Uptime string
}

type ContainerProvider interface {
	EnsureCrewRuntime(ctx context.Context, team CrewConfig) (string, error)
	StopCrewRuntime(ctx context.Context, containerID string) error
	RemoveCrewRuntime(ctx context.Context, containerID string) error
	ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error)
	Exec(ctx context.Context, cfg ExecConfig) (*ExecResult, error)
	ExecInspect(ctx context.Context, execID string) (bool, int, error)
}

// HostAddressProvider is an optional interface that container providers can
// implement to advertise the hostname/IP that containers should use to reach
// the host machine. Docker uses "host.docker.internal"; Apple Containers use
// the host's actual IP since each container runs in its own VM.
type HostAddressProvider interface {
	HostAddress() string
}
