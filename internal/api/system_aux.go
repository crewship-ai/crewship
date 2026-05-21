package api

import (
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/llm"
)

// AuxStatusHandler exposes the resolved auxiliary-model assignment for
// every PR-B F3 slot. Operators consult it to confirm a deployment is
// honouring their cfg.auxiliary.* overrides — the resolver silently
// falls back to cfg.Fallback when a slot is empty, so without a status
// surface a typo in YAML would only show up at the first eval call
// hours later. PRD §6 F3 documents this read-only diagnostic surface
// alongside the slot enum.
//
// Returns one row per slot: name, provider, model, timeout (ms), and
// the source flag ("explicit" when the slot itself was configured,
// "fallback" when ResolveAux backstopped from cfg.Fallback). When the
// router has no aux config wired the handler reports the built-in
// MVP defaults from llm.DefaultAuxiliaryModels so the surface is
// useful even before an operator overrides anything.
type AuxStatusHandler struct {
	cfg    llm.AuxiliaryModels
	logger *slog.Logger
}

// NewAuxStatusHandler builds a handler bound to cfg. Pass the same
// AuxiliaryModels struct the production subsystems read from so the
// status surface can't drift from what the resolvers actually use.
func NewAuxStatusHandler(cfg llm.AuxiliaryModels, logger *slog.Logger) *AuxStatusHandler {
	return &AuxStatusHandler{cfg: cfg, logger: logger}
}

// auxSlotRow is the wire shape returned per slot. TimeoutMS is the
// resolved timeout in milliseconds — chosen over a duration string
// because JSON consumers (web UI, jq) shouldn't have to parse "5s"
// to render a column.
type auxSlotRow struct {
	Slot      string `json:"slot"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	TimeoutMS int64  `json:"timeout_ms"`
	Source    string `json:"source"` // "explicit" | "fallback"
}

// auxStatusResponse wraps the slot rows so the response is an object
// (extensible later with summary fields like "fallback_provider")
// rather than a bare array.
type auxStatusResponse struct {
	Slots []auxSlotRow `json:"slots"`
}

// Status returns the resolved AuxModel for every Slot.
// GET /api/v1/system/aux-status
func (h *AuxStatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	// Auth gate mirrors keeper_status.go — any authenticated user can
	// read the assignment; the values are non-secret diagnostic
	// metadata (provider name + model id + timeout). If we ever store
	// API keys inside AuxModel we MUST narrow this to admin.
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Closed slot list mirrors llm.AuxiliaryModels fields. Adding a
	// new slot requires extending both lists in lockstep — the test
	// matrix in system_aux_test.go locks the ordering so a renamed
	// constant can't drift this surface silently.
	slots := []struct {
		slot     llm.Slot
		raw      llm.AuxModel // the slot's own config, pre-fallback
		fallback llm.AuxModel
	}{
		{llm.SlotCurator, h.cfg.Curator, h.cfg.Fallback},
		{llm.SlotKeeper, h.cfg.Keeper, h.cfg.Fallback},
		{llm.SlotBehavior, h.cfg.Behavior, h.cfg.Fallback},
		{llm.SlotMemoryHealth, h.cfg.MemoryHealth, h.cfg.Fallback},
		{llm.SlotNegative, h.cfg.Negative, h.cfg.Fallback},
	}

	out := auxStatusResponse{Slots: make([]auxSlotRow, 0, len(slots))}
	for _, s := range slots {
		resolved, err := llm.ResolveAux(h.cfg, s.slot)
		if err != nil {
			// A slot with no provider AND no fallback is an operator
			// misconfiguration. Surface the unconfigured row rather
			// than 500ing the whole status call — partial visibility
			// is more useful than none when the operator is trying to
			// diagnose exactly this kind of gap.
			out.Slots = append(out.Slots, auxSlotRow{
				Slot:   string(s.slot),
				Source: "unconfigured",
			})
			continue
		}
		source := "explicit"
		if s.raw.Provider == "" {
			source = "fallback"
		}
		out.Slots = append(out.Slots, auxSlotRow{
			Slot:      string(s.slot),
			Provider:  resolved.Provider,
			Model:     resolved.Model,
			TimeoutMS: resolved.Timeout.Milliseconds(),
			Source:    source,
		})
	}

	writeJSON(w, http.StatusOK, out)
}
