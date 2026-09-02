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
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
	"github.com/crewship-ai/crewship/internal/ws"
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

	// Pending holds at most one deferred chat message per chat, attached via
	// AttachPendingMessage while chatbridge.Bridge.HandleChatMessage's
	// auto-provision branch is waiting on this build. Keyed by ChatID so a
	// second deferred send on the SAME chat — a manual resend while the build
	// is still running, or a retried frame — coalesces onto the latest
	// content instead of queuing a duplicate: only the most recent message
	// for a given chat is worth replaying once the environment exists.
	//
	// Drained (read, then set to nil) in the SAME h.mu critical section that
	// flips Status to a terminal value ("completed"/"failed") — see
	// runProvisioning's success path, markJobFailed, and the panic-recovery
	// defer. That atomicity is the whole at-most-once guarantee: a message
	// attached before the flip is captured by the drain and resumed by the
	// completion goroutine; a message attached after the flip finds Status
	// already terminal and AttachPendingMessage resumes it immediately
	// instead of adding to a map nobody will ever drain again. There is no
	// interleaving where both the drain and a late attach see the same entry.
	Pending map[string]chatbridge.PendingChatMessage
}

// orphanGCClient is the minimal slice of the Docker API used by the orphan-GC
// sweepers and CacheList. Exists as an interface so tests can swap in a fake

// Fallbacks for ratelimitcfg.KeyProvMaxConcurrentWS / KeyProvMaxStartsPerMin,
// used when no store is installed. Keep them in step with the registry — a
// mismatch means a store-less path enforces a limit nobody configured.
const (
	maxConcurrentProvisionsPerWorkspace = 32
	maxProvisionStartsPerMinute         = 120
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

	// Limits are runtime-tunable via the admin Rate Limiters console; the
	// constants remain the shipped defaults and a defensive floor.
	maxConcurrent := ratelimitcfg.Int(ratelimitcfg.KeyProvMaxConcurrentWS)
	if maxConcurrent < 1 {
		maxConcurrent = maxConcurrentProvisionsPerWorkspace
	}
	maxStarts := ratelimitcfg.Int(ratelimitcfg.KeyProvMaxStartsPerMin)
	if maxStarts < 1 {
		maxStarts = maxProvisionStartsPerMinute
	}

	if r.running[workspaceID] >= maxConcurrent {
		return fmt.Errorf("%w: %d concurrent provisions already running (max %d)",
			ErrRateLimited, r.running[workspaceID], maxConcurrent)
	}
	if len(fresh) >= maxStarts {
		return fmt.Errorf("%w: %d provisions started in last minute (max %d)",
			ErrRateLimited, len(fresh), maxStarts)
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

	var devcontainerConfig, cachedImage, cfgHash, slug, resolvedFeatures sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT devcontainer_config, cached_image, config_hash, slug, resolved_features
		 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&devcontainerConfig, &cachedImage, &cfgHash, &slug, &resolvedFeatures)

	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "crew not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "query crew provisioning status", err)
		return
	}

	// Determine status -- check in-memory jobs first, then fall back to DB.
	h.mu.RLock()
	job, hasJob := h.jobs[crewID]
	h.mu.RUnlock()

	resp := map[string]any{
		// The EFFECTIVE config, not the raw column: a crew with no config of
		// its own is about to be (or already was) provisioned from
		// database.DefaultCrewDevcontainerConfig, not refused, so reporting
		// the raw NULL here would tell an operator (and the CLI doctor check
		// that reads this field) "Claude Code CLI may not be available" for a
		// crew that provisions and runs it just fine.
		"devcontainer_config": database.EffectiveCrewDevcontainerConfig(devcontainerConfig.String, devcontainerConfig.Valid),
		// Surfaced separately so a crew running on a default it never chose
		// is visible rather than silently indistinguishable from one an
		// operator explicitly configured (database.CrewDevcontainerIsDefaulted).
		"devcontainer_config_defaulted": database.CrewDevcontainerIsDefaulted(devcontainerConfig.String, devcontainerConfig.Valid),
		"cached_image":                  nullStringPtr(cachedImage),
		"config_hash":                   nullStringPtr(cfgHash),
	}

	// What the image is actually made of. Null (rather than []) when the crew
	// was provisioned before provenance was recorded — "we do not know" and
	// "it uses no features" are different answers and the UI shows them
	// differently (#1779).
	if resolvedFeatures.Valid {
		var recs []devcontainer.FeatureRecord
		if err := json.Unmarshal([]byte(resolvedFeatures.String), &recs); err == nil {
			resp["resolved_features"] = recs
		}
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
	} else if fail, ok := h.latestBuildFailure(r.Context(), workspaceID, crewID); ok {
		// No live job, but the crew's most recent BUILD outcome was a
		// failure — surface it durably (#829) from the journal so the
		// BuildKit stderr tail explaining WHY is visible after the
		// in-memory job's TTL / a server restart, not just live.
		status = "failed"
		if fail.errMsg != "" {
			resp["error"] = fail.errMsg
		}
		if len(fail.logTail) > 0 {
			resp["log_tail"] = fail.logTail
		}
		if fail.at != "" {
			resp["completed_at"] = fail.at
		}
	} else if cachedImage.Valid && cachedImage.String != "" {
		status = "completed"
	}
	resp["status"] = status

	writeJSON(w, http.StatusOK, resp)
}

