package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"io"
	"strings"
	"testing"
)

func TestToolchainCaptureIsSharedWithBuildOnlyProvisioning(t *testing.T) {
	rec := &dockerfileRecorder{}
	if err := writeToolchainInventory(context.Background(), "", []string{"claude", "codex"}, nil, rec.exec); err != nil {
		t.Fatal(err)
	}
	steps := strings.Join(rec.steps(), "\n")
	for _, want := range []string{"USER 1001:1001", "timeout -k 1 8", "ulimit -f 8", "DISABLE_AUTOUPDATER=1", toolchainDirectory, "chown -R 0:0"} {
		if !strings.Contains(steps, want) {
			t.Fatalf("build recipe missing %q", want)
		}
	}
}

func TestToolchainCapturePreservesCustomBinaryVerification(t *testing.T) {
	rec := &dockerfileRecorder{}
	if err := writeToolchainInventory(context.Background(), "", []string{"custom-binary"}, nil, rec.exec); err != nil {
		t.Fatal(err)
	}
	if len(rec.steps()) != 0 {
		t.Fatal("inventory must not guess a custom binary's version protocol")
	}
}

func TestInspectToolchainNeverStartsImageAndCleansScratch(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			files := map[string]string{"schema": "1", "codex.path": "/opt/mise/data/shims/codex", "codex.version": "codex-cli 0.153.2", "codex.status": "0"}
			if malformed {
				files["schema"] = "unsupported"
			}
			docker := &inventoryDocker{archive: toolchainArchive(t, files)}
			p := NewProvisioner(docker, nil, nil, testLogger())
			got := p.inspectToolchain(context.Background(), "mutable:tag", []string{"codex"})
			if got.ImageID != "sha256:immutable" {
				t.Fatalf("identity=%q", got.ImageID)
			}
			if !malformed && got.Tools[0].Version != "0.153.2" {
				t.Fatalf("inventory=%+v", got)
			}
			if malformed && got.Status != "unavailable" {
				t.Fatalf("malformed data was trusted: %+v", got)
			}
			if len(docker.startedIDs) != 0 {
				t.Fatal("inspection executed image code")
			}
			if len(docker.removedIDs) != 1 {
				t.Fatal("inspection leaked its scratch container")
			}
			created := docker.createdContainers[0]
			if created.config.Image != "sha256:immutable" || created.hostCfg.NetworkMode != "none" || !created.hostCfg.ReadonlyRootfs {
				t.Fatalf("unsafe image inspection: %+v", created)
			}
		})
	}
}

type inventoryDocker struct {
	mockCommitClient
	archive []byte
}

func (d *inventoryDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: "sha256:immutable"}}, nil
}
func (d *inventoryDocker) CopyFromContainer(context.Context, string, client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(d.archive))}, nil
}

func TestToolchainCaptureUsesRuntimeMiseLocationsWithoutUnrelatedEnv(t *testing.T) {
	var agentEnv []string
	exec := func(_ context.Context, _ string, _ []string, user string, env []string) (string, int, error) {
		if user == "1001:1001" {
			agentEnv = append([]string(nil), env...)
		}
		return "", 0, nil
	}
	err := writeToolchainInventory(context.Background(), "fixture", []string{"codex"}, map[string]string{"MISE_DATA_DIR": "/custom/mise-data", "UNRELATED_SECRET": "EXAMPLE-NOT-A-REAL-SECRET"}, exec)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(agentEnv, "\n")
	for _, want := range []string{"MISE_DATA_DIR=/custom/mise-data", "MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml", "DISABLE_AUTOUPDATER=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing runtime location %q", want)
		}
	}
	if strings.Contains(joined, "UNRELATED_SECRET") {
		t.Fatal("version probe inherited unrelated configured environment")
	}
}
