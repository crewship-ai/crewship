package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/stagedstart"
)

var errStagedDenied = errors.New("staged start: runtime/configuration or boot evidence unavailable")

func overlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || a == "/" || b == "/"
}
func staged(c container.InspectResponse) bool {
	return c.Config != nil && slices.Equal(c.Config.Entrypoint, []string{stagedstart.Binary}) && slices.Equal(c.Config.Cmd, []string{"--staged-keeper"})
}

// stageArtifact reuses A1's immutable source publication and static ELF parser.
// Qualification reads the published bytes, never Docker's archive mount view.
func (p *Provider) stageArtifact(ctx context.Context, image string) (string, string, error) {
	source, e := p.stageTrustedFile(p.cfg.SidecarBinaryPath, managedlaunch.MaxArtifactBytes)
	if e != nil {
		return "", "", e
	}
	raw, e := os.ReadFile(source)
	if e != nil {
		return "", "", e
	}
	artifact, e := managedlaunch.Capture("/usr/local/bin/crewship-staged-artifact", raw)
	if e != nil {
		return "", "", fmt.Errorf("%w: %v", errStagedDenied, e)
	}
	f, e := elf.NewFile(bytes.NewReader(raw))
	if e != nil {
		return "", "", e
	}
	defer f.Close()
	img, e := p.client.ImageInspect(ctx, image)
	if e != nil {
		return "", "", e
	}
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[img.Architecture]
	if img.Config == nil || len(img.Config.Volumes) > 0 || machine == 0 || f.Machine != machine {
		return "", "", errStagedDenied
	}
	return source, artifact.SHA256, nil
}

