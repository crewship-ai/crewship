package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

var _ provider.SandboxRuntime = (*Provider)(nil)
var sandboxIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)
var sandboxImagePattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const sandboxInstanceLabel = "crewship.sandbox-id"
const sandboxKindLabel = "crewship.sandbox-kind"

func (p *Provider) SandboxCapabilities() provider.SandboxCapabilities {
	return provider.SandboxCapabilities{Offline: p.cfg.InstanceID != ""}
}

func (p *Provider) CreateSandbox(ctx context.Context, spec provider.SandboxSpec) (provider.SandboxRef, error) {
	if p.cfg.InstanceID == "" || !sandboxIDPattern.MatchString(spec.ID) || !sandboxImagePattern.MatchString(spec.ImageID) {
		return provider.SandboxRef{}, provider.ErrSandboxDenied
	}
	if len(spec.Mounts) != 0 {
		return provider.SandboxRef{}, provider.ErrSandboxUnsupported
	}
	if spec.MemoryBytes < 32<<20 || spec.MemoryBytes > 4<<30 || spec.NanoCPUs < 100_000_000 || spec.NanoCPUs > 4_000_000_000 || spec.PIDs < 8 || spec.PIDs > 512 {
		return provider.SandboxRef{}, provider.ErrSandboxDenied
	}
	inspected, err := p.client.ImageInspect(ctx, spec.ImageID)
	if err != nil {
		return provider.SandboxRef{}, fmt.Errorf("sandbox image: %w", err)
	}
	if inspected.ID != spec.ImageID || len(inspected.Config.Volumes) != 0 {
		return provider.SandboxRef{}, provider.ErrSandboxDenied
	}
	// Docker merges image ENV with create ENV. Explicitly clear inherited values;
	// only fixed execution settings and trusted per-exec toolchain paths follow.
	env := []string{}
	for _, kv := range inspected.Config.Env {
		key, _, ok := strings.Cut(kv, "=")
		if ok {
			env = append(env, key+"=")
		}
	}
	env = append(env, "HOME=/home/agent", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "DISABLE_AUTOUPDATER=1")
	init := true
	pids := spec.PIDs
	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: spec.ImageID, User: "1001:1001", Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{"exec sleep 120"}, Env: env, WorkingDir: "/home/agent", Labels: map[string]string{resourcelifecycle.InstanceLabel: p.cfg.InstanceID, sandboxInstanceLabel: spec.ID, sandboxKindLabel: "offline-v1"}},
		HostConfig: &container.HostConfig{NetworkMode: "none", IpcMode: "private", CgroupnsMode: "private", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Init: &init, Resources: container.Resources{Memory: spec.MemoryBytes, MemorySwap: spec.MemoryBytes, NanoCPUs: spec.NanoCPUs, PidsLimit: &pids}, Tmpfs: map[string]string{"/home/agent": "rw,nosuid,nodev,size=67108864,uid=1001,gid=1001,mode=0700", "/tmp": "rw,nosuid,nodev,noexec,size=16777216,mode=1777"}},
	})
	if err != nil {
		return provider.SandboxRef{}, fmt.Errorf("sandbox create: %w", err)
	}
	ref := provider.SandboxRef{ID: spec.ID, RuntimeID: created.ID, ImageID: spec.ImageID}
	createdState, auditErr := p.ownedSandbox(ctx, ref)
	if auditErr == nil {
		auditErr = auditOfflineSandbox(createdState)
	}
	if auditErr != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.RemoveSandbox(cleanup, ref)
		return provider.SandboxRef{}, fmt.Errorf("sandbox audit: %w", auditErr)
	}
	if _, err = p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.RemoveSandbox(cleanup, ref)
		return provider.SandboxRef{}, fmt.Errorf("sandbox start: %w", err)
	}
	return ref, nil
}

func (p *Provider) ownedSandbox(ctx context.Context, ref provider.SandboxRef) (container.InspectResponse, error) {
	if p.cfg.InstanceID == "" || !sandboxIDPattern.MatchString(ref.ID) || !sandboxImagePattern.MatchString(ref.ImageID) || ref.RuntimeID == "" {
		return container.InspectResponse{}, provider.ErrSandboxDenied
	}
	result, err := p.client.ContainerInspect(ctx, ref.RuntimeID, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, err
	}
	c := result.Container
	if c.Config == nil || c.Config.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.Config.Labels[sandboxInstanceLabel] != ref.ID || c.Config.Labels[sandboxKindLabel] != "offline-v1" || c.Image != ref.ImageID {
		return c, provider.ErrSandboxDenied
	}
	return c, nil
}

