package api

// Sizing policy for the two runtime values a crew can configure,
// container_memory_mb and container_cpus (#1627, reshaped in #1638).
//
// There are two floors, and the distinction is the whole design.
//
// HARD floor — Docker's own. Below 0.01 NanoCPUs the daemon refuses the
// container create with "Range of CPUs is from 0.01"; below 6 MiB with
// "Minimum memory limit allowed is 6MB". Neither error names the crew or the
// field, and nothing between the manifest and the daemon used to check, so
// `container_cpus: 0.005` was accepted everywhere and then wedged EVERY agent
// run on a message the operator could not act on. Rejecting these costs the
// operator nothing — the configuration could never have run — and buys an
// error that arrives at the right time, naming the right field. This is a 400.
//
// ADVISORY floor — what one agent actually needs to run. A crew sized between
// the two floors is created successfully by the daemon and then OOM-kills the
// agent CLI on start (exit 137), or never finishes starting inside the run
// timeout. That is worth telling the operator about. It is NOT worth refusing:
//
//   - The operator may have chosen a small crew deliberately (an agentless
//     routine runner, a crew that only ever shells out, a memory-constrained
//     host where several small crews beat one large one).
//   - Refusing does not make an undersized crew any bigger. It only stops the
//     crew existing, which is a worse outcome than a crew that runs slowly or
//     needs resizing after the first failed run.
//   - The right end-state is admission control that holds a run until there is
//     room, not a create-time veto (docs/prd/crew-runtime-capacity.md §7.4).
//     A hard floor would be the wrong instrument left in place permanently.
//
// So: accept, and return a warning on the response. See crewSizingAdvisories.
//
// The ceilings are plain constants rather than a per-workspace quota. A quota
// an operator can raise needs a workspaces column, an admin route and a CLI
// command to go with it (docs/prd/crew-runtime-capacity.md §1.1 sketches
// that), and none of it helps the case this guard exists for: a MANAGER
// typing container_memory_mb: 999999. The numbers below sit well above any
// host we expect to run on, so a legitimate large-host configuration is never
// blocked, while a typo is.
//
// The CPU ceiling cannot be exact at this tier. The daemon's real upper limit
// is the host's core count ("Range of CPUs is from 0.01 to 8.00"), which the
// API does not know — a host-aware preflight belongs in the docker provider.
// This catches the typo class only.
//
// The hard bounds are mirrored in internal/manifest/kinds/crew.go, which
// cannot import this package. The advisory floor is deliberately NOT mirrored
// there: it is an instance setting, and offline `apply --dry-run` has no
// server to read it from.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const (
	dockerMinContainerMemoryMB         = 6
	dockerMinContainerCPUs     float64 = 0.01

	maxCrewContainerMemoryMB         = 262144 // 256 GiB
	maxCrewContainerCPUs     float64 = 512
)

// The resolved server default for a crew's container size — the ONE number
// behind "0 = use the server default" (#1638).
//
// It used to be written down twice: create substituted 4096 for a 0, update
// stored the 0 verbatim, and the runtime's own `<= 0` fallback then answered
// 8192. `crewship crew update <slug> --memory-mb 0` therefore handed the crew
// double what the identical flag produces on create, and double what the docs
// promise. Both handlers now resolve the sentinel here, so crews.container_*
// never holds 0 and no downstream fallback gets a vote.
//
// 4096 (not 8192) because that is what the schema column already defaults to
// (migrate_consts_v01_init.go) and what docs/api-reference/crews.mdx,
// docs/cli/crew.mdx and docs/manifest/crew.md all publish.
const (
	defaultCrewContainerMemoryMB         = 4096
	defaultCrewContainerCPUs     float64 = 2.0
)