// The same A1 immutable publisher also pins the host-owned bootstrap script.
// It is executed only after keeper admission, unlike the static helper binary.
func (p *Provider) stageTrustedFile(source string, limit int64) (string, error) {
	info, e := os.Lstat(source)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > limit || !stagedOwned(info) {
		return "", fmt.Errorf("%w: trusted host artifact missing or unsafe owner/mode/type", errStagedDenied)
	}
	if e = os.MkdirAll(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName), 0755); e != nil {
		return "", e
	}
	return stageManagedLauncher(source, p.cfg.OutputBasePath)
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
	if c.Config.Labels["crewship.keeper-sha256"] != digest {
		return fmt.Errorf("%w: keeper artifact generation changed; drained recreation required", errStagedDenied)
	}
	bootSource, e := p.stageTrustedFile(p.cfg.EntrypointPath, 1<<20)
	if e != nil {
		return e
	}
	bound, boot := false, false
	for _, m := range c.Mounts {
		switch m.Destination {
		case stagedstart.Binary:
			bound = m.Type == mount.TypeBind && m.Source == source && !m.RW
		case stagedstart.Bootstrap:
			boot = m.Type == mount.TypeBind && m.Source == bootSource && !m.RW
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

// Internal operations start only the qualified static binary. Image commands
// must use WrapExternalExec or provider Exec, never this bypass.
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
		return nil, e
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

func (p *Provider) ensureStagedStart(ctx context.Context, team provider.CrewConfig, id string) error {
	stageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	got, e := p.client.ContainerInspect(stageCtx, id, client.ContainerInspectOptions{})
	if e != nil {
		return e
	}
	c := got.Container
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
	// Mandatory new kernel readback, including ready reuse; cache is not a seal.
	targets, e := p.crewServiceTargets(stageCtx, team)
	if e != nil {
		return e
	}
	if _, e = p.runFenceHelperMode(stageCtx, team, c.ID, c.Config.Image, targets.dests, false); e != nil {
		return e
	}
	if p.stagedTestHook != nil {
		if e := p.stagedTestHook("after-check", c.ID); e != nil {
			return e
		}
	}
	current, e := p.client.ContainerInspect(stageCtx, c.ID, client.ContainerInspectOptions{})
	if e != nil || containerStartedAt(current.Container.State) != containerStartedAt(c.State) {
		return fmt.Errorf("%w: container start changed after fence readback", errStagedDenied)
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
		return fmt.Errorf("%w: bootstrap phase %s requires reconciliation", errStagedDenied, status.Phase)
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

func (p *Provider) stagedOwnership(ctx context.Context, team provider.CrewConfig, image string, dirs crewDirs, volumes []string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	source, _, e := p.stageArtifact(ctx, image)
	if e != nil {
		return e
	}
	img, e := imageEnvMap(ctx, p.client, image)
	if e != nil {
		return e
	}
	var mounts []mount.Mount
	var targets []string
	for _, dir := range []string{dirs.output, dirs.workspace, dirs.crew} {
		target := fmt.Sprintf("/mnt/init/%d", len(targets))
		role := "tree:"
		if dir == dirs.crew {
			role = "crew:"
		}
		targets = append(targets, role+target)
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: dir, Target: target})
	}
	for _, vol := range volumes {
		target := fmt.Sprintf("/mnt/init/%d", len(targets))
		targets = append(targets, "volume:"+target)
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: vol, Target: target})
	}
	mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: source, Target: stagedstart.Binary, ReadOnly: true})
	got, e := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "crewship-stage-init-" + strings.ToLower(rand.Text()), Config: &container.Config{Labels: resourcelifecycle.WithInstanceLabel(map[string]string{"managed-by": "crewship", crewCrewIDLabel: team.ID, "crewship.staged-init-helper": "true"}, p.cfg.InstanceID), Image: image, Env: keeperEnv(nil, img), User: "0:0", Entrypoint: []string{stagedstart.Binary}, Cmd: append([]string{"--staged-init"}, targets...), Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "FOWNER", "FSETID", "DAC_OVERRIDE"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: mounts}})
	if e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = p.client.ContainerRemove(cleanup, got.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, e = p.client.ContainerStart(ctx, got.ID, client.ContainerStartOptions{}); e != nil {
		return e
	}
	wait := p.client.ContainerWait(ctx, got.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			detail := "trusted initializer failed"
			if logs, e := p.client.ContainerLogs(ctx, got.ID, client.ContainerLogsOptions{ShowStderr: true}); e == nil {
				raw, _ := io.ReadAll(io.LimitReader(logs, 8192))
				logs.Close()
				for _, reason := range []string{"operation not permitted", "permission denied", "bad file descriptor", "invalid argument", "not a directory", "tree too deep", "invalid roots", "invalid role", "invalid root"} {
					if bytes.Contains(raw, []byte(reason)) {
						detail = reason
						break
					}
				}
			}
			return fmt.Errorf("%w: offline ownership initializer exited %d: %s", errStagedDenied, result.StatusCode, detail)
		}
		return nil
	case e := <-wait.Error:
		return e
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Preserve inherited image keys only as blank entries for the trusted keeper.
// Their values are restored by host-controlled exec after the boot is sealed.
func keeperEnv(env []string, img map[string]string) []string {
	keys := map[string]bool{}
	for k := range img {
		keys[k] = true
	}
	for _, entry := range env {
		k, _, _ := strings.Cut(entry, "=")
		keys[k] = true
	}
	out := []string{"PATH=/usr/bin:/bin", "HOME=/tmp"}
	for k := range keys {
		if k != "PATH" && k != "HOME" {
			out = append(out, k+"=")
		}
	}
	return out
}

// Use existing inventory without triggering its destructive migration helpers.
func (p *Provider) stagedLegacyCheck(ctx context.Context, team provider.CrewConfig) error {
	old, e := p.HasLegacyCrewResources(ctx, []provider.CrewRef{{ID: team.ID, Slug: team.Slug}})
	if e != nil {
		return e
	}
	if old {
		return fmt.Errorf("%w: legacy resources require an explicit drained migration", errStagedDenied)
	}
	return nil
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
