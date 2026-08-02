package docker

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// MemorySwappiness is a cgroup v1 knob that does not exist in cgroup v2 — there
// is no memory.swappiness file to write. Three runtimes, three behaviours for
// the same field on the same kind of host, all observed rather than reasoned
// about:
//
//	docker 28.0.4, cgroup v2 (CI, ubuntu-latest)
//	  accepts the create and returns a warning nobody was reading:
//	    "Your kernel does not support memory swappiness capabilities or the
//	     cgroup is not mounted. Memory swappiness discarded."
//
//	podman 6.0.2, cgroup v2 (applehv, macOS 26)
//	  accepts the create and then FAILS THE START:
//	    crun: cannot set memory swappiness with cgroupv2: OCI runtime error
//	  The container exists, is configured, and can never run.
//
// A field that is silently dropped on one runtime and fatal on another is worth
// removing rather than special-casing per runtime, because the condition is not
// the runtime at all — it is the host's cgroup version, which the daemon
// reports and which every modern Linux distribution answers "2" to.
//
// Nothing is lost by dropping it there. Swap is disabled by MemorySwap ==
// Memory, which works on both cgroup generations and is the control that
// actually matters; swappiness was belt-and-braces against the kernel paging
// out anonymous memory under host pressure, and on v2 it was never in effect on
// ANY runtime — Docker just declined to make that visible.
func TestMemorySwappinessOnlyOnCgroupV1(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		cgroupVersion string
		wantSet       bool
	}{
		// The v2 case is the one that matters: it is what every current
		// distribution, Docker Desktop, and podman machine actually run.
		{"cgroup v2 omits it", "2", false},
		{"cgroup v1 still gets it", "1", true},
		// An unreadable or absent version must NOT be treated as v1. Sending a
		// field that kills the container start on a host we could not identify
		// is the worse failure of the two, and the field buys nothing on v2.
		{"unknown version omits it", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newSwappinessProvider(t, tc.cgroupVersion)
			_, hostCfg, err := p.buildCrewContainerConfig(
				context.Background(),
				provider.CrewConfig{ID: "crew-id", Slug: "crew"},
				"crewship-crew", "debian:bookworm-slim", "", 1024, 1,
				crewDirs{output: t.TempDir(), workspace: t.TempDir(), crew: t.TempDir()},
			)
			if err != nil {
				t.Fatalf("build config: %v", err)
			}
			got := hostCfg.Resources.MemorySwappiness
			if tc.wantSet && got == nil {
				t.Fatalf("cgroup v%s: MemorySwappiness omitted, but v1 is the one generation where it does something", tc.cgroupVersion)
			}
			if !tc.wantSet && got != nil {
				t.Fatalf("cgroup version %q: MemorySwappiness set to %d — podman/crun refuses to START a container carrying it on cgroup v2, so every crew on that host is dead on arrival", tc.cgroupVersion, *got)
			}
			if tc.wantSet && *got != 0 {
				t.Errorf("cgroup v1: MemorySwappiness = %d, want 0 — any other value lets the kernel page out the crew's anonymous memory", *got)
			}
		})
	}

	// Dropping swappiness must not quietly drop the control that does the real
	// work. MemorySwap == Memory is Docker's documented way to say "no swap"
	// and it is honoured on both cgroup generations, so it has to survive on
	// every host regardless of what the version probe returned.
	t.Run("swap stays disabled on every cgroup version", func(t *testing.T) {
		t.Parallel()
		for _, v := range []string{"1", "2", ""} {
			p := newSwappinessProvider(t, v)
			_, hostCfg, err := p.buildCrewContainerConfig(
				context.Background(),
				provider.CrewConfig{ID: "crew-id", Slug: "crew"},
				"crewship-crew", "debian:bookworm-slim", "", 1024, 1,
				crewDirs{output: t.TempDir(), workspace: t.TempDir(), crew: t.TempDir()},
			)
			if err != nil {
				t.Fatalf("build config: %v", err)
			}
			if hostCfg.Resources.MemorySwap != hostCfg.Resources.Memory {
				t.Errorf("cgroup version %q: MemorySwap=%d Memory=%d — unequal means Docker's 2x-memory swap default is back",
					v, hostCfg.Resources.MemorySwap, hostCfg.Resources.Memory)
			}
		}
	})
}

func newSwappinessProvider(t *testing.T, cgroupVersion string) *Provider {
	t.Helper()
	p := newSecretsSpecProvider(t, "docker")
	p.cgroupVersion = cgroupVersion
	return p
}

// TestDetectCgroupVersion covers the probe itself: an unreachable or silent
// daemon must yield "", which the builder above treats as "not v1".
func TestDetectCgroupVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"daemon reports v2", `{"CgroupVersion":"2","DockerRootDir":"/var/lib/docker"}`, "2"},
		{"daemon reports v1", `{"CgroupVersion":"1","DockerRootDir":"/var/lib/docker"}`, "1"},
		{"daemon omits the field", `{"DockerRootDir":"/var/lib/docker"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/info") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.body))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			})
			defer cleanup()
			if got := detectCgroupVersion(context.Background(), p.client); got != tc.want {
				t.Errorf("detectCgroupVersion = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("unreachable daemon yields empty, never a guess", func(t *testing.T) {
		t.Parallel()
		p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		defer cleanup()
		if got := detectCgroupVersion(context.Background(), p.client); got != "" {
			t.Errorf("detectCgroupVersion on a failing daemon = %q, want \"\"", got)
		}
	})
}
