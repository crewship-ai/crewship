package docker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/egressfence"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/stagedstart"
)

// Network-layer egress fence (#1368, pilot).
//
// The fence is an nftables table installed in the crew container's network
// namespace that lets only the sidecar's UID out (see internal/egressfence).
// It is installed by a one-shot helper container that joins the crew
// container's namespace (--network container:<id>) with CAP_NET_ADMIN and
// runs the bind-mounted crewship-sidecar binary with --fence-apply. The crew
// container never holds NET_ADMIN, so nothing inside it can remove the rules.
//
// A restart recreates the namespace and the fence with it is gone, so the
// fence is (re)installed after every start this provider performs, and the
// reuse path re-checks it against the container's StartedAt.
//
// Failure is closed: a crew that is meant to be fenced does not run unfenced.

// fenceSidecarUID is the UID the sidecar runs as (exec_sidecar.go execs it as
// 1002:1002 on every image), the only socket owner the fence lets out.
const fenceSidecarUID = "1002"

// fenceHelperLabel marks the one-shot helper so an inventory can tell it from
// a crew runtime during the second it exists.
const fenceHelperLabel = "crewship.egress-fence-helper"

const fenceHelperTimeout = 30 * time.Second

// errFenceUnsupported marks a crew the fence was requested for but cannot
// mean anything on. Returned instead of running the crew silently unfenced.
var errFenceUnsupported = errors.New("egress fence unsupported for this crew")

// egressFenceWanted reports whether team is on the pilot list. It does not
// decide whether the fence CAN be applied — see egressFenceApplicable.
func (p *Provider) egressFenceWanted(team provider.CrewConfig) bool {
	for _, c := range p.cfg.EgressFenceCrews {
		if c != "" && (c == team.Slug || c == team.ID) {
			return true
		}
	}
	return false
}

// egressFenceApplicable returns nil when the fence is meaningful for team, or
// an errFenceUnsupported-wrapped reason. Free crews have nothing to fence;
// privileged crews and non-runc runtimes break the UID-in-namespace premise.
func (p *Provider) egressFenceApplicable(team provider.CrewConfig) error {
	if !strings.EqualFold(team.NetworkMode, "restricted") {
		return fmt.Errorf("%w: network_mode is %q, the fence applies to restricted crews", errFenceUnsupported, team.NetworkMode)
	}
	if team.Privileged {
		return fmt.Errorf("%w: privileged crew (the 1001/1002 UID boundary the fence rests on does not hold)", errFenceUnsupported)
	}
	if len(team.Services) > 0 && !p.crewNetworkWanted(team.ID, team.Slug) {
		// The fence opens exact service endpoints (address, protocol,
		// port). On the shared network a restarted service's old address
		// could be handed to another crew's container before the rules
		// follow; on the crew's own network only that crew's containers
		// can ever hold one of its addresses. So: services under the fence
		// require the crew's own network (container.crew_network_crews).
		return fmt.Errorf("%w: crew declares services; under the fence they need the crew's own network (add it to container.crew_network_crews)", errFenceUnsupported)
	}
	if rt := p.ociRuntime(); rt != "runc" {
		return fmt.Errorf("%w: runtime %q (the fence is verified on runc only)", errFenceUnsupported, rt)
	}
	return nil
}

// ensureEgressFence installs the fence on containerID when team is on the
// pilot list and the fence is not already in place for the container's
// current start. image is the crew container's image, reused for the helper
// because it is guaranteed to be present locally.
func (p *Provider) ensureEgressFence(ctx context.Context, team provider.CrewConfig, containerID, image string) error {
	if !p.egressFenceWanted(team) {
		if _, ok := p.fencedCrew.Load(containerID); !ok {
			return nil
		}
	}
	if err := p.egressFenceApplicable(team); err != nil {
		return err
	}
	return p.ensureStagedStart(ctx, team, containerID)
}

