package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// maxDeferralSeconds bounds delay/ttl/debounce windows (30 days) — keeps
// fire_at arithmetic well inside int64 Duration range (no overflow → no
// accidental immediate fire) and rejects absurd far-future schedules.
const maxDeferralSeconds = 30 * 24 * 3600

// defaultCapacityQueueTTL bounds how long an immediate start queued for a
// full concurrency slot waits when the caller set no ttl_seconds.
const defaultCapacityQueueTTL = time.Hour

// deferredOptions marks an immediate start that is being queued because its
// concurrency slot was full (#3025): it is due now, waits at most its TTL
// (defaultCapacityQueueTTL when none was given) and, when the caller sent an
// Idempotency-Key, gets a stable ID so a retry finds it.
type deferredOptions struct {
	queuedForCapacity bool
	id                string
	// triggeredVia / triggeredByID attribute a queued immediate start to what
	// started it; empty keeps the deferred default (effectivePendingTrigger).
	triggeredVia  pipeline.TriggeredVia
	triggeredByID string
}

// queuedKeyLocks serialises requests that carry the same Idempotency-Key
// through "look up the queued start → run → queue on refusal", so a retry
// cannot slip between the refusal and the queued row and run the work a
// second time. Process-local, matching the single-writer deployment.
type queuedKeyLocks struct {
	mu    sync.Mutex
	locks map[string]*queuedKeyLock
}

type queuedKeyLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until key is free and returns its release func, which is safe
// to call more than once.
func (l *queuedKeyLocks) lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*queuedKeyLock{}
	}
	k := l.locks[key]
	if k == nil {
		k = &queuedKeyLock{}
		l.locks[key] = k
	}
	k.refs++
	l.mu.Unlock()
	k.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			k.mu.Unlock()
			l.mu.Lock()
			if k.refs--; k.refs == 0 {
				delete(l.locks, key)
			}
			l.mu.Unlock()
		})
	}
}

// queuedStartID derives the pending ID of a capacity-queued start from the
// caller's Idempotency-Key. Empty without a key: the start then gets a random
// ID and a retry cannot be matched to it.
func queuedStartID(workspaceID, pipelineID, key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + pipelineID + "\x00" + key))
	return "pnd_q" + hex.EncodeToString(sum[:12])
}

// queuedStartForKey returns the capacity-queued start this Idempotency-Key
// already created, in any status, or nil.
func (h *PipelineHandler) queuedStartForKey(ctx context.Context, workspaceID, pipelineID, key string) *pipeline.PendingRun {
	id := queuedStartID(workspaceID, pipelineID, key)
	if id == "" || h.db == nil {
		return nil
	}
	pr, err := pipeline.NewPendingRunStore(h.db).Get(ctx, workspaceID, id)
	if err != nil {
		h.logger.Warn("lookup queued routine start", "error", err)
		return nil
	}
	return pr
}

// writeKeyedStartReceipt answers a retry whose Idempotency-Key already
// queued a start, with what became of it: still waiting (or dispatched
// without a run link yet) → the queued receipt; ran → DEDUPED with its run;
// ended without running → 409 naming the start, so the caller uses a new
// key to start again.
func writeKeyedStartReceipt(w http.ResponseWriter, pr pipeline.PendingRun) {
	switch {
	case pr.Status == "fired" && pr.FiredRunID != "":
		writeJSON(w, http.StatusOK, map[string]any{"status": "DEDUPED", "run_id": pr.FiredRunID, "pending_id": pr.ID})
	case pr.Status == "pending" || pr.Status == "fired":
		writeQueuedReceipt(w, pr, false)
	default:
		msg := fmt.Sprintf("This Idempotency-Key already queued start %s, which ended %s", pr.ID, pr.Status)
		if pr.LastError != "" {
			msg += ": " + pr.LastError
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": msg + ". Use a new Idempotency-Key to start again.", "pending_id": pr.ID, "pending_status": pr.Status,
		})
	}
}

// writeQueuedReceipt answers a start that waits for its concurrency slot.
func writeQueuedReceipt(w http.ResponseWriter, pr pipeline.PendingRun, coalesced bool) {
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":         "SCHEDULED",
		"pending_id":     pr.ID,
		"fire_at":        pr.FireAt.Format(time.RFC3339Nano),
		"coalesced":      coalesced,
		"pinned_version": pr.PinnedVersion,
		"priority":       pr.Priority,
		"queued":         true,
		"reason":         "concurrency_limit",
	})
}

