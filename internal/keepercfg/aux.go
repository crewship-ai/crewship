package keepercfg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/llm"
)

// The auxiliary evaluator slots, runtime-settable.
//
// The six slots plus the fallback are the models behind the behavioural watchdog
// and the four Keeper Reviews sweeps. Their provider/model/timeout came from
// llm.DefaultAuxiliaryModels layered with YAML and CREWSHIP_AUX_<SLOT>_* env —
// all boot-time. The admin console could show that five evaluators were pinned to
// anthropic/claude-haiku-4-5 and offered no way to change one.
//
// These are the PAID models in the Keeper stack: the credential-access judge is
// local Ollama and costs nothing per decision, while every evaluator call bills
// per token. So "which model, and am I willing to pay for it" is the decision
// this store exists to hand to the operator — including pointing a slot at the
// local judge and paying nothing.
//
// Same layering as the judge (see keepercfg.go): built-in/env ← instance row,
// resolved per field with provenance, and clearing a field returns it to the
// inherited value rather than to a guess.

// AuxSlots is the display order of the slots an operator can override — the
// order the admin card renders and the CLI lists. Fallback last: it is the
// backstop, not a subsystem.
//
// llm.SlotKeeper is deliberately absent, for the reason the aux-status endpoint
// already excludes it (internal/api/system_aux.go): nothing in the tree calls
// ResolveAux(cfg, SlotKeeper), so offering it would be a knob wired to nothing —
// and worse, it reads like the credential-access judge, which is configured
// separately (keeper_runtime_settings, the judge card). An existing
// CREWSHIP_AUX_KEEPER_* env value keeps working; it just isn't editable here.
var AuxSlots = []string{
	string(llm.SlotCurator),
	string(llm.SlotBehavior),
	string(llm.SlotMemoryHealth),
	string(llm.SlotNegative),
	string(llm.SlotRunSummary),
	auxSlotFallback,
}

// auxSlotFallback is the pseudo-slot llm.ResolveAux falls back to. It is not an
// llm.Slot constant (nothing resolves to it directly), but it is overridable for
// the same reason as the rest.
const auxSlotFallback = "fallback"

// AuxLabels are the human-facing names, matching what the admin card and the
// aux-status endpoint already call these paths.
var AuxLabels = map[string]string{
	string(llm.SlotCurator):      "Skill review + memory consolidation",
	string(llm.SlotBehavior):     "Tool-call behaviour monitor",
	string(llm.SlotMemoryHealth): "Memory-health audit",
	string(llm.SlotNegative):     "Failure → lessons extraction",
	string(llm.SlotRunSummary):   "Run summary verdicts",
	auxSlotFallback:              "Fallback (used when a slot is unset)",
}

// AppliesAt values. Which one a slot gets is not a detail: an operator who
// changes a model and sees no change in behaviour will conclude the feature is
// broken, so the surface has to say which of the two it is.
const (
	// AppliesImmediately — the evaluator picks the override up on its next
	// evaluation, via the per-request gov-model seam.
	AppliesImmediately = "immediately"
	// AppliesOnRestart — the value is captured into an executor at boot, so the
	// override takes effect the next time the server starts.
	AppliesOnRestart = "restart"
)

// auxAppliesAt records when an override for each slot takes effect.
//
// The four Keeper Reviews evaluators are reached through a Gatekeeper, which
// resolves its provider per request — so an override is live. The run-summary
// verdict provider is built once and captured by value into every pipeline
// executor at boot (cmd_start.go), and the fallback slot is only consulted
// during that same boot-time resolution; overriding either needs a restart.
var auxAppliesAt = map[string]string{
	string(llm.SlotCurator):      AppliesImmediately,
	string(llm.SlotBehavior):     AppliesImmediately,
	string(llm.SlotMemoryHealth): AppliesImmediately,
	string(llm.SlotNegative):     AppliesImmediately,
	string(llm.SlotRunSummary):   AppliesOnRestart,
	auxSlotFallback:              AppliesOnRestart,
}