// installEgressFence installs the fence for team on containerID unless it is
// already confirmed for the container's current start. It does not ask
// whether the crew is on the list: the exec guard calls it for a container
// this provider fenced before, whatever the list or labels say now.
func (p *Provider) installEgressFence(ctx context.Context, team provider.CrewConfig, containerID, image string) error {
	if err := p.egressFenceApplicable(team); err != nil {
		return err
	}
	inspect, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("egress fence: inspect crew container: %w", err)
	}
	containerID = inspect.Container.ID // canonical: callers may pass a name
	startedAt := containerStartedAt(inspect.Container.State)
	targets, err := p.crewServiceTargets(ctx, team)
	if err != nil {
		return fmt.Errorf("egress fence: %w", err)
	}
	key := targets.key()
	if p.fenceCurrent(containerID, startedAt, key) {
		return nil
	}
	if image == "" && inspect.Container.Config != nil {
		image = inspect.Container.Config.Image
	}
	// One helper per container at a time: concurrent execs after an outside
	// restart would otherwise each start one. The second waiter finds the
	// first one's record and returns.
	mu, _ := p.fenceLocks.LoadOrStore(containerID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if p.fenceCurrent(containerID, startedAt, key) {
		return nil
	}
	began := time.Now()
	out, err := p.runFenceHelper(ctx, team, containerID, image, targets.dests)
	if err != nil {
		return fmt.Errorf("egress fence: %w", err)
	}
	p.fenceDests.Store(containerID, key)
	p.fenced.Store(containerID, startedAt)
	p.fencedCrew.Store(containerID, team.ID)
	p.fenceTeams.Store(team.ID, team)
	p.logger.Info("egress fence installed",
		"crew_id", team.ID,
		"container_id", shortID(containerID),
		"result", strings.TrimSpace(out),
		"duration_ms", time.Since(began).Milliseconds(),
	)
	return nil
}

// runFenceHelper runs the one-shot helper. A non-zero exit is an error.
func (p *Provider) runFenceHelper(ctx context.Context, team provider.CrewConfig, containerID, image string, dests []egressfence.Dest) (string, error) {
	return p.runFenceHelperMode(ctx, team, containerID, image, dests, true)
}
func (p *Provider) runFenceHelperMode(ctx context.Context, team provider.CrewConfig, containerID, image string, dests []egressfence.Dest, apply bool) (string, error) {
	mode := "--fence-check"
	if apply {
		mode = "--fence-apply"
	}
	cmd := []string{mode, "--fence-allow-uids", fenceSidecarUID}
	if len(dests) > 0 {
		parts := make([]string, len(dests))
		for i, d := range dests {
			parts[i] = d.String()
		}
		cmd = append(cmd, "--fence-allow-dests", strings.Join(parts, ","))
	}
	if p.cfg.SidecarBinaryPath == "" {
		return "", errors.New("no sidecar binary path configured; the helper runs the bind-mounted crewship-sidecar")
	}
	// Legacy uses its existing fixed helper name. Staged helpers use unique
	// owned labels/names so concurrent controllers never remove each other.
	// Unknown creates remain discoverable for explicit scoped cleanup.
	source := p.cfg.SidecarBinaryPath
	env := []string(nil)
	_, stagedKnown := p.fencedCrew.Load(containerID)
	if p.egressFenceWanted(team) || stagedKnown {
		qualified, e := p.client.ImageInspect(ctx, image)
		if e != nil {
			return "", e
		}
		if qualified.ID == "" {
			return "", errStagedDenied
		}
		image = qualified.ID
		source, _, e = p.stageArtifact(ctx, image)
		if e != nil {
			return "", e
		}
		img, e := imageEnvMap(ctx, p.client, image)
		if e != nil {
			return "", e
		}
		env = keeperEnv(nil, img)
	}
	helperName := "crewship-fence-" + shortID(containerID)
	if p.egressFenceWanted(team) || stagedKnown {
		helperName += "-" + strings.ToLower(rand.Text())
	}
	if _, err := p.client.ContainerRemove(ctx, helperName, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		p.logger.Debug("stale fence helper not removed", "helper", helperName, "error", err)
	}
	helperSpec := client.ContainerCreateOptions{
		Name: helperName,
		Config: &container.Config{
			Image:       image,
			Env:         env,
			Healthcheck: &container.HealthConfig{Test: []string{"NONE"}},
			User:        "0:0",
			Entrypoint:  []string{"/usr/local/bin/crewship-sidecar"},
			Cmd:         cmd,
			Labels: resourcelifecycle.WithInstanceLabel(map[string]string{
				"managed-by":     "crewship",
				fenceHelperLabel: "true",
				crewCrewIDLabel:  team.ID,
			}, p.cfg.InstanceID),
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode("container:" + containerID),
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"NET_ADMIN"},
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: true,
			Runtime:        "runc",
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   source,
				Target:   "/usr/local/bin/crewship-sidecar",
				ReadOnly: true,
			}},
		},
	}
	if p.stagedFenceHelperTestHook != nil {
		p.stagedFenceHelperTestHook(helperSpec.Config, helperSpec.HostConfig)
	}
	created, err := p.client.ContainerCreate(ctx, helperSpec)
	if err != nil {
		return "", fmt.Errorf("create helper: %w", err)
	}
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, rmErr := p.client.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true}); rmErr != nil {
			p.logger.Warn("egress fence helper not removed", "helper", shortID(created.ID), "error", rmErr)
		}
	}()
	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start helper: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, fenceHelperTimeout)
	defer cancel()
	if p.stagedTestHook != nil && (p.egressFenceWanted(team) || stagedKnown) {
		if e := p.stagedTestHook(mode+"-started", containerID); e != nil {
			return "", e
		}
	}
	wait := p.client.ContainerWait(waitCtx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var code int64
	select {
	case status, ok := <-wait.Result:
		if !ok {
			return "", errors.New("helper wait channel closed before a status was delivered")
		}
		code = status.StatusCode
	case werr := <-wait.Error:
		return "", fmt.Errorf("wait for helper: %w", werr)
	case <-waitCtx.Done():
		return "", fmt.Errorf("wait for helper: %w", waitCtx.Err())
	}
	// The helper's output is not read back: ContainerLogs is outside the
	// published socket-proxy surface (scripts/docker-api-surface), and the
	// exit code carries the verdict (cmd/crewship-sidecar/fence.go).
	switch code {
	case 0:
		return "fence present", nil
	case 3:
		return "", errors.New("helper reports the fence absent after apply")
	default:
		return "", fmt.Errorf("helper exited %d (the sidecar logs the cause to the helper's stderr)", code)
	}
}