// The instance settings behind the advisory floor: the resources ONE agent
// needs to run. Settable per instance because the right number depends on
// which CLI the operator runs and how heavy their toolchain is — an operator
// running a lean shell-only agent can lower it and stop being warned; one
// running a browser-automation stack can raise it and be warned earlier.
//
//	crewship instance settings set runtime.agent_min_memory_mb 4096
//
// The memory value has TWO consumers, and that is the point of it being one
// value rather than two constants that happen to agree:
//
//  1. crewSizingAdvisories — warn when a crew cannot hold one agent.
//  2. computeCrewBudget (assignments_queue.go) — divide a crew's memory by it
//     to decide how many concurrent runs fit.
//
// Both are asking "how much memory does one agent need?". They were two
// literals of 2048 that nobody had connected; moving one without the other
// gave a scheduler that dispatches runs a crew cannot hold, or an advisory
// that fires at a size the scheduler is happy with.
//
// The 2048 default is measured, not guessed. A warmed agent CLI holds
// 1.5-2 GiB once its token caches load, and the docker provider records that
// 512 MiB "caused Docker OOM-kill (exit 137) on real agent runs". 0.5 CPU is
// a quarter of the 2.0 we ship as the default and half the 1.0 the sidecar
// hands a redis (sidecar.go) — NanoCPUs is a hard CFS quota, so the fraction
// scales wall-clock directly and the Node/TS toolchain start is the CPU-bound
// part of the 6.5 s wake measured in the PRD.
//
// Note that a floor costs an idle host nothing: Memory is a cgroup CEILING,
// not a reservation (docs/prd/crew-runtime-capacity.md §5).
const (
	SettingAgentMinMemoryMB = "runtime.agent_min_memory_mb"
	SettingAgentMinCPUs     = "runtime.agent_min_cpus"

	defaultAgentMinMemoryMB         = 2048
	defaultAgentMinCPUs     float64 = 0.5
)

// readInstanceSetting fetches one app_settings value. Missing key is not an
// error — every caller here has a default.
func readInstanceSetting(ctx context.Context, db *sql.DB, key string) (string, bool) {
	if db == nil {
		return "", false
	}
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		// Includes sql.ErrNoRows. A broken settings read must not take crew
		// creation down with it, and it must not silently move the floor
		// either — falling back to the compiled default does both.
		_ = errors.Is(err, sql.ErrNoRows)
		return "", false
	}
	return v, true
}

