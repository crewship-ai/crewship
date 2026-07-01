package api

// Crew runtime-config resolution + the dispatch-time provisioning gate.
//
// Background: chat-driven runs (internal/chatbridge) resolve a crew's FULL
// runtime config — cached (provisioned) image, containerEnv, mounts, caps,
// resource limits — and start the container from the provisioned image that
// has the agent CLI (`claude`) and every provisioned tool baked in. The
// mission/assignment dispatch path historically did NOT: it called the bare
// GetOrCreateContainer({slug, id}) which falls back to the runtime base image.
// On a cold crew (freshly seeded, never provisioned, or with a pruned cache
// tag) that base image has no `claude`, so the agent exec died with exit 127
// ("stdbuf: failed to run command 'claude': No such file or directory").
//
// This file gives the dispatch path the same two guarantees chatbridge has:
//  1. EnsureProvisioned — block until the devcontainer image is built+present,
//     triggering a build (and the provision.* WS events the toolbar renders)
//     when needed.
//  2. buildCrewRuntimeConfig — assemble the provider.CrewConfig from the crew's
//     DB row so the container is created from the PROVISIONED image.
//
// Keep buildCrewRuntimeConfig in sync with internal/chatbridge/bridge.go
// (~531-592); a future refactor should unify the two into one shared resolver.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

// crewNeedsProvision mirrors chatbridge.devcontainerNeedsProvision: a crew
// needs a provisioned (cached) image when it declares a mise toolset, or a
// devcontainer config with features / a postCreateCommand. Configs that are
// no-ops at provision time (e.g. only containerEnv) launch straight from the
// runtime image, so they never block dispatch on a build.
func crewNeedsProvision(devcontainerCfgJSON, miseJSON string) bool {
	if strings.TrimSpace(miseJSON) != "" {
		return true
	}
	if strings.TrimSpace(devcontainerCfgJSON) == "" {
		return false
	}
	cfg, err := devcontainer.ParseBytes([]byte(devcontainerCfgJSON))
	if err != nil {
		// Unparseable config can't be provisioned either — don't block the
		// crew on something we can't act on.
		return false
	}
	return len(cfg.Features) > 0 || cfg.PostCreateCommand != nil
}

// buildCrewRuntimeConfig loads a crew's runtime configuration from the DB and
// assembles the provider.CrewConfig the container provider needs to start the
// crew container from its PROVISIONED image — same image, env, mounts, caps and
// resource limits a chat-driven run would use.
//
// Note: sidecar Services (services_json) are intentionally NOT resolved here —
// they require credential-vault lookups the chat resolver owns. Crews that
// declare sidecars get them started on the chat path; the dispatch path reuses
// the already-running container. Callers should log if services_json is set and
// the container is being cold-created. (Tracked for the resolver-unification
// follow-up.)
func buildCrewRuntimeConfig(ctx context.Context, db *sql.DB, crewID, workspaceID string) (provider.CrewConfig, error) {
	var (
		slug                       string
		networkMode, allowedDomain sql.NullString
		memoryMB, ttlHours         sql.NullInt64
		cpus                       sql.NullFloat64
		runtimeImage, cachedImage  sql.NullString
		cachedRequirements         sql.NullString
		devcontainerCfg            sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT slug, network_mode, allowed_domains,
		       container_memory_mb, container_cpus, container_ttl_hours,
		       runtime_image, cached_image, cached_requirements,
		       devcontainer_config
		FROM crews
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&slug, &networkMode, &allowedDomain,
		&memoryMB, &cpus, &ttlHours,
		&runtimeImage, &cachedImage, &cachedRequirements,
		&devcontainerCfg)
	if err != nil {
		return provider.CrewConfig{}, fmt.Errorf("load crew runtime config: %w", err)
	}

	cfg := provider.CrewConfig{
		ID:          crewID,
		Slug:        slug,
		MemoryMB:    int(memoryMB.Int64),
		CPUs:        cpus.Float64,
		NetworkMode: networkMode.String,
		TTLHours:    int(ttlHours.Int64),
		Image:       runtimeImage.String,
		CachedImage: cachedImage.String,
	}
	if allowedDomain.Valid && allowedDomain.String != "" {
		var domains []string
		if json.Unmarshal([]byte(allowedDomain.String), &domains) == nil {
			cfg.AllowedDomains = domains
		}
	}

	// containerEnv merge precedence (matches chatbridge): feature-aggregated
	// containerEnv from cached_requirements first, then the crew's own
	// devcontainer.json containerEnv overrides.
	merged := map[string]string{}
	var reqs *devcontainer.AggregatedRequirements
	if cachedRequirements.Valid && cachedRequirements.String != "" {
		var r devcontainer.AggregatedRequirements
		if json.Unmarshal([]byte(cachedRequirements.String), &r) == nil {
			reqs = &r
			for k, v := range r.ContainerEnv {
				merged[k] = v
			}
		}
	}
	if devcontainerCfg.Valid && devcontainerCfg.String != "" {
		var dc struct {
			ContainerEnv map[string]string `json:"containerEnv"`
		}
		if json.Unmarshal([]byte(devcontainerCfg.String), &dc) == nil {
			for k, v := range dc.ContainerEnv {
				merged[k] = v
			}
		}
	}
	if len(merged) > 0 {
		cfg.ContainerEnv = merged
	}

	if reqs != nil {
		cfg.LoginPath = reqs.LoginPath
		cfg.Privileged = reqs.Privileged
		cfg.Init = reqs.Init
		cfg.CapAdd = append(cfg.CapAdd, reqs.CapAdd...)
		cfg.SecurityOpt = append(cfg.SecurityOpt, reqs.SecurityOpt...)
		for _, m := range reqs.Mounts {
			cfg.ExtraMounts = append(cfg.ExtraMounts, provider.CrewMount{
				Source: devcontainer.ExpandVars(m.Source, crewID),
				Target: devcontainer.ExpandVars(m.Target, crewID),
				Type:   m.Type,
			})
		}
		cfg.PostStartCommands = append(cfg.PostStartCommands, reqs.PostStartCommands...)
	}

	return cfg, nil
}