// durableBuildFailure is the post-hoc view of a feature-build failure,
// reconstructed from the journal when no in-memory job survives.
type durableBuildFailure struct {
	errMsg  string
	logTail []string
	at      string
}

// latestBuildFailure reports whether the crew's most recent provisioning
// outcome was a failure, returning the persisted error + (when available) the
// scrubbed BuildKit tail. It reads the newest terminal among build_failed /
// failed / complete (List returns newest-first) so a failure later fixed by a
// successful rebuild does not linger as "failed".
//
// Two rows can describe one failed build: the enriched provisioning.build_failed
// (error + tail) emitted mid-build, and the coarse provisioning.failed emitted
// by markJobFailed just after. We surface the failure off whichever is newest,
// then recover the tail from the most recent build_failed. This closes two
// fail-open gaps: a build that failed with an EMPTY tail (no build_failed row)
// still surfaces via provisioning.failed, and a plain provisioning.failed is no
// longer invisible to status.
func (h *ProvisioningHandler) latestBuildFailure(ctx context.Context, workspaceID, crewID string) (durableBuildFailure, bool) {
	terminal, _, err := journal.List(ctx, h.db, journal.Query{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Types: []journal.EntryType{
			journal.EntryProvisioningBuildFailed,
			journal.EntryProvisioningFailed,
			journal.EntryProvisioningComplete,
		},
		Limit: 1,
	})
	if err != nil || len(terminal) == 0 {
		return durableBuildFailure{}, false
	}
	e := terminal[0]
	if e.Type == journal.EntryProvisioningComplete {
		return durableBuildFailure{}, false // most recent outcome was a success
	}

	out := durableBuildFailure{at: e.TS.Format(time.RFC3339)}
	out.errMsg = payloadString(e.Payload, "error")
	if s := payloadString(e.Payload, "detail"); s != "" { // build_failed carries the tail directly
		out.logTail = strings.Split(s, "\n")
	}

	// If the terminal row was the coarse `failed`, recover the tail from the
	// most recent build_failed that post-dates the last success.
	if len(out.logTail) == 0 {
		bf, _, ferr := journal.List(ctx, h.db, journal.Query{
			WorkspaceID: workspaceID,
			CrewID:      crewID,
			Types:       []journal.EntryType{journal.EntryProvisioningBuildFailed, journal.EntryProvisioningComplete},
			Limit:       1,
		})
		if ferr == nil && len(bf) > 0 && bf[0].Type == journal.EntryProvisioningBuildFailed {
			if s := payloadString(bf[0].Payload, "detail"); s != "" {
				out.logTail = strings.Split(s, "\n")
			}
			if out.errMsg == "" {
				out.errMsg = payloadString(bf[0].Payload, "error")
			}
		}
	}
	return out, true
}