// KnownAuxSlot reports whether s is a slot this store will accept a write for.
func KnownAuxSlot(s string) bool {
	for _, known := range AuxSlots {
		if known == s {
			return true
		}
	}
	return false
}

// auxProviders are the providers an evaluator can actually be BUILT from — the
// llm.BuildAuxProviderAt switch, in lowercase as llm.AuxModel stores it.
//
// Google is absent on purpose: the model catalogue offers Gemini ids, but this
// package has no Provider implementation for them, so accepting one would give
// the operator a slot that saves cleanly and then fails at first use. Rejecting
// it with a reason is the honest surface. "ollama" means the instance judge's
// endpoint, so pointing a slot there costs nothing per call.
func auxProviders() []string {
	return []string{"anthropic", "openai", "ollama"}
}

// AuxProviders is the provider vocabulary a picker may offer, served to the
// console so it cannot hardcode a list that drifts from what the server accepts.
func AuxProviders() []string { return auxProviders() }

// KnownAuxProvider reports whether p is a provider an evaluator can be built
// from.
func KnownAuxProvider(p string) bool {
	for _, known := range auxProviders() {
		if known == p {
			return true
		}
	}
	return false
}

// auxTimeoutMax bounds a per-call deadline. An evaluator that can hang for an
// hour is not a deadline; the behaviour monitor sits behind agent tool calls and
// the sweeps run on a schedule, so minutes is already generous.
const auxTimeoutMax = 10 * time.Minute

// AuxOverride is one stored row. An empty string or nil means "inherit".
type AuxOverride struct {
	Provider  string
	Model     string
	Timeout   *time.Duration
	UpdatedAt string
	UpdatedBy string
}

func (o AuxOverride) empty() bool {
	return o.Provider == "" && o.Model == "" && o.Timeout == nil
}

// AuxEffective is one slot as resolved: the values in force plus where each came
// from, so the console can render "inherited" versus "overridden here" with a
// Reset that has a visible referent.
type AuxEffective struct {
	Slot  string `json:"slot"`
	Label string `json:"label"`
	// AppliesAt is AppliesImmediately or AppliesOnRestart — see auxAppliesAt.
	AppliesAt string        `json:"applies_at"`
	Provider  Field[string] `json:"provider"`
	Model     Field[string] `json:"model"`
	// TimeoutMS is the per-call deadline in milliseconds — the unit the wire and
	// the admin card use; a Go duration string is an operator-facing nicety the
	// CLI applies on top.
	TimeoutMS Field[int64] `json:"timeout_ms"`

	Overridden bool   `json:"overridden"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

// AuxPatch is a partial update for one slot. A nil field is left alone; a
// pointer to the zero value CLEARS the override so the field inherits again.
type AuxPatch struct {
	Provider  *string
	Model     *string
	TimeoutMS *int64
}

func (p AuxPatch) empty() bool {
	return p.Provider == nil && p.Model == nil && p.TimeoutMS == nil
}

// AuxStore is the DB-backed per-slot override, cached in memory. Reads happen on
// every evaluator build, so they must not touch the database.
type AuxStore struct {
	db   *sql.DB
	dflt llm.AuxiliaryModels
	// builtin is llm.DefaultAuxiliaryModels, kept so provenance can tell a value
	// an operator put in the environment apart from the one we shipped. Without
	// it every inherited field would read "env", and the console would claim the
	// operator configured something they never touched.
	builtin llm.AuxiliaryModels

	mu       sync.RWMutex
	cur      map[string]AuxOverride
	onChange []func()
}

// NewAuxStore builds a store over db with dflt (the YAML+env aux config) as the
// inherited layer. Call Load before serving.
func NewAuxStore(db *sql.DB, dflt llm.AuxiliaryModels) *AuxStore {
	return &AuxStore{
		db:      db,
		dflt:    dflt,
		builtin: llm.DefaultAuxiliaryModels(),
		cur:     map[string]AuxOverride{},
	}
}

// Load replaces the in-memory cache from keeper_aux_settings. Rows for slots the
// build no longer knows are ignored, so a stale row cannot resurrect a removed
// evaluator. Same once-at-boot contract as the judge store.
func (s *AuxStore) Load(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT slot, provider, model, timeout_ms, updated_at, COALESCE(updated_by, '')
		  FROM keeper_aux_settings`)
	if err != nil {
		return fmt.Errorf("keepercfg: load aux settings: %w", err)
	}
	defer rows.Close()

	next := map[string]AuxOverride{}
	for rows.Next() {
		var slot, provider, model, updatedAt, updatedBy string
		var timeoutMS sql.NullInt64
		if err := rows.Scan(&slot, &provider, &model, &timeoutMS, &updatedAt, &updatedBy); err != nil {
			return fmt.Errorf("keepercfg: scan aux setting: %w", err)
		}
		if !KnownAuxSlot(slot) {
			continue
		}
		o := AuxOverride{Provider: provider, Model: model, UpdatedAt: updatedAt, UpdatedBy: updatedBy}
		if timeoutMS.Valid {
			d := time.Duration(timeoutMS.Int64) * time.Millisecond
			o.Timeout = &d
		}
		next[slot] = o
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("keepercfg: iterate aux settings: %w", err)
	}

	s.mu.Lock()
	s.cur = next
	s.mu.Unlock()
	return nil
}

