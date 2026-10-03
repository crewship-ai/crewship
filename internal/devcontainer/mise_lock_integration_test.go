//go:build integration

package devcontainer

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Run explicitly with a local Linux image containing mise >=2026.9.18, sh,
// base64 and coreutils. Uses public node metadata, never provider credentials.
func TestMiseLock_RealInstallRejectsTamperingAndMissingVersion(t *testing.T) {
	image := os.Getenv("CREWSHIP_MISE_TEST_IMAGE")
	if image == "" {
		t.Skip("set CREWSHIP_MISE_TEST_IMAGE to an image containing mise")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
		Name: tempContainerName(), Config: &container.Config{Image: inspected.ID, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"240"}, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/agent"}},
		HostConfig: &container.HostConfig{CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: 512 << 20, NanoCPUs: 1_000_000_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = docker.ContainerRemove(clean, created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(docker, testLogger())
	run := installer.execInContainerAsUser
	setup := `mkdir -p /home/agent /tmp/resolve/config /tmp/resolve/data /tmp/resolve/cache /tmp/resolve/state; chown -R 1001:1001 /home/agent /tmp/resolve; printf '[tools]\nnode = "22.0.0"\n' > /tmp/resolve/config/config.toml; chown 1001:1001 /tmp/resolve/config/config.toml`
	if out, code, err := run(ctx, created.ID, []string{"sh", "-c", setup}, "0:0", nil); err != nil || code != 0 {
		t.Fatalf("setup %v %d %s", err, code, out)
	}
	env := []string{"HOME=/home/agent", "MISE_GLOBAL_CONFIG_FILE=/tmp/resolve/config/config.toml", "MISE_CONFIG_DIR=/tmp/resolve/config", "MISE_DATA_DIR=/tmp/resolve/data", "MISE_CACHE_DIR=/tmp/resolve/cache", "MISE_STATE_DIR=/tmp/resolve/state"}
	if out, code, err := run(ctx, created.ID, []string{"mise", "lock", "--global"}, "1001:1001", env); err != nil || code != 0 {
		t.Fatalf("resolve %v %d %s", err, code, out)
	}
	lock, code, err := run(ctx, created.ID, []string{"cat", "/tmp/resolve/config/mise.lock"}, "1001:1001", env)
	if err != nil || code != 0 {
		t.Fatalf("read lock %v %d", err, code)
	}
	cfg := &MiseConfig{Tools: map[string]string{"node": "22.0.0"}, Lock: &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": lock}}}
	if err := InstallMiseTools(ctx, created.ID, cfg, run); err != nil {
		t.Fatal(err)
	}
	out, code, err := run(ctx, created.ID, []string{"mise", "exec", "--", "node", "--version"}, "1001:1001", miseAgentEnv)
	if err != nil || code != 0 || strings.TrimSpace(out) != "v22.0.0" {
		t.Fatalf("installed version %v %d %q", err, code, out)
	}
	// Force must verify the lock even if the same version was just installed.
	checksum := regexp.MustCompile(`checksum = "sha256:[0-9a-f]{64}"`)
	if !checksum.MatchString(lock) {
		t.Fatal("fixture lock had no checksum")
	}
	cfg.Lock.Files["mise.lock"] = checksum.ReplaceAllString(lock, `checksum = "sha256:`+strings.Repeat("0", 64)+`"`)
	if err := InstallMiseTools(ctx, created.ID, cfg, run); err == nil || !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Fatalf("tampered checksum did not fail: %v", err)
	}
	cfg.Lock.Files["mise.lock"] = lock
	cfg.Tools["node"] = "24.0.0"
	if err := InstallMiseTools(ctx, created.ID, cfg, run); err == nil || !strings.Contains(err.Error(), "not in the lockfile") {
		t.Fatalf("missing version did not fail: %v", err)
	}
}
