package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// AdminKeeperConfigHandler is the instance-level Keeper judge configuration
// surface: what the credential-access judge is wired to, and whether Keeper runs
// at all.
//
// Why it exists: those three values (`keeper.enabled`, `keeper.ollama_url`,
// `keeper.model`) used to be boot-time only. The admin console could tell an
// operator the judge was not running and could not offer them a way to fix it,
// and an operator without shell access to the box had none. This endpoint plus
// the lazy judge in internal/server closes that: a change here takes effect on
// the next credential request, with no restart.
//
// Read is ADMIN+, writes are OWNER/ADMIN (the same shape as the rate-limiter
// console); the handler re-checks the role as defence in depth behind those
// route gates. Every write lands in the journal — repointing the thing that
// decides credential access is an operator action worth a durable line.
type AdminKeeperConfigHandler struct {
	store   *keepercfg.Store
	journal journal.Emitter
	logger  *slog.Logger
}

func NewAdminKeeperConfigHandler(store *keepercfg.Store, j journal.Emitter, logger *slog.Logger) *AdminKeeperConfigHandler {
	return &AdminKeeperConfigHandler{store: store, journal: j, logger: logger}
}

// keeperConfigField is one setting as the console needs it: the effective value,
// where it came from, and whether this endpoint can change it. Editable is not
// cosmetic — provider and wire are stored but not yet settable (the instance
// judge speaks native Ollama until the endpoint contract lands), and a UI that
// renders them as inputs would be offering a control that always 400s.
type keeperConfigField[T any] struct {
	Value    T      `json:"value"`
	Source   string `json:"source"`
	Editable bool   `json:"editable"`
}

type keeperConfigResponse struct {
	Enabled     keeperConfigField[bool]   `json:"enabled"`
	Provider    keeperConfigField[string] `json:"judge_provider"`
	EndpointURL keeperConfigField[string] `json:"judge_endpoint_url"`
	Wire        keeperConfigField[string] `json:"judge_wire"`
	Model       keeperConfigField[string] `json:"judge_model"`

	// Overridden is whether anything is set at instance level at all — what a
	// "Reset to inherited" control needs to know before offering itself.
	Overridden bool   `json:"overridden"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`

	// JudgeConfigured is false when the effective endpoint or model is missing —
	// the state in which enabling Keeper would DENY every credential request.
	JudgeConfigured bool `json:"judge_configured"`
}

func keeperConfigPayload(eff keepercfg.Effective) keeperConfigResponse {
	return keeperConfigResponse{
		Enabled:     keeperConfigField[bool]{Value: eff.Enabled.Value, Source: string(eff.Enabled.Source), Editable: true},
		Provider:    keeperConfigField[string]{Value: eff.Provider.Value, Source: string(eff.Provider.Source)},
		EndpointURL: keeperConfigField[string]{Value: redactEndpointUserinfo(eff.EndpointURL.Value), Source: string(eff.EndpointURL.Source), Editable: true},
		Wire:        keeperConfigField[string]{Value: eff.Wire.Value, Source: string(eff.Wire.Source)},
		Model:       keeperConfigField[string]{Value: eff.Model.Value, Source: string(eff.Model.Source), Editable: true},

		Overridden:      eff.Overridden,
		UpdatedAt:       eff.UpdatedAt,
		UpdatedBy:       eff.UpdatedBy,
		JudgeConfigured: eff.JudgeConfigured(),
	}
}

// redactEndpointUserinfo strips any embedded credentials before the endpoint
// goes back to a browser. A value written through this endpoint can never carry
// them (the store refuses), but KEEPER_OLLAMA_URL is not validated by us and an
// operator may well have put a proxy token in it.
func redactEndpointUserinfo(raw string) string {
	if raw == "" || !strings.Contains(raw, "@") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("redacted")
	return u.String()
}

// Get returns the effective judge configuration with per-field provenance.
// GET /api/v1/admin/keeper/config
func (h *AdminKeeperConfigHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.store == nil {
		replyError(w, http.StatusServiceUnavailable, "Keeper configuration is not available")
		return
	}
	writeJSON(w, http.StatusOK, keeperConfigPayload(h.store.Effective()))
}

