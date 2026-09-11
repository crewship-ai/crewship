package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/work"
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
//
// Aliased to pipeline.WebhookUntrustedInputKeys (#1416 item 1) so this
// "can't be overridden by inputs_template" allowlist and the executor's
// "must be fenced before reaching an agent_run prompt" allowlist share one
// definition instead of silently drifting apart.
var reservedWebhookInputKeys = pipeline.WebhookUntrustedInputKeys

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
	// RotateSecret is PATCH-only (F21, B9 #2362): the explicit, opt-in
	// signal to mint a new HMAC signing secret. Absent/false means "leave
	// the existing secret alone" — the whole point of this endpoint over
	// the old delete+recreate dance is that an ordinary edit (rename,
	// change the rate limit, tweak the inputs template) never invalidates
	// what the sender already has configured. The token — and therefore
	// the webhook's public URL — never rotates through this endpoint at
	// all; that still requires delete + recreate.
	RotateSecret bool `json:"rotate_secret,omitempty"`
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

	// #1416 item 4: cap the request body like every exec route already
	// does (maxExecBodyBytes) -- CreateWebhook had no MaxBytesReader at
	// all, so a create-role member could pin server memory with an
	// oversized body.
	var body webhookRequestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
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
		// #1254 item 1: the HMAC signing secret fails CLOSED when no
		// encryption key is configured. Surface the misconfiguration with
		// the fix instead of a generic failure.
		if errors.Is(err, encryption.ErrPlaintextRefused) {
			replyError(w, http.StatusInternalServerError, encryptionNotConfiguredMsg)
			return
		}
		replyError(w, http.StatusInternalServerError, "failed to create webhook")
		return
	}
	// Reveal the signing secret only on create — the UI shows it
	// once, the user copies it into the sender, and we never expose
	// it again. Stripe / GitHub use the same one-shot pattern.
	writeJSON(w, http.StatusCreated, h.toWebhookResponse(saved, slug, true))
}

// UpdateWebhook PATCH /workspaces/{wsId}/pipeline-webhooks/{webhookId}
//
// F21 (B9, #2362): the only way to change a webhook used to be delete +
// recreate, which mints a new token and therefore a new URL — every sender
// configured against the old one breaks. This edits in place: name,
// target pipeline (+ version pin), inputs_template, enabled and
// rate_limit_per_min all follow the same absent-keeps-existing convention
// UpdateSchedule uses. The token is never touched by this handler, full
// stop — there is no field that rotates it. The signing secret is
// preserved unless the caller explicitly sets rotate_secret:true, in which
// case a freshly minted secret is revealed once in the response, exactly
// like CreateWebhook's create-time reveal.
func (h *PipelineHandler) UpdateWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhooks == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_webhooks backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Same tier as UpdateSchedule: an edit changes what a sender's request
	// does (retarget, disable, reshape inputs) — manage, not create.
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
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

	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExecBodyBytes))
	if err != nil {
		replyError(w, http.StatusBadRequest, "could not read body")
		return
	}
	var body webhookRequestBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	// Distinguish "target_pipeline_version absent — keep the pin" from an
	// explicit null clearing it, same reasoning as UpdateSchedule.
	var rawKeys map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &rawKeys)
	if _, mentioned := rawKeys["target_pipeline_version"]; !mentioned {
		body.TargetPipelineVersion = existing.TargetPipelineVersion
	}

	pipelineID := existing.TargetPipelineID
	slug := ""
	if body.TargetPipelineSlug != "" || body.TargetPipelineID != "" {
		pid, sl, rerr := h.resolveWebhookPipelineID(r, workspaceID, &body)
		if rerr != nil {
			replyError(w, http.StatusBadRequest, rerr.Error())
			return
		}
		pipelineID, slug = pid, sl
	} else if p, perr := h.store.GetByID(r.Context(), pipelineID); perr == nil {
		// Unchanged target — reuse UpdateSchedule's shape (pipeline_schedules.go)
		// rather than resolving twice: resolveWebhookPipelineID above already
		// returns the slug on the retarget path, so this lookup only runs
		// when the target didn't change.
		slug = p.Slug
	}

	enabled := existing.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	inputsTemplate := body.InputsTemplate
	if _, mentioned := rawKeys["inputs_template"]; !mentioned {
		_ = json.Unmarshal([]byte(existing.InputsTemplateJSON), &inputsTemplate)
	}
	// Mentioned-only, matching every other field in this merge (and
	// CreateWebhook, which stores whatever value it's given verbatim — the
	// fire path's defaultWebhookRatePerMin floor is what turns 0 into
	// "effectively unlimited-ish default", not this handler). An earlier
	// version additionally required > 0, which silently dropped an
	// explicit 0 or negative value with a 200 that echoed the OLD limit —
	// indistinguishable from success to a caller resetting to default.
	rateLimit := existing.RateLimitPerMin
	if _, mentioned := rawKeys["rate_limit_per_min"]; mentioned {
		rateLimit = body.RateLimitPerMin
	}

	// Secret rotation is explicit and opt-in (F21). Every other path keeps
	// the existing plaintext (GetByID already decrypted it) so Save's
	// re-encrypt is a no-op change in content.
	signingSecret := existing.SigningSecret
	rotated := false
	if body.RotateSecret {
		gen, gerr := generateWebhookSigningSecret()
		if gerr != nil {
			h.logger.Error("update pipeline webhook: rotate signing secret", "error", gerr)
			replyError(w, http.StatusInternalServerError, "failed to rotate signing secret")
			return
		}
		signingSecret = gen
		rotated = true
	}

	in := pipeline.SaveWebhookInput{
		ID:                    webhookID,
		WorkspaceID:           workspaceID,
		Name:                  defaultIfBlank(body.Name, existing.Name),
		TargetPipelineID:      pipelineID,
		TargetPipelineVersion: body.TargetPipelineVersion,
		SigningSecret:         signingSecret,
		InputsTemplate:        inputsTemplate,
		Enabled:               enabled,
		RateLimitPerMin:       rateLimit,
	}
	saved, err := h.webhooks.Save(r.Context(), in)
	if err != nil {
		h.logger.Warn("update pipeline webhook", "error", err)
		if errors.Is(err, encryption.ErrPlaintextRefused) {
			replyError(w, http.StatusInternalServerError, encryptionNotConfiguredMsg)
			return
		}
		replyError(w, http.StatusInternalServerError, "failed to update webhook")
		return
	}
	// Reveal the new secret only when this call minted one — same
	// show-once contract CreateWebhook uses. An ordinary edit (no
	// rotate_secret) never returns the secret, rotated or not.
	writeJSON(w, http.StatusOK, h.toWebhookResponse(saved, slug, rotated))
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

