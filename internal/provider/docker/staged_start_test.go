package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/moby/moby/api/types/container"
)

// These assertions compile on the pre-S01 baseline. They test the effective
// program Docker starts, rather than a missing future capability interface.
func TestStagedStart_NoImageProgramAtContainerStart(t *testing.T) {
	cfg := covRTConfig(t)
	cfg.EgressFenceCrews = []string{"pilot"}
	p := (&covRT{}).provider(t, cfg)
	c, h, err := p.assembleCrewSpec(provider.CrewConfig{ID: "pilot", NetworkMode: "restricted"}, covRuntimeRef, "runc", 1024, 1, crewDirs{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "1002:1002" || len(c.Entrypoint) != 1 || c.Entrypoint[0] != "/usr/local/bin/crewship-sidecar" || len(c.Cmd) != 1 || c.Cmd[0] != "--staged-keeper" {
		t.Fatalf("image program starts before fence: user=%q entrypoint=%v cmd=%v", c.User, c.Entrypoint, c.Cmd)
	}
	if h.Init == nil || *h.Init || c.Healthcheck == nil || strings.Join(c.Healthcheck.Test, ",") != "NONE" {
		t.Fatal("keeper must own PID1 without image healthcheck")
	}
}

func TestStagedStart_OptInRejectsLegacyRuntime(t *testing.T) {
	for _, state := range []string{"running", "exited"} {
		t.Run(state, func(t *testing.T) {
			cfg := covRTConfig(t)
			cfg.EgressFenceCrews = []string{covTeam().ID}
			covCrewBindDirs(t, cfg)
			f := &covRT{listBody: covExistingList(state), inspectBody: covHealthyInspect(covRuntimeRef)}
			p := f.provider(t, cfg)
			crew := covTeam()
			crew.NetworkMode = "restricted"
			if _, err := p.EnsureCrewRuntime(context.Background(), crew); err == nil {
				t.Fatal("legacy pilot adopted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.starts) != 0 || len(f.deletes) != 0 || len(f.stops) != 0 {
				t.Fatalf("legacy opt-in mutated runtime: starts=%v removes=%v stops=%v", f.starts, f.deletes, f.stops)
			}
		})
	}
}

func TestStagedStart_RejectsProtectedMountsAndPrivileges(t *testing.T) {
	cfg := covRTConfig(t)
	cfg.EgressFenceCrews = []string{"pilot"}
	p := (&covRT{}).provider(t, cfg)
	for _, change := range []func(*provider.CrewConfig){
		func(c *provider.CrewConfig) { c.Privileged = true },
		func(c *provider.CrewConfig) { c.CapAdd = []string{"NET_RAW"} },
		func(c *provider.CrewConfig) { c.SecurityOpt = []string{"seccomp=unconfined"} },
	} {
		crew := provider.CrewConfig{ID: "pilot", NetworkMode: "restricted"}
		change(&crew)
		if _, _, e := p.assembleCrewSpec(crew, covRuntimeRef, "runc", 1024, 1, crewDirs{}, nil, nil); e == nil {
			t.Fatal("unsafe pilot admitted")
		}
	}
}

func TestStagedStart_KeeperEnvironmentHasNoImageValues(t *testing.T) {
	env := keeperEnv([]string{"TOKEN=private", "PATH=/home/agent/bin"}, map[string]string{"BASH_ENV": "/evil", "GODEBUG": "evil"})
	got := strings.Join(env, "\n")
	if strings.Contains(got, "private") || strings.Contains(got, "/evil") || strings.Contains(got, "/home/agent") || !strings.Contains(got, "BASH_ENV=") {
		t.Fatalf("inherited image environment: %v", env)
	}
}

func TestStagedStart_MissingArtifactNeverStartsImageHelper(t *testing.T) {
	cfg := covRTConfig(t)
	cfg.InstanceID = "test-installation"
	cfg.EgressFenceCrews = []string{covTeam().ID}
	f := &covRT{}
	p := f.provider(t, cfg)
	crew := covTeam()
	crew.NetworkMode = "restricted"
	if _, e := p.EnsureCrewRuntime(t.Context(), crew); e == nil {
		t.Fatal("missing trusted artifact admitted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != 0 || len(f.creates) != 0 {
		t.Fatalf("image helper started before artifact qualification: creates=%v starts=%v", f.createNames, f.starts)
	}
}

func TestStagedAuditRejectsReservedTmpfsBeforeArtifactUse(t *testing.T) {
	p := &Provider{cfg: Config{InstanceID: "fixture"}}
	for _, target := range []string{"/", "/usr", "/usr/local/bin", "/usr/local/bin/crewship-sidecar", "/usr//local/bin/entrypoint.sh", "/tmp"} {
		t.Run(target, func(t *testing.T) {
			noInit := false
			c := container.InspectResponse{Config: &container.Config{User: "1002:1002", Entrypoint: []string{stagedstart.Binary}, Cmd: []string{"--staged-keeper"}, Labels: map[string]string{resourcelifecycle.InstanceLabel: "fixture", crewCrewIDLabel: "pilot", stagedstart.Label: stagedstart.Version}, Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}}
			c.HostConfig = &container.HostConfig{ReadonlyRootfs: true, Init: &noInit, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, GroupAdd: []string{crewGroupGID, sidecarGroupGID}, Runtime: "runc", Tmpfs: map[string]string{target: "rw"}}
			err := p.auditStaged(t.Context(), c, provider.CrewConfig{ID: "pilot"})
			if !errors.Is(err, errStagedDenied) {
				t.Fatalf("audit error class: %v", err)
			}
			if target == "/tmp" {
				if !strings.Contains(err.Error(), "trusted host artifact missing") {
					t.Fatalf("ordinary tmpfs rejected before artifact qualification: %v", err)
				}
			} else if !strings.Contains(err.Error(), "tmpfs overlaps reserved executable") {
				t.Fatalf("reserved tmpfs not rejected before artifact use: %v", err)
			}
		})
	}
}
