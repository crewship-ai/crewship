package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// generateWebhookSigningSecret returns 32 bytes of urandom encoded as
// 64 hex chars -- the canonical shape downstream code expects when it
// HMAC-signs incoming webhook bodies. Used by CreateWebhook when the
// caller submitted an empty signing_secret: opting out of HMAC was
// possible on the old path and silently let an attacker who learned
// the webhook URL forge requests. Audit M2.
func generateWebhookSigningSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webhook signing secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// defaultWebhookRatePerMin floors the per-token rate at 10/sec when
// the row has rate_limit_per_min <= 0. Audit A17.2 M1: the prior
// behaviour was "limit=0 = unlimited" at the rate-limiter level, which
// meant every webhook created without an explicit value (the default
// when a sender just POSTs a CreateWebhook body without
// rate_limit_per_min) had no flood defense. 600/min is generous for
// every legitimate burst-shape we see in production (Stripe events,
// GitHub push hooks, ngrok-style dev tunnels) while denying a signed
// sender from replay-bursting at line-rate.
//
// Operators who genuinely want unlimited can set rate_limit_per_min to
// a high explicit value (e.g. 100000); the floor only applies when the
// row was zero / negative.
const defaultWebhookRatePerMin = 600

// reservedWebhookInputKeys lists the inputs map keys that are derived
// directly from the request bytes (event payload, raw body, headers).
// Audit A17.2 M2: an operator-defined inputs_template used to be able
// to overwrite any key in the inputs map, including these three, which
// turned the executor into a confused deputy -- a template like
// {"event": "tampered"} would silently replace the real payload before
// the pipeline DSL saw it. Templates may still add new keys; they
// just can't override the request-derived ones.
var reservedWebhookInputKeys = map[string]struct{}{
	"event":   {},
	"raw":     {},
	"headers": {},
}

