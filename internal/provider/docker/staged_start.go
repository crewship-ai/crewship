package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/stagedstart"
)

var errStagedBootstrapFailed = errors.New("staged start: bootstrap failed")

func overlap(a, b string) bool {
	a, b = path.Clean(a), path.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || a == "/" || b == "/"
}
func staged(c container.InspectResponse) bool {
	return c.Config != nil && slices.Equal(c.Config.Entrypoint, []string{stagedstart.Binary}) && slices.Equal(c.Config.Cmd, []string{"--staged-keeper"})
}

func (p *Provider) auditStaged(ctx context.Context, c container.InspectResponse, team provider.CrewConfig) error {
	h := c.HostConfig
	if !staged(c) || p.cfg.InstanceID == "" || c.Config.User != "1002:1002" || c.Config.Labels[stagedstart.Label] != stagedstart.Version || c.Config.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.Config.Labels[crewCrewIDLabel] != team.ID || h == nil || h.Privileged || !h.ReadonlyRootfs || h.Init == nil || *h.Init || len(h.CapAdd) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 || len(h.VolumesFrom) != 0 || h.NetworkMode == "host" || strings.HasPrefix(string(h.NetworkMode), "container:") || h.Runtime != "runc" || len(h.SecurityOpt) != 1 || !slices.Equal(h.GroupAdd, []string{crewGroupGID, sidecarGroupGID}) || len(h.Sysctls) != 0 || (h.UsernsMode != "" && h.UsernsMode != "private") || (h.UTSMode != "" && h.UTSMode != "private") || (h.CgroupnsMode != "" && h.CgroupnsMode != "private") || h.PidMode != "" || (h.IpcMode != "" && h.IpcMode != "private") || !slices.Contains(h.CapDrop, "ALL") || (!slices.Contains(h.SecurityOpt, "no-new-privileges") && !slices.Contains(h.SecurityOpt, "no-new-privileges:true")) || c.Config.Healthcheck == nil || !slices.Equal(c.Config.Healthcheck.Test, []string{"NONE"}) {
		return fmt.Errorf("%w: immutable keeper configuration or ownership mismatch", errStagedDenied)
	}
	for _, entry := range c.Config.Env {
		key, value, _ := strings.Cut(entry, "=")
		if (key == "HOME" && value != "/tmp") || (key == "PATH" && value != "/usr/bin:/bin") || (key != "HOME" && key != "PATH" && value != "") {
			return fmt.Errorf("%w: keeper inherited workload environment", errStagedDenied)
		}
	}
	for target := range h.Tmpfs {
		for _, reserved := range []string{stagedstart.Binary, stagedstart.Bootstrap} {
			if overlap(path.Clean(target), reserved) {
				return fmt.Errorf("%w: tmpfs overlaps reserved executable", errStagedDenied)
			}
		}
	}
	source, digest, e := p.stageArtifact(ctx, c.Config.Image)
	if e != nil {
		return e
	}
	revisionChanged := c.Config.Labels["crewship.keeper-sha256"] != digest
	bootSource, e := p.stageTrustedFile(p.cfg.EntrypointPath, 1<<20)
	if e != nil {
		return e
	}
	bound, boot := false, false
	for _, m := range c.Mounts {
		switch m.Destination {
		case stagedstart.Binary:
			bound = m.Type == mount.TypeBind && !m.RW
			if bound && m.Source != source {
				published, e := p.stageTrustedFile(m.Source, managedlaunch.MaxArtifactBytes)
				if e != nil || published != m.Source {
					return fmt.Errorf("%w: existing keeper bind is not trusted immutable material", errStagedDenied)
				}
				raw, e := os.ReadFile(m.Source)
				if e != nil {
					return e
				}
				artifact, e := managedlaunch.Capture("/usr/local/bin/crewship-staged-artifact", raw)
				if e != nil || artifact.SHA256 != c.Config.Labels["crewship.keeper-sha256"] {
					return fmt.Errorf("%w: existing keeper content differs from its generation", errStagedDenied)
				}
				revisionChanged = true
			} else if bound && revisionChanged {
				return fmt.Errorf("%w: keeper generation label differs from actual bind", errStagedDenied)
			}
		case stagedstart.Bootstrap:
			boot = m.Type == mount.TypeBind && !m.RW
			if boot && m.Source != bootSource {
				published, e := p.stageTrustedFile(m.Source, 1<<20)
				if e != nil || published != m.Source {
					return fmt.Errorf("%w: existing bootstrap bind is not trusted immutable material", errStagedDenied)
				}
				revisionChanged = true
			}
		default:
			if overlap(m.Destination, stagedstart.Binary) || overlap(m.Destination, stagedstart.Bootstrap) || m.Destination == "/var/run/docker.sock" {
				return errStagedDenied
			}
		}
	}
	if !bound || !boot {
		return fmt.Errorf("%w: immutable helper or bootstrap bind mismatch", errStagedDenied)
	}
	for _, target := range []string{stagedstart.Binary, stagedstart.Bootstrap} {
		for parent := path.Dir(target); parent != "/"; parent = path.Dir(parent) {
			st, e := p.client.ContainerStatPath(ctx, c.ID, client.ContainerStatPathOptions{Path: parent})
			if e != nil || !st.Stat.Mode.IsDir() || st.Stat.LinkTarget != "" {
				return fmt.Errorf("%w: helper/bootstrap ancestor must be a real directory", errStagedDenied)
			}
		}
	}
	if _, e := p.loadStagedEnv(c); e != nil {
		return fmt.Errorf("%w: private workload material unavailable or scope invalid", e)
	}
	p.fencedCrew.Store(c.ID, team.ID)
	if revisionChanged {
		return fmt.Errorf("%w: keeper/bootstrap artifact generation changed", &provider.RuntimeImageUpdatePendingError{ContainerID: c.ID, CurrentImageID: c.Image, DesiredImage: c.Config.Image})
	}
	return nil
}

