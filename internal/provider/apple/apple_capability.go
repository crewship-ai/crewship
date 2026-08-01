package apple

// What this provider does with the CrewConfig it is handed, stated out loud.
//
// EnsureCrewRuntime acts on four of CrewConfig's twenty fields — ID, Slug,
// MemoryMB, CPUs. The other sixteen were accepted and dropped in silence, so a
// caller could not tell that most of its request had been ignored (#1648).
// UnsupportedCrewConfig is the answer to "what did you actually do with this",
// asked per crew so that a field the crew never set is never mentioned.
//
// Closing the gaps themselves — the /secrets tmpfs, the sidecar and entrypoint
// binds, an init, the noexec mounts — is #1649. This file makes the gap
// visible; it does not pretend to narrow it.

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
	// the container only through the read-only bind the docker provider adds
	// at docker.go. This provider creates no such mount, so "restricted" here
	// restricts nothing. "restricted" is also the database's create-time
	// default (database.DefaultCrewNetworkMode), so this is the common case on
	// this provider rather than an exotic one.
	//
	// The crews row keeps saying "restricted" and SHOULD: it is the operator's
	// intent, and it becomes true again the moment the crew moves to docker.
	// What must not happen is a surface presenting that intent as the
	// effective state, which is why this entry is the one the crew read paths
	// look up by name.
	if strings.EqualFold(strings.TrimSpace(cfg.NetworkMode), "restricted") {
		detail := "egress is enforced by the in-container crewship-sidecar proxy, whose binary reaches " +
			"the container only through a bind mount this provider does not create, so this crew's " +
			"network is unrestricted despite being configured restricted. The setting is kept as your " +
			"intent and takes effect if the crew moves to the docker provider."
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
	// Image / CachedImage: create always uses the provider's configured
	// RuntimeImage. A crew whose tools were baked into a provisioned image
	// therefore runs without them, which is the exit-127 "no `claude` in the
	// base image" failure by another route.
	for _, f := range []struct{ name, val string }{{"Image", cfg.Image}, {"CachedImage", cfg.CachedImage}} {
		if f.val == "" || f.val == p.cfg.RuntimeImage {
			continue
		}
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: f.name, Value: f.val,
			Detail: fmt.Sprintf("the container is created from the provider's configured runtime image %q instead; "+
				"tools provisioned into the requested image will be missing at exec time", p.cfg.RuntimeImage),
		})
	}
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
	if cfg.Init {
		s.Degraded = append(s.Degraded, provider.DroppedField{
			Field: "Init",
			Detail: "no init process is used — PID 1 is `sleep infinity`, which never reaps, so orphaned " +
				"processes accumulate as zombies for the life of the container",
		})
	}
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
			Detail: "no container-preparation events are emitted, so this crew's start is absent from the " +
				"run journal and the provisioning progress UI",
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
