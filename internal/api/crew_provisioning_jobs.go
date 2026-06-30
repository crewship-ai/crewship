package api

// ProvisionJob lifecycle + status / trigger / rebuild handlers.
// Extracted from crew_provisioning.go for readability — no behavioral
// change. Public handler entry points (ProvisionStatus, ProvisionTrigger,
// ProvisionRebuild) are unchanged.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/journal"
)

// provisionLogTailCap bounds the in-memory ring buffer of progress messages
// that ProvisionStatus returns to clients connecting mid-build (e.g. after a
// page reload). 50 is plenty for the longest realistic build (1 pull + ~30
// features + mise + commit) without growing the JSON response unboundedly.
const provisionLogTailCap = 50

type ProvisionJob struct {
	CrewID      string
	Status      string // "pending", "running", "completed", "failed"
	StartedAt   time.Time
	CompletedAt *time.Time
	Error       string
	CachedImage string
	ConfigHash  string

	Step      int       // 1-based current milestone
	Total     int       // total milestones; 0 until the first progress event
	Message   string    // human-readable description of current step
	StepStart time.Time // wall clock at last step transition (for ETA hints)
	LogTail   []string  // ring buffer of past progress messages, cap = provisionLogTailCap

	// Steps is the full ordered checklist emitted up front via Provisioner's
	// WithPlan callback. Lets a UI render every row at once (done/active/
	// pending) instead of revealing them one at a time. Empty until the
	// goroutine seeds it; remains populated through completed/failed for
	// reload-replay via the GET endpoint.
	Steps []string
}

// orphanGCClient is the minimal slice of the Docker API used by the orphan-GC
// sweepers and CacheList. Exists as an interface so tests can swap in a fake

const (
	maxConcurrentProvisionsPerWorkspace = 8
	maxProvisionStartsPerMinute         = 20
)

// provisionRateLimiter tracks in-flight provisions per workspace and caps the
// number of starts per sliding 1-minute window. In-memory only; single-instance

type provisionRateLimiter struct {
	mu           sync.Mutex
	running      map[string]int         // workspace_id -> current concurrent count
	recentStarts map[string][]time.Time // workspace_id -> start timestamps in last minute
}

func newProvisionRateLimiter() *provisionRateLimiter {
	return &provisionRateLimiter{
		running:      make(map[string]int),
		recentStarts: make(map[string][]time.Time),
	}
}

// tryAcquire attempts to reserve a provisioning slot for the given workspace.
// Returns an error describing the limit hit when capacity is exhausted.
// Successful acquires must be paired with release().
func (r *provisionRateLimiter) tryAcquire(workspaceID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Prune stale timestamps (older than 1 minute).
	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)
	starts := r.recentStarts[workspaceID]
	fresh := starts[:0]
	for _, t := range starts {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	r.recentStarts[workspaceID] = fresh

	if r.running[workspaceID] >= maxConcurrentProvisionsPerWorkspace {
		return fmt.Errorf("%w: %d concurrent provisions already running (max %d)",
			ErrRateLimited, r.running[workspaceID], maxConcurrentProvisionsPerWorkspace)
	}
	if len(fresh) >= maxProvisionStartsPerMinute {
		return fmt.Errorf("%w: %d provisions started in last minute (max %d)",
			ErrRateLimited, len(fresh), maxProvisionStartsPerMinute)
	}

	r.running[workspaceID]++
	r.recentStarts[workspaceID] = append(fresh, now)
	return nil
}

// release decrements the concurrent-provision counter. Safe to call multiple
// times per workspace; will not go below zero.
func (r *provisionRateLimiter) release(workspaceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[workspaceID] > 0 {
		r.running[workspaceID]--
	}
}

// NewProvisioningHandler creates a ProvisioningHandler with the given database and logger.
// Fetchers may be nil; in that case the handler falls back to the embedded catalogs.
// If docker is nil, the provisioner is disabled and ProvisionTrigger returns 503.
// wsHub may be nil — provisioning still works, but live progress events won't reach

