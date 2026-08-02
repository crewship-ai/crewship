package docker

// The runtime-contract digest and the drift it exists to catch (#1642).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/provider"
)

// contractSpec builds the canonical crew spec through the same helper the
// digest hashes. Each call returns a fresh pair, so a test can mutate one copy
// and compare it against another without aliasing.
func contractSpec(t *testing.T, p *Provider) (*container.Config, *container.HostConfig) {
	t.Helper()
	cfg, hostCfg, err := p.assembleCrewSpec(
		provider.CrewConfig{ID: contractCrewID, Slug: contractCrewSlug},
		"", p.ociRuntime(), contractMemoryMB, contractCPUs,
		crewDirs{output: contractOutputDir, workspace: contractWorkingDir, crew: contractCrewDir},
		[]string{"CREWSHIP_CREW_ID=" + contractCrewID}, nil,
	)
	if err != nil {
		t.Fatalf("assembleCrewSpec: %v", err)
	}
	return cfg, hostCfg
}

// The property the whole mechanism rests on: every control the builder sets is
// IN the digest, so a future PR that adds one gets drift detection without
// having to remember this file exists.
//
// It is tested by mutation rather than by asserting a hash constant. A hash
// constant would pin the digest of today's builder — it would fail on every
// legitimate change and prove nothing about coverage. Mutating one field at a
// time and demanding the digest move proves the field is reachable from the
// digest, which is the actual requirement.
//
// The list is not arbitrary: it is the set of controls #1642 measured as NOT
// reaching a running crew on dev1, plus the ones whose silent absence is a
// security regression rather than a cosmetic one.
func TestCrewRuntimeContractDigest_MovesWhenAControlChanges(t *testing.T) {
	t.Parallel()

	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	baseCfg, baseHost := contractSpec(t, p)
	base := digestCrewSpec(baseCfg, baseHost)
	if base == "" {
		t.Fatal("digestCrewSpec returned empty for a well-formed spec")
	}

	tests := []struct {
		name   string
		why    string
		mutate func(*container.Config, *container.HostConfig)
	}{
		{
			name: "Init",
			why:  "PID 1 is `exec sleep infinity`, which never reaps; every orphan becomes a zombie against PidsLimit",
			mutate: func(_ *container.Config, h *container.HostConfig) {
				off := false
				h.Init = &off
			},
		},
		{
			name: "Ulimits core",
			why:  "core: 0 is what stops a crashing agent writing its whole credential environment onto a host-persistent bind",
			mutate: func(_ *container.Config, h *container.HostConfig) {
				for i, u := range h.Ulimits {
					if u.Name == "core" {
						cp := *u
						cp.Soft, cp.Hard = 1, 1
						h.Ulimits[i] = &cp
					}
				}
			},
		},
		{
			name:   "ShmSize",
			why:    "the daemon default of 64 MB is what Chromium/Playwright cannot work in",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.ShmSize = 64 << 20 },
		},
		{
			name: "GroupAdd",
			why:  "gid 1002 is the only way an agent can read the sidecar-owned crew-shared memory subtrees",
			mutate: func(_ *container.Config, h *container.HostConfig) {
				h.GroupAdd = h.GroupAdd[:1]
			},
		},
		{
			name:   "MemorySwap",
			why:    "unset means the daemon grants 2x Memory in swap, turning a bounded OOM into minutes of thrash",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.Resources.MemorySwap = h.Resources.Memory * 2 },
		},
		{
			name:   "PidsLimit",
			why:    "this is the cap the zombie leak was exhausting",
			mutate: func(_ *container.Config, h *container.HostConfig) { n := int64(4096); h.Resources.PidsLimit = &n },
		},
		{
			name:   "Memory",
			why:    "without a limit one crew's runaway process takes the host and every co-resident crew with it",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.Resources.Memory = 0 },
		},
		{
			name:   "NanoCPUs",
			why:    "an unlimited crew starves its neighbours long before it OOMs",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.Resources.NanoCPUs = 0 },
		},
		{
			name:   "ReadonlyRootfs",
			why:    "a read-only root is what makes a user-supplied base image safe to hand an agent",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.ReadonlyRootfs = false },
		},
		{
			name:   "SecurityOpt",
			why:    "no-new-privileges is what makes a setuid binary in a BYOI image a dead end",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.SecurityOpt = nil },
		},
		{
			name:   "CapDrop",
			why:    "CapDrop ALL is the baseline every other capability decision is made against",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.CapDrop = nil },
		},
		{
			name:   "RestartPolicy",
			why:    "without it a container whose PID 1 dies stays dead until someone notices",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.RestartPolicy = container.RestartPolicy{} },
		},
		{
			name:   "/tmp tmpfs options",
			why:    "noexec/nosuid on /tmp is what stops a staged payload being execve'd directly",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.Tmpfs["/tmp"] = "rw,size=1000" },
		},
		{
			name:   "/secrets tmpfs options",
			why:    "the mode/uid on this mount is what keeps credential files out of every sibling process's reach",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.Tmpfs["/secrets"] = "rw" },
		},
		{
			name:   "Mounts",
			why:    "the crew's writable mounts ride noexec bind-backed volumes; a plain bind is an exec-capable foothold",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.Mounts = h.Mounts[:1] },
		},
		{
			name:   "NetworkMode",
			why:    "a container attached to the wrong bridge cannot reach the daemon's other side of the egress fence",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.NetworkMode = "host" },
		},
		{
			name:   "ExtraHosts",
			why:    "host.docker.internal is how the sidecar reaches crewshipd; without it every assignment IPC call fails",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.ExtraHosts = nil },
		},
		{
			name:   "OCI runtime",
			why:    "runsc and runc are not the same isolation guarantee",
			mutate: func(_ *container.Config, h *container.HostConfig) { h.Runtime = "runsc" },
		},
		{
			name:   "Healthcheck",
			why:    "a nil or empty Test INHERITS the image's HEALTHCHECK, which probes forever against `sleep infinity`",
			mutate: func(c *container.Config, _ *container.HostConfig) { c.Healthcheck = nil },
		},
		{
			name:   "StopTimeout",
			why:    "the grace period is what lets a run flush before SIGKILL",
			mutate: func(c *container.Config, _ *container.HostConfig) { z := 0; c.StopTimeout = &z },
		},
		{
			name:   "User",
			why:    "the whole containment model assumes agent commands are unprivileged",
			mutate: func(c *container.Config, _ *container.HostConfig) { c.User = "0:0" },
		},
		{
			name:   "Entrypoint",
			why:    "the bind-mounted entrypoint is what makes a custom base image behave like a crew container",
			mutate: func(c *container.Config, _ *container.HostConfig) { c.Entrypoint = nil },
		},
		{
			name: "discovery labels",
			why:  "labels are the only crew-container discovery path that survives the DB row going away",
			mutate: func(c *container.Config, _ *container.HostConfig) {
				delete(c.Labels, "crewship.kind")
			},
		},
		{
			name: "locale/timezone env",
			why:  "non-ASCII paths are mangled under the POSIX locale, and an unset TZ leaves the container on the image's guess",
			mutate: func(c *container.Config, _ *container.HostConfig) {
				kept := c.Env[:0]
				for _, e := range c.Env {
					if e != "LANG=C.UTF-8" {
						kept = append(kept, e)
					}
				}
				c.Env = kept
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, hostCfg := contractSpec(t, p)
			tt.mutate(cfg, hostCfg)
			if got := digestCrewSpec(cfg, hostCfg); got == base {
				t.Errorf("changing %s left the digest at %s — a container created before that change would be reported as current.\n  why it matters: %s",
					tt.name, got, tt.why)
			}
		})
	}
}