// webhookFireFailureAlertThreshold is how many consecutive fire failures
// the SAME webhook must accumulate before a human is paged (PRD-ISSUES-
// AND-ROUTINES-2026.md §17 A4, F20). One failure is noise-tolerant — a
// momentary DB busy, a governance load hiccup, a sender retry landing
// mid-deploy — and paging on it is exactly the alert fatigue that trains
// people to ignore the inbox. Three in a row for the same webhook means
// the fire path is structurally broken for it, not unlucky timing, and
// every one of those three represents a delivery the sender believes it
// made that never became a run. Deliberately lower than a schedule's
// default of 5 (defaultMaxConsecutiveFailures, schedules.go): a schedule
// fire that reaches FAILED at least produced a run whose failure has its
// own EntryPipelineRunFailed trail (see EntryPipelineWebhookFireFailed's
// doc comment); a webhook FIRE failure means no run exists AT ALL, so
// finding out sooner matters more.
const webhookFireFailureAlertThreshold = 3

// alertWebhookFireFailure is the single chokepoint every "the fire did
// not become a run" branch in FireWebhook calls, in place of the bare
// `_ = h.webhooks.RecordFire(...)` this replaced. It ALWAYS writes a
// journal entry — the durable, queryable "should have run, didn't"
// record A4 asks for, mirroring EntryAutomationEnqueueFailed's
// every-failure cadence — and only once the webhook's consecutive-
// failure streak (read back atomically from RecordFire's RETURNING, so
// concurrent fires of the same webhook can't each see a stale count and
// both think they crossed the line) reaches webhookFireFailureAlertThreshold
// does it raise a MANAGER inbox card. This is the same split the schedule
// path already draws between "every failure is journaled"
// (EntryPipelineRunFailed, per run) and "only a repeated failure pages a
// human" (maybeTripCircuitBreaker, schedules.go).
//
// wh is the webhook row read before the fire attempt; runID is empty when
// the fire never got far enough to allocate one (every synchronous
// pre-check failure) and populated for the async-dispatch failure path;
// reason is a short, human-readable cause safe to show in the inbox.
//
// Upsert, not Insert, on the inbox write: the SAME webhook can cross this
// threshold more than once across its life (fail, recover, fail again),
// and each crossing is news about the SAME subject (source_id = webhook
// ID) rather than a new one-off event — a card a human already resolved
// must be resurrected to unread, not silently swallowed by the (kind,
// source_id) unique index the way a second Insert would be.
func (h *PipelineHandler) alertWebhookFireFailure(ctx context.Context, wh *pipeline.Webhook, runID, reason string) {
	var streak int64
	if h.webhooks != nil {
		var err error
		streak, err = h.webhooks.RecordFire(ctx, wh.ID, runID, "FAILED")
		if err != nil {
			h.logger.Warn("webhook fire: record failure", "error", err, "webhook_id", wh.ID)
		}
	}

	if h.emitter != nil {
		_, _ = h.emitter.Emit(ctx, journal.Entry{
			WorkspaceID: wh.WorkspaceID,
			Type:        journal.EntryPipelineWebhookFireFailed,
			Severity:    journal.SeverityError,
			ActorType:   journal.ActorSystem,
			ActorID:     "webhook",
			Summary:     fmt.Sprintf("Webhook %s failed to fire: %s", wh.Name, reason),
			Payload: map[string]any{
				"webhook_id":           wh.ID,
				"pipeline_id":          wh.TargetPipelineID,
				"run_id":               runID,
				"reason":               reason,
				"consecutive_failures": streak,
			},
		})
	}

	// Fire the alert exactly ONCE per streak, at the moment it crosses the
	// threshold — not "every failure from here on", which would be the same
	// flood the threshold exists to prevent. A later success (RecordFire
	// resets consecutive_fire_failures to 0) lets a FUTURE streak cross the
	// threshold again and Upsert the same card back to unread.
	if streak != webhookFireFailureAlertThreshold {
		return
	}
	if err := inbox.WriteThreaded(ctx, h.db, h.logger, inbox.Item{
		WorkspaceID: wh.WorkspaceID,
		Kind:        inbox.KindWebhookFireFailed,
		SourceID:    wh.ID,
		TargetRole:  "MANAGER",
		Title:       fmt.Sprintf("Webhook failing to fire: %s", wh.Name),
		BodyMD: fmt.Sprintf(
			"Webhook **%s** has failed to fire %d times in a row — %s. "+
				"Check the routine's recent runs and the Journal for the underlying cause.",
			wh.Name, streak, reason),
		SenderType: "pipeline",
		SenderName: wh.Name,
		Priority:   "high",
		Payload: map[string]interface{}{
			"webhook_id":           wh.ID,
			"pipeline_id":          wh.TargetPipelineID,
			"consecutive_failures": streak,
		},
		ThreadKey:      "webhook:" + wh.ID + ":fire_failed",
		AttentionClass: inbox.AttentionRepair,
		Actions:        []inbox.Action{{ID: "acknowledge", Label: "Acknowledge", Effect: "Marks the failing webhook reviewed", Irreversible: false}},
	}); err != nil {
		h.logger.Warn("webhook fire: inbox alert", "error", err, "webhook_id", wh.ID)
	}
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
// The 202 is written AFTER the delivery and its work item commit, and before
// anything outside the database happens. That order is the contract (§5's I1),
// not an implementation detail: the sender's receipt names rows that exist.
//
// Returns:
//   - 202 with { run_id, work_id, delivery_id, status: "PENDING",
//     deduped: false } on accepted (the run then starts in the background
//     under the returned run id)
//   - 202 with { run_id, work_id, delivery_id, status: "DEDUPED",
//     deduped: true } on a replay inside the retention window — run_id is
//     the ORIGINAL run's id, and no second attempt is created
//   - 400 on a body that could not be read
//   - 401 on HMAC mismatch
//   - 404 on unknown / disabled / deleted token (deliberate to
//     avoid leaking which tokens exist)
//   - 409 when the target routine is 'proposed'/'disabled' (governance),
//     or "delivery_conflict" when the same source delivery id arrives with a
//     different body — the original record is kept
//   - 413 when the body is over the ingress limit. It used to be truncated
//     silently and rejected as a signature mismatch
//   - 429 + Retry-After on per-token rate limit hit, or when the
//     routine's concurrency gate is at capacity — checked
//     synchronously against the same run registry the executor
//     enforces, and BEFORE the delivery is recorded, so the sender's retry
//     is accepted rather than deduped
//   - 503 if the runner / webhook store isn't wired, or if the acceptance
//     transaction did not commit. Never a false 202
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

	// Read the body under a REAL limit. It used to be
	// io.ReadAll(io.LimitReader(r.Body, 1<<20)), and a LimitReader does not
	// fail: it stops at the limit and reports EOF. A 2 MiB delivery was
	// therefore silently TRUNCATED to its first mebibyte and handed to HMAC
	// verification, where it failed — so the sender was told 401 "signature
	// mismatch" when the truth was that we had refused to read its request,
	// and an operator debugging it had nothing pointing at the size.
	//
	// http.MaxBytesReader errors instead, so the oversized case is answerable
	// with §5's 413, and it counts bytes actually read, so the cap holds for a
	// chunked upload that declares no Content-Length.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhook.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.logger.Warn("webhook fire: body over the ingress limit",
				"webhook_id", wh.ID, "limit_bytes", webhook.MaxBodyBytes)
			replyError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
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
	//
	// #1416 item 2: when the sender includes X-Crewship-Timestamp, the
	// signature must cover "<timestamp>.<body>" AND the timestamp must be
	// fresh (DefaultWebhookTimestampTolerance) — the same ts.body scheme +
	// freshness window internal/webhook.Handler already enforces for
	// agent webhooks. This closes the replay gap ValidateSignature's
	// bare-body HMAC leaves open: a captured signed request could
	// otherwise be re-fired any time inside the (up to 24h,
	// Forget-reopenable) idempotency window. Optional — a sender that
	// hasn't adopted the header falls back to the body-only scheme
	// unchanged, so existing integrations keep working.
	sig := r.Header.Get("X-Crewship-Signature")
	if ts := r.Header.Get("X-Crewship-Timestamp"); ts != "" {
		if !wh.ValidateTimestampedSignature(body, ts, sig, time.Now(), 0) {
			replyError(w, http.StatusUnauthorized, "signature mismatch")
			return
		}
	} else if !wh.ValidateSignature(body, sig) {
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
		h.alertWebhookFireFailure(r.Context(), wh, "", "could not load the target routine")
		h.logger.Warn("webhook fire: load target pipeline", "error", err, "webhook_id", wh.ID)
		replyError(w, http.StatusInternalServerError, "pipeline run failed")
		return
	}
	if !pipeline.StatusRunnable(target.Status) {
		h.alertWebhookFireFailure(r.Context(), wh, "", "target routine is not active (awaiting approval or disabled)")
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
			h.alertWebhookFireFailure(r.Context(), wh, "", "pinned routine version no longer exists")
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
				h.alertWebhookFireFailure(r.Context(), wh, "", "concurrency limit reached before a run could start")
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

	receipt, acceptErr := h.acceptRoutineDelivery(r, wh, idemKey, runID, body, targetDefinitionJSON)
	if acceptErr != nil {
		switch {
		case errors.Is(acceptErr, work.ErrDeliveryConflict):
			// Same source delivery id, different body. §5: keep the original
			// record and say so; do not guess which of the two is right.
			h.logger.Warn("webhook fire: delivery conflict", "webhook_id", wh.ID)
			replyError(w, http.StatusConflict, "delivery_conflict")
		default:
			// Budget exceeded, database unavailable, constraint fault: 503 and
			// no dispatch. Never a false 202 — §5's last row.
			h.logger.Error("webhook fire: acceptance did not commit; no run started",
				"error", acceptErr, "webhook_id", wh.ID)
			w.Header().Set("Retry-After", webhook.IngressRetryAfterSeconds)
			replyError(w, http.StatusServiceUnavailable, "acceptance unavailable")
		}
		return
	}
	if receipt.Duplicate {
		// Duplicate delivery — answer with the ORIGINAL run's id so retried
		// webhooks see a stable success response. No dispatch, and no second
		// attempt merely because the message arrived twice.
		resolvedRunID := h.runIDForWork(r.Context(), receipt.WorkID)
		if resolvedRunID == "" {
			resolvedRunID = runID
		}
		_, _ = h.webhooks.RecordFire(r.Context(), wh.ID, resolvedRunID, "DEDUPED")
		writeJSON(w, http.StatusAccepted, map[string]any{
			"run_id":      resolvedRunID,
			"status":      "DEDUPED",
			"deduped":     true,
			"delivery_id": receipt.DeliveryID,
			"work_id":     receipt.WorkID,
			"duplicate":   true,
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
	// The receipt goes out FIRST. The delivery and its work item are already
	// committed, so this is the answer §5 asks for — after the commit, before
	// anything outside the database. Nothing below this line runs until the
	// sender has been told what was accepted.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id":      runID,
		"status":      "PENDING",
		"deduped":     false,
		"delivery_id": receipt.DeliveryID,
		"work_id":     receipt.WorkID,
		"duplicate":   false,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	exec := h.newExecutor()
	dispatchCtx := h.webhookDispatchContext()
	h.webhookDispatchWG.Add(1)
	finish := beginBackgroundWork()
	go func() {
		defer finish()
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
			// TriggeredVia marks this run as webhook-originated so the
			// audit trail is accurate AND so the executor's egress/fence
			// hardening (#1416 items 1 & 3) engages: agent_run prompts
			// fence the request-derived inputs, and http-step egress is
			// held to the crew's 'restricted' floor regardless of the
			// crew's own network_mode.
			TriggeredVia: pipeline.TriggeredViaWebhook,
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
			// Nothing is deleted here any more. The delivery ledger records
			// that this delivery was accepted and what it produced, and a
			// delivery is never removed on failure — a retry becomes another
			// attempt of the SAME work, not a second piece of work. Deleting
			// the dedup key was what made a redelivery repeat effects that had
			// already happened once.
			reason := "run dispatch failed: " + truncate(runErr.Error(), 200)
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
				reason = "concurrency limit reached (race after the synchronous pre-check)"
				h.logger.Warn("webhook fire (async run): concurrency limit reached despite synchronous pre-check (TOCTOU window); fire recorded FAILED, work stays queued in the delivery ledger",
					"webhook_id", wh.ID, "run_id", runID, "work_id", receipt.WorkID)
			} else {
				h.logger.Warn("webhook fire (async run)", "error", runErr,
					"webhook_id", wh.ID, "run_id", runID)
			}
			// This is the "fire never became a run" case exec.Run itself
			// reported (as opposed to a run that started and whose ROUTINE
			// then failed — see the res.Status == "FAILED" branch below,
			// which already gets full journal coverage from the executor's
			// own EntryPipelineRunFailed and is deliberately NOT routed
			// through the fire-failure alert). Bookkeeping runs on a fresh
			// context: at shutdown dispatchCtx is already cancelled when
			// the run winds down, and the terminal record must still land.
			h.alertWebhookFireFailure(context.Background(), wh, firedRunID, reason)
			return
		}
		// A FAILED run used to DELETE its dedup key here (#1429, 2.6), so the
		// sender's next redelivery of the same event re-executed the routine.
		// That contract is inverted, deliberately.
		//
		// The argument for deleting was that a wedged key denies a re-fire for
		// the full 24h TTL. The argument against is stronger and is §4's: a run
		// that FAILED did not necessarily fail before touching anything. It may
		// have posted the comment, opened the PR, charged the card, and then
		// failed on the step after. Re-running it because the delivery arrived
		// again repeats effects that already happened, and the sender never
		// asked for that — it asked for its event to be handled once.
		//
		// So the delivery stays recorded and a redelivery is a duplicate. A
		// re-run is an authorized REPLAY: a new work item carrying replay_of,
		// a reason and a fresh authorization, which is a deliberate act rather
		// than a consequence of a provider's retry timer. An ambiguous external
		// effect belongs in needs_reconciliation, not in a blind retry.
		//
		// What this costs, plainly: a routine that fails for a transient reason
		// no longer re-fires by itself on the sender's retry. Until the
		// dispatcher owns retry_wait, recovering that run is a manual replay.
		// Bookkeeping on a fresh context: at shutdown dispatchCtx is
		// already cancelled when the run winds down, and the terminal
		// record must still land.
		_, _ = h.webhooks.RecordFire(context.Background(), wh.ID, firedRunID, status)
	}()
}

// acceptRoutineDelivery records the delivery and the work it produces in ONE
// transaction, and returns the receipt.
//
// This is W3. The two writes used to be separate: a reservation in
// pipeline_run_idempotency, written synchronously, and the run itself created
// inside a goroutine after the 202 had already gone out. A crash in the window
// between them left a reservation naming a run that never existed, and because
// the reservation was what a redelivery matched against, the sender's retry was
// answered "202 DEDUPED" — pointing at a phantom — for the rest of the 24-hour
// TTL. Nothing ever ran, and nothing ever said so.
//
// The delivery row and the work_items row now commit together or not at all, so
// there is no state in which one exists without the other, and a redelivery is
// matched against the ledger rather than against a promise.
//
// The transaction is bounded by the request context only. The dedicated
// acceptance handle in internal/work — the one whose busy_timeout actually
// bounds a contended SQLite write, which a context deadline does not — needs a
// database path and an owner that closes it, and PipelineHandler is built
// elsewhere. Wiring it belongs with the server's database handle; until then
// this surface gets the atomicity and not the bounded wait.
func (h *PipelineHandler) acceptRoutineDelivery(
	r *http.Request, wh *pipeline.Webhook, sourceDeliveryID, runID string, body []byte, targetDefinitionJSON string,
) (work.Receipt, error) {
	if h.db == nil {
		return work.Receipt{}, fmt.Errorf("%w: no database handle", work.ErrAcceptanceBudget)
	}
	sum := sha256.Sum256(body)
	bodySHA := hex.EncodeToString(sum[:])

	// The routine version this delivery was accepted against. §3 wants the
	// target revision on the record, so a replay against a different version is
	// an explicit act rather than an accident of head having moved.
	targetRevision := ""
	if wh.TargetPipelineVersion != nil {
		targetRevision = fmt.Sprintf("v%d", *wh.TargetPipelineVersion)
	} else {
		vsum := sha256.Sum256([]byte(targetDefinitionJSON))
		targetRevision = "sha256:" + hex.EncodeToString(vsum[:])
	}

	// The profile is RECORDED, not chosen. §12 forbids converting an endpoint's
	// signature behaviour without an explicit configuration change, so
	// verification stays exactly where it was, above, with exactly the
	// semantics it had; this only writes down which of the two legacy shapes
	// the request actually presented.
	profile := "legacy-routine-hmac"
	if r.Header.Get("X-Crewship-Timestamp") != "" {
		profile = "legacy-routine-ts-hmac"
	}

	d := work.Delivery{
		WorkspaceID:      wh.WorkspaceID,
		EndpointID:       wh.ID,
		EndpointKind:     "routine",
		Profile:          profile,
		SourceDeliveryID: sourceDeliveryID,
		BodySHA256:       bodySHA,
		BodyBytes:        len(body),
		RawBody:          body,
		FilterDecision:   work.FilterAccepted,
		TargetRevision:   targetRevision,
	}
	// The immutable input is a reference, not a copy: the raw body is already
	// on the delivery row above, and duplicating a mebibyte of it into
	// work_items.input_json would double the ingress byte budget §6 caps.
	inputJSON, _ := json.Marshal(map[string]any{
		"pipeline_id":     wh.TargetPipelineID,
		"pinned_version":  wh.TargetPipelineVersion,
		"triggered_via":   string(pipeline.TriggeredViaWebhook),
		"delivery_source": "routine_webhook",
	})
	req := work.AcceptRequest{
		WorkspaceID:    wh.WorkspaceID,
		Source:         work.SourceWebhook,
		DomainKind:     work.DomainPipelineRun,
		DomainID:       runID,
		Class:          work.ClassBackground,
		InputJSON:      string(inputJSON),
		InputSHA256:    bodySHA,
		TargetRevision: targetRevision,
	}

	store := work.NewStore(h.db)
	acceptor := &mainHandleAcceptor{db: h.db, budget: work.DefaultAcceptanceBudget}
	var receipt work.Receipt
	err := acceptor.Do(r.Context(), func(ctx context.Context, tx *sql.Tx) error {
		got, err := store.AcceptDeliveryTx(ctx, tx, d, req)
		if err != nil {
			return err
		}
		receipt = got
		return nil
	})
	if err != nil {
		return work.Receipt{}, err
	}
	return receipt, nil
}

// runIDForWork reads the run id a work item was accepted for, so a duplicate
// delivery answers with the ORIGINAL run's id rather than the handle this
// request happened to mint.
func (h *PipelineHandler) runIDForWork(ctx context.Context, workID string) string {
	if workID == "" || h.db == nil {
		return ""
	}
	item, err := work.NewStore(h.db).Get(ctx, workID)
	if err != nil || item == nil {
		return ""
	}
	return item.DomainID
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
