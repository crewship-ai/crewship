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
// buildCrewRuntimeConfig is now also the DB-backed crewstart.Completer
// (CrewConfigCompleter below), i.e. the answer every caller that holds only a
// crew id gets for "what does this crew's container look like?". The chat path
// keeps its own resolver because it runs off the agent-resolve IPC response and
// has no DB handle; the two agree field for field, and what they produce is
// merged rather than chosen between (internal/crewstart.mergeConfig).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/encryption"
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

// BuildCrewRuntimeConfig resolves a crew's full PROVISIONED container config
// (cached image, mounts, caps, env, resource limits, declared sidecar services)
// by crew id — exported so the pipeline script-step runner (wired in
// cmd/crewship, which can import this package without the internal/pipeline →
// internal/api import cycle) can start a crew from the provisioned image rather
// than the bare base. A cold crew launched from base lacks the interpreters a
// script step needs.
//
// workspaceID scopes the lookup; pass "" to resolve by the (globally unique)
// crew id alone, which is what CrewConfigCompleter does — the container config
// it hands to crewstart carries a crew id and nothing else to scope by, and the
// caller has already decided this crew may be started.
func BuildCrewRuntimeConfig(ctx context.Context, db *sql.DB, crewID, workspaceID string) (provider.CrewConfig, error) {
	return buildCrewRuntimeConfig(ctx, db, crewID, workspaceID)
}

func buildCrewRuntimeConfig(ctx context.Context, db *sql.DB, crewID, workspaceID string) (provider.CrewConfig, error) {
	var (
		slug                       string
		networkMode, allowedDomain sql.NullString
		memoryMB, ttlHours         sql.NullInt64
		cpus                       sql.NullFloat64
		runtimeImage, cachedImage  sql.NullString
		cachedRequirements         sql.NullString
		devcontainerCfg            sql.NullString
		servicesJSON               sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT slug, network_mode, allowed_domains,
		       container_memory_mb, container_cpus, container_ttl_hours,
		       runtime_image, cached_image, cached_requirements,
		       devcontainer_config, services_json
		FROM crews
		WHERE id = ? AND (? = '' OR workspace_id = ?) AND deleted_at IS NULL`,
		crewID, workspaceID, workspaceID,
	).Scan(&slug, &networkMode, &allowedDomain,
		&memoryMB, &cpus, &ttlHours,
		&runtimeImage, &cachedImage, &cachedRequirements,
		&devcontainerCfg, &servicesJSON)
	if err != nil {
		return provider.CrewConfig{}, fmt.Errorf("load crew runtime config: %w", err)
	}

	cfg := provider.CrewConfig{
		ID:   crewID,
		Slug: slug,
		// #1643: this read the raw columns, so a row still holding the
		// "use the server default" sentinel — 0, as `PATCH /crews/{id}`
		// wrote it before #1641 resolved it — arrived here as 0 and hit the
		// docker provider's own `<= 0` fallback of 8192: twice the 4096 the
		// create path, the schema DEFAULT and the docs all promise.
		//
		// The backfill migration clears the rows that exist. This clears the
		// ones that arrive later: a restore re-inserts the source bundle's
		// rows verbatim and file migrations carry no restore-backfill hook,
		// so a bundle from before the fix puts the 0s straight back. Same
		// argument, same shape, and the same three lines down as the TTL
		// resolution #1662 added for exactly this reason.
		MemoryMB:    resolveCrewContainerMemoryMB(int(memoryMB.Int64)),
		CPUs:        resolveCrewContainerCPUs(cpus.Float64),
		NetworkMode: networkMode.String,
		// #1662: this read the raw column, so a NULL arrived as 0 — which the
		// reaper reads as "never stop". It is the field the two wake paths
		// that never reach RunAgent (script steps, prewarm) use to register a
		// TTL, so handing them a 0 registered never-stop for exactly the
		// crews nobody had ever configured.
		TTLHours:    resolveCrewContainerTTLHours(nullIntPtr(ttlHours)),
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
		// reqs.Init is deliberately NOT plumbed (#1636). The docker provider
		// sets HostConfig.Init unconditionally for every crew container
		// (#1630) because PID 1 is `exec sleep infinity`, which never calls
		// wait(), and the sidecar is always an orphan reparented onto it —
		// so an "init: false" would restore a monotonic zombie leak ending in
		// `fork: Resource temporarily unavailable`, and an "init: true" is
		// redundant. Carrying the value here made `crewship crew config
		// <crew> --init` look like a working switch: the row changed and the
		// runtime ignored it. The flag is now a warned no-op
		// (cmd/crewship/cmd_crew_config.go) and the requirement stops here.
		// AggregatedRequirements.Init is still read by isEmptyRequirements
		// (crew_provisioning_jobs.go), which is why the field itself stays.
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

	// The crew's declared sidecars (#1708). This used to be resolved only by
	// the chat resolver, which is why a crew with `services: [redis]` ran
	// database-less on every headless path — the dispatch path assembled a
	// config with an empty Services and the sidecars were never asked for.
	// Decoding here puts them on the config every caller passes to
	// crewstart.Starter.Start, which is what starts them.
	//
	// A malformed services_json is NOT fatal: the crew starts without its
	// sidecars and the caller logs, matching what the chat path has always
	// done. Hard-failing here would take a crew's whole runtime down over a
	// stale column that only some of its work needs.
	if strings.TrimSpace(servicesJSON.String) != "" {
		svcs, sErr := crewstart.DecodeServices(servicesJSON.String, crewServiceEnvLookup(ctx, db, crewID))
		if sErr != nil {
			return cfg, fmt.Errorf("decode services_json for crew %s: %w", crewID, sErr)
		}
		cfg.Services = svcs
	}

	return cfg, nil
}

// CrewConfigCompleter is the DB-backed crewstart.Completer: it answers "what
// does this crew's container actually look like?" for the callers that hold
// only a crew id — the web terminal, the container-start and agent-start
// routes, the orchestrator's crew warmers.
//
// Those callers passed provider.CrewConfig{ID, Slug} and nothing else, so the
// docker provider fell back to the global default runtime image and the crew
// came up as bare debian with none of its provisioned toolchain (#1717), and
// with none of its declared sidecars (#1708).
type CrewConfigCompleter struct {
	db *sql.DB
}

// NewCrewConfigCompleter returns a completer reading from db. A nil db yields a
// completer that adds nothing (dev / no-DB), never an error.
func NewCrewConfigCompleter(db *sql.DB) *CrewConfigCompleter {
	return &CrewConfigCompleter{db: db}
}

// CompleteCrewConfig implements crewstart.Completer.
//
// A crew id with no row is NOT an error: the scheduler starts a synthetic
// "scheduler-<workspace>" crew that has never existed in the crews table, and
// that path must keep working exactly as it does today.
func (c *CrewConfigCompleter) CompleteCrewConfig(ctx context.Context, cfg provider.CrewConfig) (provider.CrewConfig, error) {
	if c == nil || c.db == nil || cfg.ID == "" {
		return cfg, nil
	}
	resolved, err := buildCrewRuntimeConfig(ctx, c.db, cfg.ID, "")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cfg, nil
		}
		return cfg, err
	}
	return resolved, nil
}

