package provider

import (
	"context"
	"io"
)

type TeamConfig struct {
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
	EnsureTeamRuntime(ctx context.Context, team TeamConfig) (string, error)
	StopTeamRuntime(ctx context.Context, containerID string) error
	RemoveTeamRuntime(ctx context.Context, containerID string) error
	ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error)
	Exec(ctx context.Context, cfg ExecConfig) (*ExecResult, error)
	ExecInspect(ctx context.Context, execID string) (bool, int, error)
}