// payloadString reads a string value from a journal payload, tolerating a nil
// map / missing key / non-string value.
func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	s, _ := payload[key].(string)
	return s
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
// has no Docker client wired up. ErrCrewNotFound signals a load-time issue;
// ErrRateLimited surfaces the per-workspace cap; ErrInvalidCrewID covers
// caller-side argument validation. Callers MUST use errors.Is for matching —
// the rate-limit case wraps the sentinel with fmt.Errorf("%w: ...", ...) so
// the message can carry the actual counts.
//
// ErrCrewNoDevcontainer is kept but is now unreachable from EnqueueForCrew:
// database.EffectiveCrewDevcontainerConfig means every crew resolves to a
// usable config (its own, or the default), so there is no longer a "no
// devcontainer" case to refuse. It is not deleted because ProvisionTrigger's
// error-to-HTTP-status switch still names it (dead but harmless — matching an
// error that provably cannot occur is not a bug), and deleting a public
// sentinel is a breaking change for any external caller matching on it. If a
// future caller finds a real path that can still hit "there is truly nothing
// to provision", it has a name to return.
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
	// A crew with no config of its own provisions from
	// database.DefaultCrewDevcontainerConfig rather than being refused — this
	// used to be `if !devcontainerCfg.Valid || devcontainerCfg.String == ""
	// { return ErrCrewNoDevcontainer }`, which is exactly the bug: it left
	// crew_templates.go / onboarding.go / recipes.go / internal_status.go
	// crews (all four omit devcontainer_config on INSERT) permanently
	// unprovisionable, so they ran on bare debian:bookworm-slim and the agent
	// died with exit 127. effectiveCfg is never empty, so ErrCrewNoDevcontainer
	// can no longer be returned from here.
	effectiveCfg := database.EffectiveCrewDevcontainerConfig(devcontainerCfg.String, devcontainerCfg.Valid)

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

	finish := beginBackgroundWork()
	go func() {
		defer finish()
		h.runProvisioning(crewID, workspaceID, effectiveCfg, miseCfg.String, runtimeImage.String, job)
	}()
	h.logger.Info("provisioning triggered", "crew_id", crewID)
	return EnqueueResult{Started: true}, nil
}

// resumeMessageTimeout bounds a resumed message's own run — same ballpark as
// a live chat send would get from its adapter/orchestrator layer. This is a
// ceiling on the RESUME call itself, not on the provisioning job (which has
// its own 30-minute budget in runProvisioning); by the time resumeMessage
// runs, the build already finished.
const resumeMessageTimeout = 10 * time.Minute

// AttachPendingMessage attaches a chat send that chatbridge.Bridge deferred
// (HandleChatMessage's auto-provision branch) to crewID's tracked
// provisioning job, so the job resumes or fails the message exactly once when
// it reaches a terminal state. See ProvisionJob.Pending for the at-most-once
// mechanics this relies on: Pending is keyed by ChatID (a second attach for
// the same chat coalesces rather than queuing a duplicate) and is drained
// atomically with the job's Status transition, so a late attach — the job
// already went terminal by the time this call takes the lock — resumes or
// fails the message immediately instead of writing into a map nobody will
// ever drain again.
//
// Returns false only when no job is tracked for crewID at all. That should
// not happen immediately after EnqueueForCrew (which is always the caller's
// preceding step), but is not treated as fatal: the crew_provisioning event
// HandleChatMessage already streamed told the user what is happening.
func (h *ProvisioningHandler) AttachPendingMessage(crewID string, msg chatbridge.PendingChatMessage) bool {
	h.mu.Lock()
	job, ok := h.jobs[crewID]
	if !ok {
		h.mu.Unlock()
		return false
	}
	switch job.Status {
	case "pending", "running":
		if job.Pending == nil {
			job.Pending = make(map[string]chatbridge.PendingChatMessage)
		}
		// Coalesce: the latest send for this chat is the only one worth
		// replaying, and the map shape makes "replace" and "insert" the same
		// operation.
		job.Pending[msg.ChatID] = msg
		h.mu.Unlock()
		return true
	case "completed":
		h.mu.Unlock()
		h.spawnResumeMessage(msg, nil)
		return true
	case "failed":
		buildErr := fmt.Errorf("build failed: %s", job.Error)
		h.mu.Unlock()
		h.spawnResumeMessage(msg, buildErr)
		return true
	default:
		// Unknown/unreachable status — fail closed rather than silently drop.
		h.mu.Unlock()
		return false
	}
}