// errFenceNotInPlace refuses an exec into a fenced crew's container whose
// current start has no confirmed fence.
var errFenceNotInPlace = errors.New("egress fence not in place for this container start")

// fencedExec is what guardFencedExec confirmed for one exec: the container
// and the start (StartedAt) the fence was verified for. The zero value means
// "not a fenced crew", and the follow-up checks are no-ops for it.
type fencedExec struct {
	containerID string
	team        provider.CrewConfig
	startedAt   string
}

func (f fencedExec) active() bool { return f.containerID != "" }

// fenceExecStage names the points between the exec checks; tests use
// fenceTestHook to restart the container at exactly one of them.
type fenceExecStage string

const (
	// fenceStageAfterGuard is between the guard and ExecCreate.
	fenceStageAfterGuard  fenceExecStage = "after-guard"
	fenceStageBeforeStart fenceExecStage = "before-start"
	fenceStageAfterStart  fenceExecStage = "after-start"
)

// guardFencedExec runs before every Exec / ExecInteractive. Code reaches a
// crew container only through an exec — the entrypoint is crewship's own
// script and ends in `sleep infinity` — so refusing the exec until the fence
// is confirmed for the container's CURRENT start closes the window a restart
// outside EnsureCrewRuntime opens (daemon, restart policy, operator), for
// every caller including the ones that never go through EnsureCrewRuntime,
// such as the web terminal and the orchestrator's cached-container path.
// When the provider has fenced this crew before it re-installs the fence for
// the new start before letting the exec through; a container id alone does
// not carry the settings (network mode, privileged) the install decision
// needs, so without that record it refuses.
//
// A restart can still land between this check and the exec starting, so the
// confirmed start is re-checked by fenceBeforeStart (after ExecCreate, before
// the exec runs) and fenceAfterStart (once it runs).
//
// Free when the pilot list is empty: no inspect, no behaviour change.
func (p *Provider) guardFencedExec(ctx context.Context, containerID string) (fencedExec, error) {
	if len(p.cfg.EgressFenceCrews) == 0 && !p.anyFenced() {
		return fencedExec{}, nil
	}
	insp, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fencedExec{}, fmt.Errorf("egress fence: inspect before exec: %w", err)
	}
	c := insp.Container
	if c.Config == nil {
		return fencedExec{}, nil
	}
	team := provider.CrewConfig{ID: c.Config.Labels[crewCrewIDLabel], Slug: c.Config.Labels[crewCrewLabel]}
	if p.egressFenceWanted(team) && c.Config.Labels[stagedstart.Label] != stagedstart.Version {
		return fencedExec{}, errStagedDenied
	}
	// Labels can be missing (pre-label containers) or stale (a renamed
	// slug); a container this provider has ever fenced stays fenced by id.
	if crewID, ok := p.fencedCrew.Load(c.ID); ok {
		if known, ok := p.fenceTeams.Load(crewID); ok {
			team = known.(provider.CrewConfig)
		}
	} else if !p.egressFenceWanted(team) {
		return fencedExec{}, nil
	}
	// Callers pass a container id OR a name (the file-save path uses the
	// name); the fence record is keyed by the canonical id, so look it up by
	// what the daemon resolved, never by the caller's spelling.
	id := c.ID
	startedAt := containerStartedAt(c.State)
	if prev, ok := p.fenced.Load(id); ok && startedAt != "" && prev.(string) == startedAt {
		if p.fenceTestHook != nil {
			p.fenceTestHook(fenceStageAfterGuard)
		}
		return fencedExec{containerID: id, team: team, startedAt: startedAt}, nil
	}
	// The container was (re)started without a confirmed fence. If this
	// provider fenced the crew before, it knows the settings the install
	// decision needs: re-install now and let the exec through only on
	// success. Otherwise (e.g. after a server restart) refuse; the next
	// EnsureCrewRuntime installs it.
	if v, ok := p.fenceTeams.Load(team.ID); ok && c.State != nil && c.State.Running {
		known := v.(provider.CrewConfig)
		image := ""
		if c.Config != nil {
			image = c.Config.Image
		}
		if err := p.installEgressFence(ctx, known, id, image); err != nil {
			p.stopUnfenced(ctx, known, id, err)
			return fencedExec{}, err
		}
		if p.fenceTestHook != nil {
			p.fenceTestHook(fenceStageAfterGuard)
		}
		return fencedExec{containerID: id, team: known, startedAt: startedAt}, nil
	}
	return fencedExec{}, fmt.Errorf("%w (crew %s, container %s); the next crew start installs it", errFenceNotInPlace, team.ID, shortID(containerID))
}

