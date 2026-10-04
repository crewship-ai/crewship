//go:build integration

package devcontainer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Explicit upstream packaging qualification, separate from the offline CI
// fixtures. Requires an owned local image with mise; only public downloads and
// --version are used, without provider credentials or model requests.
func TestCodexNativePackaging(t *testing.T) {
	image := os.Getenv("CREWSHIP_CODEX_QUALIFICATION_IMAGE")
	if image == "" {
		t.Fatal("set CREWSHIP_CODEX_QUALIFICATION_IMAGE for this explicit gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	inspected, err := docker.ImageInspect(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       tempContainerName(),
		Config:     &container.Config{Image: inspected.ID, User: "0:0", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"480"}, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/agent"}},
		HostConfig: &container.HostConfig{CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: 512 << 20, NanoCPUs: 1e9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var committed, runtimeID string
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := docker.ContainerRemove(clean, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Error(err)
		}
		if runtimeID != "" {
			if _, err := docker.ContainerRemove(clean, runtimeID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				t.Error(err)
			}
		}
		if committed != "" {
			if _, err := docker.ImageRemove(clean, committed, client.ImageRemoveOptions{}); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(docker, testLogger())
	run := installer.execInContainerAsUser
	setup := `mkdir -p /home/agent /tmp/codex-qualification/config /tmp/codex-qualification/data /tmp/codex-qualification/cache /tmp/codex-qualification/state; chown -R 1001:1001 /home/agent /tmp/codex-qualification; printf '[tools]\ncodex = "0.160.0"\n' > /tmp/codex-qualification/config/config.toml; chown 1001:1001 /tmp/codex-qualification/config/config.toml`
	if out, code, err := run(ctx, created.ID, []string{"sh", "-c", setup}, "0:0", nil); err != nil || code != 0 {
		t.Fatalf("setup: %v %d %s", err, code, out)
	}
	env := []string{"HOME=/home/agent", "MISE_CONFIG_DIR=/tmp/codex-qualification/config", "MISE_GLOBAL_CONFIG_FILE=/tmp/codex-qualification/config/config.toml", "MISE_DATA_DIR=/tmp/codex-qualification/data", "MISE_CACHE_DIR=/tmp/codex-qualification/cache", "MISE_STATE_DIR=/tmp/codex-qualification/state"}
	if out, code, err := run(ctx, created.ID, []string{"/usr/local/bin/mise", "lock", "--global"}, "1001:1001", env); err != nil || code != 0 {
		t.Fatalf("lock: %v %d %s", err, code, out)
	}
	lock, code, err := run(ctx, created.ID, []string{"cat", "/tmp/codex-qualification/config/mise.lock"}, "1001:1001", env)
	if err != nil || code != 0 {
		t.Fatalf("read lock: %v %d", err, code)
	}
	t.Logf("native lock=%s", lock)
	cfg := &MiseConfig{Tools: map[string]string{"codex": "0.160.0"}, Lock: &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": lock}}}
	if version, err := LockedToolVersion(cfg.Lock, "codex"); err != nil || version != "0.160.0" {
		t.Fatalf("native lock: %s %v\n%s", version, err, lock)
	}
	if err := InstallMiseTools(ctx, created.ID, cfg, run); err != nil {
		t.Fatal(err)
	}
	if err := writeToolchainInventory(ctx, created.ID, []string{"codex"}, nil, run); err != nil {
		t.Fatal(err)
	}
	result, err := docker.ContainerCommit(ctx, created.ID, client.ContainerCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	committed = result.ID
	p := NewProvisioner(docker, installer, nil, testLogger())
	got := p.inspectToolchain(ctx, committed, []string{"codex"})
	if got.ImageID != committed || len(got.Tools) != 1 {
		t.Fatalf("inventory: %+v", got)
	}
	tool := got.Tools[0]
	if tool.Status != "observed" || tool.Version != "0.160.0" || tool.LaunchArtifact == nil || !strings.HasPrefix(tool.Path, "/opt/mise/data/installs/") {
		t.Fatalf("native artifact: %+v", tool)
	}
	launcher := filepath.Join(t.TempDir(), "launcher")
	build := exec.CommandContext(ctx, "go", "build", "-o", launcher, "../../cmd/crewship-sidecar")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("launcher build: %v %s", err, out)
	}
	runtime, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: tempContainerName(), Config: &container.Config{Image: committed, User: "1001:1001", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"120"}, Env: []string{"HOME=/home/agent", "PATH=/usr/local/bin:/usr/bin:/bin", "LD_PRELOAD=/home/agent/evil.so", "NODE_OPTIONS=evil"}},
		HostConfig: &container.HostConfig{ReadonlyRootfs: true, NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: launcher, Target: managedlaunch.LauncherPath, ReadOnly: true}}, Resources: container.Resources{Memory: 1 << 30, NanoCPUs: 1e9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeID = runtime.ID
	if _, err := docker.ContainerStart(ctx, runtimeID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	descriptor := managedlaunch.Descriptor{Artifact: *tool.LaunchArtifact, ImageID: committed, RevisionID: "packaging-fixture", LockSHA256: strings.TrimPrefix(miseLockDigest(cfg.Lock), "sha256:"), Binary: "codex", Version: "0.160.0", EnvKeys: []string{"HOME"}}
	encoded, _ := json.Marshal(descriptor)
	if out, code, err := run(ctx, runtimeID, []string{managedlaunch.LauncherPath, "--managed-launch", base64.RawURLEncoding.EncodeToString(encoded), "--version"}, "1001:1001", []string{"HOME=/home/agent"}); err != nil || code != 0 || !strings.Contains(out, "codex-cli 0.160.0") {
		t.Fatalf("native managed launcher: %v %d %s", err, code, out)
	}
	t.Logf("image=%s path=%s version=%s artifact=%+v native_lock=%s", committed, tool.Path, tool.Version, tool.LaunchArtifact, lock)
}
