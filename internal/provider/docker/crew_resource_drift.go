package docker

// Per-crew resource drift: the crew's own memory / CPU limit moved after its
// container was created (#1681).
//
// # Why this is not covered by the runtime contract
//
// #1642 stamps every crew container with a digest of the create spec this
// build applies, and rebuilds a stopped container carrying an older one. That
// digest is computed over a CANONICAL crew precisely so it depends on the
// build and never on which crew is starting — so a per-crew number moving does
// not move it, by design. `crewship crew update <slug> --memory-mb 8192` wrote
// the row, returned 200, and `crew get` reported the new figure while the
// running container kept the old cgroup limit and nothing anywhere said so.
//
// # Why an observation rather than a second digest
//
// The obvious symmetric fix is a second, per-crew digest stamped beside the
// contract one. It works, and it costs a label plus the discipline of keeping
// two digests apart — but it is also a SECOND RECONSTRUCTION of what the
// builder would have asked for, and a reconstruction that can silently
// disagree with the builder is the failure mode HANDOFF-2026-08-02 §6 records
// for capability reporting. So this compares against what the daemon actually
// has: Memory and NanoCPUs come straight off the container's own HostConfig.
// An observation cannot drift from the builder, because it is not derived from
// the builder at all — and the same reading is what the status surface reports
// (provider.ContainerStatus.MemoryMB / CPUs), so the operator sees the number
// this decision was made on.
//
// # The two rules that keep it honest
//
//  1. Only limits the CALLER STATED are compared. Not every EnsureCrewRuntime
//     caller carries the crew's configuration — the assignment path's
//     bare-config callers pass ID + Slug, and the 8192 MiB / 2 CPU floor is
//     substituted for them after the reconcile. Comparing that floor against a
//     4096 MiB crew would rebuild a perfectly good container against a number
//     nobody configured, which is the bug callerSpecifiedImage exists to stop
//     on the image-drift path.
//
//  2. Only limits the CONTAINER DECLARES are compared. A container reporting
//     no limit is saying nothing, not saying zero, and "no opinion" must never
//     be read as drift — the same rule the contract digest follows when it
//     cannot compute itself. Such a container is almost certainly older than
//     this build, which is exactly what the contract check already catches.
//
// The response is #1642's asymmetry, unchanged: a stopped container is rebuilt
// (nothing is executing in it, the caller is already paying for a start, and
// the limits can ONLY change at ContainerCreate), a running one is reported
// and left serving (tearing it down SIGKILLs whatever is executing in it, and
// a stale memory limit — unlike a stale network policy — is not a live
// security exposure).

import (
	"fmt"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/provider"
)

// containerCrewLimits reads the cgroup limits a container was created with,
// in the units the crew is configured in. Zero means the container declares
// no such limit — never "zero MiB".
func containerCrewLimits(hostCfg *container.HostConfig) (memoryMB int, cpus float64) {
	if hostCfg == nil {
		return 0, 0
	}
	if hostCfg.Memory > 0 {
		memoryMB = int(hostCfg.Memory / (1024 * 1024))
	}
	if hostCfg.NanoCPUs > 0 {
		cpus = float64(hostCfg.NanoCPUs) / 1e9
	}
	return memoryMB, cpus
}

// crewResourceDrift returns a human-readable description of every configured
// limit the container disagrees with, or "" when there is nothing to compare
// or nothing disagrees.
//
// Comparison is in the daemon's own units — bytes and nano-CPUs, through the
// same conversion the builder applies — so it is exact, and a container
// created by this build from the same configuration always compares equal.
func crewResourceDrift(team provider.CrewConfig, hostCfg *container.HostConfig) string {
	if hostCfg == nil {
		return ""
	}
	var drift string
	add := func(s string) {
		if drift != "" {
			drift += ", "
		}
		drift += s
	}
	if team.MemoryMB > 0 && hostCfg.Memory > 0 {
		if want := int64(team.MemoryMB) * 1024 * 1024; want != hostCfg.Memory {
			add(fmt.Sprintf("memory %d MiB configured, container has %d MiB",
				team.MemoryMB, hostCfg.Memory/(1024*1024)))
		}
	}
	if team.CPUs > 0 && hostCfg.NanoCPUs > 0 {
		if want := int64(team.CPUs * 1e9); want != hostCfg.NanoCPUs {
			add(fmt.Sprintf("%g CPUs configured, container has %g",
				team.CPUs, float64(hostCfg.NanoCPUs)/1e9))
		}
	}
	return drift
}