// Effective returns every slot resolved, in display order.
func (s *AuxStore) Effective() []AuxEffective {
	out := make([]AuxEffective, 0, len(AuxSlots))
	for _, slot := range AuxSlots {
		out = append(out, s.EffectiveSlot(slot))
	}
	return out
}

// EffectiveSlot resolves one slot. Nil-receiver safe: a process with no store
// reports the built-in defaults rather than panicking.
func (s *AuxStore) EffectiveSlot(slot string) AuxEffective {
	var (
		base    llm.AuxModel
		builtin llm.AuxModel
		o       AuxOverride
	)
	if s != nil {
		base = auxBaseFor(s.dflt, slot)
		builtin = auxBaseFor(s.builtin, slot)
		s.mu.RLock()
		o = s.cur[slot]
		s.mu.RUnlock()
	} else {
		base = auxBaseFor(llm.DefaultAuxiliaryModels(), slot)
		builtin = base
	}

	eff := AuxEffective{
		Slot:       slot,
		Label:      AuxLabels[slot],
		AppliesAt:  auxAppliesAt[slot],
		Overridden: !o.empty(),
		UpdatedAt:  o.UpdatedAt,
		UpdatedBy:  o.UpdatedBy,
	}
	eff.Provider = pickAux(o.Provider, base.Provider, builtin.Provider)
	eff.Model = pickAux(o.Model, base.Model, builtin.Model)

	switch {
	case o.Timeout != nil:
		eff.TimeoutMS = Field[int64]{Value: o.Timeout.Milliseconds(), Source: SourceInstance}
	case base.Timeout != builtin.Timeout:
		eff.TimeoutMS = Field[int64]{Value: base.Timeout.Milliseconds(), Source: SourceEnv}
	default:
		eff.TimeoutMS = Field[int64]{Value: base.Timeout.Milliseconds(), Source: SourceDefault}
	}
	return eff
}

// Resolved returns the aux configuration to build evaluators from: the YAML+env
// defaults with every stored override applied. This is what replaces the
// boot-time llm.AuxiliaryModels value at the call sites.
func (s *AuxStore) Resolved() llm.AuxiliaryModels {
	if s == nil {
		return llm.DefaultAuxiliaryModels()
	}
	s.mu.RLock()
	cur := make(map[string]AuxOverride, len(s.cur))
	for k, v := range s.cur {
		cur[k] = v
	}
	s.mu.RUnlock()

	out := s.dflt
	for slot, o := range cur {
		target := auxTargetFor(&out, slot)
		if target == nil {
			continue
		}
		if o.Provider != "" {
			target.Provider = o.Provider
		}
		if o.Model != "" {
			target.Model = o.Model
		}
		if o.Timeout != nil {
			target.Timeout = *o.Timeout
		}
	}
	return out
}

