package apple

// What this provider does with the CrewConfig it is handed, stated out loud.
//
// EnsureCrewRuntime acts on four of CrewConfig's twenty fields — ID, Slug,
// MemoryMB, CPUs. The other sixteen were accepted and dropped in silence, so a
// caller could not tell that most of its request had been ignored (#1648).
// UnsupportedCrewConfig is the answer to "what did you actually do with this",
// asked per crew so that a field the crew never set is never mentioned.
//
// #1649 then closed two of them for real — the delivery mounts landed, so
// NetworkMode is enforced and Init is honoured — and both entries came out of
// this report. That direction matters more than it looks: an entry that
// outlives the gap it describes is not a harmless over-report. It feeds the
// crew read paths and the agent's own system prompt, so a stale "not enforced"
// instructs every agent on this provider to behave as though its egress were
// open when it is not. Under-claiming is the safe direction to be wrong in,
// not a direction it is safe to stay wrong in.
//
// Still open here: CapDrop/SecurityOpt/noexec mounts and the rest of the
// table below.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

var _ provider.CrewConfigReporter = (*Provider)(nil)

// providerName is how this runtime is named in operator-facing messages —
// matching the `container.provider` config value and docs/configuration/providers.mdx.
const providerName = "apple-container"

// UnsupportedCrewConfig reports the fields of cfg this provider will not act
// on. Every entry is Degraded — this provider blocks no crew — so the answer
// doubles as the source for the effective-state fields on the crew read paths.
func (p *Provider) UnsupportedCrewConfig(cfg provider.CrewConfig) provider.CrewConfigSupport {
	var s provider.CrewConfigSupport

	// Nothing here is Refused. The product runs on every platform it can, so a
	// setting this provider cannot apply is reported rather than used to block
	// the crew — see the class note in internal/provider/capability.go. The
	// egress entry below is the one that made refusal look right, and what
	// actually fixes it is that no read surface repeats "restricted" as though
	// it were in effect (internal/api/crew_egress_enforcement.go).

	// Egress restriction is implemented by the in-container crewship-sidecar
	// proxy (internal/orchestrator/exec_sidecar.go), and that binary reaches
	// the container through a read-only bind mount. This provider creates that
	// mount now (#1649), so the fence is real here and this entry is NOT
	// emitted in the normal case — reporting otherwise would tell every agent
	// on this provider to act as if unfenced when it is not, which is a lie in
	// the safe direction but a lie that changes behaviour.
	//
	// Nothing else in the chain is provider-specific: the orchestrator starts
	// the proxy through the plain Exec interface and every exec carries
	// HTTP_PROXY/HTTPS_PROXY (exec_env.go SidecarProxyEnv), the same mechanism
	// that fences a docker crew. There is no kernel-level fence on either
	// provider for this entry to be understating.
	//
	// The one case where it still holds is a deployment with no sidecar binary
	// configured at all. buildCreateArgs refuses to create a container then, so
	// a crew cannot start unfenced-but-reported-fenced through the create path
	// — but the reuse path returns an already-running container without
	// consulting it, and this report runs ahead of that lookup by design. A
	// container that predates the mount cannot silently serve an unfenced crew
	// either: startSidecar's health check exits non-zero when the binary is not
	// there and orchestrator_run.go fails the run rather than proceeding. So
	// the honest statement is about what this provider can deliver, not about
	// what some older container happens to have.
	if strings.EqualFold(strings.TrimSpace(cfg.NetworkMode), "restricted") && p.cfg.SidecarBinaryPath == "" {
		detail := "egress is enforced by the in-container crewship-sidecar proxy, and this deployment has " +
			"no sidecar binary configured for the provider to mount, so this crew's network is " +
			"unrestricted despite being configured restricted. Set CREWSHIP_SIDECAR_PATH (or reinstall " +
			"crewship, whose release archive bundles the binary) to make the fence real."
		if len(cfg.AllowedDomains) > 0 {
			detail += fmt.Sprintf(" The %d-entry AllowedDomains allowlist is part of the same control and "+
				"is equally unenforced.", len(cfg.AllowedDomains))
		}
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "NetworkMode", Value: cfg.NetworkMode, Detail: detail,
		})
	}

	// Everything below costs the crew a capability. None of it makes the
	// container less contained than it already is here, and none of it is
	// reported as active anywhere else in the product.

	if cfg.TTLHours > 0 {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "TTLHours", Value: fmt.Sprintf("%d", cfg.TTLHours),
			Detail: "no idle auto-stop is scheduled; the container runs until it is stopped explicitly",
		})
	}
	// Image / CachedImage are honoured now: create uses CachedImage > Image >
	// the provider default (crewImage in apple.go). Reporting them as dropped
	// would understate the provider, and a capability report that lies in the
	// cautious direction still changes what every reader does with it.
	if len(cfg.ContainerEnv) > 0 {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "ContainerEnv", Value: sortedKeys(cfg.ContainerEnv),
			Detail: "devcontainer containerEnv variables are not set on the container; only CREWSHIP_CREW_ID is",
		})
	}
	if strings.TrimSpace(cfg.LoginPath) != "" {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "LoginPath", Value: cfg.LoginPath,
			Detail: "the captured login-shell PATH is not set on the container, so feature/pipx tool " +
				"directories are not on PATH for a non-login exec",
		})
	}
	if cfg.Privileged {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field:  "Privileged",
			Detail: "the container is not created privileged; features that need it (docker-in-docker) will not work",
		})
	}
	// Init is deliberately absent from this report. The container is created
	// with --init unconditionally (#1649), exactly as the docker provider sets
	// HostConfig.Init unconditionally, so there is no configuration of this
	// field the provider fails to honour — asking for an init gets one, and not
	// asking for one gets one anyway because the sidecar is always reparented
	// onto PID 1 and would otherwise leak zombies.
	if len(cfg.CapAdd) > 0 {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "CapAdd", Value: strings.Join(cfg.CapAdd, ","),
			Detail: "no additional Linux capabilities are granted; features requiring them will fail at runtime",
		})
	}
	if len(cfg.SecurityOpt) > 0 {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "SecurityOpt", Value: strings.Join(cfg.SecurityOpt, ","),
			Detail: "security options are not applied to the container",
		})
	}
	if len(cfg.ExtraMounts) > 0 {
		targets := make([]string, 0, len(cfg.ExtraMounts))
		for _, m := range cfg.ExtraMounts {
			targets = append(targets, m.Source+"->"+m.Target)
		}
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "ExtraMounts", Value: strings.Join(targets, ","),
			Detail: "feature-requested mounts are not attached; only /workspace, /output and /crew are bound",
		})
	}
	if len(cfg.PostStartCommands) > 0 {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "PostStartCommands", Value: fmt.Sprintf("%d command(s)", len(cfg.PostStartCommands)),
			Detail: "post-start hooks are not executed on create or restart",
		})
	}
	if cfg.InitHookEnabled {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field:  "InitHookEnabled",
			Detail: "/crew/init.sh is not executed on container start",
		})
	}
	if cfg.ProvisionSink != nil {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "ProvisionSink",
			Detail: "container-preparation steps are not emitted, so this crew's start is absent from the " +
				"run journal and the provisioning progress UI; capacity holds ARE reported (#1675)",
		})
	}
	if len(cfg.Services) > 0 {
		names := make([]string, 0, len(cfg.Services))
		for _, svc := range cfg.Services {
			names = append(names, svc.Name)
		}
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "Services", Value: strings.Join(names, ","),
			Detail: "sidecar containers are not started; the agent cannot reach them by name",
		})
	}

	return s
}

func sortedKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
