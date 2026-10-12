//go:build integration

package docker

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Use a fresh installation id and only remove immutable ids/names created by
// this fixture. No provider startup sweeps, shared names or global pruning.
func TestBindVolumeLifecycleRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pingCtx, stopPing := context.WithTimeout(ctx, 5*time.Second)
	_, err = d.Ping(pingCtx, client.PingOptions{})
	stopPing()
	if err != nil {
		// The integration build tag opts into real Docker; missing prerequisites
		// must fail rather than report unexecuted lifecycle coverage as success.
		t.Fatalf("integration requires a live Docker daemon: %v", err)
	}
	image, err := d.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration requires local alpine:3: %v", err)
	}
	instance := "bind-volume-test-" + strings.ToLower(rand.Text())
	var containers, volumes []string
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, id := range containers {
			_, _ = d.ContainerRemove(cleanup, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
		for _, name := range volumes {
			if _, err := d.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{Force: false}); err != nil && !cerrdefs.IsNotFound(err) {
				t.Errorf("fixture cleanup volume %s: %v", name, err)
			}
		}
	}()
	home := instance + "-home"
	tools := instance + "-tools"
	for _, name := range []string{home, tools} {
		created, err := d.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		volumes = append(volumes, created.Volume.Name)
	}
	base := t.TempDir()
	paths := []string{filepath.Join(base, "workspace"), filepath.Join(base, "output"), filepath.Join(base, "crew")}
	for _, path := range paths {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "history"), []byte("preserved"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	rt := &cleanupRuntime{client: d}
	// Also leave an orphan from another installation. Even with identical
	// driver options it must survive this installation's reaper.
	foreign := noexecBindMount(paths[2], "/crew", "crew", instance+"-foreign")
	createdForeign, err := d.VolumeCreate(ctx, client.VolumeCreateOptions{Driver: "local", DriverOpts: foreign.VolumeOptions.DriverConfig.Options, Labels: foreign.VolumeOptions.Labels})
	if err != nil {
		t.Fatal(err)
	}
	volumes = append(volumes, createdForeign.Volume.Name)
	for cycle := 0; cycle < 3; cycle++ {
		mounts := []mount.Mount{
			noexecBindMount(paths[0], "/workspace", "crew", instance),
			noexecBindMount(paths[1], "/output", "crew", instance),
			noexecBindMount(paths[2], "/crew", "crew", instance),
			{Type: mount.TypeVolume, Source: home, Target: "/home/agent"},
			{Type: mount.TypeVolume, Source: tools, Target: "/opt/crew-tools"},
		}
		created, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{
			Config:     &container.Config{Image: image.ID, Cmd: []string{"sh", "-c", `test "$(cat /workspace/history)" = preserved && test "$(cat /output/history)" = preserved && test "$(cat /crew/history)" = preserved && if [ -f /home/agent/history ]; then test "$(cat /home/agent/history)" = preserved && test "$(cat /opt/crew-tools/history)" = preserved; fi && echo preserved > /home/agent/history && echo preserved > /opt/crew-tools/history`}},
			HostConfig: &container.HostConfig{Mounts: mounts, NetworkMode: "none"},
		})
		if err != nil {
			t.Fatal(err)
		}
		containers = append(containers, created.ID)
		inspected, err := d.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var bindNames []string
		for _, m := range inspected.Container.Mounts {
			if agentWritableNoexecTargets[m.Destination] {
				bindNames = append(bindNames, m.Name)
				volumes = append(volumes, m.Name)
				v, err := d.VolumeInspect(ctx, m.Name, client.VolumeInspectOptions{})
				if err != nil || !eligibleBindVolume(v.Volume, instance) {
					t.Fatalf("unattributed bind volume %+v: %v", v.Volume, err)
				}
			}
		}
		if len(bindNames) != 3 {
			t.Fatalf("bind records %v", bindNames)
		}
		// A created/stopped container's references also prevent reaping.
		if err := rt.ReapBindVolumes(ctx, instance, 100); err != nil {
			t.Fatal(err)
		}
		for _, name := range bindNames {
			if _, err := d.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err != nil {
				t.Fatalf("attached volume lost: %v", err)
			}
		}
		if _, err := d.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		wait := d.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case res, ok := <-wait.Result:
			if !ok || res.StatusCode != 0 {
				t.Fatalf("history verification failed: %+v", res)
			}
		case err := <-wait.Error:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if err := rt.Remove(ctx, created.ID); err != nil {
			t.Fatal(err)
		}
		if err := rt.ReapBindVolumes(ctx, instance, 100); err != nil {
			t.Fatal(err)
		}
		for _, name := range bindNames {
			if _, err := d.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
				t.Fatalf("cycle %d leaked bind record %s: %v", cycle, name, err)
			}
		}
		for _, name := range []string{home, tools, createdForeign.Volume.Name} {
			if _, err := d.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err != nil {
				t.Fatalf("persistent/foreign volume lost: %v", err)
			}
		}
		for _, path := range paths {
			if b, err := os.ReadFile(filepath.Join(path, "history")); err != nil || string(b) != "preserved" {
				t.Fatalf("host history lost at %s: %q %v", path, b, err)
			}
		}
	}
}
