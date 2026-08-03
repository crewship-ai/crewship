package chatbridge

// ChatInfo → provider.CrewConfig, in one place.
//
// This assembly used to exist four times: here (bridge.go), in the scheduler,
// in the agent-webhook handler and in the pipeline's agent step. All four start
// the same crew container from the same resolved agent, and the three copies
// outside chat were each a strict subset — no containerEnv, no feature mounts,
// no capabilities, no login PATH, and (the reason #1708 was filed) no sidecar
// services. Which of them cold-created a crew's container therefore decided
// what that container could do for the rest of its life.
//
// The DB-side twin of this function is internal/api.buildCrewRuntimeConfig,
// which the callers that hold a crew id rather than a resolved agent use. The
// two produce the same config; crewstart merges whichever half a caller has.

import (
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

// CrewRuntimeConfig assembles the crew container config for this resolved
// agent. defaultMemoryMB/defaultCPUs apply only where the crew configured
// nothing (pass 0 to leave the provider's own default to decide).
//
// The error reports a services_json that could not be decoded. It is
// deliberately NOT fatal to the returned config: the caller gets a config for
// the same crew minus its sidecars, so a stale services column costs the crew
// its databases and not its runtime. Every caller logs it; the chat path also
// shows it to the user.
func (i *ChatInfo) CrewRuntimeConfig(defaultMemoryMB int, defaultCPUs float64) (provider.CrewConfig, error) {
	if i == nil {
		return provider.CrewConfig{}, nil
	}

	memoryMB := i.MemoryMB
	if memoryMB <= 0 {
		memoryMB = defaultMemoryMB
	}
	cpus := i.CPUs
	if cpus <= 0 {
		cpus = defaultCPUs
	}

	// Merge feature-level ContainerEnv (from CachedRequirements) with the
	// root-level ContainerEnv. Root wins on conflict so user intent in
	// devcontainer.json overrides feature defaults.
	mergedEnv := make(map[string]string)
	if i.CachedRequirements != nil {
		for k, v := range i.CachedRequirements.ContainerEnv {
			mergedEnv[k] = v
		}
	}
	for k, v := range i.ContainerEnv {
		mergedEnv[k] = v
	}

	cfg := provider.CrewConfig{
		ID:             i.CrewID,
		Slug:           i.CrewSlug,
		MemoryMB:       memoryMB,
		CPUs:           cpus,
		Image:          i.RuntimeImage,
		CachedImage:    i.CachedImage,
		NetworkMode:    i.NetworkMode,
		AllowedDomains: i.AllowedDomains,
		TTLHours:       i.TTLHours,
		ContainerEnv:   mergedEnv,
	}
	if i.CachedRequirements != nil {
		cfg.LoginPath = i.CachedRequirements.LoginPath
		cfg.Privileged = i.CachedRequirements.Privileged
		cfg.Init = i.CachedRequirements.Init
		cfg.CapAdd = append(cfg.CapAdd, i.CachedRequirements.CapAdd...)
		cfg.SecurityOpt = append(cfg.SecurityOpt, i.CachedRequirements.SecurityOpt...)
		for _, m := range i.CachedRequirements.Mounts {
			// Expand devcontainer.json variables (e.g. ${devcontainerId})
			// before passing source/target to Docker — Docker rejects volume
			// names containing "$" with a cryptic error otherwise.
			cfg.ExtraMounts = append(cfg.ExtraMounts, provider.CrewMount{
				Source: devcontainer.ExpandVars(m.Source, i.CrewID),
				Target: devcontainer.ExpandVars(m.Target, i.CrewID),
				Type:   m.Type,
			})
		}
		cfg.PostStartCommands = append(cfg.PostStartCommands, i.CachedRequirements.PostStartCommands...)
	}
	// Root-level postStartCommand runs after feature hooks so user intent
	// (e.g. "start my app-specific DB") wins over feature defaults.
	cfg.PostStartCommands = append(cfg.PostStartCommands, i.RootPostStart...)

	if i.ServicesJSON == "" {
		return cfg, nil
	}
	svcs, err := crewstart.DecodeServices(i.ServicesJSON, i.ServiceEnvLookup)
	if err != nil {
		return cfg, err
	}
	cfg.Services = svcs
	return cfg, nil
}
