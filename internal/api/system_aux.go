package api

import (
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/config"
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
	cfg llm.AuxiliaryModels
	// keeper is the credential-access judge's config — a DIFFERENT path
	// from the aux slots (server.go builds it straight from cfg.Keeper).
	// It is reported here because it is the judge an operator actually
	// asks about, and reading its model off an aux row was the single
	// most misleading thing this surface did.
	keeper *config.KeeperConfig
	logger *slog.Logger
}

// NewAuxStatusHandler builds a handler bound to cfg. Pass the same
// AuxiliaryModels struct the production subsystems read from so the
// status surface can't drift from what the resolvers actually use.
func NewAuxStatusHandler(cfg llm.AuxiliaryModels, keeper *config.KeeperConfig, logger *slog.Logger) *AuxStatusHandler {
	return &AuxStatusHandler{cfg: cfg, keeper: keeper, logger: logger}
}

// auxSlotRow is the wire shape returned per slot. TimeoutMS is the
// resolved timeout in milliseconds — chosen over a duration string
// because JSON consumers (web UI, jq) shouldn't have to parse "5s"
// to render a column.
type auxSubsystemRow struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	Source    string `json:"source"` // "explicit" | "fallback" | "unconfigured" | "keeper_config"
	// Healthy reports whether this judge could actually run: the provider
	// builds, and (for the access judge) it is switched on. Detail carries
	// the reason when it cannot — a red dot with no reason is not
	// actionable, and this surface exists to be acted on.
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail,omitempty"`
}

// auxStatusResponse wraps the slot rows so the response is an object
// (extensible later with summary fields like "fallback_provider")
// rather than a bare array.
type auxStatusResponse struct {
	Subsystems []auxSubsystemRow `json:"subsystems"`
}

// Status returns the resolved AuxModel for every Slot.
// GET /api/v1/system/aux-status
func (h *AuxStatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	// ADMIN+ floor is enforced at the route (authedAdmin, #868): the slot
	// rows expose provider + model id for every evaluator including Keeper —
	// operational metadata a workspace MEMBER should not enumerate (same
	// class as /system/keeper, closed in #893). This nil-check is
	// defence-in-depth; RequireAuth already guarantees a user upstream.
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Only slots something actually consumes. `keeper` is deliberately
	// absent: nothing in the tree calls ResolveAux(cfg, SlotKeeper), so
	// listing it invited an operator to configure a knob wired to nothing —
	// and to read the real credential judge's model off the wrong row.
	// The config field stays (an existing KEEPER= override must not start
	// erroring); it just stops pretending to drive anything.
	slots := []struct {
		slot  llm.Slot
		label string
		raw   llm.AuxModel // the slot's own config, pre-fallback
	}{
		{llm.SlotCurator, "Skill review + memory consolidation", h.cfg.Curator},
		{llm.SlotBehavior, "Tool-call behaviour monitor", h.cfg.Behavior},
		{llm.SlotMemoryHealth, "Memory-health audit", h.cfg.MemoryHealth},
		{llm.SlotNegative, "Failure → lessons extraction", h.cfg.Negative},
		{llm.SlotRunSummary, "Run summary verdicts", h.cfg.RunSummary},
	}

	out := auxStatusResponse{Subsystems: make([]auxSubsystemRow, 0, len(slots)+1)}

	// The credential-access judge first: it is the one operators mean when
	// they ask "is the keeper working?", and it is the one most likely to be
	// silently down (it defaults to a local Ollama that may not be running).
	out.Subsystems = append(out.Subsystems, h.accessJudgeRow())

	for _, s := range slots {
		resolved, err := llm.ResolveAux(h.cfg, s.slot)
		if err != nil {
			// No provider AND no fallback is a misconfiguration. Surface the
			// row rather than 500ing the whole call — partial visibility is
			// more useful than none to someone diagnosing exactly this gap.
			out.Subsystems = append(out.Subsystems, auxSubsystemRow{
				ID: string(s.slot), Label: s.label, Source: "unconfigured",
				Detail: err.Error(),
			})
			continue
		}
		source := "explicit"
		if s.raw.Provider == "" {
			source = "fallback"
		}
		row := auxSubsystemRow{
			ID:        string(s.slot),
			Label:     s.label,
			Provider:  resolved.Provider,
			Model:     resolved.Model,
			TimeoutMS: resolved.Timeout.Milliseconds(),
			Source:    source,
			Healthy:   true,
		}
		// Construction only — no network. This is the exact check the server
		// makes at boot before falling back to the local judge, so a row that
		// reports unhealthy here is a row that fell back there.
		if _, berr := llm.BuildAuxProvider(resolved); berr != nil {
			row.Healthy = false
			row.Detail = berr.Error()
		}
		out.Subsystems = append(out.Subsystems, row)
	}

	writeJSON(w, http.StatusOK, out)
}

// accessJudgeRow describes the credential-access gatekeeper, which server.go
// builds from cfg.Keeper rather than from any aux slot. Separate function
// because the two config paths must not be allowed to blur again.
func (h *AuxStatusHandler) accessJudgeRow() auxSubsystemRow {
	row := auxSubsystemRow{
		ID:     "access_gatekeeper",
		Label:  "Credential access judge",
		Source: "keeper_config",
	}
	if h.keeper == nil {
		row.Detail = "no keeper configuration wired into this build"
		return row
	}
	row.Provider = "ollama"
	row.Model = h.keeper.Model
	if !h.keeper.Enabled {
		// Reported rather than omitted: an absent row reads as "fine", and
		// a disabled access judge is a thing an operator should know.
		row.Detail = "disabled by configuration (keeper.enabled = false)"
		return row
	}
	if h.keeper.Model == "" || h.keeper.OllamaURL == "" {
		row.Detail = "enabled but incompletely configured (missing model or ollama url)"
		return row
	}
	row.Healthy = true
	return row
}
