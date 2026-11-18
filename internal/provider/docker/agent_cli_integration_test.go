//go:build integration && linux

package docker

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// This gate provisions REAL crew runtimes through the production provider.
// It qualifies SDK delivery/retained-data upgrade, not Vault authentication or
// model CLI shells. The latter need their own real-container receipts.
func TestAgentCLIRealDockerCleanAndRetainedUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	daemon, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()
	imageRef := os.Getenv("CREWSHIP_CODEX_QUALIFICATION_IMAGE")
	if imageRef == "" {
		imageRef = "node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e"
	}
	image, err := daemon.ImageInspect(ctx, imageRef)
	if err != nil {
		t.Fatalf("explicit SDK integration requires the local pinned Bookworm fixture, without pulling: %v", err)
	}
	if image.Os != "linux" || (image.Architecture != "amd64" && image.Architecture != "arm64") {
		t.Fatal("unsupported fixture platform")
	}
	sourceCommit, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(sourceCommit))
	install := t.TempDir()
	sdk, sidecar := filepath.Join(install, "crewship-agent-cli"), filepath.Join(install, "crewship-sidecar")
	for _, spec := range []struct {
		path, source string
		cli          bool
	}{{sdk, "../../../cmd/crewship", true}, {sidecar, "../../../cmd/crewship-sidecar", false}} {
		args := []string{"build", "-trimpath", "-o", spec.path}
		if spec.cli {
			args = append(args, "-tags", "clionly", "-ldflags", "-X main.commit="+commit+" -X main.version=fixture-sdk")
		}
		args = append(args, spec.source)
		build := exec.CommandContext(ctx, "go", args...)
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+image.Architecture)
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build native SDK companion: %v %s", err, out)
		}
	}
	entrypoint, err := filepath.Abs("../../../scripts/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, scenario := range []string{"clean", "retained-upgrade"} {
		t.Run(scenario, func(t *testing.T) {
			prefix := "crewship-sdk-fixture-" + strings.ToLower(rand.Text())
			crew := provider.CrewConfig{ID: "sdk-fixture", Slug: "sdk-fixture", CachedImage: image.ID}
			cfg := Config{InstanceID: prefix, ContainerPrefix: prefix, OutputBasePath: t.TempDir(), Network: "bridge", RuntimeImage: image.ID, SidecarBinaryPath: sidecar, EntrypointPath: entrypoint, AgentCLIBinaryPath: sdk, RequireAgentCLI: true, ExpectedAgentCLICommit: commit, RequiredAgentCLICommands: []string{"version", "commands"}}
			legacy := cfg
			legacy.RequireAgentCLI = false
			legacy.AgentCLIBinaryPath = ""
			if scenario == "clean" {
				legacy = cfg
			}
			old, err := New(ctx, legacy, logger)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { old.Close() })
			current := old
			var use *provider.RuntimeUse
			t.Cleanup(func() {
				if use != nil {
					use.Release()
				}
				cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				name := old.CrewContainerName(crew.ID, crew.Slug)
				found, err := daemon.ContainerInspect(cleanup, name, client.ContainerInspectOptions{})
				if err == nil {
					if found.Container.Config == nil || found.Container.Config.Labels[resourcelifecycle.InstanceLabel] != prefix {
						t.Error("refused SDK fixture cleanup without exact installation ownership")
						return
					}
					// Remove the entrypoint marker as its owning agent before host TempDir
					// cleanup; the named volumes are removed by exact fixture identities below.
					if result, err := old.Exec(cleanup, provider.ExecConfig{ContainerID: found.Container.ID, User: "1001:1001", Cmd: []string{"sh", "-c", "rm -f /workspace/.ready"}}); err == nil {
						io.Copy(io.Discard, result.Reader)
						result.Reader.Close()
					}
					if _, err := daemon.ContainerRemove(cleanup, found.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
						t.Errorf("remove owned SDK fixture: %v", err)
					}
				}
				for _, name := range []string{old.homeVolumeName(crew.ID, crew.Slug), old.toolsVolumeName(crew.ID, crew.Slug)} {
					if _, err := daemon.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{}); err != nil {
						t.Errorf("remove exact owned SDK fixture volume: %v", err)
					}
				}
			})
			use, err = old.AcquireCrewRuntimeUse(ctx, crew)
			if err != nil {
				t.Fatal(err)
			}
			run := func(p *Provider, id string, cmd []string) ([]byte, int) {
				t.Helper()
				result, err := p.Exec(ctx, provider.ExecConfig{ContainerID: id, User: "1001:1001", Cmd: cmd, WorkingDir: "/"})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(io.LimitReader(result.Reader, 64<<10))
				result.Reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				code, running, err := p.waitExecExit(ctx, result.ExecID, 40)
				if err != nil || running {
					t.Fatal("fixture command did not settle")
				}
				return raw, code
			}
			oldID := use.ContainerID()
			if _, code := run(old, oldID, []string{"sh", "-c", "printf fixture-retained > /home/agent/sdk-retained; printf fixture-retained > /opt/crew-tools/sdk-retained"}); code != 0 {
				t.Fatal("could not seed real persistent fixture data")
			}
			if scenario == "retained-upgrade" {
				next, err := New(ctx, cfg, logger)
				if err != nil {
					t.Fatal(err)
				}
				defer next.Close()
				current = next
				active := true
				next.SetRuntimeIdleVerifier(func(context.Context, string, string) error {
					if active {
						return errors.New("fixture has recovered active crew work")
					}
					return nil
				})
				before, err := daemon.ContainerInspect(ctx, oldID, client.ContainerInspectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if admitted, err := next.AcquireCrewRuntimeUse(ctx, crew); err == nil {
					admitted.Release()
					t.Fatal("upgrade ignored recovered active work")
				}
				after, err := daemon.ContainerInspect(ctx, oldID, client.ContainerInspectOptions{})
				if err != nil || !after.Container.State.Running || after.Container.State.StartedAt != before.Container.State.StartedAt {
					t.Fatal("upgrade interrupted the active legacy crew")
				}
				use.Release()
				use = nil
				active = false
				use, err = next.AcquireCrewRuntimeUse(ctx, crew)
				if err != nil {
					t.Fatal(err)
				}
				if use.ContainerID() == oldID {
					t.Fatal("legacy runtime was not recreated for native SDK delivery")
				}
			}
			id := use.ContainerID()
			raw, code := run(current, id, []string{"/usr/local/bin/crewship", "agent-runtime-probe"})
			var probe buildinfo.AgentRuntimeProbe
			if code != 0 || json.Unmarshal(raw, &probe) != nil {
				t.Fatal("actual read-only runtime SDK did not execute")
			}
			if err := validateAgentCLIProbe(probe, current.cfg.agentCLIArtifact, commit, []string{"version", "commands"}); err != nil {
				t.Fatal(err)
			}
			if _, code := run(current, id, []string{"sh", "-c", "test \"$(cat /home/agent/sdk-retained)\" = fixture-retained && test \"$(cat /opt/crew-tools/sdk-retained)\" = fixture-retained"}); code != 0 {
				t.Fatal("runtime upgrade lost named-volume data")
			}
			if _, code := run(current, id, []string{"sh", "-c", "printf tamper > /usr/local/bin/crewship"}); code == 0 {
				t.Fatal("agent could overwrite its authoritative client")
			}
			t.Logf("SDK container=%s image=%s sha256=%s commit=%s platform=%s/%s", id, image.ID, probe.SHA256, probe.Commit, probe.OS, probe.Arch)
		})
	}
}
