package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/keepercfg"
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
	// aux resolves the auxiliary-model config in force, per request. A
	// captured struct would have made this diagnostic disagree with the admin
	// card the moment an operator overrode a slot (#1556) — the aux settings
	// are runtime-settable, and a status surface that reports the boot-time
	// value is worse than none.
	aux func() llm.AuxiliaryModels
	// keeper is the credential-access judge's config — a DIFFERENT path
	// from the aux slots (server.go builds it straight from cfg.Keeper).
	// It is reported here because it is the judge an operator actually
	// asks about, and reading its model off an aux row was the single
	// most misleading thing this surface did.
	keeper *config.KeeperConfig
	logger *slog.Logger

	// auxStore/creds answer "which vault key does this slot spend" (#1554).
	// Without them this surface builds every provider from the process
	// environment, so a slot deliberately pinned to a vault key on a box with no
	// ANTHROPIC_API_KEY rendered "ANTHROPIC_API_KEY env not set" against an
	// evaluator that works — the same class of lie, pointing the other way, as
	// the one the credential field exists to remove. Both nil (test/older
	// wirings) keeps the exact previous behaviour.
	auxStore *keepercfg.AuxStore
	creds    keepercfg.AuxCredentialLookup

	// Probe results are cached so several open admin consoles do not turn a
	// status read into a poll loop against the model server.
	probeMu  sync.Mutex
	probeAt  time.Time
	probeOK  map[string]bool
	probeErr map[string]string
}

// NewAuxStatusHandler builds a handler over aux. Pass the same accessor the
// production subsystems resolve through (Router.AuxModels) so the status
// surface can't drift from what the resolvers actually use. A nil accessor
// reports the built-in MVP defaults.
func NewAuxStatusHandler(aux func() llm.AuxiliaryModels, keeper *config.KeeperConfig, logger *slog.Logger) *AuxStatusHandler {
	if aux == nil {
		aux = llm.DefaultAuxiliaryModels
	}
	return &AuxStatusHandler{aux: aux, keeper: keeper, logger: logger}
}

// WithCredentials makes the buildability check honour each slot's pinned vault
// key. See AuxStatusHandler.auxStore.
func (h *AuxStatusHandler) WithCredentials(store *keepercfg.AuxStore, lookup keepercfg.AuxCredentialLookup) *AuxStatusHandler {
	h.auxStore = store
	h.creds = lookup
	return h
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
	// Reachable answers a DIFFERENT question from Healthy: not "is this
	// configured and buildable" but "did the model server answer just now".
	// llm.NewOllama never dials, so a box with no Ollama running reported a
	// perfectly healthy judge — that gap is what this closes.
	//
	// nil means "not probed", an honest third state. Only self-hosted
	// providers are dialled: rendering a status card must not spend money on
	// a paid API, and an admin refreshing the page would do exactly that.
	Reachable   *bool  `json:"reachable,omitempty"`
	ReachDetail string `json:"reach_detail,omitempty"`
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
	cfg := h.aux()
	slots := []struct {
		slot  llm.Slot
		label string
		raw   llm.AuxModel // the slot's own config, pre-fallback
	}{
		{llm.SlotCurator, "Skill review + memory consolidation", cfg.Curator},
		{llm.SlotBehavior, "Tool-call behaviour monitor", cfg.Behavior},
		{llm.SlotMemoryHealth, "Memory-health audit", cfg.MemoryHealth},
		{llm.SlotNegative, "Failure → lessons extraction", cfg.Negative},
		{llm.SlotRunSummary, "Run summary verdicts", cfg.RunSummary},
	}

	out := auxStatusResponse{Subsystems: make([]auxSubsystemRow, 0, len(slots)+1)}

	// The credential-access judge first: it is the one operators mean when
	// they ask "is the keeper working?", and it is the one most likely to be
	// silently down (it defaults to a local Ollama that may not be running).
	out.Subsystems = append(out.Subsystems, h.accessJudgeRow(r.Context()))

	for _, s := range slots {
		resolved, err := llm.ResolveAux(cfg, s.slot)
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
		if _, berr := h.buildForStatus(r.Context(), string(s.slot), resolved); berr != nil {
			row.Healthy = false
			row.Detail = berr.Error()
		}
		h.annotateReach(r.Context(), &row, resolved.Provider)
		out.Subsystems = append(out.Subsystems, row)
	}

	writeJSON(w, http.StatusOK, out)
}