// A spec that has not changed must hash the same, or every cold reconcile
// recreates every stopped container forever. Recomputed from scratch rather
// than compared to a cached value, so map iteration order inside the builder
// (Labels, Tmpfs, containerEnv) cannot leak into the digest.
func TestCrewRuntimeContractDigest_IsStable(t *testing.T) {
	t.Parallel()

	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	first, firstHost := contractSpec(t, p)
	want := digestCrewSpec(first, firstHost)
	for i := 0; i < 20; i++ {
		cfg, hostCfg := contractSpec(t, p)
		if got := digestCrewSpec(cfg, hostCfg); got != want {
			t.Fatalf("digest is not stable across builds: %s then %s (iteration %d)", want, got, i)
		}
	}
	// And the provider's own cached answer agrees with a fresh computation.
	if got := p.crewRuntimeContractDigest(); got != want {
		t.Errorf("crewRuntimeContractDigest() = %q, want %q", got, want)
	}
}

// Two providers configured differently must not share a contract digest: the
// crew container's spec depends on the provider's network name, sidecar and
// entrypoint paths and OCI runtime, and a container created under one of those
// is genuinely stale under another.
func TestCrewRuntimeContractDigest_TracksProviderConfiguration(t *testing.T) {
	t.Parallel()

	base := covRTConfig(t)
	baseP := (&covRT{}).provider(t, base)

	other := base
	other.Network = "some-other-bridge"
	otherP := (&covRT{}).provider(t, other)

	if baseP.crewRuntimeContractDigest() == otherP.crewRuntimeContractDigest() {
		t.Error("two providers on different networks produced the same contract digest")
	}
}