func (h *ProvisioningHandler) cleanupOldJobs() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	const ttl = 1 * time.Hour
	for crewID, job := range h.jobs {
		if job.Status != "completed" && job.Status != "failed" {
			continue
		}
		if job.CompletedAt == nil {
			continue
		}
		if now.Sub(*job.CompletedAt) > ttl {
			delete(h.jobs, crewID)
		}
	}
}

// startJobCleanupRoutine runs cleanupOldJobs every 10 minutes.
// Shuts down when ctx is cancelled.

func (h *ProvisioningHandler) startJobCleanupRoutine(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.cleanupOldJobs()
		}
	}
}

// CatalogList returns the devcontainer feature catalog, optionally filtered
// by a search query parameter. Data comes from the dynamic fetcher when
// available; otherwise from the embedded fallback.

func (h *ProvisioningHandler) ProvisionStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew ID is required")
		return
	}

	var devcontainerConfig, cachedImage, cfgHash, slug sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT devcontainer_config, cached_image, config_hash, slug
		 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&devcontainerConfig, &cachedImage, &cfgHash, &slug)

	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "crew not found")
		return
	}
	if err != nil {
		h.logger.Error("query crew provisioning status", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Determine status -- check in-memory jobs first, then fall back to DB.
	h.mu.RLock()
	job, hasJob := h.jobs[crewID]
	h.mu.RUnlock()

	resp := map[string]any{
		"devcontainer_config": nullStringPtr(devcontainerConfig),
		"cached_image":        nullStringPtr(cachedImage),
		"config_hash":         nullStringPtr(cfgHash),
	}

	// agents_pending_restart: agents in this crew running on a stale image.
	// One container per crew, so this is "is the live container's image
	// different from cached_image?" — if yes, every active agent in the
	// crew is pinned to the old image and needs the container recreated.
	if cachedImage.Valid && cachedImage.String != "" && slug.Valid && slug.String != "" {
		pending := h.agentsPendingRestartCount(r.Context(), crewID, slug.String, cachedImage.String)
		resp["agents_pending_restart"] = pending
	} else {
		resp["agents_pending_restart"] = 0
	}

	status := "idle"
	if hasJob {
		// Snapshot progress fields under the lock so the response is internally
		// consistent (step / total / message all reflect the same moment).
		h.mu.RLock()
		status = job.Status
		if job.Error != "" {
			resp["error"] = job.Error
		}
		if job.Total > 0 {
			resp["step"] = job.Step
			resp["total"] = job.Total
			resp["message"] = job.Message
		}
		if len(job.Steps) > 0 {
			steps := make([]string, len(job.Steps))
			copy(steps, job.Steps)
			resp["steps"] = steps
		}
		if len(job.LogTail) > 0 {
			tail := make([]string, len(job.LogTail))
			copy(tail, job.LogTail)
			resp["log_tail"] = tail
		}
		startedAt := job.StartedAt.Format(time.RFC3339)
		var completedAt string
		if job.CompletedAt != nil {
			completedAt = job.CompletedAt.Format(time.RFC3339)
		}
		h.mu.RUnlock()
		resp["started_at"] = startedAt
		if completedAt != "" {
			resp["completed_at"] = completedAt
		}
	} else if cachedImage.Valid && cachedImage.String != "" {
		status = "completed"
	}
	resp["status"] = status

	writeJSON(w, http.StatusOK, resp)
}

// EnqueueResult captures the outcome of EnqueueForCrew so callers can tell
// "started a fresh build" from "build was already running" without having to
// double-check the in-memory jobs map.
type EnqueueResult struct {
	Started        bool   // true when a new goroutine was spawned
	AlreadyRunning bool   // true when a job for this crew was pending/running
	Status         string // existing job status when AlreadyRunning is true
}

// ErrProvisionerUnavailable is returned by EnqueueForCrew when the handler
// has no Docker client wired up. ErrCrewNotFound and ErrCrewNoDevcontainer
// signal load-time issues; ErrRateLimited surfaces the per-workspace cap;
// ErrInvalidCrewID covers caller-side argument validation. Callers MUST
// use errors.Is for matching — the rate-limit case wraps the sentinel with
// fmt.Errorf("%w: ...", ...) so the message can carry the actual counts.
var (
	ErrProvisionerUnavailable = fmt.Errorf("provisioner not available (Docker client not configured)")
	ErrCrewNotFound           = fmt.Errorf("crew not found")
	ErrCrewNoDevcontainer     = fmt.Errorf("crew has no devcontainer_config to provision")
	ErrRateLimited            = fmt.Errorf("rate limited")
	ErrInvalidCrewID          = fmt.Errorf("invalid crew ID")
)

// EnqueueForCrew kicks off an asynchronous provisioning job for the given
// crew. Idempotent: when a job is already pending or running for the same
// crew, returns AlreadyRunning=true with that job's status instead of
// starting a duplicate. Used both by the HTTP handler and by chatbridge so
// "send first message" can auto-provision a crew whose devcontainer hasn't
// been built yet — without the bridge needing to round-trip through HTTP.
func (h *ProvisioningHandler) EnqueueForCrew(ctx context.Context, crewID, workspaceID string) (EnqueueResult, error) {
	if h.provisioner == nil {
		return EnqueueResult{}, ErrProvisionerUnavailable
	}
	if crewID == "" {
		return EnqueueResult{}, ErrInvalidCrewID
	}

	var devcontainerCfg, miseCfg, runtimeImage sql.NullString
	err := h.db.QueryRowContext(ctx,
		`SELECT devcontainer_config, mise_config, runtime_image
		 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&devcontainerCfg, &miseCfg, &runtimeImage)
	if err == sql.ErrNoRows {
		return EnqueueResult{}, ErrCrewNotFound
	}
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("query crew: %w", err)
	}
	if !devcontainerCfg.Valid || devcontainerCfg.String == "" {
		return EnqueueResult{}, ErrCrewNoDevcontainer
	}

	// First lock-and-check: if a job is already pending/running, fast-path
	// out without touching the rate limiter — no slot needed for a request
	// we're not actually starting.
	h.mu.Lock()
	if existing, ok := h.jobs[crewID]; ok && (existing.Status == "pending" || existing.Status == "running") {
		status := existing.Status
		h.mu.Unlock()
		return EnqueueResult{AlreadyRunning: true, Status: status}, nil
	}
	h.mu.Unlock()

	// Acquire the rate-limit slot BEFORE publishing the job. The previous
	// order (publish "pending" → tryAcquire → delete on failure) created a
	// visible-but-doomed job: a concurrent caller could see status="pending"
	// and report AlreadyRunning, only for this goroutine to delete the
	// entry a moment later when the limiter rejected it. Acquiring first
	// keeps h.jobs honest — every published row corresponds to a goroutine
	// the limiter has already greenlit.
	if err := h.rateLimiter.tryAcquire(workspaceID); err != nil {
		return EnqueueResult{}, err
	}

	// Second lock-and-check: a different caller may have raced past the
	// first check and won the rate-limit slot before us. Recheck under the
	// lock to avoid double-publishing the same crew. If a duplicate already
	// landed, release our slot and report AlreadyRunning so the limiter
	// counter stays consistent.
	h.mu.Lock()
	if existing, ok := h.jobs[crewID]; ok && (existing.Status == "pending" || existing.Status == "running") {
		status := existing.Status
		h.mu.Unlock()
		h.rateLimiter.release(workspaceID)
		return EnqueueResult{AlreadyRunning: true, Status: status}, nil
	}
	job := &ProvisionJob{
		CrewID:    crewID,
		Status:    "pending",
		StartedAt: time.Now(),
	}
	h.jobs[crewID] = job
	h.mu.Unlock()

	go h.runProvisioning(crewID, workspaceID, devcontainerCfg.String, miseCfg.String, runtimeImage.String, job)
	h.logger.Info("provisioning triggered", "crew_id", crewID)
	return EnqueueResult{Started: true}, nil
}

// ProvisionTrigger starts an asynchronous provisioning job for the given crew.
// Returns 202 immediately; the caller polls ProvisionStatus for progress.
// Returns 503 if the Docker client is not configured, 409 if a job is already
// in progress for the same crew.

func (h *ProvisioningHandler) ProvisionTrigger(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	res, err := h.EnqueueForCrew(r.Context(), crewID, workspaceID)
	if err != nil {
		// Match by typed sentinel — message strings drift; an HTTP contract
		// keyed off strings.Contains(err.Error(), "rate limited") would
		// silently degrade if the wrapping format ever changed.
		switch {
		case errors.Is(err, ErrProvisionerUnavailable):
			writeProblem(w, r, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, ErrCrewNotFound):
			writeProblem(w, r, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrCrewNoDevcontainer):
			writeProblem(w, r, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrInvalidCrewID):
			writeProblem(w, r, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrRateLimited):
			writeProblem(w, r, http.StatusTooManyRequests, err.Error())
		default:
			h.logger.Error("provision trigger", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		}
		return
	}
	if res.AlreadyRunning {
		// 409 carries the existing job's status as an RFC 7807 extension
		// member so callers can decide whether to wait or surface the state
		// without re-fetching status separately.
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"type":       "about:blank",
			"title":      http.StatusText(http.StatusConflict),
			"status":     http.StatusConflict,
			"detail":     "provisioning already in progress",
			"instance":   r.URL.Path,
			"job_status": res.Status,
		})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "started",
		"message": "Provisioning started. Monitor with 'crewship crew provision status <slug>'.",
	})
}

// emitProvisionEvent routes one structured ProvisionEvent into BOTH the journal
// (persisted = auditable across thousands of runs) and the WS hub (live =
// visible in the Activity Bar), tied to a crew/workspace. It is the single
// routing point shared by the explicit provisioning-job runner (image BUILD,
// via runProvisioning) and the agent-run/ensure-container path (runtime
// container prep, via RuntimeProvisionSink) so every container preparation —
// whichever path triggered it — lands in the same queryable audit vocabulary.
// Cheap: a single indexed journal insert plus a WS broadcast.
func (h *ProvisioningHandler) emitProvisionEvent(ctx context.Context, crewID, workspaceID string, ev devcontainer.ProvisionEvent) {
	payload := map[string]any{
		"crew_id": crewID,
		"phase":   ev.Phase,
		"step":    ev.Step,
	}
	if ev.Feature != "" {
		payload["feature"] = ev.Feature
	}
	if ev.Status != "" {
		payload["status"] = ev.Status
	}
	if ev.Detail != "" {
		payload["detail"] = ev.Detail
	}
	if ev.Error != "" {
		payload["error"] = ev.Error
	}
	if ev.Tag != "" {
		payload["tag"] = ev.Tag
	}
	if ev.DurationMs != 0 {
		payload["duration_ms"] = ev.DurationMs
	}

	// Live: push to any open Activity Bar.
	h.wsHub.BroadcastWorkspace(workspaceID, "provision.event", payload)

	// Persisted: one auditable journal row per step. Failures surface at
	// warn so the Timeline highlights them without scrolling.
	severity := journal.SeverityInfo
	if ev.Status == devcontainer.ProvStatusFailed || ev.Step == devcontainer.ProvStepFailed {
		severity = journal.SeverityWarn
	}
	summary := fmt.Sprintf("provision %s", ev.Step)
	if ev.Feature != "" {
		summary += " " + ev.Feature
	}
	if ev.Status != "" {
		summary += " (" + ev.Status + ")"
	}
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntryProvisioningStep,
		Severity:    severity,
		ActorType:   journal.ActorOrchestrator,
		Summary:     summary,
		Payload:     payload,
		Refs:        map[string]any{"crew_id": crewID},
	})
}

// RuntimeProvisionSink returns a ProvisionSink for the agent-run /
// ensure-container path: the runtime container-preparation events emitted by the
// container provider's EnsureCrewRuntime (start → container_create → ready, plus
// failed) are journaled + live-streamed with the SAME schema and routing as the
// explicit provisioning-job runner. Wiring this onto provider.CrewConfig closes
// the gap where agent-triggered container creation prepared a container with no
// audit trail. Returns nil on a nil handler (provisioning disabled) so the
// caller can assign it unconditionally — a nil sink is a no-op in the provider.
//
// Uses context.Background() for journal emits (not a request/run ctx) so a
// completed/cancelled run can't drop the final ready/failed audit row.
func (h *ProvisioningHandler) RuntimeProvisionSink(crewID, workspaceID string) func(devcontainer.ProvisionEvent) {
	if h == nil {
		return nil
	}
	return func(ev devcontainer.ProvisionEvent) {
		h.emitProvisionEvent(context.Background(), crewID, workspaceID, ev)
	}
}

// runProvisioning executes the full provisioning pipeline asynchronously.
// It updates the in-memory job state and persists the result to the DB.

func (h *ProvisioningHandler) runProvisioning(crewID, workspaceID, cfgJSON, miseJSON, runtimeImg string, job *ProvisionJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// Release the rate-limit slot regardless of success/failure.
	defer h.rateLimiter.release(workspaceID)

	// Panic recovery — mark job as failed and log, don't crash the server.
	// Registered AFTER rate-limit release so LIFO order runs this first:
	// job state is updated, then the slot is freed.
	defer func() {
		if r := recover(); r != nil {
			panicErr := fmt.Sprintf("internal error: %v", r)
			h.mu.Lock()
			if j, ok := h.jobs[crewID]; ok {
				j.Status = "failed"
				j.Error = panicErr
				now := time.Now()
				j.CompletedAt = &now
			}
			h.mu.Unlock()
			h.logger.Error("provisioning panicked",
				"crew_id", crewID,
				"workspace_id", workspaceID,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			h.wsHub.BroadcastWorkspace(workspaceID, "provision.failed", map[string]any{
				"crew_id": crewID,
				"error":   panicErr,
			})
		}
	}()

	h.mu.Lock()
	job.Status = "running"
	h.mu.Unlock()

	// Emit provisioning.queued so the Timeline gets a marker before
	// the (potentially multi-minute) image pull begins. Without this,
	// the Crow's Nest viewer sees a long silence between the trigger
	// and the first exec.command in the new container.
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntryProvisioningQueued,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorOrchestrator,
		Summary:     fmt.Sprintf("provisioning queued for crew %s", crewID),
		Payload: map[string]any{
			"crew_id":      crewID,
			"workspace_id": workspaceID,
		},
		Refs: map[string]any{"crew_id": crewID},
	})

	cfg, err := devcontainer.ParseBytes([]byte(cfgJSON))
	if err != nil {
		h.markJobFailed(job, workspaceID, fmt.Errorf("parse devcontainer_config: %w", err))
		return
	}

	// Resolve base image: runtime_image takes precedence (user override) over cfg.Image.
	baseImage := cfg.Image
	if runtimeImg != "" {
		baseImage = runtimeImg
	}
	if baseImage == "" {
		h.markJobFailed(job, workspaceID, fmt.Errorf("no base image in devcontainer config or runtime_image"))
		return
	}
	// Ensure the config hash reflects the resolved base image.
	cfg.Image = baseImage

	h.logger.Info("starting provisioning",
		"crew_id", crewID,
		"base_image", baseImage,
		"features", len(cfg.Features),
	)

	plan := func(steps []string) {
		h.mu.Lock()
		// Defensive copy: caller already cloned, but better safe than
		// have a slice header race with the GET handler reading concurrently.
		dup := make([]string, len(steps))
		copy(dup, steps)
		job.Steps = dup
		h.mu.Unlock()

		h.logger.Info("provision plan emitted", "crew_id", crewID, "steps", len(steps), "ws_hub", h.wsHub != nil)
		h.wsHub.BroadcastWorkspace(workspaceID, "provision.started", map[string]any{
			"crew_id": crewID,
			"steps":   steps,
		})
	}

	progress := func(step, total int, message string) {
		now := time.Now()
		h.mu.Lock()
		job.Step = step
		job.Total = total
		job.Message = message
		job.StepStart = now
		job.LogTail = append(job.LogTail, message)
		if len(job.LogTail) > provisionLogTailCap {
			// Drop oldest entries when the ring buffer is full. Allocates a
			// fresh slice to release the head storage; otherwise long builds
			// would hold on to old strings via the underlying array.
			tail := make([]string, provisionLogTailCap)
			copy(tail, job.LogTail[len(job.LogTail)-provisionLogTailCap:])
			job.LogTail = tail
		}
		h.mu.Unlock()

		h.logger.Debug("provision progress", "crew_id", crewID, "step", step, "total", total, "message", message)
		h.wsHub.BroadcastWorkspace(workspaceID, "provision.progress", map[string]any{
			"crew_id": crewID,
			"step":    step,
			"total":   total,
			"message": message,
		})
	}

	// provisionEventSink routes every structured ProvisionEvent from the
	// container-preparation pipeline into BOTH the journal (persisted =
	// auditable across thousands of runs) and the WS hub (live = visible in the
	// Activity Bar), tied to the triggering crew/workspace. This is the channel
	// that guarantees no provisioning step fails silently: each step — resolve,
	// build, per-feature install, container create, env apply, ready, cache_hit,
	// and any failure — lands here with structured fields. Runs synchronously on
	// the provisioning goroutine (same as the progress callback above), so it
	// must stay cheap; journal.Emit is a single indexed insert.
	provisionEventSink := func(ev devcontainer.ProvisionEvent) {
		h.emitProvisionEvent(ctx, crewID, workspaceID, ev)
	}

	// Emit provisioning.building once the plan is set and the actual
	// image build is about to start. Distinct from queued so a viewer
	// can tell pre-flight-config-parse from honest-build progress.
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntryProvisioningBuilding,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		Summary:     fmt.Sprintf("provisioning crew %s (base=%s)", crewID, baseImage),
		Payload: map[string]any{
			"crew_id":    crewID,
			"base_image": baseImage,
			"features":   len(cfg.Features),
		},
		Refs: map[string]any{"crew_id": crewID},
	})

	result, err := h.provisioner.Provision(ctx, baseImage, cfg, miseJSON,
		devcontainer.WithPlan(plan),
		devcontainer.WithProgress(progress),
		devcontainer.WithProvisionSink(provisionEventSink),
	)
	if err != nil {
		h.markJobFailed(job, workspaceID, fmt.Errorf("provision: %w", err))
		return
	}

	// Serialize aggregated feature requirements (privileged, capAdd, mounts,
	// containerEnv) so the runtime can apply them when starting the crew
	// container. Without this, features like DinD (privileged:true +
	// docker.sock mount) would silently not work at runtime.
	var reqJSON sql.NullString
	if reqBytes, marshalErr := json.Marshal(result.Requirements); marshalErr != nil {
		h.logger.Warn("marshal cached_requirements failed, storing NULL",
			"crew_id", crewID, "error", marshalErr)
	} else if !isEmptyRequirements(result.Requirements) {
		reqJSON = sql.NullString{String: string(reqBytes), Valid: true}
	}

	// Persist the cached image reference on the crew row. Use a fresh context
	// (not the 30-min provisioning ctx, which may be near its deadline).
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer updateCancel()
	_, err = h.db.ExecContext(updateCtx,
		`UPDATE crews SET cached_image = ?, config_hash = ?, cached_requirements = ?, updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		result.CachedImage, result.ConfigHash, reqJSON, crewID, workspaceID,
	)
	if err != nil {
		h.markJobFailed(job, workspaceID, fmt.Errorf("update db: %w", err))
		return
	}

	now := time.Now()
	h.mu.Lock()
	job.Status = "completed"
	job.CompletedAt = &now
	job.CachedImage = result.CachedImage
	job.ConfigHash = result.ConfigHash
	h.mu.Unlock()

	h.logger.Info("provisioning completed",
		"crew_id", crewID,
		"cached_image", result.CachedImage,
		"config_hash", result.ConfigHash,
	)
	h.wsHub.BroadcastWorkspace(workspaceID, "provision.completed", map[string]any{
		"crew_id":      crewID,
		"cached_image": result.CachedImage,
		"config_hash":  result.ConfigHash,
	})
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        journal.EntryProvisioningComplete,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorOrchestrator,
		Summary:     fmt.Sprintf("provisioning complete for crew %s", crewID),
		Payload: map[string]any{
			"crew_id":      crewID,
			"cached_image": result.CachedImage,
			"config_hash":  result.ConfigHash,
		},
		Refs: map[string]any{"crew_id": crewID},
	})
}

