package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// hostCaptureAuthority is sent only through the host's private inherited pipe.
// It binds the helper to the exact container start admitted by the controller.
type hostCaptureAuthority struct {
	Version     int      `json:"version"`
	ContainerID string   `json:"container_id"`
	ImageID     string   `json:"image_id"`
	StartedAt   string   `json:"started_at"`
	Directory   string   `json:"directory"`
	Fence       bool     `json:"fence"`
	CrewID      string   `json:"crew_id"`
	CrewSlug    string   `json:"crew_slug"`
	StopArgs    []string `json:"stop_args"`
}

// SupportsHostCapture reports the qualified Docker transport. Kernel process
// identity permissions are checked again before any exact-execution stop.
func (p *Provider) SupportsHostCapture() bool {
	return runtime.GOOS == "linux" && strings.HasPrefix(p.detected.Host, "unix://") && hostCaptureControllerUIDSupported(os.Geteuid())
}

// Workload identities cannot own a protected host spool. Select the legacy
// transport before qualification/claim instead of rejecting their CLI runs.
func hostCaptureControllerUIDSupported(uid int) bool {
	return uid != 1001 && uid != 1002
}

// PrepareHostCapture performs the same fence admission as Exec. A host spool
// may not be reachable through any container bind, including an ancestor bind.
func (p *Provider) PrepareHostCapture(ctx context.Context, cfg provider.ExecConfig, directory string, stopArgs []string) (string, json.RawMessage, error) {
	if !p.SupportsHostCapture() {
		return "", nil, errors.New("host capture currently requires a supported controller and local Linux Docker daemon")
	}
	return p.prepareSupportedHostCapture(ctx, cfg, directory, stopArgs)
}

// prepareSupportedHostCapture performs admission after the public capability
// boundary. Keeping admission separate lets mock tests exercise its protections
// even when their host identity is deliberately ineligible for capture.
func (p *Provider) prepareSupportedHostCapture(ctx context.Context, cfg provider.ExecConfig, directory string, stopArgs []string) (string, json.RawMessage, error) {
	if cfg.User != "1001:1001" || cfg.AllowPrivileged || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || len(stopArgs) == 0 {
		return "", nil, errors.New("host capture requires an admitted unprivileged run and stop command")
	}
	fence, err := p.guardFencedExec(ctx, cfg.ContainerID)
	if err != nil {
		return "", nil, err
	}
	insp, err := p.client.ContainerInspect(ctx, cfg.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		return "", nil, err
	}
	c := insp.Container
	if c.State == nil || !c.State.Running || c.ID == "" || c.Image == "" || c.State.StartedAt == "" {
		return "", nil, errors.New("host capture container has no running immutable identity")
	}
	if fence.active() && (fence.containerID != c.ID || fence.startedAt != c.State.StartedAt) {
		return "", nil, errors.New("host capture fence admission belongs to a different container start")
	}
	if err := p.hostSpoolHidden(ctx, c.Mounts, directory); err != nil {
		return "", nil, err
	}

	a := hostCaptureAuthority{Version: 1, ContainerID: c.ID, ImageID: c.Image, StartedAt: c.State.StartedAt, Directory: directory,
		Fence: fence.active(), CrewID: fence.team.ID, CrewSlug: fence.team.Slug, StopArgs: stopArgs}
	data, err := json.Marshal(a)
	return p.detected.Host, data, err
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// prospectiveHostPath resolves existing ancestors while allowing the final run
// directory not to exist yet. Docker preserves symlink spellings in bind Source.
func prospectiveHostPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("host mount path is not absolute")
	}
	current := filepath.Clean(path)
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || current == filepath.Dir(current) {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
		current = filepath.Dir(current)
	}
}

