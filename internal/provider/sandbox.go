package provider

import (
	"context"
	"errors"
	"time"
)

var ErrSandboxUnsupported = errors.New("sandbox capability unavailable")
var ErrSandboxDenied = errors.New("sandbox request denied")
var ErrSandboxOutputLimit = errors.New("sandbox output limit exceeded")

// SandboxRuntime owns individual instances independently of legacy crew
// containers. Callers supply trusted server intent, never a public request
// deserialized directly into SandboxSpec. Credentials belong to a separate
// authority/broker contract and are deliberately absent here.
type SandboxRuntime interface {
	SandboxCapabilities() SandboxCapabilities
	CreateSandbox(context.Context, SandboxSpec) (SandboxRef, error)
	ExecSandbox(context.Context, SandboxRef, SandboxExec) (SandboxExecResult, error)
	InspectSandbox(context.Context, SandboxRef) (SandboxState, error)
	RemoveSandbox(context.Context, SandboxRef) error
}

type SandboxCapabilities struct {
	// MaxLifetime bounds transient instances. Zero is not a promise of durable
	// execution; a backend must document a separate persistence capability.
	MaxLifetime time.Duration

	Offline       bool
	NamedVolumes  bool
	MemorySuspend bool
}

type SandboxMount struct {
	ResourceID string
	Target     string
	ReadOnly   bool
}

type SandboxSpec struct {
	Lifetime time.Duration

	ID          string
	ImageID     string
	MemoryBytes int64
	NanoCPUs    int64
	PIDs        int64
	Mounts      []SandboxMount
}

// Ref is scoped to an installation and an instance; implementations must check
// ownership on every operation, including deletion. RuntimeID alone is not
// authority. References are server-side objects, not public bearer tokens.
type SandboxRef struct {
	ID        string
	RuntimeID string
	ImageID   string
}

type SandboxExec struct {
	Command     []string
	Env         []string
	Timeout     time.Duration
	OutputLimit int
}

type SandboxExecResult struct {
	Output   string
	ExitCode int
}

type SandboxState struct {
	Running bool
	ImageID string
}