// agentMinMemoryMB is the configured "memory one agent needs", in MiB.
//
// Anything unparseable, or outside [dockerMin, ceiling], yields the default.
// Out-of-range is treated as unusable rather than clamped on purpose: a stored
// 0 would make computeCrewBudget divide by zero, and a stored 999999999 would
// warn about every crew on the instance. Neither is a coherent answer to "how
// much memory does one agent need", so neither is honoured.
func agentMinMemoryMB(ctx context.Context, db *sql.DB) int {
	raw, ok := readInstanceSetting(ctx, db, SettingAgentMinMemoryMB)
	if !ok {
		return defaultAgentMinMemoryMB
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < dockerMinContainerMemoryMB || v > maxCrewContainerMemoryMB {
		return defaultAgentMinMemoryMB
	}
	return v
}

// agentMinCPUs is the configured "CPU one agent needs", in cores.
func agentMinCPUs(ctx context.Context, db *sql.DB) float64 {
	raw, ok := readInstanceSetting(ctx, db, SettingAgentMinCPUs)
	if !ok {
		return defaultAgentMinCPUs
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < dockerMinContainerCPUs || v > maxCrewContainerCPUs {
		return defaultAgentMinCPUs
	}
	return v
}

// resolveCrewContainerMemoryMB / resolveCrewContainerCPUs turn the "use the
// server default" sentinel (0, or a value the caller never sent) into the
// stored size. Non-positive covers both the explicit 0 and any stray negative
// that got past validation.
func resolveCrewContainerMemoryMB(memoryMB int) int {
	if memoryMB <= 0 {
		return defaultCrewContainerMemoryMB
	}
	return memoryMB
}

func resolveCrewContainerCPUs(cpus float64) float64 {
	if cpus <= 0 {
		return defaultCrewContainerCPUs
	}
	return cpus
}

// validateCrewContainerResources range-checks the container sizing fields on
// create and update against the HARD bounds only. nil means "field absent"; an
// explicit 0 is the long standing "use the server default" sentinel (the CLI's
// `--memory-mb 0` and an omitted `hostRequirements` block both depend on it)
// and stays accepted — the caller resolves it via resolveCrewContainer*.
//
// Undersized-but-runnable values do NOT come back here; they go to
// crewSizingAdvisories and are reported as warnings.
//
// The 400 names Docker as the authority so the next person to read it does not
// "fix" the floor by raising it into the band that is supposed to warn.
func validateCrewContainerResources(memoryMB *int, cpus *float64) error {
	if memoryMB != nil && *memoryMB != 0 &&
		(*memoryMB < dockerMinContainerMemoryMB || *memoryMB > maxCrewContainerMemoryMB) {
		return fmt.Errorf("container_memory_mb must be between %d and %d (0 = use the server default); "+
			"%d MiB is Docker's own minimum and the daemon refuses to create a container below it",
			dockerMinContainerMemoryMB, maxCrewContainerMemoryMB, dockerMinContainerMemoryMB)
	}
	if cpus != nil && *cpus != 0 &&
		(*cpus < dockerMinContainerCPUs || *cpus > maxCrewContainerCPUs) {
		return fmt.Errorf("container_cpus must be between %g and %g (0 = use the server default); "+
			"%g is Docker's own minimum and the daemon refuses to create a container below it",
			dockerMinContainerCPUs, maxCrewContainerCPUs, dockerMinContainerCPUs)
	}
	return nil
}

// crewSizingAdvisories reports the ways a crew's RESOLVED size will bite,
// without blocking anything. Takes the values as stored, so the advisory
// describes the crew that now exists rather than the request that made it.
//
// Each message has to carry three things or it is not actionable: the field
// and its value, the floor it is under, and the setting that moves the floor.
// An operator who disagrees with our number needs to be able to change it
// without reading our source.
func crewSizingAdvisories(ctx context.Context, db *sql.DB, memoryMB int, cpus float64) []string {
	var out []string
	if minMem := agentMinMemoryMB(ctx, db); memoryMB > 0 && memoryMB < minMem {
		out = append(out, fmt.Sprintf(
			"container_memory_mb %d is below %d, the memory one agent needs: the container will be created, "+
				"but the agent CLI is likely to be OOM-killed (exit 137) on start. Raise the crew's memory, "+
				"or lower the floor with `crewship instance settings set %s <mb>`.",
			memoryMB, minMem, SettingAgentMinMemoryMB))
	}
	if minCPUs := agentMinCPUs(ctx, db); cpus > 0 && cpus < minCPUs {
		out = append(out, fmt.Sprintf(
			"container_cpus %g is below %g, the CPU one agent needs: the container will be created, but the "+
				"agent CLI may not finish starting before the run times out. Raise the crew's CPUs, "+
				"or lower the floor with `crewship instance settings set %s <cores>`.",
			cpus, minCPUs, SettingAgentMinCPUs))
	}
	return out
}

// crewResponseWithAdvisories is a crewResponse carrying non-fatal sizing
// warnings. The crew fields are promoted by encoding/json, so the wire shape
// is the ordinary crew object with one extra `warnings` key — additive, and
// omitted entirely when there is nothing to say, so no existing client sees a
// changed response.
//
// Carried on the API response rather than computed in the CLI so that every
// client gets it: the dashboard's crew settings panel and `crewship apply`
// hit the same endpoints, and a rule implemented in the CLI would be a second
// copy of the floor that could disagree with the instance setting.
type crewResponseWithAdvisories struct {
	crewResponse
	Warnings []string `json:"warnings,omitempty"`
}