func (p *Provider) stagedControl(ctx context.Context, id, op, nonce string) (stagedstart.Status, error) {
	challenge := rand.Text()
	out, e := p.stagedRawExec(ctx, id, "1002:1002", []string{stagedstart.Binary, "--staged-control", op, nonce, challenge}, nil)
	if e != nil {
		return stagedstart.Status{}, e
	}
	var s stagedstart.Status
	if json.Unmarshal(out, &s) != nil || s.Challenge != challenge || s.Error != "" || len(s.Nonce) < 32 {
		return s, errStagedDenied
	}
	return s, nil
}

// Internal operations use qualified static code; image commands require a gate.
func (p *Provider) stagedRawExec(ctx context.Context, id, user string, cmd, env []string) ([]byte, error) {
	ex, e := p.client.ExecCreate(ctx, id, client.ExecCreateOptions{User: user, Cmd: cmd, Env: env, AttachStdout: true, AttachStderr: true})
	if e != nil {
		return nil, e
	}
	if len(cmd) > 1 && cmd[1] == "--staged-bootstrap" && p.stagedTestHook != nil {
		if e := p.stagedTestHook("before-bootstrap-start", id); e != nil {
			return nil, e
		}
	}
	resp, e := p.client.ExecAttach(ctx, ex.ID, client.ExecAttachOptions{})
	if e != nil {
		return nil, fmt.Errorf("%w: trusted exec attach in container %s: %v", errStagedDenied, id, e)
	}
	defer resp.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = resp.Conn.SetDeadline(deadline)
	}
	out := boundedBuffer{remaining: 8192}
	stderr := boundedBuffer{remaining: 8192}
	_, e = stdcopy.StdCopy(&out, &stderr, resp.Reader)
	if e != nil {
		return nil, e
	}
	if len(cmd) > 1 && cmd[1] == "--staged-bootstrap" && p.stagedTestHook != nil {
		if e := p.stagedTestHook("bootstrap-result", id); e != nil {
			return nil, e
		}
	}
	status, e := p.client.ExecInspect(ctx, ex.ID, client.ExecInspectOptions{})
	if e != nil || status.Running || status.ExitCode != 0 {
		return nil, fmt.Errorf("staged start: trusted exec %s incomplete or exited %d (bounded stderr %d bytes withheld)", ex.ID, status.ExitCode, stderr.Len())
	}
	if out.Len() > 8192 {
		return nil, errStagedDenied
	}
	return out.Bytes(), nil
}

type stagedVerification struct{ nonce, startedAt, policy string }