// fenceBeforeStart re-checks, after ExecCreate and before the exec runs, that
// the container is still in the start the fence was confirmed for. Docker
// binds an exec to the container, not to one start of it, so an exec created
// before a restart would run in the new, unfenced namespace.
func (p *Provider) fenceBeforeStart(ctx context.Context, f fencedExec) error {
	if !f.active() {
		return nil
	}
	if p.fenceTestHook != nil {
		p.fenceTestHook(fenceStageBeforeStart)
	}
	return p.fenceSameStart(ctx, f)
}

// fenceAfterStart re-checks once the exec runs. A restart that landed between
// fenceBeforeStart and the start means the process may be running unfenced;
// the container is stopped, which kills it. Fail closed: the crew is down,
// not open.
func (p *Provider) fenceAfterStart(ctx context.Context, f fencedExec) error {
	if !f.active() {
		return nil
	}
	if p.fenceTestHook != nil {
		p.fenceTestHook(fenceStageAfterStart)
	}
	if err := p.fenceSameStart(ctx, f); err != nil {
		// The exec may already be running in an unconfirmed start: stop the
		// crew whatever the reason, including a cancelled caller.
		p.stopCrewContainer(ctx, f.team, f.containerID, err)
		return err
	}
	return nil
}

func (p *Provider) fenceSameStart(ctx context.Context, f fencedExec) error {
	// Not the caller's context: a caller that gives up between the exec
	// starting and this check must not turn "could not check" into "fine".
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	insp, err := p.client.ContainerInspect(checkCtx, f.containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("egress fence: inspect around exec: %w", err)
	}
	if containerStartedAt(insp.Container.State) != f.startedAt {
		p.fenced.Delete(f.containerID)
		return fmt.Errorf("%w (crew %s, container %s restarted during the exec)", errFenceNotInPlace, f.team.ID, shortID(f.containerID))
	}
	return nil
}

func containerStartedAt(st *container.State) string {
	if st == nil {
		return ""
	}
	return st.StartedAt
}

