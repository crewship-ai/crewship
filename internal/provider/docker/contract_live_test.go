//go:build conformance

package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/providertest"
)

// The live half of the shared provider contract suite.
//
// contract_test.go runs the same contracts against a fake dockerd, which proves
// what the provider ASKS for. This proves what a real runtime does with it —
// and it is the half that matters most on the podman legs of
// runtime-conformance.yml, a runtime we advertise and had never exec'd against.
//
// It reuses the existing conformance harness rather than standing up a second
// one: container creation is EnsureCrewRuntime's contract and is already
// covered there, so this file only borrows a running container to exec in.
func TestDockerProvider_LiveContractSuite(t *testing.T) {
	providertest.RunLiveContractSuite(t, func(t *testing.T) providertest.Runtime {
		t.Helper()

		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		t.Cleanup(cancel)

		image := os.Getenv("CREWSHIP_CONFORMANCE_IMAGE")
		if image == "" {
			image = "debian:bookworm-slim"
		}

		p, cleanupProvider := newConformanceProvider(ctx, t)
		t.Cleanup(cleanupProvider)

		if err := p.ensureImage(ctx, image); err != nil {
			t.Fatalf("pull %s: %v", image, err)
		}

		crew := provider.CrewConfig{
			ID:       "contract-crew-id",
			Slug:     "contract",
			MemoryMB: conformanceMemoryMB,
			CPUs:     conformanceCPUs,
		}
		dirs, err := p.prepareCrewDirs(crew)
		if err != nil {
			t.Fatalf("prepare crew dirs: %v", err)
		}
		ensureCrewVolumesOwned(ctx, t, p, image, crew, dirs)

		name := p.CrewContainerName(crew.ID, crew.Slug)
		// A previous aborted run leaves the container behind and the create
		// would then fail on the name rather than on anything under test.
		_, _ = p.client.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})

		cfg, hostCfg, err := p.buildCrewContainerConfig(ctx, crew, name, image, "", conformanceMemoryMB, conformanceCPUs, dirs)
		if err != nil {
			t.Fatalf("build crew container config: %v", err)
		}
		created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
			Config:           cfg,
			HostConfig:       hostCfg,
			NetworkingConfig: &dockernetwork.NetworkingConfig{},
			Name:             name,
		})
		if err != nil {
			t.Fatalf("%s rejected the crew HostConfig at create time: %v", p.detected.Runtime, err)
		}
		t.Cleanup(func() {
			rmCtx, rmCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer rmCancel()
			_, _ = p.client.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			for _, v := range []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)} {
				_, _ = p.client.VolumeRemove(rmCtx, v, client.VolumeRemoveOptions{Force: true})
			}
		})
		if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatalf("start crew container: %v", err)
		}

		// BlockCmd waits on a file the agent user can create, so Unblock needs
		// no second exec path and is safe to call more than once.
		unblockPath := filepath.ToSlash(filepath.Join("/tmp", "crewship-contract-unblock"))

		return providertest.Runtime{
			Provider:     p,
			ContainerID:  created.ID,
			EchoStdinCmd: []string{"sh", "-c", "cat"},
			ExitCmd: func(code int) []string {
				return []string{"sh", "-c", "exit " + itoaContract(code)}
			},
			StderrCmd:  []string{"sh", "-c", "printf '%s' contract-stderr >&2"},
			StderrText: "contract-stderr",
			BlockCmd:   []string{"sh", "-c", "while [ ! -f " + unblockPath + " ]; do sleep 0.1; done"},
			Unblock: func() {
				uCtx, uCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer uCancel()
				res, err := p.Exec(uCtx, provider.ExecConfig{
					ContainerID: created.ID,
					Cmd:         []string{"sh", "-c", "touch " + unblockPath},
					User:        providertest.DefaultSafeUser,
				})
				if err != nil {
					return
				}
				if res.Reader != nil {
					_ = res.Reader.Close()
				}
			},
		}
	})
}

func itoaContract(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