// covLabelledInspect is covHealthyInspect with a caller-chosen state and label
// set, so a test can present a container created by an older build (no
// contract label, or a different one) or by this one.
func covLabelledInspect(image string, running bool, labels map[string]string) string {
	b, _ := json.Marshal(map[string]any{
		"Id":     "old-cid",
		"State":  map[string]any{"Running": running},
		"Config": map[string]any{"Image": image, "Labels": labels},
		"Mounts": []map[string]any{
			{"Destination": "/crew"},
			{"Destination": "/home/agent"},
			{"Destination": "/opt/crew-tools"},
		},
		"HostConfig": map[string]any{
			"Tmpfs": map[string]string{"/secrets": secretsTmpfsSpec},
		},
	})
	return string(b)
}

// A STOPPED container carrying an older contract is replaced rather than
// started.
//
// This is the moment the recreate is free: nothing is running inside it, the
// caller is already paying for a container start, and the crew container's
// root filesystem is read-only, so its writable layer holds nothing the
// recreate could lose — /workspace, /output and /crew are host binds and
// /home/agent and /opt/crew-tools are named volumes. Every crew that idles
// past its TTL therefore converges onto the current configuration on its own,
// with no operator action and no interrupted run.
func TestEnsureCrewRuntime_StoppedContainerWithOldContractIsRecreated(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody: covExistingList(string(container.StateExited)),
		// Exactly what every crew container created before #1642 looks like:
		// the discovery labels from #1630 and no contract stamp.
		inspectBody: covLabelledInspect(covRuntimeRef, false, map[string]string{
			"managed-by":    "crewship",
			"crewship.kind": "crew",
		}),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id == "old-cid" {
		t.Fatal("the stopped container was started as-is; it keeps the configuration of the build that created it — on dev1 that meant no init, no supplementary group, unlimited core dumps and a 64 MB /dev/shm")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	removed := false
	for _, d := range f.deletes {
		if d == "old-cid" {
			removed = true
		}
	}
	if !removed {
		t.Errorf("stale container was not removed, deletes = %v", f.deletes)
	}
	if len(f.creates) == 0 {
		t.Error("no replacement container was created")
	}
	for _, s := range f.starts {
		if s == "old-cid" {
			t.Errorf("ContainerStart was issued against the stale container (starts = %v)", f.starts)
		}
	}
}

// The negative half, and the one that stops this becoming "recreate
// everything, always": a stopped container whose stamp matches this build is
// started, not rebuilt. Without this the TTL reaper and the drift check
// together would rebuild every crew container on every wake.
func TestEnsureCrewRuntime_StoppedContainerWithCurrentContractIsStarted(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{listBody: covExistingList(string(container.StateExited))}
	p := f.provider(t, cfg)
	f.inspectBody = covLabelledInspect(covRuntimeRef, false, map[string]string{
		crewRuntimeContractLabel: p.crewRuntimeContractDigest(),
	})

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid — a container matching the current contract must be started, not rebuilt", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 0 {
		t.Errorf("container matching the current contract was rebuilt anyway (%d creates)", len(f.creates))
	}
	if len(f.deletes) != 0 {
		t.Errorf("container matching the current contract was torn down, deletes = %v", f.deletes)
	}
}