func (p *Provider) hostSpoolHidden(ctx context.Context, mounts []container.MountPoint, directory string) error {
	spool, err := prospectiveHostPath(directory)
	if err != nil {
		return fmt.Errorf("resolve protected host spool: %w", err)
	}
	for _, m := range mounts {
		if m.Source != "" && (pathContains(m.Source, directory) || pathContains(m.Source, spool)) {
			return errors.New("host capture spool is exposed to the workload")
		}
		source := ""
		switch m.Type {
		case mount.TypeTmpfs:
			continue
		case mount.TypeBind:
			source = m.Source
		case mount.TypeVolume:
			if m.Name == "" {
				return errors.New("host capture volume has no identity")
			}
			inspected, err := p.client.VolumeInspect(ctx, m.Name, client.VolumeInspectOptions{})
			if err != nil {
				return err
			}
			v := inspected.Volume
			if v.Driver != "local" {
				return errors.New("host capture volume driver is unqualified")
			}
			if len(v.Options) == 0 {
				if !filepath.IsAbs(v.Mountpoint) || filepath.Clean(v.Mountpoint) != v.Mountpoint {
					return errors.New("host capture volume mountpoint is unqualified")
				}
				// Ordinary local volumes use daemon-owned storage, which need
				// not be traversable by the controller UID. Bind-backed local
				// volumes below must authenticate the actual host device path.
				if pathContains(v.Mountpoint, spool) {
					return errors.New("host capture spool is exposed through a volume")
				}
				continue
			}
			if v.Options["type"] == "tmpfs" {
				continue
			}
			if v.Options["type"] != "none" {
				return errors.New("host capture volume options are unqualified")
			}
			bound := false
			for _, option := range strings.Split(v.Options["o"], ",") {
				if option == "bind" || option == "rbind" {
					bound = true
				}
			}
			if !bound {
				return errors.New("host capture local volume is not an authenticated bind")
			}
			source = v.Options["device"]
		default:
			return errors.New("host capture mount type is unqualified")
		}
		// Unlike a prospective spool, an inspected bind source must exist.
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil || !filepath.IsAbs(resolved) {
			return errors.New("host capture cannot authenticate bind source")
		}
		if pathContains(resolved, spool) {
			return errors.New("host capture spool is exposed through a bind alias")
		}
	}
	return nil
}

type hostCaptureExecutor struct {
	provider  *Provider
	authority hostCaptureAuthority
	execID    string
}