// markJobFailed records a failure on the job, logs it, and broadcasts a
// `provision.failed` event so any open browser updates without polling.
// workspaceID is required for the broadcast — callers always know it because
// runProvisioning is the only call site.

func (h *ProvisioningHandler) markJobFailed(job *ProvisionJob, workspaceID string, err error) {
	h.logger.Error("provisioning failed", "crew_id", job.CrewID, "error", err)
	now := time.Now()
	h.mu.Lock()
	job.Status = "failed"
	job.CompletedAt = &now
	job.Error = err.Error()
	h.mu.Unlock()

	h.wsHub.BroadcastWorkspace(workspaceID, "provision.failed", map[string]any{
		"crew_id": job.CrewID,
		"error":   err.Error(),
	})
	// Mirror the failure to the journal at warn so the Timeline
	// surfaces it without the viewer having to scroll. context.Background
	// because the caller's ctx may already be cancelled by the time
	// markJobFailed runs (e.g. provisioning timeout).
	_, _ = h.journal.Emit(context.Background(), journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      job.CrewID,
		Type:        journal.EntryProvisioningFailed,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorOrchestrator,
		Summary:     fmt.Sprintf("provisioning failed for crew %s: %v", job.CrewID, err),
		Payload: map[string]any{
			"crew_id": job.CrewID,
			"error":   err.Error(),
		},
		Refs: map[string]any{"crew_id": job.CrewID},
	})
}

