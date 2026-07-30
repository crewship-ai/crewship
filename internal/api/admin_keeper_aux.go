package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// AdminKeeperAuxHandler is the instance-level evaluator-model surface: which
// model each Keeper Reviews sweep and the behaviour watchdog run on.
//
// Why it exists: the judge endpoint became settable at runtime, but the five
// evaluators behind it stayed pinned to whatever CREWSHIP_AUX_* the process
// booted with. Those are the paid models in the Keeper stack — the credential
// judge is local and free per decision, the evaluators bill per token — so
// "which model, and am I willing to pay for it" was the one Keeper cost decision
// an operator could see on the admin page and not make.
//
// Read is ADMIN+, writes are OWNER/ADMIN, same as the judge config handler, with
// an in-handler role re-check behind the route gate.
type AdminKeeperAuxHandler struct {
	store   *keepercfg.AuxStore
	judge   *keepercfg.Store
	journal journal.Emitter
	logger  *slog.Logger
	// probe runs one real evaluation against a provider/model, delegated to the
	// judge handler so the rate limit, the budget comparison and the stage
	// vocabulary have exactly one implementation. nil → the probe route 503s.
	probe func(w http.ResponseWriter, r *http.Request, provider, model string)
	// creds validates, at write time, that a chosen credential is usable AND
	// belongs to the caller's own workspace (#1554). nil → the credential field
	// 503s rather than being stored unchecked: keeper_aux_settings is
	// instance-global while the vault is workspace-scoped, so this check is the
	// only thing standing between one workspace's admin and another's key.
	creds func(ctx context.Context, workspaceID, credentialID string) error
}

// WithProbe wires the shared judge probe. See AdminKeeperAuxHandler.probe.
func (h *AdminKeeperAuxHandler) WithProbe(fn func(http.ResponseWriter, *http.Request, string, string)) *AdminKeeperAuxHandler {
	h.probe = fn
	return h
}

// WithCredentials wires the write-time credential check. See
// AdminKeeperAuxHandler.creds.
func (h *AdminKeeperAuxHandler) WithCredentials(fn func(context.Context, string, string) error) *AdminKeeperAuxHandler {
	h.creds = fn
	return h
}

// NewAdminKeeperAuxHandler wires the handler. judge is the instance judge store,
// read only by the bulk "use the local judge everywhere" action so it writes the
// endpoint's actual model rather than asking the caller to retype it.
func NewAdminKeeperAuxHandler(store *keepercfg.AuxStore, judge *keepercfg.Store, j journal.Emitter, logger *slog.Logger) *AdminKeeperAuxHandler {
	return &AdminKeeperAuxHandler{store: store, judge: judge, journal: j, logger: logger}
}