// buildForStatus is the row's buildability check, with the slot's pinned vault
// key in force (#1554).
//
// It resolves the key the same way the running evaluator does, so the card
// agrees with what actually happens:
//
//   - no key pinned (or no vault wired) → the historical env-key build.
//   - a key that resolves → build from it, so a slot deliberately moved off the
//     process environment stops being reported as a missing env var.
//   - a key that does NOT resolve → retry the env build, because that is exactly
//     what the runtime degrade does. If the env key covers it the slot really is
//     running and the row is honest to say so; if it does not, the reason
//     reported is the CREDENTIAL's, not "ANTHROPIC_API_KEY env not set", which
//     would send the operator to fix a variable they deliberately stopped using.
//
// No network either way: the same construction-only contract as before.
func (h *AuxStatusHandler) buildForStatus(ctx context.Context, slot string, m llm.AuxModel) (llm.Provider, error) {
	credID := h.auxStore.CredentialFor(slot)
	if h.creds == nil || credID == "" || m.Provider == keepercfg.ProviderOllama {
		return llm.BuildAuxProvider(m)
	}
	key, err := h.creds(ctx, credID)
	if err != nil {
		p, envErr := llm.BuildAuxProvider(m)
		if envErr == nil {
			return p, nil
		}
		return nil, fmt.Errorf("the key pinned to this evaluator is unusable (%v), and there is no server key to fall back on", err)
	}
	return llm.BuildAuxProviderWithKey(m, "", key)
}

// accessJudgeRow describes the credential-access gatekeeper, which server.go
// builds from cfg.Keeper rather than from any aux slot. Separate function
// because the two config paths must not be allowed to blur again.
func (h *AuxStatusHandler) accessJudgeRow(ctx context.Context) auxSubsystemRow {
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
	// Configured and buildable — but is anything listening? This is the
	// check that would have caught dev3, where the judge reported fine
	// while Ollama was not running at all.
	ok, detail := h.probe(ctx, h.keeper.OllamaURL)
	row.Reachable = &ok
	row.ReachDetail = detail
	return row
}

// probeBudget bounds a single reachability dial. A model server that hangs
// must not hang the admin console; 2s is generous for a local process and
// short enough that a page load never feels stuck.
const probeBudget = 2 * time.Second

// probeTTL is how long a dial result is reused. Long enough that several
// open consoles cost one dial, short enough that an operator who just
// started Ollama sees it turn green on the next refresh.
const probeTTL = 30 * time.Second

// selfHostedProviders are the ones worth dialling: local, free, and the
// realistic failure mode (a model server that simply is not running). Paid
// APIs are deliberately absent — see auxSubsystemRow.Reachable.
func isSelfHosted(provider string) bool {
	return strings.EqualFold(provider, "ollama")
}

// reachOllama reports whether an Ollama server answers at base. Any HTTP
// response counts as reachable: a 404 still proves something is listening
// and speaking HTTP, which is the question being asked. Only a transport
// failure or a timeout is "not reachable".
func reachOllama(ctx context.Context, base string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()

	url := strings.TrimSuffix(base, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, "malformed model server url: " + err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "no response from " + base
	}
	_ = resp.Body.Close()
	return true, ""
}

// probe dials base once per probeTTL, keyed by url. Returns (ok, detail).
func (h *AuxStatusHandler) probe(ctx context.Context, base string) (bool, string) {
	h.probeMu.Lock()
	if time.Since(h.probeAt) > probeTTL {
		h.probeOK, h.probeErr, h.probeAt = map[string]bool{}, map[string]string{}, time.Now()
	}
	if ok, seen := h.probeOK[base]; seen {
		detail := h.probeErr[base]
		h.probeMu.Unlock()
		return ok, detail
	}
	h.probeMu.Unlock()

	ok, detail := reachOllama(ctx, base)

	h.probeMu.Lock()
	h.probeOK[base], h.probeErr[base] = ok, detail
	h.probeMu.Unlock()
	return ok, detail
}

// annotateReach fills the reachability fields for a slot row. Self-hosted
// providers get dialled; a paid API is left unprobed with a reason, because
// the alternative is billing the operator for looking at a status page.
func (h *AuxStatusHandler) annotateReach(ctx context.Context, row *auxSubsystemRow, provider string) {
	if !isSelfHosted(provider) {
		// Short, and it names the ACTION rather than the policy. The old wording
		// ("not probed — Crewship does not call a paid API to render a status
		// page") explained our reasoning to someone who had not asked for it, on
		// five rows at once, and left them with nothing to do about it. The Test
		// link beside the row is the thing to do.
		row.ReachDetail = "not checked — press Test to call it once"
		return
	}
	base := os.Getenv("KEEPER_OLLAMA_URL")
	if base == "" {
		// Mirrors llm.BuildAuxProvider's own default, so the row reports the
		// endpoint the evaluator would actually use.
		base = "http://localhost:11434"
	}
	ok, detail := h.probe(ctx, base)
	row.Reachable = &ok
	row.ReachDetail = detail
}
