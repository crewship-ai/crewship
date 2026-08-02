package docker

// The runtime contract: what this build asks the daemon for on every crew
// container, and how a container created by an older build is noticed (#1642).
//
// # The problem
//
// Two days of hardening landed on the crew container — Init, Ulimits with
// core: 0, GroupAdd, MemorySwap == Memory, ShmSize, a RestartPolicy, an
// explicit ["NONE"] healthcheck, LANG/TZ, StopTimeout, Labels. Every one of
// them is applied at ContainerCreate and nowhere else, and a container that
// already exists is reused: reconcileExistingContainer checked the image, the
// required mounts and the /secrets tmpfs, and nothing else. So a crew that was
// already running kept the OLD configuration indefinitely, and nothing said so.
//
// Measured on dev1 after the fixes merged: the server was new and the crew
// container was old — /proc/1/comm was still `sleep` rather than an init, `id`
// reported groups=1001 alone, `ulimit -c` was unlimited and /dev/shm was
// 64 MB. Two of those are security controls. core: 0 closes a path where a
// crashing agent writes a core dump containing every credential in its exec
// environment onto /output/<slug>, a host-persistent bind. Until the container
// is recreated, that fix is not in effect on that crew.
//
// # Why a digest rather than a field list
//
// The obvious fix — add Init, Ulimits, ShmSize, GroupAdd… to the drift check —
// has to be written again by whoever adds the next control, and the failure
// mode of forgetting is silence, which is exactly the defect. So the check is
// computed FROM the builder instead of restated next to it: assembleCrewSpec
// is run over a canonical crew, the resulting Config/HostConfig pair is
// serialised, and the digest of that is what gets compared. A field a future
// PR adds to the HostConfig is in the digest the moment it is in the builder,
// without anyone remembering anything.
//
// # What it deliberately does NOT cover
//
// The canonical crew is fixed, so nothing crew-specific reaches the digest:
// this detects "the build's contract changed", not "this crew's configuration
// changed". A crew whose container_memory_mb is edited while its container
// runs still keeps the old limit until the container is recreated, exactly as
// before. That is a real gap and a different one — it needs the crew's config
// at the comparison point, which the status surface below cannot obtain
// without reconstructing it, and a second reconstruction that can disagree
// with the first is the failure mode HANDOFF-2026-08-02.md §6 warns about for
// capability reporting. Filed as #1681 rather than half-solved here.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/provider"
)

// crewRuntimeContractLabel carries the digest on the container itself. A label
// and not a name or an annotation: labels survive on the container, are
// readable from a plain inspect (so the status path needs no extra daemon
// call), and are what `docker ps --filter` can already select on.
const crewRuntimeContractLabel = "crewship.runtime-contract"

// The canonical crew. Every value here exists to be CONSTANT, not to be
// realistic: the digest must depend on the build and the provider's own
// configuration, never on which crew is being started. A memory figure is
// still needed because ShmSize and the /tmp tmpfs size are derived from it
// (crewTmpfsSizes), and pinning it means a change to that derivation moves the
// digest while a crew being 4 GiB rather than 8 GiB does not.
const (
	contractCrewID     = "contract"
	contractCrewSlug   = "contract"
	contractMemoryMB   = 1024
	contractCPUs       = 1.0
	contractOutputDir  = "/crewship-contract/output"
	contractWorkingDir = "/crewship-contract/workspace"
	contractCrewDir    = "/crewship-contract/crew"
)

// crewRuntimeContractDigest returns the digest of the crew-container contract
// this build applies, or "" when it cannot be computed.
//
// Computed once per provider: it is a pure function of the binary plus the
// provider's own configuration (network name, sidecar/entrypoint paths, OCI
// runtime, detected container runtime, cgroup generation), none of which
// changes while the process runs. That matters because this is consulted on
// every cold reconcile and on every container-status call.
//
// Fails soft. A provider that cannot build its own canonical spec — the only
// realistic cause is a misconfigured SidecarBinaryPath, which fails the real
// create too — returns "", and every caller treats "" as "no opinion" rather
// than as drift. Tearing a crew container down because we could not compute a
// hash would be a far worse bug than the one this file exists to fix.
func (p *Provider) crewRuntimeContractDigest() string {
	p.contractOnce.Do(func() {
		cfg, hostCfg, err := p.assembleCrewSpec(
			provider.CrewConfig{ID: contractCrewID, Slug: contractCrewSlug},
			"", // image: the container's own image is checked separately (image drift)
			p.ociRuntime(),
			contractMemoryMB,
			contractCPUs,
			crewDirs{output: contractOutputDir, workspace: contractWorkingDir, crew: contractCrewDir},
			[]string{"CREWSHIP_CREW_ID=" + contractCrewID},
			nil, // no image ENV: applyDefaultEnv/applyAgentLoginPath both tolerate it
		)
		if err != nil {
			p.logger.Warn("cannot compute the crew runtime-contract digest; container-config drift will not be reported",
				"error", err)
			return
		}
		p.contractDigest = digestCrewSpec(cfg, hostCfg)
	})
	return p.contractDigest
}

// digestCrewSpec hashes a create spec. Split out from the caller so the
// sensitivity of the digest to each field can be tested directly — mutate one
// field of a real spec, the digest must move — which is the property the whole
// mechanism rests on.
//
// JSON rather than a hand-written field list, for the same reason the spec
// comes from the builder: encoding/json walks whatever the struct has today,
// including a field added by a moby upgrade, and sorts map keys so Labels and
// Tmpfs are order-stable.
func digestCrewSpec(cfg *container.Config, hostCfg *container.HostConfig) string {
	payload, err := json.Marshal(struct {
		Config     *container.Config     `json:"config"`
		HostConfig *container.HostConfig `json:"host_config"`
	}{cfg, hostCfg})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	// 12 hex chars = 48 bits. This is a change detector, not a security
	// boundary — nobody is attacking it, and the only cost of a collision is
	// one missed recreate — and it has to be readable in a `docker inspect`
	// and in a log line.
	return hex.EncodeToString(sum[:])[:12]
}

// runtimeContractOf reads the digest a container was created with. Empty means
// the container predates the label entirely, which is itself the drift this
// file is about: every container created before #1642 carries no such label.
func runtimeContractOf(cfg *container.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Labels[crewRuntimeContractLabel]
}

// runtimeContractStatus compares a container's stamp against this build's and
// returns the verdict for provider.ContainerStatus. Returns "" — no opinion —
// when this provider cannot compute its own digest.
func (p *Provider) runtimeContractStatus(cfg *container.Config) string {
	want := p.crewRuntimeContractDigest()
	if want == "" {
		return ""
	}
	if runtimeContractOf(cfg) == want {
		return provider.RuntimeContractCurrent
	}
	return provider.RuntimeContractStale
}
