//go:build integration && linux

package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
)

// Run against each supported local engine with an image that contains sh and
// awk. Each incarnation writes the host directory and checks the kernel flags.
func TestCrewDataSurvivesContainerReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	engine := "docker"
	if dockerutil.IsPodman(ctx, cli) {
		engine = "podman"
	}
	_, selinux, err := detectMountEnvironment(ctx, cli, engine)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	p := &Provider{client: cli, detected: DetectResult{Runtime: engine}, cfg: Config{OutputBasePath: base}, selinuxEnabled: selinux}
	source := p.crewDataSource("fixture", "/output")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	img := os.Getenv("CREWSHIP_DATA_MOUNT_TEST_IMAGE")
	if img == "" {
		img = "alpine:3"
	}
	for _, turn := range []string{"first", "second"} {
		mounts, binds, err := p.crewRuntimeBinds([]mount.Mount{noexecBindMount(source, "/output", "fixture", "fixture")}, false)
		if err != nil {
			t.Fatal(err)
		}
		host := &container.HostConfig{Mounts: mounts, Binds: binds}
		script := `set -eu
awk '$5 == "/output" && $6 ~ /(^|,)noexec(,|$)/ && $6 ~ /(^|,)nosuid(,|$)/ {ok=1} END {exit !ok}' /proc/self/mountinfo
if [ "$1" = second ]; then [ "$(cat /output/marker)" = first ]; fi
echo "$1" >> /output/marker`
		created, err := dockerutil.CreateCrewContainer(ctx, cli, client.ContainerCreateOptions{Config: &container.Config{Image: img, User: "0:0", Entrypoint: []string{"/bin/sh", "-c", script, "sh", turn}}, HostConfig: host})
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if _, err := cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
					t.Error(err)
				}
			}()
			if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
				t.Fatal(err)
			}
			wait := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
			select {
			case result := <-wait.Result:
				if result.StatusCode != 0 {
					t.Fatalf("%s: %s exited %d", engine, turn, result.StatusCode)
				}
			case err := <-wait.Error:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			inspected, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range inspected.Container.Mounts {
				if engine == "podman" && m.Destination == "/output" && !p.verifiedPodmanDataBind(inspected.Container.HostConfig, m, "fixture") {
					t.Fatal("engine did not retain the verified bind")
				}
			}
		}()
	}
	data, err := os.ReadFile(filepath.Join(source, "marker"))
	if err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("host data %q: %v", data, err)
	}
}