// crewServiceEnvLookup resolves a sidecar's `env_refs` entries to plaintext
// credential values, scoped to the CREW.
//
// Crew-scoped is the correct scope and it is also the only one that is stable:
// a sidecar's environment feeds the docker provider's spec hash, so resolving
// refs against whichever agent happened to trigger the start would give the
// same crew a different postgres container per agent, and recreate it (dropping
// connections) every time the trigger changed. credential_crews is the link a
// crew-wide secret is expressed through, and the arm the agent-scoped delivery
// query reads for exactly these credentials.
//
// Auto-managed sidecar credentials do not come through here at all — they are
// generated at manifest-apply time and stored as literal env in services_json
// (internal/manifest/auto_managed.go), which is why a `services:` block works
// out of the box with no vault lookup.
//
// Errors are swallowed into "resolves to nothing": a ref that cannot be read is
// dropped from the sidecar's env, the same treatment a PENDING credential gets.
func crewServiceEnvLookup(ctx context.Context, db *sql.DB, crewID string) func(string) string {
	values := map[string]string{}
	if db == nil || crewID == "" {
		return func(string) string { return "" }
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.name, c.encrypted_value
		FROM credential_crews cc
		JOIN credentials c ON c.id = cc.credential_id
		JOIN crews cr ON cr.id = cc.crew_id
		WHERE cc.crew_id = ?
		  AND c.deleted_at IS NULL
		  AND c.status = 'ACTIVE'
		  AND c.workspace_id = cr.workspace_id`, crewID)
	if err != nil {
		return func(string) string { return "" }
	}
	defer rows.Close()
	for rows.Next() {
		var name, enc string
		if err := rows.Scan(&name, &enc); err != nil {
			continue
		}
		plain, err := encryption.Decrypt(enc)
		if err != nil || isPendingSentinel(plain) {
			continue
		}
		values[name] = plain
	}
	return func(envVar string) string { return values[envVar] }
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