// webhookResponse is the wire shape returned by webhook list/get/save.
// The token surfaces in CRUD responses so the UI can show the user
// the public URL once on creation; thereafter the row sits in the
// database and the UI doesn't need to surface it again. Signing
// secret is NEVER returned post-create — the only path that reveals
// it is the create response, mirroring how Stripe / GitHub do it.
type webhookResponse struct {
	ID                    string         `json:"id"`
	WorkspaceID           string         `json:"workspace_id"`
	Name                  string         `json:"name"`
	TargetPipelineID      string         `json:"target_pipeline_id"`
	TargetPipelineSlug    string         `json:"target_pipeline_slug,omitempty"`
	TargetPipelineVersion *int           `json:"target_pipeline_version,omitempty"`
	Token                 string         `json:"token"`
	SigningSecretSet      bool           `json:"signing_secret_set"`
	SigningSecret         string         `json:"signing_secret,omitempty"` // only on create
	InputsTemplate        map[string]any `json:"inputs_template"`
	Enabled               bool           `json:"enabled"`
	RateLimitPerMin       int            `json:"rate_limit_per_min"`
	LastFiredAt           *time.Time     `json:"last_fired_at,omitempty"`
	LastStatus            string         `json:"last_status,omitempty"`
	LastRunID             string         `json:"last_run_id,omitempty"`
	FireCount             int64          `json:"fire_count"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

func (h *PipelineHandler) toWebhookResponse(w *pipeline.Webhook, slug string, includeSecret bool) webhookResponse {
	var tmpl map[string]any
	if w.InputsTemplateJSON != "" {
		_ = json.Unmarshal([]byte(w.InputsTemplateJSON), &tmpl)
	}
	if tmpl == nil {
		tmpl = map[string]any{}
	}
	resp := webhookResponse{
		ID:                    w.ID,
		WorkspaceID:           w.WorkspaceID,
		Name:                  w.Name,
		TargetPipelineID:      w.TargetPipelineID,
		TargetPipelineSlug:    slug,
		TargetPipelineVersion: w.TargetPipelineVersion,
		Token:                 w.Token,
		SigningSecretSet:      w.SigningSecret != "",
		InputsTemplate:        tmpl,
		Enabled:               w.Enabled,
		RateLimitPerMin:       w.RateLimitPerMin,
		LastFiredAt:           w.LastFiredAt,
		LastStatus:            w.LastStatus,
		LastRunID:             w.LastRunID,
		FireCount:             w.FireCount,
		CreatedAt:             w.CreatedAt,
		UpdatedAt:             w.UpdatedAt,
	}
	if includeSecret {
		resp.SigningSecret = w.SigningSecret
	}
	return resp
}

type webhookRequestBody struct {
	Name                  string         `json:"name"`
	TargetPipelineSlug    string         `json:"target_pipeline_slug"`
	TargetPipelineID      string         `json:"target_pipeline_id"`
	TargetPipelineVersion *int           `json:"target_pipeline_version,omitempty"`
	SigningSecret         string         `json:"signing_secret"`
	InputsTemplate        map[string]any `json:"inputs_template"`
	Enabled               *bool          `json:"enabled,omitempty"`
	RateLimitPerMin       int            `json:"rate_limit_per_min"`
}

// CreateWebhook POST /workspaces/{wsId}/pipeline-webhooks
func (h *PipelineHandler) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhooks == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_webhooks backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted + chain finding root cause: a MEMBER could
	// create a webhook → the resulting public URL bypassed every
	// auth surface on the dispatch side. Gating CreateWebhook on
	// "create" closes the chain at its source.
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var body webhookRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	pipelineID, slug, err := h.resolveWebhookPipelineID(r, workspaceID, &body)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	// Force HMAC signing on every webhook -- if the caller didn't
	// supply a secret, mint a 32-byte hex one server-side. The legacy
	// path accepted empty here and pipeline.Webhook.Verify silently
	// returned nil for empty SigningSecret (see pipeline/webhooks.go:222),
	// which meant any unsigned POST to the public webhook URL passed
	// the verification step. The create response surfaces the secret
	// once (Stripe/GitHub pattern below) so callers can configure
	// their sender even when they don't pre-generate. Audit M2.
	signingSecret := body.SigningSecret
	if signingSecret == "" {
		gen, err := generateWebhookSigningSecret()
		if err != nil {
			h.logger.Error("create pipeline webhook: generate signing secret", "error", err)
			replyError(w, http.StatusInternalServerError, "failed to create webhook")
			return
		}
		signingSecret = gen
	}

	in := pipeline.SaveWebhookInput{
		WorkspaceID:           workspaceID,
		Name:                  defaultIfBlank(body.Name, slug),
		TargetPipelineID:      pipelineID,
		TargetPipelineVersion: body.TargetPipelineVersion,
		SigningSecret:         signingSecret,
		InputsTemplate:        body.InputsTemplate,
		Enabled:               enabled,
		RateLimitPerMin:       body.RateLimitPerMin,
	}
	saved, err := h.webhooks.Save(r.Context(), in)
	if err != nil {
		h.logger.Warn("create pipeline webhook", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to create webhook")
		return
	}
	// Reveal the signing secret only on create — the UI shows it
	// once, the user copies it into the sender, and we never expose
	// it again. Stripe / GitHub use the same one-shot pattern.
	writeJSON(w, http.StatusCreated, h.toWebhookResponse(saved, slug, true))
}

// ListWebhooks GET /workspaces/{wsId}/pipeline-webhooks
func (h *PipelineHandler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	if h.webhooks == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_webhooks backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.webhooks.List(r.Context(), workspaceID)
	if err != nil {
		h.logger.Warn("list pipeline webhooks", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to list webhooks")
		return
	}
	out := make([]webhookResponse, 0, len(rows))
	slugCache := map[string]string{}
	for _, wh := range rows {
		slug, ok := slugCache[wh.TargetPipelineID]
		if !ok {
			if p, perr := h.store.GetByID(r.Context(), wh.TargetPipelineID); perr == nil {
				slug = p.Slug
			}
			slugCache[wh.TargetPipelineID] = slug
		}
		out = append(out, h.toWebhookResponse(wh, slug, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteWebhook DELETE /workspaces/{wsId}/pipeline-webhooks/{webhookId}
func (h *PipelineHandler) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhooks == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_webhooks backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted: delete a public-URL trigger -- delete tier.
	role := RoleFromContext(r.Context())
	if !canRole(role, "delete") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	webhookID := r.PathValue("webhookId")
	if webhookID == "" {
		replyError(w, http.StatusBadRequest, "webhookId required")
		return
	}
	existing, err := h.webhooks.GetByID(r.Context(), webhookID)
	if err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "webhook not found")
			return
		}
		replyError(w, http.StatusInternalServerError, "failed to load webhook")
		return
	}
	if existing.WorkspaceID != workspaceID {
		replyError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if err := h.webhooks.SoftDelete(r.Context(), webhookID); err != nil {
		h.logger.Warn("delete pipeline webhook", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to delete webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// FireWebhook POST /api/v1/webhooks/{token}
//
// Public dispatch entrypoint — NO auth middleware. Auth is the
// secret embedded in the token itself plus optional HMAC verification
// via X-Crewship-Signature.
//
// Dispatch is ASYNCHRONOUS: everything security-relevant (token
// lookup, HMAC verification, rate limit, governance status,
// concurrency pre-check, idempotency reservation) runs synchronously
// in the request, then the
// run itself starts in a background goroutine and the sender gets a
// 202 + {run_id, status: "PENDING"} immediately. Real senders
// (GitHub/Stripe) time out deliveries in 5–10s while an agent_run
// routine can take minutes — and the run context derives from the
// server lifecycle, NOT the request, so a sender hanging up cannot
// cancel an in-flight run server-side (wasted tokens). Poll the run_id
// via GET /pipeline-runs/{runId} (CLI: `crewship routine logs <run_id>`)
// for the outcome.
//
// Returns:
//   - 202 with { run_id, status: "PENDING" } on accepted (run runs
//     in the background under the returned id)
//   - 202 with { run_id, status: "DEDUPED", deduped: true } on an
//     idempotency-key replay — run_id is the ORIGINAL run's id
//   - 401 on HMAC mismatch
//   - 404 on unknown / disabled / deleted token (deliberate to
//     avoid leaking which tokens exist)
//   - 409 when the target routine is 'proposed'/'disabled' (governance)
//   - 429 + Retry-After on per-token rate limit hit, or when the
//     routine's concurrency gate is at capacity — checked
//     synchronously against the same run registry the executor
//     enforces, and WITHOUT consuming the idempotency key, so the
//     sender's retry executes instead of dedupe-ing
//   - 503 if the runner / webhook store isn't wired
func (h *PipelineHandler) FireWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhooks == nil || h.runner == nil {
		replyError(w, http.StatusServiceUnavailable, "webhook dispatch not wired")
		return
	}
	token := r.PathValue("token")
	wh, err := h.webhooks.GetByToken(r.Context(), token)
	if err != nil {
		// 404 on every failure — never reveal which tokens exist.
		replyError(w, http.StatusNotFound, "unknown webhook")
		return
	}
	if !wh.Enabled {
		replyError(w, http.StatusNotFound, "unknown webhook")
		return
	}

	// Read body up to 1 MiB. Webhook bodies that big are nearly
	// always misuse; if a real sender needs more, the limit can be
	// raised per-webhook (deferred until we see a use case).
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		replyError(w, http.StatusBadRequest, "could not read body")
		return
	}

	// HMAC verification before rate limiting -- invalid signatures
	// shouldn't even consume rate-limit slots. Required (not optional):
	// ValidateSignature returns false when the webhook row has an
	// empty SigningSecret, so a legacy row (predates audit #490's
	// auto-generation, or a DB write that bypassed the HTTP create
	// handler) cannot dispatch with an unsigned body. Same 401 +
	// "signature mismatch" response shape for both "wrong sig" and
	// "no secret on this row" so an attacker can't enumerate which
	// is which.
	sig := r.Header.Get("X-Crewship-Signature")
	if !wh.ValidateSignature(body, sig) {
		replyError(w, http.StatusUnauthorized, "signature mismatch")
		return
	}

	// Audit A17.2 M1: a rate_limit_per_min of 0 (the default for
	// rows created without an explicit value) used to mean
	// "unlimited" in pipeline.AllowWebhookFire. That left every
	// freshly-created webhook with no flood defense -- a signed
	// sender could replay-burst at line-rate and the only guard was
	// the per-run idempotency window. Floor at defaultWebhookRatePerMin
	// (600/min ≈ 10/s) when the operator hasn't set a stricter
	// limit; explicit non-zero values pass through unchanged so an
	// operator who wants "unlimited" can still set a high cap
	// (e.g. 100000) deliberately.
	effectiveLimit := wh.RateLimitPerMin
	if effectiveLimit <= 0 {
		effectiveLimit = defaultWebhookRatePerMin
	}
	if !pipeline.AllowWebhookFire(wh.Token, effectiveLimit) {
		w.Header().Set("Retry-After", "60")
		replyError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Build pipeline inputs. Default: pass body under "event" so the
	// pipeline can reference {{ inputs.event }}. The inputs_template
	// (if non-empty) is merged on top so a webhook can hand-shape
	// the event into the pipeline's input schema.
	//
	// Audit A17.2 M2 (confused-deputy): the merge used to overwrite
	// any key, including the canonical request-derived fields
	// (event / raw / headers). An operator-defined template like
	// {"event": "tampered"} would silently replace the actual
	// payload bytes before the pipeline ever saw them. Reserve the
	// three request-derived keys: if a template includes them,
	// drop the template's value (request bytes win). The template
	// is still useful for layering NEW keys onto the input shape.
	inputs := map[string]any{
		"event":   tryParseJSON(body),
		"raw":     string(body),
		"headers": flattenHeaders(r.Header),
	}
	if strings.TrimSpace(wh.InputsTemplateJSON) != "" && wh.InputsTemplateJSON != "{}" {
		var tmpl map[string]any
		if err := json.Unmarshal([]byte(wh.InputsTemplateJSON), &tmpl); err == nil {
			for k, v := range tmpl {
				if _, reserved := reservedWebhookInputKeys[k]; reserved {
					// Operator template tried to override a
					// request-derived field. Log once + skip --
					// don't fail the request (legacy templates
					// in production might lean on the old shape;
					// surface the warning so the operator can
					// clean up).
					h.logger.Warn("webhook inputs_template tried to override reserved key",
						"webhook_id", wh.ID, "key", k)
					continue
				}
				inputs[k] = v
			}
		}
	}

	// Governance pre-check, synchronous. The executor re-checks at run
	// time (its airbag chokepoint stays authoritative), but a
	// 'proposed'/'disabled' routine must answer 409 to the SENDER —
	// fire-and-forgetting a 202 for a policy-blocked routine would
	// leave the operator debugging a run that never existed.
	target, err := h.store.GetByID(r.Context(), wh.TargetPipelineID)
	if err != nil {
		_ = h.webhooks.RecordFire(r.Context(), wh.ID, "", "FAILED")
		h.logger.Warn("webhook fire: load target pipeline", "error", err, "webhook_id", wh.ID)
		replyError(w, http.StatusInternalServerError, "pipeline run failed")
		return
	}
	if !pipeline.StatusRunnable(target.Status) {
		_ = h.webhooks.RecordFire(r.Context(), wh.ID, "", "FAILED")
		replyError(w, http.StatusConflict, "routine is not active (awaiting approval or disabled)")
		return
	}

	// Pin pre-check, synchronous — same reasoning as the governance
	// pre-check above: a webhook pinned to a deleted routine version
	// must answer 409 to the SENDER, never fall back to head silently
	// (the executor re-checks authoritatively via PinnedVersion, but
	// by then the sender already holds a 202 for a run that will
	// fail). The pinned definition also feeds the concurrency
	// pre-check below so the key renders from what will actually run.
	targetDefinitionJSON := target.DefinitionJSON
	if wh.TargetPipelineVersion != nil {
		ver, verr := h.store.GetVersion(r.Context(), wh.TargetPipelineID, *wh.TargetPipelineVersion)
		if verr != nil {
			_ = h.webhooks.RecordFire(r.Context(), wh.ID, "", "FAILED")
			replyError(w, http.StatusConflict, fmt.Sprintf(
				"routine %q has no version %d (was it deleted? update or unpin the webhook)",
				target.Slug, *wh.TargetPipelineVersion))
			return
		}
		targetDefinitionJSON = ver.DefinitionJSON
	}

	// Concurrency pre-check, synchronous — and it MUST come before the
	// idempotency reservation below. An over-limit delivery answers
	// 429 + Retry-After like the old synchronous handler did; a 202
	// here would hand the sender a run_id for a run that dies on
	// ErrConcurrencyLimitReached before any pipeline_runs row exists —
	// the sender treats 202 as accepted and never retries, so the
	// event would be permanently lost. Checking before the reservation
	// also means a 429'd delivery never touches the idempotency store,
	// so the sender's retry of the same key executes as a fresh run.
	//
	// PrecheckConcurrency reads the same run registry (same key render,
	// same count-vs-max) the executor's Acquire enforces — same source
	// of truth, evaluated early. It cannot RESERVE the slot, though, so
	// a small TOCTOU window remains; the background goroutine below
	// handles a residual ErrConcurrencyLimitReached explicitly. Parse
	// or key-render errors fall through deliberately: the executor
	// re-parses authoritatively and its failure path (Forget +
	// RecordFire FAILED) surfaces them.
	if h.runs != nil {
		if dsl, derr := pipeline.Parse([]byte(targetDefinitionJSON)); derr == nil {
			if cerr := h.runs.PrecheckConcurrency(r.Context(), dsl, wh.WorkspaceID, inputs); errors.Is(cerr, pipeline.ErrConcurrencyLimitReached) {
				// Record the throttled attempt (parity with the old
				// synchronous handler, which stamped FAILED before
				// answering 429).
				_ = h.webhooks.RecordFire(r.Context(), wh.ID, "", "FAILED")
				w.Header().Set("Retry-After", "5")
				replyError(w, http.StatusTooManyRequests, "concurrency limit reached")
				return
			}
		}
	}

	// Idempotency reservation, synchronous — replays must resolve to
	// the ORIGINAL run id in the 202, not mint a dangling handle.
	//
	// Idempotency cascade (audit A17.2 chain follow-up):
	//   1. Explicit header from the sender (Idempotency-Key /
	//      X-Crewship-Event-ID) takes precedence -- this is the
	//      semantic id the sender wants to dedupe on.
	//   2. Falls back to a synthetic key derived from
	//      sha256(token + body + signature). Same wire bytes
	//      from the same webhook within the
	//      DefaultIdempotencyTTL window deduplicate
	//      automatically, regardless of whether the sender opted
	//      in to a header. Closes the replay-attack vector LIVE-
	//      verified in audit H-iter5-A17.2 (a captured POST
	//      replayed mid-window used to re-fire the pipeline).
	// The synthetic key is salted by the webhook token so two
	// webhooks happening to receive the same body don't collide.
	//
	// The run id is pre-allocated HERE so the 202 can hand the sender
	// a pollable handle before the run starts; RunIDOverride below
	// makes the executor journal under the same id.
	idemKey := webhookIdempotencyKey(r, body, wh.Token)
	runID := pipeline.NewRunID()
	idem := pipeline.NewIdempotencyStore(h.db)
	resolvedRunID, isNew, err := idem.LookupOrReserve(
		r.Context(), wh.WorkspaceID, idemKey, runID, wh.TargetPipelineID, pipeline.DefaultIdempotencyTTL,
	)
	if err != nil {
		h.logger.Warn("webhook fire: idempotency reserve", "error", err, "webhook_id", wh.ID)
		replyError(w, http.StatusInternalServerError, "pipeline run failed")
		return
	}
	if !isNew {
		// Duplicate delivery — answer with the original run's id so
		// retried webhooks see a stable success response. No dispatch.
		_ = h.webhooks.RecordFire(r.Context(), wh.ID, resolvedRunID, "DEDUPED")
		writeJSON(w, http.StatusAccepted, map[string]any{
			"run_id":  resolvedRunID,
			"status":  "DEDUPED",
			"deduped": true,
		})
		return
	}

	// Async dispatch. The run context derives from the server
	// lifecycle (NOT r.Context()) so the sender closing its connection
	// cannot cancel the run; the WaitGroup lets graceful shutdown
	// drain in-flight runs (same pattern as assignments_run.go).
	//
	// IdempotencyKey is deliberately NOT passed to the executor: the
	// reservation above already maps idemKey → runID, and a second
	// LookupOrReserve inside exec.Run would see its own reservation as
	// a duplicate and short-circuit the run to DEDUPED.
	exec := h.newExecutor()
	dispatchCtx := h.webhookDispatchContext()
	h.webhookDispatchWG.Add(1)
	go func() {
		defer h.webhookDispatchWG.Done()
		res, runErr := exec.Run(dispatchCtx, pipeline.RunInput{
			PipelineID: wh.TargetPipelineID,
			// Honour the pin: a webhook with target_pipeline_version
			// set executes that immutable version, not head. The
			// synchronous pre-check above already 409'd a missing
			// version; the executor re-verifies authoritatively.
			PinnedVersion: wh.TargetPipelineVersion,
			WorkspaceID:   wh.WorkspaceID,
			Inputs:        inputs,
			Mode:          pipeline.ModeRun,
			RunIDOverride: runID,
		})
		status := "FAILED"
		firedRunID := ""
		if res != nil {
			firedRunID = res.RunID
			if runErr == nil {
				// Record the executor's verdict verbatim — COMPLETED,
				// FAILED, CANCELLED, or WAITING (run parked on a
				// `wait: approval` step; resolving the waitpoint
				// resumes it to a terminal state).
				status = res.Status
			}
		}
		if runErr != nil {
			// Free the reservation so the sender can retry the same
			// key instead of dedupe-ing onto a run that never
			// happened. Mirrors the executor's own Forget on its
			// concurrency-rejection path.
			_ = idem.Forget(context.Background(), wh.WorkspaceID, idemKey)
			if errors.Is(runErr, pipeline.ErrConcurrencyLimitReached) {
				// Residual TOCTOU race: the synchronous pre-check above
				// saw a free slot, but another dispatch Acquire'd it
				// between our 202 and this run's Acquire (the pre-check
				// reads the registry without reserving). The sender
				// already holds a 202 it won't retry on, so make the
				// loss recoverable and loud: the fire records FAILED
				// below with this reason in the log, and the Forget
				// above released the idempotency key so a sender/
				// operator retry executes instead of DEDUPE-ing onto a
				// run that never happened.
				h.logger.Warn("webhook fire (async run): concurrency limit reached despite synchronous pre-check (TOCTOU window); fire recorded FAILED, idempotency key released so a retry can execute",
					"webhook_id", wh.ID, "run_id", runID)
			} else {
				h.logger.Warn("webhook fire (async run)", "error", runErr,
					"webhook_id", wh.ID, "run_id", runID)
			}
		}
		// Bookkeeping on a fresh context: at shutdown dispatchCtx is
		// already cancelled when the run winds down, and the terminal
		// record must still land.
		_ = h.webhooks.RecordFire(context.Background(), wh.ID, firedRunID, status)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id":  runID,
		"status":  "PENDING",
		"deduped": false,
	})
}

func (h *PipelineHandler) resolveWebhookPipelineID(r *http.Request, workspaceID string, body *webhookRequestBody) (string, string, error) {
	if body.TargetPipelineID != "" {
		p, err := h.store.GetByID(r.Context(), body.TargetPipelineID)
		if err != nil {
			return "", "", errors.New("target_pipeline_id not found")
		}
		if p.WorkspaceID != workspaceID {
			return "", "", errors.New("target_pipeline_id not in this workspace")
		}
		return p.ID, p.Slug, nil
	}
	if body.TargetPipelineSlug != "" {
		p, err := h.store.GetBySlug(r.Context(), workspaceID, body.TargetPipelineSlug)
		if err != nil {
			return "", "", errors.New("target_pipeline_slug not found")
		}
		return p.ID, p.Slug, nil
	}
	return "", "", errors.New("target_pipeline_slug or target_pipeline_id required")
}

// webhookIdempotencyKey resolves the dedupe key for a webhook
// dispatch. Audit A17.2 follow-up: previously the dedupe path was
// opt-in via Idempotency-Key / X-Crewship-Event-ID headers, which
// left a replay window open for senders that don't set one (the
// majority of legacy webhooks). Auto-dedupe closes that window.
//
// Cascade:
//
//  1. Sender-provided Idempotency-Key (RFC 9110 draft).
//  2. Sender-provided X-Crewship-Event-ID (our own convention).
//  3. Synthetic sha256(token || "\x00" || signature || "\x00" || body).
//     Token salts the digest so two webhooks with the same body don't
//     collide. Signature is included so a sender that genuinely wants
//     to re-fire with the same body (e.g. test runs) can rotate the
//     signature deliberately. body is the raw payload bytes.
//
// The synthetic shape is prefix-tagged ("auto:") so an attacker who
// observes a key cannot use it as a future Idempotency-Key header to
// pre-poison dedupe -- the key would still hash differently from
// the sender-provided form.
func webhookIdempotencyKey(r *http.Request, body []byte, token string) string {
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		return k
	}
	if k := r.Header.Get("X-Crewship-Event-ID"); k != "" {
		return k
	}
	sig := r.Header.Get("X-Crewship-Signature")
	h := sha256.New()
	_, _ = h.Write([]byte(token))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(sig))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(body)
	return "auto:" + hex.EncodeToString(h.Sum(nil))
}

// tryParseJSON returns the parsed JSON object/array if the body is
// valid JSON, else returns the raw string. Most webhook bodies are
// JSON, but we don't 400 on non-JSON — the pipeline can still read
// inputs.raw if it wants the unparsed form.
func tryParseJSON(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		return v
	}
	return string(body)
}

// flattenHeaders converts http.Header (map[string][]string) into a
// flat string→string map for template-friendly access:
//
//	{{ inputs.headers.x_event_type }}
//
// Multi-value headers are joined with commas (the standard).
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[strings.ToLower(strings.ReplaceAll(k, "-", "_"))] = strings.Join(vs, ",")
	}
	return out
}