// spawnResumeMessage runs resumeMessage on its own goroutine, registered with
// beginBackgroundWork so a test's teardown can drain it rather than race it
// (#1596) — a detached resumeMessage call outliving the request/test that
// triggered it is exactly the shape that guard exists to catch, since it goes
// on to touch the DB, the WS hub and the chat resolver well after the
// triggering handler has returned.
func (h *ProvisioningHandler) spawnResumeMessage(msg chatbridge.PendingChatMessage, buildErr error) {
	finish := beginBackgroundWork()
	go func() {
		defer finish()
		h.resumeMessage(msg, buildErr)
	}()
}

// resumePending fires the success path of resumeMessage for every message a
// terminal-transition drained off a job. Each runs on its own goroutine: the
// messages target independent chats (a crew can have several agents/chats)
// and none should block another or the caller (runProvisioning /
// markJobFailed), which still has its own DB/journal/broadcast work to do.
func (h *ProvisioningHandler) resumePending(pending map[string]chatbridge.PendingChatMessage) {
	for _, msg := range pending {
		h.spawnResumeMessage(msg, nil)
	}
}

// failPending fires the failure path of resumeMessage for every message a
// terminal-transition drained off a job whose build did NOT succeed.
func (h *ProvisioningHandler) failPending(pending map[string]chatbridge.PendingChatMessage, buildErr error) {
	for _, msg := range pending {
		h.spawnResumeMessage(msg, buildErr)
	}
}