// NewHostCaptureExecutor opens only an execution connection. Unlike New, it
// never provisions, reconciles, sweeps secrets, or changes another runtime.
func NewHostCaptureExecutor(input runoutput.HostInput) (runoutput.HostExecutor, io.Closer, error) {
	var a hostCaptureAuthority
	dec := json.NewDecoder(bytes.NewReader(input.Authority))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, nil, errors.New("invalid host capture authority")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || a.Version != 1 || a.ContainerID != input.Command.ContainerID || a.Directory != input.Directory || a.ImageID == "" || a.StartedAt == "" || len(a.StopArgs) == 0 || input.Endpoint == "" {
		return nil, nil, errors.New("host capture authority does not match execution")
	}
	cli, err := client.New(client.WithHost(input.Endpoint))
	if err != nil {
		return nil, nil, err
	}
	p := &Provider{client: cli, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if a.Fence {
		p.cfg.EgressFenceCrews = []string{a.CrewID}
		p.fenced.Store(a.ContainerID, a.StartedAt)
		p.fencedCrew.Store(a.ContainerID, a.CrewID)
		// This handoff certifies one existing start, not the full crew config.
		// Do not seed fenceTeams: a restart must refuse rather than reinstall
		// from the incomplete ID/slug snapshot.
	}
	return &hostCaptureExecutor{provider: p, authority: a}, cli, nil
}

// RestoreHostCapture rebinds a durable host-owned execution identity without
// starting or provisioning anything. The daemon must attest the same container.
func (p *Provider) RestoreHostCapture(input runoutput.HostInput, execID string) (runoutput.HostExecutor, io.Closer, error) {
	if execID == "" {
		return nil, nil, errors.New("host capture restore requires an execution identity")
	}
	executor, closer, err := NewHostCaptureExecutor(input)
	if err != nil {
		return nil, nil, err
	}
	e := executor.(*hostCaptureExecutor)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := e.provider.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
	if err != nil || inspected.ContainerID != e.authority.ContainerID {
		_ = closer.Close()
		return nil, nil, errors.New("restored host execution does not belong to admitted container")
	}
	e.execID = execID
	return e, closer, nil
}

func (e *hostCaptureExecutor) check(ctx context.Context) error {
	insp, err := e.provider.client.ContainerInspect(ctx, e.authority.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	c := insp.Container
	if c.ID != e.authority.ContainerID || c.Image != e.authority.ImageID || c.State == nil || !c.State.Running || c.State.StartedAt != e.authority.StartedAt {
		return errors.New("host capture container changed since admission")
	}
	if err := e.provider.hostSpoolHidden(ctx, c.Mounts, e.authority.Directory); err != nil {
		return err
	}

	return nil
}

func (e *hostCaptureExecutor) Start(ctx context.Context, command runoutput.HostCommand) (*runoutput.HostExecution, error) {
	if e.execID != "" || command.ContainerID != e.authority.ContainerID || command.User != "1001:1001" {
		return nil, errors.New("host execution identity already used or mismatched")
	}
	if err := e.check(ctx); err != nil {
		return nil, err
	}
	fence, err := e.provider.guardFencedExec(ctx, command.ContainerID)
	if err != nil {
		return nil, err
	}
	if fence.active() != e.authority.Fence || (fence.active() && (fence.containerID != e.authority.ContainerID || fence.startedAt != e.authority.StartedAt)) {
		return nil, errors.New("host capture fence changed since admission")
	}
	created, err := e.provider.client.ExecCreate(ctx, command.ContainerID, client.ExecCreateOptions{Cmd: command.Args, Env: command.Env, WorkingDir: command.WorkingDir, User: command.User, AttachStdout: true, AttachStderr: true, AttachStdin: len(command.Stdin) > 0})
	if err != nil {
		return nil, err
	}
	if err := e.check(ctx); err != nil {
		return nil, err
	}
	if err := e.provider.fenceBeforeStart(ctx, fence); err != nil {
		return nil, err
	}
	response, err := e.provider.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	e.execID = created.ID
	if err := e.provider.fenceAfterStart(ctx, fence); err != nil {
		response.Close()
		return nil, err
	}
	if err := e.check(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = e.Stop(stopCtx, created.ID)
		response.Close()
		return nil, err
	}
	if len(command.Stdin) > 0 {
		go func() { _, _ = io.Copy(response.Conn, bytes.NewReader(command.Stdin)); _ = response.CloseWrite() }()
	}
	out, outWriter := io.Pipe()
	errOut, errWriter := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(outWriter, errWriter, response.Reader)
		_ = outWriter.CloseWithError(err)
		_ = errWriter.CloseWithError(err)
		response.Close()
	}()
	return &runoutput.HostExecution{ID: created.ID, Stdout: out, Stderr: errOut}, nil
}

func (e *hostCaptureExecutor) Inspect(ctx context.Context, id string) (bool, int, error) {
	if id == "" || id != e.execID {
		return false, -1, errors.New("host exec identity mismatch")
	}
	inspected, err := e.provider.client.ExecInspect(ctx, id, client.ExecInspectOptions{})
	if err != nil {
		return false, -1, err
	}
	if inspected.ContainerID != e.authority.ContainerID {
		return false, -1, errors.New("host inspect container mismatch")
	}
	return inspected.Running, inspected.ExitCode, nil
}

func (e *hostCaptureExecutor) Stop(ctx context.Context, id string) error {
	if id == "" || id != e.execID {
		return errors.New("host stop identity mismatch")
	}
	inspection, err := e.provider.client.ExecInspect(ctx, id, client.ExecInspectOptions{})
	if err != nil {
		return err
	}
	if inspection.ContainerID != e.authority.ContainerID {
		return errors.New("host stop container mismatch")
	}
	if !inspection.Running {
		return nil
	}
	if err := e.check(ctx); err != nil {
		return err
	}
	containerState, err := e.provider.client.ContainerInspect(ctx, e.authority.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if containerState.Container.State == nil || containerState.Container.State.StartedAt != e.authority.StartedAt {
		return errors.New("host stop container start changed")
	}
	namespacePID, ticks, err := hostProcessIdentity(inspection.PID, containerState.Container.State.Pid)
	if err != nil {
		return err
	}
	fresh, err := e.provider.client.ExecInspect(ctx, id, client.ExecInspectOptions{})
	if err != nil {
		return err
	}
	if fresh.ContainerID != inspection.ContainerID || !fresh.Running || fresh.PID != inspection.PID {
		return errors.New("host stop execution changed during process identification")
	}
	if err := e.check(ctx); err != nil {
		return err
	}
	if err := runoutput.RequestHostStop(e.authority.Directory); err != nil {
		return err
	}
	// pidfd in the container binds the signal to the inspected kernel process,
	// even if its numeric PID is recycled between verification and signalling.
	res, err := e.provider.Exec(ctx, provider.ExecConfig{ContainerID: e.authority.ContainerID, User: "1001:1001", Cmd: []string{"/usr/local/bin/crewship-sidecar", "run-stop-process", "--pid", strconv.Itoa(namespacePID), "--starttime", strconv.FormatUint(ticks, 10)}})
	if err != nil {
		return err
	}
	defer res.Reader.Close()
	data, err := io.ReadAll(io.LimitReader(res.Reader, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New("host stop probe unreadable")
	}
	code, err := provider.WaitExecExit(ctx, e.provider, res.ExecID, 5*time.Second)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("host process signal failed: helper exit %d", code)
	}
	if _, err := provider.WaitExecExit(ctx, e.provider, id, 5*time.Second); err != nil {
		return fmt.Errorf("host process terminal not confirmed: %w", err)
	}
	return nil
}