// Apply validates and persists a partial update for one slot.
func (s *AuxStore) Apply(ctx context.Context, slot string, p AuxPatch, actor string) (AuxEffective, error) {
	if s == nil || s.db == nil {
		return AuxEffective{}, fmt.Errorf("keepercfg: no aux settings store configured")
	}
	if !KnownAuxSlot(slot) {
		return AuxEffective{}, newValidation(fmt.Sprintf("unknown evaluator slot %q — use one of %s",
			slot, strings.Join(AuxSlots, ", ")))
	}
	if p.empty() {
		return s.EffectiveSlot(slot), nil
	}

	s.mu.Lock()
	next := s.cur[slot]
	if p.Provider != nil {
		next.Provider = strings.ToLower(strings.TrimSpace(*p.Provider))
	}
	if p.Model != nil {
		next.Model = strings.TrimSpace(*p.Model)
	}
	if p.TimeoutMS != nil {
		if *p.TimeoutMS == 0 {
			next.Timeout = nil // clear → inherit
		} else {
			d := time.Duration(*p.TimeoutMS) * time.Millisecond
			next.Timeout = &d
		}
	}
	if err := validateAux(next); err != nil {
		s.mu.Unlock()
		return AuxEffective{}, err
	}
	if err := s.persist(ctx, slot, next, actor); err != nil {
		s.mu.Unlock()
		return AuxEffective{}, err
	}
	s.mu.Unlock()

	if err := s.Load(ctx); err != nil {
		return AuxEffective{}, err
	}
	eff := s.EffectiveSlot(slot)
	s.fireOnChange()
	return eff, nil
}

// Reset drops the override for one slot, or every slot when slot is empty.
func (s *AuxStore) Reset(ctx context.Context, slot string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("keepercfg: no aux settings store configured")
	}
	var err error
	if slot == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM keeper_aux_settings`)
	} else {
		if !KnownAuxSlot(slot) {
			return newValidation(fmt.Sprintf("unknown evaluator slot %q", slot))
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM keeper_aux_settings WHERE slot = ?`, slot)
	}
	if err != nil {
		return fmt.Errorf("keepercfg: reset aux settings: %w", err)
	}
	if err := s.Load(ctx); err != nil {
		return err
	}
	s.fireOnChange()
	return nil
}

// UseJudgeForAll points every evaluator slot at the instance judge — one action
// for the decision an operator most often wants to make in one go: stop paying
// per token for the evaluators and run them on the local model that already
// decides credential access.
//
// It writes explicit per-slot overrides rather than a mode flag, so the console
// keeps showing exactly what each slot resolves to, and Reset still means the
// same thing per slot.
func (s *AuxStore) UseJudgeForAll(ctx context.Context, provider, model string, actor string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("keepercfg: no aux settings store configured")
	}
	if strings.TrimSpace(model) == "" {
		return newValidation("the instance judge has no model configured yet — set one before pointing the evaluators at it")
	}
	if provider == "" {
		provider = ProviderOllama
	}
	for _, slot := range AuxSlots {
		p := provider
		m := model
		if _, err := s.Apply(ctx, slot, AuxPatch{Provider: &p, Model: &m}, actor); err != nil {
			return err
		}
	}
	return nil
}

// OnChange registers a callback fired after a committed Apply/Reset. The
// evaluators are rebuilt from the store on their next use, so the callback is
// for logging and cache invalidation rather than for re-plumbing.
func (s *AuxStore) OnChange(fn func()) {
	if s == nil || fn == nil {
		return
	}
	s.mu.Lock()
	s.onChange = append(s.onChange, fn)
	s.mu.Unlock()
}

func (s *AuxStore) fireOnChange() {
	s.mu.RLock()
	cbs := make([]func(), len(s.onChange))
	copy(cbs, s.onChange)
	s.mu.RUnlock()
	for _, fn := range cbs {
		fn()
	}
}