// EnsureProvisioned blocks until the crew's devcontainer image is built and
// present on the local Docker daemon, triggering a provisioning job if needed.
// It is the dispatch-time guarantee that the crew container can be created from
// a provisioned image (with the agent CLI + tools) rather than the bare runtime
// image — so a cold crew "just works" instead of failing the run with exit 127.
//
// Behaviour:
//   - crew needs no provisioning (no features/mise/postCreate) → nil immediately.
//   - cached image already present locally → nil immediately.
//   - otherwise → EnqueueForCrew (which broadcasts the provision.* events the
//     toolbar popover renders) and wait until the job reaches completed/failed,
//     or until ctx is cancelled / timeout elapses.
//
// timeout <= 0 applies a 15-minute default (a large base image like
// universal:2 plus features can take many minutes on a cold daemon). Returns a
// descriptive error on build failure/timeout so the caller can surface
// "preparing the crew container failed: …" instead of a cryptic 127.
func (h *ProvisioningHandler) EnsureProvisioned(ctx context.Context, crewID, workspaceID string, timeout time.Duration) error {
	if h == nil || h.provisioner == nil {
		// Provisioning disabled (no Docker client) — nothing to ensure; the
		// run path will start from whatever image it can.
		return nil
	}

	var devcontainerCfg, miseCfg, cachedImage sql.NullString
	err := h.db.QueryRowContext(ctx,
		`SELECT devcontainer_config, mise_config, cached_image
		 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&devcontainerCfg, &miseCfg, &cachedImage)
	if err != nil {
		return fmt.Errorf("load crew for provisioning check: %w", err)
	}

	if !crewNeedsProvision(devcontainerCfg.String, miseCfg.String) {
		return nil
	}
	if cachedImage.Valid && cachedImage.String != "" && h.imagePresentLocally(ctx, cachedImage.String) {
		return nil
	}

	// EnqueueForCrew returns AlreadyRunning (nil error) when a build is already
	// in flight — we still want to wait for it below. A non-nil error means we
	// cannot prepare the container at all (rate limit, no devcontainer, etc.).
	if _, err := h.EnqueueForCrew(ctx, crewID, workspaceID); err != nil {
		return fmt.Errorf("trigger provisioning: %w", err)
	}

	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	pollInterval := h.provisionPollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("provisioning crew %s did not finish within %s", crewID, timeout)
		case <-ticker.C:
			h.mu.Lock()
			var status, jobErr string
			if job := h.jobs[crewID]; job != nil {
				status, jobErr = job.Status, job.Error
			}
			h.mu.Unlock()
			switch status {
			case "completed":
				return nil
			case "failed":
				if jobErr == "" {
					jobErr = "unknown error"
				}
				return fmt.Errorf("provisioning crew %s failed: %s", crewID, jobErr)
			}
		}
	}
}

// imagePresentLocally reports whether the given image tag exists on the local
// Docker daemon. A definitive not-found returns false (triggering a rebuild);
// any other error (transport / wedged daemon) returns true to avoid spurious
// rebuilds on every dispatch — mirroring chatbridge's "assume present" stance.
func (h *ProvisioningHandler) imagePresentLocally(ctx context.Context, ref string) bool {
	if h.docker == nil {
		return true
	}
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := h.docker.ImageInspect(ictx, ref); err != nil {
		if cerrdefs.IsNotFound(err) {
			return false
		}
		return true
	}
	return true
}