// A RUNNING container carrying an older contract is left alone.
//
// This is the deliberate half of the design and the reason the fix is not
// simply "add the fields to the drift check". Tearing down a running crew
// container kills whatever is executing in it with exit 137 — the assignment
// path already learned that once, when an image-drift recreate clobbered a
// lead's run mid-flight. A configuration improvement is not worth a killed
// run, so a live crew keeps serving and the staleness is REPORTED instead:
// on the log, and on `crewship crew container-status`.
func TestEnsureCrewRuntime_RunningContainerWithOldContractIsNotKilled(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateRunning)),
		inspectBody: covLabelledInspect(covRuntimeRef, true, map[string]string{"crewship.kind": "crew"}),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid — a running crew must keep serving", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deletes) != 0 {
		t.Errorf("a RUNNING crew container was torn down for a configuration change (deletes = %v); every agent executing in it dies with exit 137", f.deletes)
	}
	if len(f.creates) != 0 {
		t.Errorf("a replacement was created while the old container was still running (%d creates)", len(f.creates))
	}
}

// The report itself. `crewship crew container-status` is where an operator
// finds out, so the verdict has to reach provider.ContainerStatus.
func TestContainerStatus_ReportsRuntimeContract(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	f := &covRT{}
	p := f.provider(t, cfg)

	t.Run("stale", func(t *testing.T) {
		f.mu.Lock()
		f.inspectBody = covLabelledInspect(covRuntimeRef, true, map[string]string{"crewship.kind": "crew"})
		f.mu.Unlock()
		st, err := p.ContainerStatus(context.Background(), "old-cid")
		if err != nil {
			t.Fatalf("ContainerStatus: %v", err)
		}
		if st.RuntimeContract != provider.RuntimeContractStale {
			t.Errorf("RuntimeContract = %q, want %q — a container with no contract stamp predates the label and every control added with it",
				st.RuntimeContract, provider.RuntimeContractStale)
		}
	})

	t.Run("current", func(t *testing.T) {
		f.mu.Lock()
		f.inspectBody = covLabelledInspect(covRuntimeRef, true, map[string]string{
			crewRuntimeContractLabel: p.crewRuntimeContractDigest(),
		})
		f.mu.Unlock()
		st, err := p.ContainerStatus(context.Background(), "old-cid")
		if err != nil {
			t.Fatalf("ContainerStatus: %v", err)
		}
		if st.RuntimeContract != provider.RuntimeContractCurrent {
			t.Errorf("RuntimeContract = %q, want %q", st.RuntimeContract, provider.RuntimeContractCurrent)
		}
	})
}

// The create request carries the stamp, or nothing downstream has anything to
// compare against.
func TestCrewContainer_CarriesRuntimeContractLabel(t *testing.T) {
	t.Parallel()

	f := &covRT{}
	p := f.provider(t, covRTConfig(t))
	if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	got := f.realCreate(t).Config.Labels[crewRuntimeContractLabel]
	if got == "" {
		t.Fatal("create request carries no contract label; every container it creates is indistinguishable from one created by an older build")
	}
	if want := p.crewRuntimeContractDigest(); got != want {
		t.Errorf("label = %q, want %q (the digest this build would compare against)", got, want)
	}
}
