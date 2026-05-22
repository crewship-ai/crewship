package api

import (
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
// Returns:
//   - 202 with { run_id } on accepted (run starts async)
//   - 401 on HMAC mismatch
//   - 404 on unknown / disabled / deleted token (deliberate to
//     avoid leaking which tokens exist)
//   - 429 on per-token rate limit hit
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

	if !pipeline.AllowWebhookFire(wh.Token, wh.RateLimitPerMin) {
		w.Header().Set("Retry-After", "60")
		replyError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Build pipeline inputs. Default: pass body under "event" so the
	// pipeline can reference {{ inputs.event }}. The
	// inputs_template (if non-empty) is merged on top so a webhook
	// can hand-shape the event into the pipeline's input schema.
	inputs := map[string]any{
		"event":   tryParseJSON(body),
		"raw":     string(body),
		"headers": flattenHeaders(r.Header),
	}
	if strings.TrimSpace(wh.InputsTemplateJSON) != "" && wh.InputsTemplateJSON != "{}" {
		var tmpl map[string]any
		if err := json.Unmarshal([]byte(wh.InputsTemplateJSON), &tmpl); err == nil {
			for k, v := range tmpl {
				inputs[k] = v
			}
		}
	}

	exec := h.newExecutor()
	res, err := exec.Run(r.Context(), pipeline.RunInput{
		PipelineID:  wh.TargetPipelineID,
		WorkspaceID: wh.WorkspaceID,
		Inputs:      inputs,
		Mode:        pipeline.ModeRun,
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
		IdempotencyKey: webhookIdempotencyKey(r, body, wh.Token),
	})
	status := "FAILED"
	runID := ""
	if res != nil {
		runID = res.RunID
		if err == nil && (res.Status == "COMPLETED" || res.Status == "DEDUPED") {
			status = res.Status
		}
	}
	_ = h.webhooks.RecordFire(r.Context(), wh.ID, runID, status)

	if err != nil {
		// Concurrency-rejected webhooks: signal 429 so the sender
		// can retry instead of recording a generic 5xx.
		if errors.Is(err, pipeline.ErrConcurrencyLimitReached) {
			w.Header().Set("Retry-After", "5")
			replyError(w, http.StatusTooManyRequests, "concurrency limit reached")
			return
		}
		h.logger.Warn("webhook fire", "error", err, "webhook_id", wh.ID)
		replyError(w, http.StatusInternalServerError, "pipeline run failed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id":  res.RunID,
		"status":  res.Status,
		"deduped": res.Deduped,
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
