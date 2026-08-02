package api

// Host-level admission policy (#1668) — the instance settings behind
// "should this host start one more crew container right now".
//
// Companion to crew_resource_policy.go, which owns the PER-CREW sizing
// numbers. The split is the point: that file answers "how big is one crew",
// this one answers "how many of them fit on this box at once".
//
// # What the threshold means, and why it is not a fraction
//
// The memory floor is ONE AGENT PLUS A RESERVE, in absolute MiB:
//
//	RequiredFreeMB = runtime.agent_min_memory_mb + runtime.host_memory_reserve_mb
//
// The alternatives were considered and rejected:
//
//   - A fixed MB floor alone does not scale with the agent the operator
//     actually runs. An instance running a browser-automation stack has
//     already told us its agent needs 6 GiB by setting
//     runtime.agent_min_memory_mb; a separate hard-coded floor would ignore
//     that and admit a run the host cannot hold.
//   - A fraction of MemTotal answers the wrong question. "Keep 10% free" is
//     819 MiB on an 8 GiB host and 51 GiB on a 512 GiB one, for the same
//     workload. The question here is absolute — does the NEXT container fit —
//     and MemAvailable is already an absolute answer.
//   - A PSI stall percentage alone is lagging: a host with 200 MiB free and
//     nothing running yet reports 0.00% and sails through. PSI is kept as a
//     secondary veto (below) precisely because it catches what MemAvailable
//     misses, not as the primary gate.
//
// Reusing runtime.agent_min_memory_mb rather than inventing a fourth number
// is deliberate and follows the precedent set when that setting landed: it
// already has two consumers (the crew sizing advisory, and the per-crew
// concurrency budget in assignments_queue.go), both asking "how much memory
// does one agent need". This is the third, asking the same question of the
// host. Two literals that happen to agree are how the scheduler ends up
// dispatching runs the crews cannot hold.
//
// The RESERVE is what is left for everything that is not an agent: the
// kernel, dockerd + containerd + one shim per container, crewshipd itself,
// and enough page cache that the host stays responsive. 1 GiB is small
// against any host that can run agents at all (one agent alone wants 2 GiB)
// and large enough that admitting the last container does not tip the box
// into reclaim.

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/admission"
)

const (
	// SettingHostMemoryReserveMB is the host headroom kept free ON TOP of one
	// agent's memory. 0 disables the host-memory gate entirely — a reserve of
	// exactly zero (admit as long as one agent fits with nothing left for the
	// kernel) is not a configuration anyone wants, so the value is free to
	// mean "off" instead.
	SettingHostMemoryReserveMB = "runtime.host_memory_reserve_mb"

	// SettingHostMemoryPressurePct is the PSI "some avg10" share above which
	// the host counts as already stalling on memory. 0 disables the veto.
	// Only readable on Linux kernels built with CONFIG_PSI; where the file is
	// absent the veto simply never fires (it is NOT treated as infinite
	// pressure).
	SettingHostMemoryPressurePct = "runtime.host_memory_pressure_pct"

	// SettingMaxConcurrentContainerStarts bounds simultaneous container
	// creates/starts. 0 = unbounded.
	SettingMaxConcurrentContainerStarts = "runtime.max_concurrent_container_starts"

	// SettingContainerStartStaggerMs is the minimum spacing between two
	// admitted starts. 0 = no stagger.
	SettingContainerStartStaggerMs = "runtime.container_start_stagger_ms"
)

const (
	defaultHostMemoryReserveMB = 1024

	// 20%: below that a host is doing ordinary reclaim, which is healthy and
	// is not a reason to stop working. Above it, one task in five is waiting
	// on memory rather than running, and the container we are about to add
	// would be waiting too. Chosen as a tripwire, not a set point — the knob
	// an operator tunes for headroom is the MiB reserve.
	defaultHostMemoryPressurePct = 20.0

	// 4 concurrent starts. Network-namespace creation takes a single global
	// lock whose cost grows with concurrency: clone() with CLONE_NEWNET is
	// 1.45 ms serial and ~418 ms at 24x (Ant Group, OSDI '25, production
	// fleet). 4 keeps contention on that lock low while still letting a
	// genuine wake wave proceed several at a time. Deliberately far below the
	// orchestrator's runSem of 8: that bounds RUNS, which are mostly LLM
	// latency, while this bounds CONTAINER STARTS, which are mostly kernel
	// work on one shared lock.
	defaultMaxConcurrentContainerStarts = 4

	// 150 ms between starts. With the bound above this admits ~6.7 starts/s,
	// comfortably inside the ~200 containers/s netns ceiling SOCK measured
	// (USENIX ATC '18) while spreading a twenty-crew cron minute over ~3 s
	// instead of one millisecond. The last crew in such a wave pays about
	// what a single container start costs anyway (~250-450 ms), and every
	// start in the wave gets cheaper for it.
	defaultContainerStartStagger = 150 * time.Millisecond

	// Sanity ceilings. A value past these is a typo, not a policy, and is
	// answered with the default rather than clamped — same convention as
	// agentMinMemoryMB: a clamped nonsense value is a gate running on a
	// number nobody chose.
	maxHostMemoryReserveMB          = maxCrewContainerMemoryMB // 256 GiB
	maxConcurrentContainerStartsCap = 4096
	maxContainerStartStaggerMs      = 60_000
)

// AdmissionLimits resolves the live host-admission policy. Read fresh on every
// decision (it is one indexed SELECT per key, on a path that only runs when a
// container is actually being started), so an operator's
// `crewship instance settings set` takes effect on the next start rather than
// on the next daemon restart.
//
// A nil or unreadable DB yields the compiled defaults. It must never yield a
// gate that is shut: the whole point of holding a run is that it eventually
// proceeds.
func AdmissionLimits(ctx context.Context, db *sql.DB) admission.Limits {
	lim := admission.Limits{
		MaxConcurrentStarts: settingInt(ctx, db, SettingMaxConcurrentContainerStarts,
			defaultMaxConcurrentContainerStarts, 0, maxConcurrentContainerStartsCap),
		MinStartInterval: time.Duration(settingInt(ctx, db, SettingContainerStartStaggerMs,
			int(defaultContainerStartStagger/time.Millisecond), 0, maxContainerStartStaggerMs)) * time.Millisecond,
		MaxPressurePct: settingFloat(ctx, db, SettingHostMemoryPressurePct,
			defaultHostMemoryPressurePct, 0, 100),
	}

	reserve := settingInt(ctx, db, SettingHostMemoryReserveMB,
		defaultHostMemoryReserveMB, 0, maxHostMemoryReserveMB)
	if reserve > 0 {
		lim.RequiredFreeMB = int64(agentMinMemoryMB(ctx, db) + reserve)
	}
	return lim
}

// settingInt reads one integer instance setting, answering def for anything
// missing, unparseable, or outside [lo, hi].
func settingInt(ctx context.Context, db *sql.DB, key string, def, lo, hi int) int {
	raw, ok := readInstanceSetting(ctx, db, key)
	if !ok {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}

func settingFloat(ctx context.Context, db *sql.DB, key string, def, lo, hi float64) float64 {
	raw, ok := readInstanceSetting(ctx, db, key)
	if !ok {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}