// enqueueDeferredRun parks a delayed/debounced trigger in pending_runs.
// Always writes the HTTP response (scheduled receipt or error).
func (h *PipelineHandler) enqueueDeferredRun(w http.ResponseWriter, r *http.Request, workspaceID, invokingUser string, p *pipeline.Pipeline, body runRequestBody, opts deferredOptions) {
	// Bound every duration field so a huge value can't overflow the
	// fire_at/expires_at arithmetic (which would wrap negative and fire
	// immediately). Reject rather than clamp so the caller sees the limit.
	for name, v := range map[string]int{
		"delay_seconds": body.DelaySeconds, "ttl_seconds": body.TTLSeconds,
		"debounce_window_seconds": body.DebounceWindowSecond, "debounce_max_seconds": body.DebounceMaxSeconds,
	} {
		if v < 0 || v > maxDeferralSeconds {
			replyError(w, http.StatusBadRequest, name+" out of range (0.."+strconv.Itoa(maxDeferralSeconds)+"s)")
			return
		}
	}
	now := time.Now().UTC()

	// fire_at: a debounce trigger fires window-seconds after the latest
	// trigger (default 30s); a plain delay fires delay-seconds out.
	fireAt := now.Add(time.Duration(body.DelaySeconds) * time.Second)
	if body.FireAt != "" {
		at, err := time.Parse(time.RFC3339, body.FireAt)
		if err != nil || !at.After(now) || at.After(now.AddDate(2, 0, 0)) {
			replyError(w, http.StatusBadRequest, "fire_at must be a future RFC3339 timestamp within two years, including timezone offset")
			return
		}
		if body.DelaySeconds != 0 || body.DebounceKey != "" || body.TTLSeconds != 0 {
			replyError(w, http.StatusBadRequest, "fire_at cannot be combined with delay, debounce or ttl")
			return
		}
		fireAt = at.UTC()
	}

	if body.DebounceKey != "" {
		window := body.DebounceWindowSecond
		if window <= 0 {
			window = 30
		}
		fireAt = now.Add(time.Duration(window) * time.Second)
	}

	var expiresAt *time.Time
	if opts.queuedForCapacity && body.TTLSeconds == 0 {
		body.TTLSeconds = int(defaultCapacityQueueTTL / time.Second)
	}
	if body.TTLSeconds > 0 {
		e := now.Add(time.Duration(body.TTLSeconds) * time.Second)
		expiresAt = &e
	}
	var debounceMaxAt *time.Time
	if body.DebounceKey != "" && body.DebounceMaxSeconds > 0 {
		m := now.Add(time.Duration(body.DebounceMaxSeconds) * time.Second)
		debounceMaxAt = &m
	}

	inputsJSON := "{}"
	if len(body.Inputs) > 0 {
		if b, err := json.Marshal(body.Inputs); err == nil {
			inputsJSON = string(b)
		}
	}
	tagsJSON := "[]"
	if len(body.Tags) > 0 {
		if b, err := json.Marshal(body.Tags); err == nil {
			tagsJSON = string(b)
		}
	}

	// Pin the definition that passed preflight, not a concurrently changed
	// HEAD. Every deferral needs this, not only the one-time start (#2500):
	// a `delay_seconds` or debounced trigger is parked in the same table,
	// cleared the same governance/integration/resource/credential gates, and
	// hands the caller a receipt — so coming back minutes later running a
	// recipe that did not exist when they pressed Run is the same defect
	// whichever shape of deferral they used. A debounced burst keeps the
	// first trigger's pin, because coalescing makes it one logical trigger.
	if body.PinnedVersion == nil {
		var version int
		err := h.db.QueryRowContext(r.Context(), `SELECT version FROM pipeline_versions WHERE pipeline_id=? AND definition_hash=?`, p.ID, p.DefinitionHash).Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.logger.Error("deferred run: load archive", "error", err, "slug", p.Slug)
			replyError(w, http.StatusInternalServerError, "Could not load the published recipe archive.")
			return
		}
		if version < 1 {
			replyError(w, http.StatusConflict, "Published recipe archive is unavailable. Publish a version before scheduling a deferred start.")
			return
		}
		body.PinnedVersion = &version
	}
	// A debounced trigger may coalesce into a row that keeps an EARLIER pin
	// than the one this request was preflighted against. The inputs this
	// request brings then replace the row's, so they must satisfy the recipe
	// the row will actually run — judged inside the enqueue, against the pin
	// the store says wins, before anything is written. An incompatible
	// request is refused and the accepted row is left as it was.
	admit := func(ctx context.Context, pin *int) error {
		return h.deferredInputsFitPin(ctx, p, pin, body)
	}
	id := opts.id
	if id == "" {
		id = "pnd_" + generateCUID()
	}
	store := pipeline.NewPendingRunStore(h.db)
	stored, err := store.EnqueueChecked(r.Context(), pipeline.PendingRun{
		PinnedVersion: body.PinnedVersion,
		ID:            id,
		WorkspaceID:   workspaceID,
		PipelineID:    p.ID,
		PipelineSlug:  p.Slug,
		InputsJSON:    inputsJSON,
		TagsJSON:      tagsJSON,
		MetadataJSON:  marshalMetadata(body.Metadata),
		TierOverride:  body.TierOverride,
		Priority:      body.Priority,
		DebounceKey:   body.DebounceKey,
		FireAt:        fireAt,
		ExpiresAt:     expiresAt,
		DebounceMaxAt: debounceMaxAt,
		// Carry the triggering user so a notify step's `to: trigger` in the
		// deferred run reaches the person who scheduled it, not a workspace
		// notice (issue #842 Phase 1). Empty for service/token triggers.
		InvokingUserID:      invokingUser,
		InvocationAuthority: pipeline.HumanInvocationAuthority(invokingUser, pipeline.RoutineRunAuthority),
		TriggeredVia:        opts.triggeredVia,
		TriggeredByID:       opts.triggeredByID,
	}, admit)
	var conflict *deferredPinConflict
	if errors.As(err, &conflict) {
		replyError(w, http.StatusConflict, conflict.Error())
		return
	}
	if err != nil && opts.id != "" {
		// The key's start already exists (written by another request with
		// the same key): answer with it instead of failing.
		if existing, gerr := store.Get(r.Context(), workspaceID, opts.id); gerr == nil && existing != nil {
			writeKeyedStartReceipt(w, *existing)
			return
		}
	}
	if err != nil {
		h.logger.Error("enqueue deferred run", "error", err, "slug", p.Slug)
		replyError(w, http.StatusInternalServerError, "enqueue deferred run")
		return
	}
	// The receipt reports what was STORED. A coalescing request and the row
	// it joined can disagree on the pin (the row keeps the first trigger's,
	// #2500), and the only reading that cannot be wrong is the one the
	// compare-and-set just committed — not this request's computed pin, and
	// not a re-read that could fail and fall back to it.
	if opts.queuedForCapacity {
		writeQueuedReceipt(w, pipeline.PendingRun{ID: stored.ID, FireAt: stored.FireAt, PinnedVersion: stored.PinnedVersion, Priority: body.Priority}, stored.Coalesced)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":         "SCHEDULED",
		"pending_id":     stored.ID,
		"fire_at":        stored.FireAt.Format(time.RFC3339Nano),
		"coalesced":      stored.Coalesced,
		"pinned_version": stored.PinnedVersion,
		"priority":       body.Priority,
	})
}

