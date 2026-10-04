package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// This helper target is independent of the staged keeper protocol.
const stagedInitializerBinary = "/usr/local/bin/crewship-sidecar"

func (p *Provider) stagedOwnership(ctx context.Context, team provider.CrewConfig, image string, dirs crewDirs, volumes []string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Resolve a mutable reference once, before any qualification. All later
	// reads and creation use the same immutable Docker image identity.
	qualified, e := p.client.ImageInspect(ctx, image)
	if e != nil {
		return e
	}
	if strings.TrimSpace(qualified.ID) == "" {
		return errStagedDenied
	}
	image = qualified.ID
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
	mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: source, Target: stagedInitializerBinary, ReadOnly: true})
	got, e := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "crewship-stage-init-" + strings.ToLower(rand.Text()), Config: &container.Config{Labels: resourcelifecycle.WithInstanceLabel(map[string]string{"managed-by": "crewship", crewCrewIDLabel: team.ID, "crewship.staged-init-helper": "true"}, p.cfg.InstanceID), Image: image, Env: keeperEnv(nil, img), User: "0:0", Entrypoint: []string{stagedInitializerBinary}, Cmd: append([]string{"--staged-init"}, targets...), Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "FOWNER", "FSETID", "DAC_OVERRIDE"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: mounts}})
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