// resumeMessage runs (buildErr == nil) or fails (buildErr != nil) exactly one
// deferred chat message, once. It is always called either from inside the
// h.mu critical section that just drained the message off ProvisionJob.Pending
// (runProvisioning's success path, markJobFailed, the panic-recovery defer),
// or from AttachPendingMessage's late-attach branches when the job was
// already terminal — either way, by construction, exactly one call is made
// per drained/late-attached message (see ProvisionJob.Pending's doc comment).
//
// Both paths stream on msg.ChatID's session channel via BeginSessionRun — the
// SAME channel a live send uses and the client is already reliably
// subscribed to for ordinary chat traffic. There is no dependency on the
// workspace realtime socket, on workspaceId having resolved client-side, or
// on the tab having stayed open since the message was sent.
func (h *ProvisioningHandler) resumeMessage(msg chatbridge.PendingChatMessage, buildErr error) {
	if h.wsHub == nil {
		// No live channel to publish on (headless boot / most unit tests).
		// Nothing to resume into — logged so a real deployment missing this
		// wiring is visible rather than silently dropping every deferred send.
		h.logger.Warn("cannot resume deferred chat message: no WS hub wired",
			"chat_id", msg.ChatID, "user_id", msg.UserID)
		return
	}
	run := h.wsHub.BeginSessionRun(msg.ChatID)
	defer run.End()

	if buildErr != nil {
		// The build itself failed: say so plainly and point at the fix,
		// rather than leaving the user's original message answered with
		// silence (the bug this whole mechanism exists to close) or, worse,
		// a false "done" with nothing having run.
		run.Emit(ws.ChatEvent{
			Type: "error",
			Content: fmt.Sprintf(
				"Your message could not run: the environment build failed (%s). Fix the devcontainer configuration and send your message again.",
				buildErr.Error(),
			),
			Metadata: map[string]any{"reason": "provisioning_failed"},
		})
		run.Emit(ws.ChatEvent{Type: "done", Content: ""})
		return
	}

	if h.chatResumer == nil {
		// Should not happen in production boot (cmd_start.go wires this
		// alongside SetProvisioningEnqueuer) but surface rather than silently
		// swallow the user's message if it ever does.
		h.logger.Error("cannot resume deferred chat message: no chat resumer wired", "chat_id", msg.ChatID)
		run.Emit(ws.ChatEvent{Type: "error", Content: "internal error: could not resume your message — please send it again"})
		run.Emit(ws.ChatEvent{Type: "done", Content: ""})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), resumeMessageTimeout)
	defer cancel()

	// Replays the ORIGINAL send through the exact path a live one takes:
	// same persistence, same cross-chat run exclusivity (tryMarkRunStart), same
	// error classification. devcontainerNeedsProvision now resolves false
	// (cached_image is set), so this runs the agent instead of deferring
	// again — except in the pathological case where the image was pruned
	// again in the seconds since this job finished, which re-enters the same
	// defer-and-attach path and is handled identically to the first time.
	err := h.chatResumer.HandleChatMessage(ctx, msg.UserID, msg.ChatID, msg.Content, run.Emit, msg.Opts)
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, chatbridge.ErrAgentBusyElsewhere):
		// Since #2269 the run holding the agent's slot may be an assignment
		// on an issue or a turn in another chat — not the user's own resend
		// here — so silently dropping the deferred message is no longer
		// safe: nothing else will deliver it. Tell the sender, the same way
		// the build-failure branch above does, so they can resend once the
		// agent is free. The plain ErrAgentBusy case below (the SAME chat
		// already has a live run — most plausibly the user's own manual
		// resend racing this one) keeps its zero-trace contract: that run
		// will settle the UI, and a notice here would be a lie.
		run.Emit(ws.ChatEvent{
			Type:     "error",
			Content:  "Your message was not delivered: the agent is busy with another run. Send it again once the agent is free.",
			Metadata: map[string]any{"reason": "agent_busy"},
		})
		run.Emit(ws.ChatEvent{Type: "done", Content: ""})
		// Another run (most plausibly the user's own manual resend, racing
		// this one) already owns the chat's run slot. That run is the one
		// that will complete and settle the UI; nothing further to do here,
		// and nothing to stream — ws.ErrAgentBusy's contract is that the
		// handler emitted NOTHING, and staying silent is what keeps this
		// from being a second, redundant execution of the same message.
	case errors.Is(err, ws.ErrAgentBusy):
		// The SAME chat already has a live run — most plausibly the user's own
		// manual resend racing this one. That run settles the UI; a notice here
		// would be a lie and a second delivery a duplicate. Stay silent.
		h.logger.Info("deferred message not resumed: a run was already in progress for this chat",
			"chat_id", msg.ChatID)
	case errors.Is(err, ws.ErrCrewProvisioning):
		// The crew needed re-provisioning again (e.g. its cached image was
		// pruned in the moments since this job completed). HandleChatMessage
		// already streamed its own crew_provisioning card and re-attached the
		// message to the new job — this call's job is done.
		h.logger.Info("deferred message deferred again: crew needs re-provisioning", "chat_id", msg.ChatID)
	default:
		// Everything else: HandleChatMessage already streams a classified
		// error event for every other failure mode before returning
		// non-nil, so there's nothing further to emit here — just log for
		// operator visibility, matching ws/client.go's handleSendMessage.
		h.logger.Warn("resuming deferred chat message returned an error", "chat_id", msg.ChatID, "error", err)
	}
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
	if ev.Reason != "" {
		payload["reason"] = ev.Reason
	}
	if ev.Error != "" {
		payload["error"] = ev.Error
	}
	if ev.Tag != "" {
		payload["tag"] = ev.Tag
	}
	// Supply-chain provenance (#1825). The digest is the only field here that
	// answers "which image actually ran", so it rides the same durable row as
	// the rest of the step vocabulary — no new table, no migration, and the
	// tamper-evident chain (#1369, v152) hashes it along with everything else.
	//
	// `pinned` is written whenever a digest is, INCLUDING when it is false.
	// Every other optional field here is omitted when empty because "absent"
	// and "empty" mean the same thing for them. They do not for this one: a
	// missing `pinned` would be indistinguishable from `pinned: false`, and
	// the difference is exactly the security claim — did the daemon fetch the
	// manifest we verified, or did it re-resolve a mutable tag? A digest with
	// no qualifier would read as a guarantee we did not make.
	if ev.Digest != "" {
		payload["digest"] = ev.Digest
		payload["pinned"] = ev.Pinned
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
	// The feature-build failure carries the BuildKit stderr tail — persist it
	// under a dedicated, durable type so ProvisionStatus can read it back post
	// hoc (#829). Every other step stays a generic provisioning.step row.
	entryType := journal.EntryProvisioningStep
	emitCtx := ctx
	if ev.Step == devcontainer.ProvStepBuildFailed {
		entryType = journal.EntryProvisioningBuildFailed
		// This is the whole point of #829: the durable diagnostic row MUST
		// land even when the provisioning ctx was already cancelled (build
		// timeout / client disconnect). journal.Emit races the queue send
		// against ctx.Done(), so a cancelled ctx can non-deterministically
		// drop the entry. Detach from cancellation (values preserved) —
		// same durability intent as markJobFailed's context.Background().
		emitCtx = context.WithoutCancel(ctx)
	}
	_, _ = h.journal.Emit(emitCtx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Type:        entryType,
		Severity:    severity,
		ActorType:   journal.ActorOrchestrator,
		Summary:     summary,
		Payload:     payload,
		Refs:        map[string]any{"crew_id": crewID},
	})
}