// deferredPinConflict is the refusal for a coalesce whose inputs the kept
// pin cannot run. 409 rather than 400: the request is well-formed for HEAD,
// it collides with an earlier accepted trigger.
type deferredPinConflict struct {
	version int
	cause   error
}

func (c *deferredPinConflict) Error() string {
	return fmt.Sprintf("A pending run for this debounce key is pinned to version %d, which rejects these inputs: %v. Let it fire or cancel it before sending inputs written for the current version.", c.version, c.cause)
}

func (c *deferredPinConflict) Unwrap() error { return c.cause }

// deferredInputsFitPin re-judges body.Inputs against the archived recipe at
// pin. The request already passed ValidateFormInputs against HEAD, so the
// only case with any work in it is a pin that is NOT the version this
// request resolved from HEAD. A storage failure is an error (the enqueue
// does not proceed on a guess); a pin that names no archive is one too —
// a row pinned to nothing must not be extended.
func (h *PipelineHandler) deferredInputsFitPin(ctx context.Context, p *pipeline.Pipeline, pin *int, body runRequestBody) error {
	if pin == nil || (body.PinnedVersion != nil && *body.PinnedVersion == *pin) {
		return nil
	}
	v, err := h.store.GetVersion(ctx, p.ID, *pin)
	if err != nil {
		return fmt.Errorf("load pinned recipe v%d for %s: %w", *pin, p.Slug, err)
	}
	dsl, err := pipeline.Parse([]byte(v.DefinitionJSON))
	if err != nil {
		return fmt.Errorf("parse pinned recipe v%d for %s: %w", *pin, p.Slug, err)
	}
	if verr := pipeline.ValidateFormInputs(dsl, body.Inputs); verr != nil {
		return &deferredPinConflict{version: *pin, cause: verr}
	}
	return nil
}

