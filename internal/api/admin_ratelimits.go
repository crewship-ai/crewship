package api

import (
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

// AdminRateLimitsHandler is the admin "Rate Limiters" console backend: list
// every tunable limiter with its current value, override one at runtime, or
// reset it to the shipped default. Overrides apply instance-wide and take
// effect immediately — the per-IP HTTP buckets are retuned live via the
// router's OnChange hook; the other limiters read their value on next use.
//
// Read is ADMIN+ (authedAdmin); writes are OWNER/ADMIN (authedMut roleManage).
// The handler-level canRole check is defence in depth behind those gates.
type AdminRateLimitsHandler struct {
	store  *ratelimitcfg.Store
	logger *slog.Logger
}

func NewAdminRateLimitsHandler(store *ratelimitcfg.Store, logger *slog.Logger) *AdminRateLimitsHandler {
	return &AdminRateLimitsHandler{store: store, logger: logger}
}

type rateLimitsListResponse struct {
	Limiters []ratelimitcfg.State `json:"limiters"`
}

// List returns every limiter's current state. GET /api/v1/admin/rate-limits.
func (h *AdminRateLimitsHandler) List(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.store == nil {
		replyError(w, http.StatusServiceUnavailable, "Rate limit configuration is not available")
		return
	}
	writeJSON(w, http.StatusOK, rateLimitsListResponse{Limiters: h.store.List()})
}

// Set overrides a limiter. PUT /api/v1/admin/rate-limits/{key} with
// {"value": N}. Unknown key → 404; out-of-range value → 400.
func (h *AdminRateLimitsHandler) Set(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.store == nil {
		replyError(w, http.StatusServiceUnavailable, "Rate limit configuration is not available")
		return
	}
	key := r.PathValue("key")
	if _, ok := ratelimitcfg.Lookup(key); !ok {
		replyError(w, http.StatusNotFound, "Unknown rate limiter")
		return
	}
	var body struct {
		Value int `json:"value"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	if err := h.store.Set(r.Context(), key, body.Value, actor); err != nil {
		// Key existence was checked above, so a validation error here can only
		// be an out-of-range value → 400. Anything else is infrastructure.
		if ratelimitcfg.IsValidation(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		replyInternalError(w, h.logger, "set rate limit override", err)
		return
	}
	// WARN so the change is audited even on a quieted instance — retuning a
	// security-relevant limiter is an operator action worth a durable line.
	h.logger.Warn("rate limit override set via admin API", "key", key, "value", body.Value, "actor", actor)

	state, _ := h.store.StateFor(key)
	writeJSON(w, http.StatusOK, state)
}

// Reset drops a limiter's override so it reverts to the shipped default.
// DELETE /api/v1/admin/rate-limits/{key}.
func (h *AdminRateLimitsHandler) Reset(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.store == nil {
		replyError(w, http.StatusServiceUnavailable, "Rate limit configuration is not available")
		return
	}
	key := r.PathValue("key")
	if _, ok := ratelimitcfg.Lookup(key); !ok {
		replyError(w, http.StatusNotFound, "Unknown rate limiter")
		return
	}
	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	if err := h.store.Reset(r.Context(), key, actor); err != nil {
		replyInternalError(w, h.logger, "reset rate limit override", err)
		return
	}
	h.logger.Warn("rate limit override reset via admin API", "key", key, "actor", actor)

	state, _ := h.store.StateFor(key)
	writeJSON(w, http.StatusOK, state)
}