// keeperConfigRequest is the partial-update body. Pointers distinguish "absent"
// (leave alone) from an explicit empty string (clear the override, inherit
// again) — "" is a meaningful value on this endpoint, not a missing one.
//
// enabled is a RawMessage for the same reason with a third state on top: absent
// leaves it, null returns it to inheriting KEEPER_ENABLED, true/false override.
type keeperConfigRequest struct {
	Enabled     json.RawMessage `json:"enabled"`
	Provider    *string         `json:"judge_provider"`
	EndpointURL *string         `json:"judge_endpoint_url"`
	Wire        *string         `json:"judge_wire"`
	Model       *string         `json:"judge_model"`
}

// Put applies a partial update. PUT /api/v1/admin/keeper/config
func (h *AdminKeeperConfigHandler) Put(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.store == nil {
		replyError(w, http.StatusServiceUnavailable, "Keeper configuration is not available")
		return
	}

	var body keeperConfigRequest
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	patch := keepercfg.Patch{
		Provider:    body.Provider,
		EndpointURL: body.EndpointURL,
		Wire:        body.Wire,
		Model:       body.Model,
	}
	if len(body.Enabled) > 0 {
		tri, ok := triFromJSON(body.Enabled)
		if !ok {
			replyError(w, http.StatusBadRequest, `"enabled" must be true, false, or null (null = inherit the server setting)`)
			return
		}
		patch.Enabled = &tri
	}

	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	eff, err := h.store.Apply(r.Context(), patch, actor)
	if err != nil {
		if keepercfg.IsValidation(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		replyInternalError(w, h.logger, "apply keeper runtime config", err)
		return
	}

	h.audit(r, actor, "keeper instance judge configuration updated", eff)
	writeJSON(w, http.StatusOK, keeperConfigPayload(eff))
}

// Reset drops every instance override so the judge returns to the KEEPER_*
// values the server booted with. DELETE /api/v1/admin/keeper/config
func (h *AdminKeeperConfigHandler) Reset(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.store == nil {
		replyError(w, http.StatusServiceUnavailable, "Keeper configuration is not available")
		return
	}
	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	eff, err := h.store.Reset(r.Context(), actor)
	if err != nil {
		replyInternalError(w, h.logger, "reset keeper runtime config", err)
		return
	}

	h.audit(r, actor, "keeper instance judge configuration reset to the server default", eff)
	writeJSON(w, http.StatusOK, keeperConfigPayload(eff))
}

// triFromJSON maps the three legal JSON forms of `enabled` onto a TriBool.
func triFromJSON(raw json.RawMessage) (keepercfg.TriBool, bool) {
	switch strings.TrimSpace(string(raw)) {
	case "null":
		return keepercfg.TriInherit, true
	case "true":
		return keepercfg.TriOn, true
	case "false":
		return keepercfg.TriOff, true
	default:
		return "", false
	}
}

// audit records who changed the judge, to what. WARN rather than Info: on a
// quieted instance this is still a line an incident review needs, and the value
// being changed is the one that decides credential access.
func (h *AdminKeeperConfigHandler) audit(r *http.Request, actor, summary string, eff keepercfg.Effective) {
	// Redacted here as well as in the journal payload below: an endpoint written
	// through this handler can never carry credentials (the store refuses), but
	// KEEPER_OLLAMA_URL is not validated by us and an inherited value may well
	// hold a proxy token — which would otherwise land in cleartext logs on every
	// config change.
	h.logger.Warn("keeper instance judge configuration changed via admin API",
		"actor", actor, "enabled", eff.Enabled.Value,
		"endpoint", redactEndpointUserinfo(eff.EndpointURL.Value),
		"model", eff.Model.Value, "overridden", eff.Overridden)
	if h.journal == nil {
		return
	}
	if _, err := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: WorkspaceIDFromContext(r.Context()),
		Type:        journal.EntryKeeperDecision,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     actor,
		Summary:     summary,
		Payload: map[string]any{
			// Endpoint is recorded because "where did the judge point at 03:00"
			// is the question this entry exists to answer; userinfo is stripped
			// on the way in by the store's validation and here for env values.
			"enabled":         eff.Enabled.Value,
			"enabled_source":  string(eff.Enabled.Source),
			"endpoint":        redactEndpointUserinfo(eff.EndpointURL.Value),
			"endpoint_source": string(eff.EndpointURL.Source),
			"model":           eff.Model.Value,
			"model_source":    string(eff.Model.Source),
			"overridden":      eff.Overridden,
			"rule":            "keeper_runtime_config",
		},
	}); err != nil {
		h.logger.Warn("keeper config: journal emit failed", "error", err)
	}
}
