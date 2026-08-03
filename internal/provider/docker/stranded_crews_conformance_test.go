//go:build conformance

// A real crew container, stranded on a real second daemon (#1704).
//
// The unit tests prove the sweep's logic against a fake container list. This
// proves the thing the issue is actually about: a crew container left running
// on the daemon Crewship used to be on, with write access to the same host
// /crew directory the live crew reads — and that after the sweep it no longer
// has it.
//
// It asserts the write access, not the container state. "Stopped" is a status;
// "an agent in the old container can no longer write the memory the new one
// reads" is the property that matters, and the two are only the same thing if
// the stop actually took.
//
// Run (needs two reachable daemons):
//
//	CREWSHIP_CONFORMANCE_DAEMON_A=unix://$HOME/.orbstack/run/docker.sock \
//	CREWSHIP_CONFORMANCE_DAEMON_B=unix://$HOME/.colima/default/docker.sock \
//	  go test -tags conformance -run TestStrandedCrewIsFoundAndDisarmed -v ./internal/provider/docker/

package docker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestStrandedCrewIsFoundAndDisarmed(t *testing.T) {
	oldDaemon := os.Getenv("CREWSHIP_CONFORMANCE_DAEMON_A") // where the crew was started
	newDaemon := os.Getenv("CREWSHIP_CONFORMANCE_DAEMON_B") // where Crewship moved to
	if oldDaemon == "" || newDaemon == "" {
		t.Fatal("set CREWSHIP_CONFORMANCE_DAEMON_A (the runtime switched away FROM) and CREWSHIP_CONFORMANCE_DAEMON_B (switched TO) — a runtime switch needs two runtimes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	image := os.Getenv("CREWSHIP_CONFORMANCE_IMAGE")
	if image == "" {
		image = "debian:bookworm-slim"
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The crew data dir has to be bindable from the OLD daemon. Reuse the
	// reachability probe rather than guessing.
	base := strandedDataDir(ctx, t, oldDaemon)
	t.Logf("crew data dir (shared with the old daemon): %s", base)
	defer os.RemoveAll(base)

	sidecar := filepath.Join(base, "crewship-sidecar")
	if err := os.WriteFile(sidecar, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o755); err != nil {
		t.Fatal(err)
	}
	entrypoint := filepath.Join(base, "entrypoint.sh")
	src, err := os.ReadFile(repoFile(t, "scripts", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, src, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		OutputBasePath:  filepath.Join(base, "output"),
		ContainerPrefix: "crewship-conf1704",
		// Deliberately NOT staged: this test is about #1704, and staging is
		// #1706's business. The paths are already under a shared base.
		SidecarBinaryPath: sidecar,
		EntrypointPath:    entrypoint,
	}

	oldCli := dialOrFail(ctx, t, oldDaemon)
	defer oldCli.Close()
	newCli := dialOrFail(ctx, t, newDaemon)
	defer newCli.Close()

	old := &Provider{
		client:         oldCli,
		cfg:            cfg,
		logger:         logger,
		detected:       DetectResult{Runtime: "old", Socket: oldDaemon, Host: oldDaemon},
		digestResolver: dockerutil.NewDigestResolver(0, 0),
	}
	if err := old.ensureImage(ctx, image); err != nil {
		t.Fatalf("pull %s on the old daemon: %v", image, err)
	}

	crew := provider.CrewConfig{ID: "conf1704crew", Slug: "stranded", MemoryMB: 512, CPUs: 1}
	dirs, err := old.prepareCrewDirs(crew)
	if err != nil {
		t.Fatalf("prepare crew dirs: %v", err)
	}
	old.fixBindMountOwnership(ctx, image, dirs)
	stageCrewVolumes(ctx, t, old, image, crew)

	name := old.CrewContainerName(crew.ID, crew.Slug)
	_, _ = oldCli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	ccfg, hostCfg, err := old.buildCrewContainerConfig(ctx, crew, name, image, "", 512, 1, dirs)
	if err != nil {
		t.Fatalf("build crew container config: %v", err)
	}
	created, err := oldCli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: ccfg, HostConfig: hostCfg, NetworkingConfig: &dockernetwork.NetworkingConfig{}, Name: name,
	})
	if err != nil {
		t.Fatalf("create the crew on the old daemon: %v", old.explainBindFailure(err))
	}
	defer func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), time.Minute)
		defer rmCancel()
		_, _ = oldCli.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		for _, v := range []string{old.homeVolumeName(crew.ID, crew.Slug), old.toolsVolumeName(crew.ID, crew.Slug)} {
			_, _ = oldCli.VolumeRemove(rmCtx, v, client.VolumeRemoveOptions{Force: true})
		}
	}()
	if _, err := oldCli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start the crew on the old daemon: %v", err)
	}
	waitForPID1(ctx, t, old, created.ID)

	// The hazard, demonstrated before it is removed: this container can write
	// the host /crew tree, which is where agent memory lives.
	const marker = "WRITTEN-BY-THE-STRANDED-CONTAINER"
	if _, err := execInContainer(ctx, old, created.ID, `echo `+marker+` > /crew/stranded-write.txt`); err != nil {
		t.Fatalf("the stranded container could not write /crew, so this test is not reproducing the hazard: %v", err)
	}
	onHost, err := os.ReadFile(filepath.Join(dirs.crew, "stranded-write.txt"))
	if err != nil || !strings.Contains(string(onHost), marker) {
		t.Fatalf("the write did not reach the host crew dir (%v, %q) — without that there is no shared-memory hazard to fix", err, onHost)
	}
	t.Logf("BEFORE: the container on the old daemon wrote %q into the live host crew dir %s", marker, dirs.crew)

	// Crewship is now on the other daemon. Same instance, same container
	// prefix, same host data dir — a runtime switch, not a second install.
	moved := &Provider{
		client:         newCli,
		cfg:            cfg,
		logger:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		detected:       DetectResult{Runtime: "new", Socket: newDaemon, Host: newDaemon},
		digestResolver: dockerutil.NewDigestResolver(0, 0),
	}
	found := moved.findStrandedCrewsIn(ctx, []DetectResult{
		{Runtime: "new", Socket: newDaemon, Host: newDaemon},
		{Runtime: "old", Socket: oldDaemon, Host: oldDaemon},
	}, listContainersAt)
	if len(found) != 1 || found[0].Name != name || !found[0].Running {
		t.Fatalf("the sweep did not see the running crew stranded on the old daemon: %+v", found)
	}
	t.Logf("FOUND: %s running on %s (crew %s)", found[0].Name, found[0].Socket, found[0].CrewID)

	moved.actOnStrandedCrews(ctx, found, "stop", moved.stopStrandedCrew)

	// The assertion that matters: the write access is gone. Not "the status
	// string says exited" — an exec that the daemon refuses because the
	// container is not running is the property an operator cares about.
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = execInContainer(ctx, old, created.ID, `echo STILL-WRITING > /crew/stranded-write-2.txt`)
		if lastErr != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if lastErr == nil {
		t.Fatalf("the stranded container can STILL write the live crew directory after the sweep — the dangerous half of #1704 is not fixed")
	}
	if _, err := os.Stat(filepath.Join(dirs.crew, "stranded-write-2.txt")); err == nil {
		t.Errorf("a second write from the stranded container reached the host crew dir after the sweep")
	}
	t.Logf("AFTER: the stranded container can no longer write the live crew dir (%v)", lastErr)

	// Stopped, not removed — the operator still has it to inspect.
	insp, err := oldCli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("the stranded container was removed, not stopped: %v", err)
	}
	if insp.Container.State != nil && insp.Container.State.Running {
		t.Errorf("the stranded container is still running")
	}
	t.Logf("the stranded container is still present for a post-mortem (state=%s)", insp.Container.State.Status)
}

func dialOrFail(ctx context.Context, t *testing.T, host string) *client.Client {
	t.Helper()
	cli, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatalf("client for %s: %v", host, err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("daemon %s unreachable: %v", host, err)
	}
	return cli
}

// strandedDataDir returns a host directory the OLD daemon can bind-mount the
// contents of, so the crew's /crew mount is a real shared host directory rather
// than an empty one — the whole hazard is that both containers see the same
// files.
func strandedDataDir(ctx context.Context, t *testing.T, oldDaemon string) string {
	t.Helper()
	cli := dialOrFail(ctx, t, oldDaemon)
	defer cli.Close()
	probe := &Provider{client: cli, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	home, _ := os.UserHomeDir()
	for _, parent := range []string{home, os.TempDir(), "/private/tmp"} {
		if parent == "" {
			continue
		}
		dir, err := os.MkdirTemp(parent, "crewship-1704-")
		if err != nil {
			continue
		}
		ok, _, _, _, cleanup := contentReachableFromDaemon(ctx, probe, dir)
		cleanup()
		if ok {
			return dir
		}
		_ = os.RemoveAll(dir)
	}
	t.Fatalf("no host directory whose contents %s can bind — cannot reproduce a shared crew dir", oldDaemon)
	return ""
}