func (s *AuxStore) persist(ctx context.Context, slot string, o AuxOverride, actor string) error {
	// A row whose every field is back to inherit is a deleted row, not a row of
	// empty strings — otherwise `overridden` would stay true forever after the
	// last field was cleared.
	if o.empty() {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM keeper_aux_settings WHERE slot = ?`, slot); err != nil {
			return fmt.Errorf("keepercfg: clear aux slot %s: %w", slot, err)
		}
		return nil
	}
	var timeout any
	if o.Timeout != nil {
		timeout = o.Timeout.Milliseconds()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO keeper_aux_settings (slot, provider, model, timeout_ms, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(slot) DO UPDATE SET
			provider   = excluded.provider,
			model      = excluded.model,
			timeout_ms = excluded.timeout_ms,
			updated_by = excluded.updated_by,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		slot, o.Provider, o.Model, timeout, nullIfEmpty(actor))
	if err != nil {
		return fmt.Errorf("keepercfg: persist aux slot %s: %w", slot, err)
	}
	return nil
}

func validateAux(o AuxOverride) error {
	if o.Provider != "" && !KnownAuxProvider(o.Provider) {
		if o.Provider == "google" || o.Provider == "gemini" {
			return newValidation("google models cannot back an evaluator — this build has no Gemini provider; use anthropic, openai, or ollama")
		}
		return newValidation(fmt.Sprintf("unknown evaluator provider %q — use one of %s",
			o.Provider, strings.Join(auxProviders(), ", ")))
	}
	if o.Model != "" {
		if err := validateModel(o.Model); err != nil {
			return err
		}
	}
	// A provider with no model is the one combination that cannot resolve: the
	// builder needs both, and llm.ResolveAux would fall through to the fallback
	// slot rather than erroring, which looks like the override was ignored.
	if o.Provider != "" && o.Model == "" {
		return newValidation("an evaluator provider needs a model — pick one, or clear the provider to inherit both")
	}
	if o.Timeout != nil {
		switch {
		case *o.Timeout <= 0:
			return newValidation("the evaluator timeout must be positive (clear it to inherit)")
		case *o.Timeout > auxTimeoutMax:
			return newValidation(fmt.Sprintf("the evaluator timeout must be at most %s", auxTimeoutMax))
		}
	}
	return nil
}

// pickAux layers one string field: instance override, else the inherited value —
// attributed to env only when it actually differs from what we shipped.
func pickAux(instance, inherited, builtin string) Field[string] {
	switch {
	case instance != "":
		return Field[string]{Value: instance, Source: SourceInstance}
	case inherited != "" && inherited != builtin:
		return Field[string]{Value: inherited, Source: SourceEnv}
	case inherited != "":
		return Field[string]{Value: inherited, Source: SourceDefault}
	default:
		return Field[string]{Source: SourceDefault}
	}
}

// auxBaseFor reads one slot out of an llm.AuxiliaryModels by name.
func auxBaseFor(cfg llm.AuxiliaryModels, slot string) llm.AuxModel {
	if p := auxTargetFor(&cfg, slot); p != nil {
		return *p
	}
	return llm.AuxModel{}
}

// auxTargetFor maps a slot name onto the corresponding field. The switch is the
// one place the name↔field mapping lives; llm.AuxiliaryModels is a struct rather
// than a map, so a slot added there has to be added here too — KnownAuxSlot is
// driven off llm.Slot, so a missing case shows up as a slot that cannot be
// written rather than as one that writes to the wrong field.
func auxTargetFor(cfg *llm.AuxiliaryModels, slot string) *llm.AuxModel {
	switch slot {
	case string(llm.SlotCurator):
		return &cfg.Curator
	case string(llm.SlotKeeper):
		return &cfg.Keeper
	case string(llm.SlotBehavior):
		return &cfg.Behavior
	case string(llm.SlotMemoryHealth):
		return &cfg.MemoryHealth
	case string(llm.SlotNegative):
		return &cfg.Negative
	case string(llm.SlotRunSummary):
		return &cfg.RunSummary
	case auxSlotFallback:
		return &cfg.Fallback
	default:
		return nil
	}
}