// stopUnfenced stops a crew container whose fence could not be installed, so
// nothing reaches it through a path that skips EnsureCrewRuntime. A crew the
// fence does not apply to (errFenceUnsupported) is stopped too: the operator
// asked for it, and running it unfenced would be the silent downgrade #1368
// forbids.
func (p *Provider) stopUnfenced(ctx context.Context, team provider.CrewConfig, containerID string, cause error) {
	if errors.Is(cause, provider.ErrRuntimeImageUpdatePending) {
		p.logger.Warn("staged runtime revision pending; active work retained, drain and stop the crew for controlled replacement", "crew_id", team.ID, "container_id", containerID, "error", cause)
		return
	}
	if errors.Is(cause, errStagedDenied) && !errors.Is(cause, errStagedBootstrapFailed) {
		p.fenced.Delete(containerID)
		p.stagedVerified.Delete(containerID)
		p.evictWarm(team.ID)
		p.logger.Error("staged runtime rejected; workload exec admission refused; runtime retained for explicit ownership inspection and drained recreation", "crew_id", team.ID, "container_id", containerID, "error", cause)
		return
	}
	if ctx.Err() != nil {
		// The caller's context ended (a cancelled run, a request deadline):
		// nothing is known to be wrong with the fence, and stopping would kill
		// every other run on the crew. Still closed: without a confirmed
		// record for this start, guardFencedExec refuses every exec. Decided
		// on the caller's context, so the helper's own timeout still stops.
		p.logger.Warn("egress fence check interrupted by the caller; crew left running, execs stay refused until confirmed",
			"crew_id", team.ID, "container_id", shortID(containerID), "error", cause)
		return
	}
	p.stopCrewContainer(ctx, team, containerID, cause)
}

// stopCrewContainer stops a crew container whose fence is not confirmed,
// unconditionally.
func (p *Provider) stopCrewContainer(ctx context.Context, team provider.CrewConfig, containerID string, cause error) {
	p.logger.Error("crew requires the egress fence and it is not in place; stopping the crew container",
		"crew_id", team.ID, "container_id", shortID(containerID), "error", cause)
	p.fenced.Delete(containerID)
	p.stagedVerified.Delete(containerID)
	// Drop the warm entry too: the next EnsureCrewRuntime must take the full
	// reconcile path (start + fence), not hand back a stopped container.
	p.evictWarm(team.ID)
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	timeout := 5
	if _, err := p.client.ContainerStop(stopCtx, containerID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
		p.logger.Warn("could not stop unfenced crew container", "container_id", shortID(containerID), "error", err)
	}
}

// anyFenced reports whether this provider has fenced any container still
// on record.
func (p *Provider) anyFenced() bool {
	has := false
	p.fencedCrew.Range(func(_, _ any) bool { has = true; return false })
	return has
}