func (p *Provider) ensureStagedStart(ctx context.Context, team provider.CrewConfig, id string) error {
	stageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	got, e := p.client.ContainerInspect(stageCtx, id, client.ContainerInspectOptions{})
	if e != nil {
		return e
	}
	c := got.Container
	if c.State == nil || !c.State.Running || c.State.Status != "running" || c.State.Paused || c.State.Restarting || c.State.Dead || containerStartedAt(c.State) == "" {
		return fmt.Errorf("%w: current running keeper generation unavailable", errStagedDenied)
	}
	if e = p.auditStaged(stageCtx, c, team); e != nil {
		return e
	}
	var status stagedstart.Status
	for {
		status, e = p.stagedControl(stageCtx, c.ID, "status", "")
		if e == nil {
			break
		}
		current, inspectErr := p.client.ContainerInspect(stageCtx, c.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			return errors.Join(e, inspectErr)
		}
		if current.Container.State == nil || !current.Container.State.Running {
			return fmt.Errorf("%w: keeper stopped before stage confirmation", errStagedDenied)
		}
		select {
		case <-stageCtx.Done():
			return stageCtx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	targets, e := p.crewServiceTargets(stageCtx, team)
	if e != nil {
		return e
	}
	proof := stagedVerification{status.Nonce, containerStartedAt(c.State), targets.key()}
	switch status.Phase {
	case "staging", "sealed", "reserved", "bootstrapping", "ready":
	case "failed":
		return errors.Join(errStagedDenied, errStagedBootstrapFailed)
	default:
		return fmt.Errorf("%w: bootstrap phase %s requires reconciliation", errStagedDenied, status.Phase)
	}
	prior, verified := p.stagedVerified.Load(c.ID)
	verified = verified && prior == proof
	if verified {
		current, e := p.client.ContainerInspect(stageCtx, c.ID, client.ContainerInspectOptions{})
		if e != nil || current.Container.State == nil || !current.Container.State.Running || containerStartedAt(current.Container.State) != proof.startedAt {
			return errStagedDenied
		}
	}
	if !verified {
		// A changed boot/policy invalidates both completed readback and the older
		// apply record. A nonce is live keeper evidence, not filesystem metadata.
		if prior, ok := p.stagedVerified.Load(c.ID); ok && prior != proof {
			p.stagedVerified.Delete(c.ID)
			p.fenced.Delete(c.ID)
		}
		if p.stagedTestHook != nil {
			if e := p.stagedTestHook("before-fence", c.ID); e != nil {
				return e
			}
		}
		if e = p.installEgressFence(stageCtx, team, c.ID, c.Config.Image); e != nil {
			return e
		}
		if p.stagedTestHook != nil {
			if e := p.stagedTestHook("before-check", c.ID); e != nil {
				return e
			}
		}
		// Every new boot or policy requires a completed kernel readback before seal.
		if _, e = p.runFenceHelperMode(stageCtx, team, c.ID, c.Config.Image, targets.dests, false); e != nil {
			return e
		}
		if p.stagedTestHook != nil {
			if e := p.stagedTestHook("after-check", c.ID); e != nil {
				return e
			}
		}
		current, e := p.client.ContainerInspect(stageCtx, c.ID, client.ContainerInspectOptions{})
		if e != nil || current.Container.State == nil || !current.Container.State.Running || containerStartedAt(current.Container.State) != containerStartedAt(c.State) {
			return fmt.Errorf("%w: container start changed after fence readback", errStagedDenied)
		}
		p.stagedVerified.Store(c.ID, proof)
	}
	if status.Phase == "ready" {
		return nil
	}
	if status.Phase == "staging" {
		nonce := status.Nonce
		status, e = p.stagedControl(stageCtx, c.ID, "seal", nonce)
		if e != nil {
			status, e = p.stagedControl(stageCtx, c.ID, "status", "")
			if e != nil || status.Nonce != nonce {
				return errStagedDenied
			}
		}
	}
	reservedHere := false
	var env []string
	if status.Phase == "sealed" {
		if p.stagedTestHook != nil {
			if e := p.stagedTestHook("before-reserve", c.ID); e != nil {
				return e
			}
		}
		// A transient material/image read failure must not burn the one-use permit.
		env, e = p.stagedWorkloadEnv(stageCtx, c, nil)
		if e != nil {
			return fmt.Errorf("staged start: workload material before reservation: %w", e)
		}
		nonce := status.Nonce
		status, e = p.stagedControl(stageCtx, c.ID, "reserve", nonce)
		reservedHere = e == nil
		if e != nil {
			status, e = p.stagedControl(stageCtx, c.ID, "status", "")
			if e != nil || status.Nonce != nonce {
				return errStagedDenied
			}
		}
	}
	if reservedHere {
		_, startErr := p.stagedRawExec(stageCtx, c.ID, "1001:1001", []string{stagedstart.Binary, "--staged-bootstrap", status.Nonce}, env)
		// Even attach errors are unknown outcomes: inspect keeper state, never retry
		// bootstrap blindly. The keeper's one-use consume also protects controllers.
		checked, e := p.stagedControl(stageCtx, c.ID, "status", "")
		if e != nil {
			return fmt.Errorf("staged start: bootstrap outcome unknown: %w", errors.Join(startErr, e))
		}
		if checked.Nonce != status.Nonce {
			return errStagedDenied
		}
		status = checked
		if startErr != nil && status.Phase != "ready" && status.Phase != "bootstrapping" {
			return fmt.Errorf("staged start: bootstrap outcome unconfirmed: %w", startErr)
		}
	}
	for status.Phase == "bootstrapping" || status.Phase == "reserved" {
		select {
		case <-stageCtx.Done():
			return stageCtx.Err()
		case <-time.After(25 * time.Millisecond):
		}
		next, e := p.stagedControl(stageCtx, c.ID, "status", "")
		if e != nil || next.Nonce != status.Nonce {
			return errStagedDenied
		}
		status = next
	}
	if status.Phase != "ready" {
		return fmt.Errorf("%w: bootstrap phase %s requires reconciliation", errors.Join(errStagedDenied, errStagedBootstrapFailed), status.Phase)
	}
	return nil
}

func (p *Provider) stagedWorkloadEnv(ctx context.Context, c container.InspectResponse, extra []string) ([]string, error) {
	img, e := imageEnvMap(ctx, p.client, c.Config.Image)
	if e != nil {
		return nil, e
	}
	env := make([]string, 0, len(img)+len(extra))
	for k, v := range img {
		env = append(env, k+"="+v)
	}
	requested, e := p.loadStagedEnv(c)
	if e != nil {
		return nil, e
	}
	env = append(env, requested...)
	env = append(env, extra...)
	// Docker permits duplicate env overrides; canonicalize before the gate so
	// PATH lookup and the actual executed process see the same final values.
	values := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	if _, ok := values["HOME"]; !ok {
		values["HOME"] = "/home/agent"
	}
	env = env[:0]
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	return env, nil
}

// WrapExternalExec is the optional backup/restore gate. Legacy calls preserve
// argv and environment; selected image commands first validate the current boot
// inside that boot's namespace, closing the host check-to-exec restart race.
func (p *Provider) WrapExternalExec(ctx context.Context, id string, cmd, env []string) ([]string, []string, error) {
	if p.stagedDiscoveryPending.Load() {
		if e := p.discoverStaged(ctx); e == nil {
			p.stagedDiscoveryPending.Store(false)
		}
	}
	if len(p.cfg.EgressFenceCrews) == 0 && !p.anyFenced() && !p.stagedDiscoveryPending.Load() {
		return cmd, env, nil
	}
	got, e := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if e != nil {
		return nil, nil, e
	}
	c := got.Container
	team := provider.CrewConfig{}
	if c.Config != nil {
		team.ID = c.Config.Labels[crewCrewIDLabel]
		team.Slug = c.Config.Labels[crewCrewLabel]
	}
	if !staged(c) {
		if p.egressFenceWanted(team) {
			return nil, nil, errStagedDenied
		}
		return cmd, env, nil
	}
	if e = p.auditStaged(ctx, c, team); e != nil {
		return nil, nil, e
	}
	status, e := p.stagedControl(ctx, c.ID, "status", "")
	if e != nil || status.Phase != "ready" {
		return nil, nil, fmt.Errorf("%w: workload gate requires ready keeper (observed %s)", errors.Join(errStagedDenied, e), status.Phase)
	}
	env, e = p.stagedWorkloadEnv(ctx, c, env)
	if e != nil {
		return nil, nil, e
	}
	return append([]string{stagedstart.Binary, "--staged-exec", status.Nonce}, cmd...), env, nil
}

// Already-created staged configurations retain the gate until explicitly
// drained/recreated, even if the operator removes the selector and restarts.
func (p *Provider) discoverStaged(ctx context.Context) error {
	if p.cfg.InstanceID == "" {
		return nil
	}
	list, e := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if e != nil {
		return e
	}
	for _, c := range list.Items {
		if c.Labels[stagedstart.Label] == stagedstart.Version && c.Labels[resourcelifecycle.InstanceLabel] == p.cfg.InstanceID && p.cfg.InstanceID != "" {
			p.fencedCrew.Store(c.ID, c.Labels[crewCrewIDLabel])
		}
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, io.ErrShortBuffer
	}
	b.remaining -= len(p)
	return b.Buffer.Write(p)
}
