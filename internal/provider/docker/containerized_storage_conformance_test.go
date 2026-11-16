//go:build conformance

package docker

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/client"
)

// Executed INSIDE the exact candidate server image by smoke-image-runtime.sh.
// Ordinary host conformance cannot reproduce the server/daemon namespace split.
func TestContainerizedStorage(t *testing.T) {
	if os.Getenv("CREWSHIP_CONTAINERIZED_STORAGE_SMOKE") != "1" {
		t.Skip("run scripts/ci/smoke-image-runtime.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := "/var/lib/crewship/runtime-smoke"
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OutputBasePath: root, ServerContainer: os.Getenv("CREWSHIP_DOCKER_SERVER_CONTAINER"), SidecarBinaryPath: "/usr/local/bin/crewship-sidecar", EntrypointPath: "/usr/local/bin/entrypoint.sh", ContainerPrefix: os.Getenv("CREWSHIP_SMOKE_PREFIX"), RuntimeImage: "debian:bookworm-slim", InstanceID: os.Getenv("CREWSHIP_SMOKE_PREFIX")}
	if cfg.ServerContainer == "" || cfg.ContainerPrefix == "" {
		t.Fatal("explicit isolated server identity and prefix required")
	}
	p, err := New(ctx, cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.RuntimeReady(ctx); err != nil {
		t.Fatal(err)
	}
	original, readErr := os.ReadFile(cfg.SidecarBinaryPath)
	staged, stagedErr := os.ReadFile(p.cfg.SidecarBinaryPath)
	if readErr != nil || stagedErr != nil || string(original) != string(staged) {
		t.Fatal("staged sidecar differs from distributed image")
	}
	if p.pathMap == nil || p.pathMap.local == p.pathMap.daemon {
		t.Fatal("test did not exercise different server/daemon paths")
	}
	crew := provider.CrewConfig{ID: "storage-smoke", Slug: "storage-smoke"}
	id, err := p.EnsureCrewRuntime(ctx, crew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		for _, name := range []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)} {
			_, _ = p.client.VolumeRemove(context.Background(), name, client.VolumeRemoveOptions{})
		}
	}()
	run := func(user, script string) string {
		t.Helper()
		out, code, err := execInContainerAs(ctx, p, id, user, script)
		if err != nil || code != 0 {
			t.Fatalf("exec failed (%d): %v %s", code, err, out)
		}
		return out
	}
	dirs, err := p.prepareCrewDirs(crew)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ local, target string }{{dirs.workspace, "/workspace"}, {dirs.output, "/output"}, {dirs.crew, "/crew"}} {
		if err := os.WriteFile(filepath.Join(pair.local, "from-server"), []byte(cfg.ContainerPrefix), 0664); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(run("1001:1001", "cat "+pair.target+"/from-server")); got != cfg.ContainerPrefix {
			t.Fatalf("cross-instance data: %s", got)
		}
		run("1001:1001", "printf agent > "+pair.target+"/from-agent")
		if raw, err := os.ReadFile(filepath.Join(pair.local, "from-agent")); err != nil || string(raw) != "agent" {
			t.Fatalf("agent write invisible: %q %v", raw, err)
		}
		run("1001:1001", fmt.Sprintf(`awk '$2=="%s" && $4 ~ /noexec/ && $4 ~ /nosuid/ {ok=1} END {exit !ok}' /proc/mounts`, pair.target))
	}
	run("1001:1001", `cp /bin/true /output/exec-probe; chmod +x /output/exec-probe; if /output/exec-probe; then exit 1; fi`)
	run("0:0", `if echo corrupt >> /usr/local/bin/crewship-sidecar; then exit 1; fi; if echo corrupt >> /usr/local/bin/entrypoint.sh; then exit 1; fi`)
	run("1001:1001", `test "$(stat -c %u /workspace)" = 1001; test "$(stat -c %u /home/agent)" = 1001; awk '$2=="/secrets" && $3=="tmpfs" {ok=1} END {exit !ok}' /proc/mounts`)
	// Real distributed sidecar; empty credential list, no model endpoint called.
	run("1002:1002", onboardingSidecarLaunchScript(base64.StdEncoding.EncodeToString([]byte(`{}`))))
	again, err := p.EnsureCrewRuntime(ctx, crew)
	if err != nil || again != id {
		t.Fatalf("unnecessary recreate: %s -> %s: %v", id, again, err)
	}
	marker := filepath.Join(root, "persistent")
	if os.Getenv("CREWSHIP_SMOKE_PERSISTED") == "1" {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("persisted marker lost: %v", err)
		}
	}
	if raw, err := os.ReadFile(marker); err == nil && string(raw) != cfg.ContainerPrefix {
		t.Fatalf("wrong persisted instance: %q", raw)
	}
	if err := os.WriteFile(marker, []byte(cfg.ContainerPrefix), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("real sidecar health, exec, bidirectional data, readonly runtime, noexec/nosuid, ownership and stable reconcile verified")
}