// RuntimeProvisionSink returns a ProvisionSink for the agent-run /
// ensure-container path: the runtime container-preparation events emitted by the
// container provider's EnsureCrewRuntime (start → image_resolved →
// container_create → ready, plus failed) are journaled + live-streamed with the
// SAME schema and routing as the explicit provisioning-job runner. Wiring this
// onto provider.CrewConfig closes the gap where agent-triggered container
// creation prepared a container with no audit trail. Returns nil on a nil
// handler (provisioning disabled) so the caller can assign it unconditionally —
// a nil sink is a no-op in the provider.
//
// runCtx is the DISPATCH context, taken for its values only: both call sites
// stamp journal.WithRunID before building the sink, so the emitted rows inherit
// trace_id == run.id and the image digest recorded here (#1825) is attributable
// to a specific run rather than merely to a crew and a timestamp.
//
// It is passed through context.WithoutCancel, not used directly. The original
// reason this function reached for context.Background() still holds — a run
// that completes or is cancelled the instant its container comes up must not
// drop the final ready/failed audit row, and journal.Emit races the queue send
// against ctx.Done(). WithoutCancel keeps that durability while stopping the
// values from being thrown away with the cancellation.
func (h *ProvisioningHandler) RuntimeProvisionSink(runCtx context.Context, crewID, workspaceID string) func(devcontainer.ProvisionEvent) {
	if h == nil {
		return nil
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	emitCtx := context.WithoutCancel(runCtx)
	return func(ev devcontainer.ProvisionEvent) {
		h.emitProvisionEvent(emitCtx, crewID, workspaceID, ev)
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
			var pending map[string]chatbridge.PendingChatMessage
			h.mu.Lock()
			if j, ok := h.jobs[crewID]; ok {
				j.Status = "failed"
				j.Error = panicErr
				now := time.Now()
				j.CompletedAt = &now
				pending, j.Pending = j.Pending, nil
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
			h.failPending(pending, fmt.Errorf("%s", panicErr))
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

	// #1032 (visibility mitigation): a privileged crew runs its container with
	// --privileged, which collapses the UID 1001 (agent) / 1002 (sidecar)
	// boundary that keeps a compromised agent from reading /proc/<sidecar>/mem
	// — i.e. the sidecar's crew-bound IPC token and any injected credentials.
	// The full fix (a non-privileged path for DinD etc.) is out of scope; the
	// WARN at least surfaces the trust downgrade in ops the moment such a crew
	// is provisioned, rather than leaving it silent.
	if result.Requirements.Privileged {
		h.logger.Warn("provisioned a PRIVILEGED crew — the UID 1001/1002 sidecar boundary is collapsed; a compromised agent can read the sidecar's IPC token and injected credentials (#1032)",
			"crew_id", crewID, "workspace_id", workspaceID, "base_image", baseImage)
	}

	// Persist the cached image reference on the crew row. Use a fresh context
	// (not the 30-min provisioning ctx, which may be near its deadline).
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer updateCancel()
	// What the build actually installed, so the image can be audited rather
	// than inferred from the config (#1779). Stored as '[]' when a crew uses no
	// features — distinct from NULL, which means "built before this existed".
	// nil and empty mean different things here, so the test is `!= nil` and
	// not `len() > 0`. A build always sets the slice — featureRecords returns
	// a non-nil slice even for a crew with no features — so empty means "this
	// build installed none", which is an answer and belongs in the column as
	// '[]'. A cache hit builds nothing and leaves the field nil; writing that
	// would serialize to JSON `null` and erase the digests an earlier build
	// recorded, after which the CLI reports the crew as "not recorded" and the
	// audit trail the column exists for is gone (#1779).
	setFeatures := ""
	updateArgs := []any{result.CachedImage, result.ConfigHash, reqJSON}
	if result.Features != nil {
		featBytes, marshalErr := json.Marshal(result.Features)
		if marshalErr != nil {
			// Leave the column alone rather than blanking it: "unknown" is
			// better recorded as the previous answer than as no answer.
			h.logger.Warn("marshal resolved_features failed, leaving the column untouched",
				"crew_id", crewID, "error", marshalErr)
		} else {
			setFeatures = "resolved_features = ?, "
			updateArgs = append(updateArgs, string(featBytes))
		}
	}
	updateArgs = append(updateArgs, crewID, workspaceID)

	_, err = h.db.ExecContext(updateCtx,
		`UPDATE crews SET cached_image = ?, config_hash = ?, cached_requirements = ?, `+
			setFeatures+
			`updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		updateArgs...,
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
	pending := job.Pending
	job.Pending = nil
	h.mu.Unlock()
	h.resumePending(pending)

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
	pending := job.Pending
	job.Pending = nil
	h.mu.Unlock()
	h.failPending(pending, err)

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
	// cached_requirements is deliberately left alone (#1032): it's the only
	// signal resolveAgentConfig's fail-closed credential gate has for "is
	// this crew's actual RUNNING container privileged", and EnqueueForCrew
	// below is async — nulling it here would open a window where the
	// container is STILL privileged (unchanged until the rebuild completes)
	// but the gate reads "unknown" and hands out credentials anyway. The
	// stale value stays accurate until the provisioning job's completion
	// handler overwrites it with the freshly computed one.
	_, err := h.db.ExecContext(r.Context(),
		`UPDATE crews SET cached_image = NULL, config_hash = NULL, updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		crewID, workspaceID,
	)
	if err != nil {
		replyInternalError(w, h.logger, "clear cached image for rebuild", err)
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