// pendingRunReceipt is a read-only projection, not a replay payload. Fired
// means claimed/dispatched, not completed; its run ID can still be empty.
type pendingRunReceipt struct {
	Inputs           map[string]any `json:"inputs"`
	PinnedVersion    *int           `json:"pinned_version"`
	ID               string         `json:"id"`
	PipelineSlug     string         `json:"pipeline_slug"`
	DebounceKey      string         `json:"debounce_key,omitempty"`
	Priority         int            `json:"priority"`
	FireAt           string         `json:"fire_at"`
	ExpiresAt        *string        `json:"expires_at"`
	NextAttemptAt    *string        `json:"next_attempt_at"`
	Status           string         `json:"status"`
	RunID            string         `json:"run_id"`
	DispatchAttempts int            `json:"dispatch_attempts"`
	LastError        string         `json:"last_error"`
	CanCancel        bool           `json:"can_cancel"`
}

func deferredReceipt(pr pipeline.PendingRun, canUpdate bool) (pendingRunReceipt, error) {
	inputs, err := planPresetInputs(pr.InputsJSON)
	if err != nil {
		return pendingRunReceipt{}, err
	}
	asString := func(at *time.Time) *string {
		if at == nil {
			return nil
		}
		text := at.UTC().Format(time.RFC3339Nano)
		return &text
	}
	return pendingRunReceipt{
		Inputs: inputs, PinnedVersion: pr.PinnedVersion, ID: pr.ID,
		PipelineSlug: pr.PipelineSlug, DebounceKey: pr.DebounceKey,
		Priority: pr.Priority, FireAt: pr.FireAt.Format(time.RFC3339Nano),
		ExpiresAt: asString(pr.ExpiresAt), NextAttemptAt: asString(pr.NextAttemptAt),
		Status: pr.Status, RunID: pr.FiredRunID, DispatchAttempts: pr.DispatchAttempts,
		LastError: pr.LastError, CanCancel: pr.Status == "pending" && canUpdate,
	}, nil
}

// ListPendingRuns defaults to not-yet-fired rows. status=all includes receipts
// that have dispatched, expired, failed or been canceled.
// GET /api/v1/workspaces/{workspaceId}/pipelines/pending
func (h *PipelineHandler) ListPendingRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	switch status {
	case "pending", "fired", "failed", "expired", "cancelled", "all":
	default:
		replyError(w, http.StatusBadRequest, "invalid deferred status")
		return
	}
	rows, err := pipeline.NewPendingRunStore(h.db).ListByStatus(r.Context(), workspaceID, status, 100)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "list pending runs")
		return
	}
	out := make([]pendingRunReceipt, 0, len(rows))
	for _, pr := range rows {
		dto, err := deferredReceipt(pr, canRole(RoleFromContext(r.Context()), "update"))
		if err != nil {
			replyError(w, 500, "read pending inputs")
			return
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetPendingRun returns an accepted start by ID even after it leaves pending.
// GET /api/v1/workspaces/{workspaceId}/pipeline-pending/{pendingId}
func (h *PipelineHandler) GetPendingRun(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	pr, err := pipeline.NewPendingRunStore(h.db).Get(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("pendingId"))
	if err != nil {
		replyError(w, http.StatusInternalServerError, "read deferred start")
		return
	}
	if pr == nil {
		replyError(w, http.StatusNotFound, "deferred start not found")
		return
	}
	dto, err := deferredReceipt(*pr, canRole(RoleFromContext(r.Context()), "update"))
	if err != nil {
		replyError(w, 500, "read pending inputs")
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// CancelPendingRun cancels a not-yet-fired deferred run.
// POST /api/v1/workspaces/{workspaceId}/pipelines/pending/{pendingId}/cancel
func (h *PipelineHandler) CancelPendingRun(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "update") {
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	pendingID := r.PathValue("pendingId")
	if pendingID == "" {
		replyError(w, http.StatusBadRequest, "pendingId required")
		return
	}
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	store := pipeline.NewPendingRunStore(h.db)
	ok, err := store.Cancel(r.Context(), workspaceID, pendingID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "cancel pending run")
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "pending run not found (already fired, expired, or cancelled)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": pendingID})
}