// ProvisionRebuild invalidates the cached image and triggers re-provisioning.
// Implemented as: clear DB cache columns, then delegate to ProvisionTrigger.

func (h *ProvisioningHandler) ProvisionRebuild(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew ID is required")
		return
	}
	// Clear cache so Provisioner won't short-circuit on the existing tag.
	_, err := h.db.ExecContext(r.Context(),
		`UPDATE crews SET cached_image = NULL, config_hash = NULL, cached_requirements = NULL, updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		crewID, workspaceID,
	)
	if err != nil {
		h.logger.Error("clear cached image for rebuild", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	h.ProvisionTrigger(w, r)
}

// cacheImagePrefix is the Docker repository name used for all provisioned
// devcontainer caches. CacheList and CacheDelete refuse to touch anything
// outside this namespace.

func isEmptyRequirements(r devcontainer.AggregatedRequirements) bool {
	return !r.Privileged && !r.Init &&
		len(r.ContainerEnv) == 0 &&
		len(r.Mounts) == 0 &&
		len(r.CapAdd) == 0 &&
		len(r.SecurityOpt) == 0 &&
		len(r.PostStartCommands) == 0 &&
		r.LoginPath == ""
}

// crewContainerName mirrors docker.Provider.CrewContainerName for the cases
// where we don't hold a provider reference. Hardcoded to the default Docker
// prefix because the provider is the only consumer that customizes it, and
// the restart endpoint always targets that exact runtime. If we ever support
// multiple container providers per workspace, this needs to round-trip
// through the orchestrator.