// keeperAuxSlotResponse is one slot as the console renders it.
type keeperAuxSlotResponse struct {
	Slot  string `json:"slot"`
	Label string `json:"label"`
	// AppliesAt is "immediately" or "restart". Surfaced because an operator who
	// changes run_summary and sees no change would otherwise conclude the write
	// silently failed: that provider is captured into the pipeline executors at
	// boot, while the four evaluator slots resolve per request.
	AppliesAt string                    `json:"applies_at"`
	Provider  keeperConfigField[string] `json:"provider"`
	Model     keeperConfigField[string] `json:"model"`
	TimeoutMS keeperConfigField[int64]  `json:"timeout_ms"`
	// CredentialID is the vault API_KEY this slot spends (#1554). Empty means the
	// provider reads its key from the server process environment, which is what
	// every slot did before the field existed. Editable only when the server has
	// a credential check wired — a picker that cannot be validated is a picker
	// that would let one workspace bind another's key.
	CredentialID keeperConfigField[string] `json:"credential_id"`

	Overridden bool   `json:"overridden"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

type keeperAuxResponse struct {
	Slots []keeperAuxSlotResponse `json:"slots"`
	// Providers is the vocabulary a picker may offer — the set that can actually
	// be BUILT, which is narrower than the model catalogue (no Gemini provider
	// exists in this build). Serving it beats the console hardcoding a list that
	// drifts from what the server accepts.
	Providers []string `json:"providers"`
	// JudgeModel/JudgeProvider back the "use the local judge for every evaluator"
	// action: empty JudgeModel means the instance has no judge configured yet, so
	// the console can disable the button instead of surfacing a 400.
	JudgeProvider string `json:"judge_provider"`
	JudgeModel    string `json:"judge_model"`
	// AnyOverridden is what a "Reset all" control needs before offering itself.
	AnyOverridden bool `json:"any_overridden"`
}

func auxSlotPayload(eff keepercfg.AuxEffective, credsEditable bool) keeperAuxSlotResponse {
	return keeperAuxSlotResponse{
		Slot:      eff.Slot,
		Label:     eff.Label,
		AppliesAt: eff.AppliesAt,
		Provider:  keeperConfigField[string]{Value: eff.Provider.Value, Source: string(eff.Provider.Source), Editable: true},
		Model:     keeperConfigField[string]{Value: eff.Model.Value, Source: string(eff.Model.Source), Editable: true},
		TimeoutMS: keeperConfigField[int64]{Value: eff.TimeoutMS.Value, Source: string(eff.TimeoutMS.Source), Editable: true},
		CredentialID: keeperConfigField[string]{
			Value: eff.CredentialID.Value, Source: string(eff.CredentialID.Source), Editable: credsEditable,
		},
		Overridden: eff.Overridden,
		UpdatedAt:  eff.UpdatedAt,
		UpdatedBy:  eff.UpdatedBy,
	}
}

func (h *AdminKeeperAuxHandler) payload() keeperAuxResponse {
	effs := h.store.Effective()
	out := keeperAuxResponse{
		Slots:     make([]keeperAuxSlotResponse, 0, len(effs)),
		Providers: keepercfg.AuxProviders(),
	}
	for _, e := range effs {
		out.Slots = append(out.Slots, auxSlotPayload(e, h.creds != nil))
		if e.Overridden {
			out.AnyOverridden = true
		}
	}
	if h.judge != nil {
		jeff := h.judge.Effective()
		out.JudgeProvider = jeff.Provider.Value
		out.JudgeModel = jeff.Model.Value
	}
	return out
}

// guard resolves the common preconditions: role, then a wired store. An unwired
// store is 503 rather than a silent success — a write that goes nowhere is worse
// than a refusal.
func (h *AdminKeeperAuxHandler) guard(w http.ResponseWriter, r *http.Request) bool {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return false
	}
	if h.store == nil {
		replyError(w, http.StatusServiceUnavailable, "Keeper evaluator configuration is not available")
		return false
	}
	return true
}

// Get returns every slot with per-field provenance.
// GET /api/v1/admin/keeper/aux
func (h *AdminKeeperAuxHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, h.payload())
}

// keeperAuxSlotRequest is the partial update for one slot. Pointers separate
// "absent" (leave alone) from an explicit clear: "" for provider/model and 0 for
// timeout_ms return the field to the inherited value.
type keeperAuxSlotRequest struct {
	Provider  *string `json:"provider"`
	Model     *string `json:"model"`
	TimeoutMS *int64  `json:"timeout_ms"`
	// CredentialID pins the vault API_KEY this slot spends; "" clears it and
	// returns the slot to the server's own environment key.
	CredentialID *string `json:"credential_id"`
}

// Put applies a partial update to one slot.
// PUT /api/v1/admin/keeper/aux/{slot}
func (h *AdminKeeperAuxHandler) Put(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w, r) {
		return
	}
	slot := strings.TrimSpace(r.PathValue("slot"))
	if slot == "" {
		replyError(w, http.StatusBadRequest, "Missing evaluator slot")
		return
	}

	var body keeperAuxSlotRequest
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// A chosen credential is validated BEFORE it is stored, against the caller's
	// own workspace. Storing first and finding out at build time would degrade
	// silently — the failure mode this feature exists to remove — and would let an
	// admin bind a key their workspace does not hold.
	if body.CredentialID != nil {
		id := strings.TrimSpace(*body.CredentialID)
		if h.creds == nil && id != "" {
			replyError(w, http.StatusServiceUnavailable,
				"This server cannot verify credentials, so an evaluator key cannot be pinned here")
			return
		}
		if h.creds != nil {
			if err := h.creds(r.Context(), WorkspaceIDFromContext(r.Context()), id); err != nil {
				replyError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		body.CredentialID = &id
	}

	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	eff, err := h.store.Apply(r.Context(), slot, keepercfg.AuxPatch{
		Provider:     body.Provider,
		Model:        body.Model,
		TimeoutMS:    body.TimeoutMS,
		CredentialID: body.CredentialID,
	}, actor)
	if err != nil {
		if keepercfg.IsValidation(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		replyInternalError(w, h.logger, "apply keeper evaluator override", err)
		return
	}

	h.audit(r, actor, "keeper evaluator model updated", []keepercfg.AuxEffective{eff})
	writeJSON(w, http.StatusOK, h.payload())
}

// Reset drops the override for one slot, or for every slot when no slot is given.
// DELETE /api/v1/admin/keeper/aux/{slot}  ·  DELETE /api/v1/admin/keeper/aux
func (h *AdminKeeperAuxHandler) Reset(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w, r) {
		return
	}
	slot := strings.TrimSpace(r.PathValue("slot"))

	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	if err := h.store.Reset(r.Context(), slot); err != nil {
		if keepercfg.IsValidation(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		replyInternalError(w, h.logger, "reset keeper evaluator override", err)
		return
	}

	summary := "keeper evaluator models reset to the server defaults"
	if slot != "" {
		summary = "keeper evaluator model reset to the server default: " + slot
	}
	h.audit(r, actor, summary, h.store.Effective())
	writeJSON(w, http.StatusOK, h.payload())
}

// UseJudge points every evaluator slot at the instance judge.
// POST /api/v1/admin/keeper/aux/use-judge
//
// One action for the decision an operator most often wants whole: stop paying
// per token for the sweeps and run them on the local model that already decides
// credential access. It writes explicit per-slot overrides rather than a mode
// flag, so each row still shows what it resolves to and Reset still means the
// same thing per slot.
func (h *AdminKeeperAuxHandler) UseJudge(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w, r) {
		return
	}
	if h.judge == nil {
		replyError(w, http.StatusServiceUnavailable, "The instance judge configuration is not available")
		return
	}
	jeff := h.judge.Effective()
	if jeff.Model.Value == "" || jeff.EndpointURL.Value == "" {
		replyError(w, http.StatusBadRequest,
			"Set the judge endpoint and model first — the evaluators would have nothing to point at")
		return
	}

	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	if err := h.store.UseJudgeForAll(r.Context(), jeff.Provider.Value, jeff.Model.Value, actor); err != nil {
		if keepercfg.IsValidation(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		replyInternalError(w, h.logger, "point keeper evaluators at the local judge", err)
		return
	}

	h.audit(r, actor, "every keeper evaluator pointed at the local instance judge", h.store.Effective())
	writeJSON(w, http.StatusOK, h.payload())
}

// audit records who repointed which evaluator. WARN, like the judge-config
// handler: on a quieted instance this is still a line an incident review needs,
// because it changes which model reads agent behaviour — and, for a paid
// provider, what the instance spends.
func (h *AdminKeeperAuxHandler) audit(r *http.Request, actor, summary string, effs []keepercfg.AuxEffective) {
	slots := make(map[string]any, len(effs))
	for _, e := range effs {
		slots[e.Slot] = map[string]any{
			"provider":   e.Provider.Value,
			"model":      e.Model.Value,
			"timeout_ms": e.TimeoutMS.Value,
			// The credential ID, not its value: which subscription an evaluator
			// spends is exactly what an incident review needs, and the id is a
			// reference rather than a secret.
			"credential_id": e.CredentialID.Value,
			"source":        string(e.Model.Source),
			"overridden":    e.Overridden,
		}
	}
	h.logger.Warn("keeper evaluator models changed via admin API",
		"actor", actor, "summary", summary, "slots", len(slots))
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
			"slots": slots,
			"rule":  "keeper_aux_config",
		},
	}); err != nil {
		h.logger.Warn("keeper aux config: journal emit failed", "error", err)
	}
}

// Probe runs one real evaluation on a slot's resolved model.
// POST /api/v1/admin/keeper/aux/{slot}/probe
//
// The Judge models card said "not probed — Crewship does not call a paid API to
// render a status page" against every evaluator, which is the right default and a
// dead end: the operator can see that five judges are configured and has no way to
// learn whether any of them works until a sweep runs and fails. The default stays
// (no page render spends money); this is the explicit ask, one slot at a time, on
// a button the operator pressed.
//
// It reuses the judge check's stages so a local and a hosted evaluator are held to
// the same bar, and the same instance-wide probe bucket, because it spends both a
// dial and — for a hosted slot — the operator's money.
func (h *AdminKeeperAuxHandler) Probe(w http.ResponseWriter, r *http.Request) {
	if !h.guard(w, r) {
		return
	}
	if h.probe == nil {
		replyError(w, http.StatusServiceUnavailable, "The judge probe is not available on this server")
		return
	}
	slot := strings.TrimSpace(r.PathValue("slot"))
	if !keepercfg.KnownAuxSlot(slot) {
		replyError(w, http.StatusBadRequest, "Unknown evaluator slot "+slot)
		return
	}

	eff := h.store.EffectiveSlot(slot)
	provider, model := eff.Provider.Value, eff.Model.Value
	if provider == "" || model == "" {
		replyError(w, http.StatusBadRequest, "This slot has no provider or model configured")
		return
	}

	// Delegated whole: the judge handler owns the probe budget, the rate limit and
	// the stage vocabulary, and a second implementation of "does this model
	// answer" is a second thing to keep true.
	h.probe(w, r, provider, model)
}
