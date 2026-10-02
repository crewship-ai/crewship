package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
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
		return nil
	}
	if err := p.egressFenceApplicable(team); err != nil {
		return err
	}
	inspect, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("egress fence: inspect crew container: %w", err)
	}
	startedAt := ""
	if inspect.Container.State != nil {
		startedAt = inspect.Container.State.StartedAt
	}
	if prev, ok := p.fenced.Load(containerID); ok && prev.(string) == startedAt && startedAt != "" {
		return nil
	}
	if image == "" && inspect.Container.Config != nil {
		image = inspect.Container.Config.Image
	}
	began := time.Now()
	out, err := p.runFenceHelper(ctx, team, containerID, image)
	if err != nil {
		return fmt.Errorf("egress fence: %w", err)
	}
	p.fenced.Store(containerID, startedAt)
	p.logger.Info("egress fence installed",
		"crew_id", team.ID,
		"container_id", shortID(containerID),
		"result", strings.TrimSpace(out),
		"duration_ms", time.Since(began).Milliseconds(),
	)
	return nil
}

// runFenceHelper runs the one-shot helper. A non-zero exit is an error.
func (p *Provider) runFenceHelper(ctx context.Context, team provider.CrewConfig, containerID, image string) (string, error) {
	if p.cfg.SidecarBinaryPath == "" {
		return "", errors.New("no sidecar binary path configured; the helper runs the bind-mounted crewship-sidecar")
	}
	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      image,
			User:       "0:0",
			Entrypoint: []string{"/usr/local/bin/crewship-sidecar"},
			Cmd:        []string{"--fence-apply", "--fence-allow-uids", fenceSidecarUID},
			Labels: map[string]string{
				"managed-by":     "crewship",
				fenceHelperLabel: "true",
				crewCrewIDLabel:  team.ID,
			},
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
				Source:   p.cfg.SidecarBinaryPath,
				Target:   "/usr/local/bin/crewship-sidecar",
				ReadOnly: true,
			}},
		},
	})
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

// stopUnfenced stops a crew container whose fence could not be installed, so
// nothing reaches it through a path that skips EnsureCrewRuntime. A crew the
// fence does not apply to (errFenceUnsupported) is stopped too: the operator
// asked for it, and running it unfenced would be the silent downgrade #1368
// forbids.
func (p *Provider) stopUnfenced(ctx context.Context, team provider.CrewConfig, containerID string, cause error) {
	p.logger.Error("crew requires the egress fence and it is not in place; stopping the crew container",
		"crew_id", team.ID, "container_id", shortID(containerID), "error", cause)
	p.fenced.Delete(containerID)
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	timeout := 5
	if _, err := p.client.ContainerStop(stopCtx, containerID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
		p.logger.Warn("could not stop unfenced crew container", "container_id", shortID(containerID), "error", err)
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