// forgetFenced drops every record kept for a removed container. Each map is
// walked on its own: a stop deletes the confirmed-start entry (fenced) first,
// so the others cannot be found through it.
func (p *Provider) forgetFenced(containerID string) {
	if containerID == "" {
		return
	}
	for _, m := range []*sync.Map{&p.fenced, &p.fenceLocks, &p.fencedCrew, &p.stagedVerified} {
		m.Range(func(k, _ any) bool {
			if id := k.(string); id == containerID || strings.HasPrefix(id, containerID) {
				m.Delete(k)
			}
			return true
		})
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// GuardExternalExec is the exec guard for callers that exec into crew
// containers with their own Docker client (backup flush hooks, restore and
// memory-import tar): the same three checks as Exec — before ExecCreate,
// before the exec starts, and once it runs (a restart there stops the crew).
// Wired into backup.MobyDockerOps.Guard by the server.
func (p *Provider) GuardExternalExec(ctx context.Context, containerID string) (beforeStart, afterStart func(context.Context) error, err error) {
	f, err := p.guardFencedExec(ctx, containerID)
	if err != nil {
		return nil, nil, err
	}
	if _, stagedKnown := p.fencedCrew.Load(f.containerID); stagedKnown {
		return nil, nil, fmt.Errorf("%w: staged external exec requires PrepareExternalExec", errStagedDenied)
	}

	return func(ctx context.Context) error { return p.fenceBeforeStart(ctx, f) },
		func(ctx context.Context) error { return p.fenceAfterStart(ctx, f) }, nil
}

// PrepareExternalExec combines admission and wrapping for external clients.
// A nonpilot command uses only the existing guard's inspect. The returned
// checks still bind Docker exec to that exact inspected start.
func (p *Provider) PrepareExternalExec(ctx context.Context, id string, cmd, env []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error) {
	f, err := p.guardFencedExec(ctx, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if f.active() || p.stagedDiscoveryPending.Load() {
		cmd, env, err = p.WrapExternalExec(ctx, id, cmd, env)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return cmd, env, func(ctx context.Context) error { return p.fenceBeforeStart(ctx, f) },
		func(ctx context.Context) error { return p.fenceAfterStart(ctx, f) }, nil
}

// serviceTargets are a crew's own service endpoints the fence opens.
type serviceTargets struct {
	dests []egressfence.Dest
}

// key identifies the exact endpoint set, so a changed service address
// re-installs the fence even within one container start.
func (t serviceTargets) key() string {
	parts := make([]string, 0, len(t.dests))
	for _, d := range t.dests {
		parts = append(parts, d.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// fenceCurrent reports whether containerID's fence is confirmed for this
// start and this service endpoint set.
func (p *Provider) fenceCurrent(containerID, startedAt, key string) bool {
	prev, ok := p.fenced.Load(containerID)
	if !ok || startedAt == "" || prev.(string) != startedAt {
		return false
	}
	have, _ := p.fenceDests.Load(containerID)
	haveKey, _ := have.(string)
	return haveKey == key
}

// crewServiceTargets lists the endpoints the fence opens for a crew's own
// services: each declared service's FIXED address on the crew's own network
// (serviceAddrs), with its declared ports plus, once the service runs, the
// ports its image exposes. Fixed addresses are never handed to another
// container (dynamic allocation is limited to the upper half of the subnet).
func (p *Provider) crewServiceTargets(ctx context.Context, team provider.CrewConfig) (serviceTargets, error) {
	t := serviceTargets{}
	if len(team.Services) == 0 {
		return t, nil
	}
	subnet, ok, err := p.crewSubnet(ctx, team.ID, team.Slug)
	if err != nil {
		return t, err
	}
	if !ok {
		return t, fmt.Errorf("crew declares services but is not on its own network")
	}
	addrs, err := serviceAddrs(subnet, serviceNames(team))
	if err != nil {
		return t, err
	}
	seen := map[string]bool{}
	add := func(d egressfence.Dest) {
		if !seen[d.String()] {
			seen[d.String()] = true
			t.dests = append(t.dests, d)
		}
	}
	for _, svc := range team.Services {
		for _, raw := range svc.Ports {
			if port, err := dockernetwork.ParsePort(raw); err == nil {
				add(egressfence.Dest{Addr: addrs[svc.Name], Port: port.Num(), Proto: string(port.Proto())})
			}
		}
	}
	list, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return t, fmt.Errorf("list services: %w", err)
	}
	for _, c := range list.Items {
		name, ok := matchCrewService(c.Labels, team.ID)
		if !ok || c.State != "running" {
			continue
		}
		addr, declared := addrs[name]
		if !declared {
			continue
		}
		insp, err := p.client.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			return t, fmt.Errorf("inspect service %q: %w", name, err)
		}
		if insp.Container.Config == nil {
			continue
		}
		for port := range insp.Container.Config.ExposedPorts {
			add(egressfence.Dest{Addr: addr, Port: port.Num(), Proto: string(port.Proto())})
		}
	}
	sort.Slice(t.dests, func(i, j int) bool { return t.dests[i].String() < t.dests[j].String() })
	return t, nil
}

// refreshFenceServices re-reads a fenced crew's services after they were
// ensured and re-installs the fence when their endpoints changed: the new
// address opens, the old one closes, in one atomic rule swap.
func (p *Provider) refreshFenceServices(ctx context.Context, team provider.CrewConfig) error {
	if !p.egressFenceWanted(team) {
		return nil
	}
	cid, running, err := p.FindCrewContainer(ctx, team.ID, team.Slug)
	if err != nil || cid == "" || !running {
		return err
	}
	if err := p.installEgressFence(ctx, team, cid, ""); err != nil {
		p.stopUnfenced(ctx, team, cid, err)
		return err
	}
	return nil
}