func (p *Provider) InspectSandbox(ctx context.Context, ref provider.SandboxRef) (provider.SandboxState, error) {
	c, err := p.ownedSandbox(ctx, ref)
	if err != nil {
		return provider.SandboxState{}, err
	}
	return provider.SandboxState{Running: c.State != nil && c.State.Running, ImageID: c.Image}, nil
}
func (p *Provider) RemoveSandbox(ctx context.Context, ref provider.SandboxRef) error {
	if _, err := p.ownedSandbox(ctx, ref); err != nil {
		return err
	}
	_, err := p.client.ContainerRemove(ctx, ref.RuntimeID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}

type sandboxOutput struct {
	mu sync.Mutex
	bytes.Buffer
	limit int
}

func (b *sandboxOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) > b.limit-b.Len() {
		return 0, provider.ErrSandboxOutputLimit
	}
	return b.Buffer.Write(p)
}

func (p *Provider) ExecSandbox(ctx context.Context, ref provider.SandboxRef, spec provider.SandboxExec) (provider.SandboxExecResult, error) {
	if len(spec.Command) == 0 || spec.Timeout <= 0 || spec.Timeout > 30*time.Second || spec.OutputLimit < 1 || spec.OutputLimit > 65536 {
		return provider.SandboxExecResult{}, provider.ErrSandboxDenied
	}
	c, err := p.ownedSandbox(ctx, ref)
	if err != nil {
		return provider.SandboxExecResult{}, err
	}
	if c.State == nil || !c.State.Running || auditOfflineSandbox(c) != nil {
		return provider.SandboxExecResult{}, provider.ErrSandboxDenied
	}

	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	created, err := p.client.ExecCreate(ctx, ref.RuntimeID, client.ExecCreateOptions{Cmd: spec.Command, Env: spec.Env, User: "1001:1001", WorkingDir: "/home/agent", AttachStdout: true, AttachStderr: true})
	if err != nil {
		return provider.SandboxExecResult{}, err
	}
	attached, err := p.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return provider.SandboxExecResult{}, err
	}
	defer attached.Close()
	output := &sandboxOutput{limit: spec.OutputLimit}
	done := make(chan error, 1)
	go func() { _, copyErr := stdcopy.StdCopy(output, output, attached.Reader); done <- copyErr }()
	select {
	case <-ctx.Done():
		attached.Close()
		<-done
		return provider.SandboxExecResult{}, ctx.Err()
	case err = <-done:
		if err != nil && err != io.EOF {
			return provider.SandboxExecResult{}, err
		}
	}
	inspected, err := p.client.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return provider.SandboxExecResult{}, err
	}
	if inspected.Running {
		return provider.SandboxExecResult{}, fmt.Errorf("sandbox exec stream closed before exit")
	}
	return provider.SandboxExecResult{Output: output.String(), ExitCode: inspected.ExitCode}, nil
}

// Verify the effective daemon configuration before starting or executing code.
// Docker and the host remain trusted; this is not a defense against host admin.
func auditOfflineSandbox(c container.InspectResponse) error {
	h := c.HostConfig
	if c.Config == nil || h == nil || c.Config.User != "1001:1001" || h.Privileged || !h.ReadonlyRootfs || h.NetworkMode != "none" || h.IpcMode != "private" || h.PidMode != "" || h.UTSMode != "" || h.CgroupnsMode != "private" || len(h.CapAdd) != 0 || !slices.Contains(h.CapDrop, "ALL") || !slices.Contains(h.SecurityOpt, "no-new-privileges:true") || len(h.Binds) != 0 || len(h.Mounts) != 0 || len(c.Mounts) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 || len(h.VolumesFrom) != 0 {
		return provider.ErrSandboxDenied
	}
	if h.Memory < 32<<20 || h.Memory > 4<<30 || h.MemorySwap != h.Memory || h.NanoCPUs < 100_000_000 || h.NanoCPUs > 4_000_000_000 || h.PidsLimit == nil || *h.PidsLimit < 8 || *h.PidsLimit > 512 {
		return provider.ErrSandboxDenied
	}
	if len(h.Tmpfs) != 2 || h.Tmpfs["/home/agent"] != "rw,nosuid,nodev,size=67108864,uid=1001,gid=1001,mode=0700" || h.Tmpfs["/tmp"] != "rw,nosuid,nodev,noexec,size=16777216,mode=1777" {
		return provider.ErrSandboxDenied
	}
	return nil
}
