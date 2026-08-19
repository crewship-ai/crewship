package keepercfg

import (
	"context"
	"database/sql"
	"errors"
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
	SlotFallback,
}

// SlotFallback is the pseudo-slot llm.ResolveAux falls back to. It is not an
// llm.Slot constant (nothing resolves to it directly), but it is overridable for
// the same reason as the rest — and exported because the evaluator wiring has to
// name it to keep an override live (internal/server/keeper_aux_live.go).
const SlotFallback = "fallback"

// AuxLabels are the human-facing names, matching what the admin card and the
// aux-status endpoint already call these paths.
var AuxLabels = map[string]string{
	string(llm.SlotCurator):      "Skill review + memory consolidation",
	string(llm.SlotBehavior):     "Tool-call behaviour monitor",
	string(llm.SlotMemoryHealth): "Memory-health audit",
	string(llm.SlotNegative):     "Failure → lessons extraction",
	string(llm.SlotRunSummary):   "Run summary verdicts",
	SlotFallback:                 "Fallback (used when a slot is unset)",
}

// Every slot applies on the next evaluation. There used to be an applies_at
// field here, and a map marking run_summary and fallback as taking effect only
// after a server restart — their provider was built once and captured by value
// into every pipeline executor at boot. Both now resolve from this store at use
// time, the way the four Keeper Reviews slots always did (#1556), so the label
// was deleted rather than kept describing a limitation that no longer exists.

// KnownAuxSlot reports whether s is a slot this store will accept a write for.
func KnownAuxSlot(s string) bool {
	for _, known := range AuxSlots {
		if known == s {
			return true
		}
	}
	return false
}

// auxProviders are the providers an evaluator can actually be BUILT from, read
// straight off the llm provider registry that llm.BuildAuxProviderAt resolves
// against — in lowercase as llm.AuxModel stores it, and in the registry's
// declaration order, which is the order this list has always been in and the
// order the console's picker renders.
//
// It used to be a literal here, which meant the validator and the builder could
// disagree: a provider added to one was not added to the other, and the
// operator found out at first use. Now there is one table.
//
// Google is absent on purpose: the model catalogue offers Gemini ids, but the
// llm package has no Provider implementation for them, so accepting one would
// give the operator a slot that saves cleanly and then fails at first use.
// Rejecting it with a reason is the honest surface. "ollama" means the instance
// judge's endpoint, so pointing a slot there costs nothing per call.
func auxProviders() []string { return llm.RegisteredProviders() }

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

// maxAuxCredentialIDLen bounds a stored credential id. Vault ids are CUIDs
// (~25 characters); the ceiling is here so a hand-edited row cannot put an
// unbounded string into a log line or an error message.
const maxAuxCredentialIDLen = 200

// AuxOverride is one stored row. An empty string or nil means "inherit".
type AuxOverride struct {
	Provider string
	Model    string
	Timeout  *time.Duration
	// CredentialID names the vault API_KEY this slot's provider dials with
	// (#1554). Empty is the pre-existing behaviour: the builder reads the key
	// from the process environment.
	//
	// Revoke-safety, mirroring governance.Settings.GovModelCredentialID: a revoke
	// is a SOFT delete, so the column's ON DELETE SET NULL does not fire and the
	// id stays here. The resolver looks the credential up per build and degrades
	// a missing/revoked/wrong-typed one back to the env key with a WARN rather
	// than dialling with a stale id.
	CredentialID string
	UpdatedAt    string
	UpdatedBy    string
}

func (o AuxOverride) empty() bool {
	return o.Provider == "" && o.Model == "" && o.Timeout == nil && o.CredentialID == ""
}

// AuxEffective is one slot as resolved: the values in force plus where each came
// from, so the console can render "inherited" versus "overridden here" with a
// Reset that has a visible referent.
type AuxEffective struct {
	Slot     string        `json:"slot"`
	Label    string        `json:"label"`
	Provider Field[string] `json:"provider"`
	Model    Field[string] `json:"model"`
	// TimeoutMS is the per-call deadline in milliseconds — the unit the wire and
	// the admin card use; a Go duration string is an operator-facing nicety the
	// CLI applies on top.
	TimeoutMS Field[int64] `json:"timeout_ms"`
	// CredentialID is the vault key this slot spends. It has only two sources —
	// set here, or nothing — because there is no env layer to inherit from: the
	// pre-#1554 behaviour is not "a credential named in the environment", it is
	// "no credential at all, the provider reads the raw key from the process
	// env". So an empty value here means exactly that, and Source is default.
	CredentialID Field[string] `json:"credential_id"`

	Overridden bool   `json:"overridden"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

// AuxPatch is a partial update for one slot. A nil field is left alone; a
// pointer to the zero value CLEARS the override so the field inherits again.
type AuxPatch struct {
	Provider     *string
	Model        *string
	TimeoutMS    *int64
	CredentialID *string
}

func (p AuxPatch) empty() bool {
	return p.Provider == nil && p.Model == nil && p.TimeoutMS == nil && p.CredentialID == nil
}

// AuxCredentialLookup turns a slot's stored credential id into the API key its
// provider dials with. It is the seam the evaluator builders use to reach the
// vault without this package (or internal/server) importing the credentials
// layer; the concrete implementation lives in internal/api, and tests pass a
// stub.
//
// A missing / revoked / soft-deleted / undecryptable / wrong-typed credential
// returns a non-nil error, which callers treat the way ResolveGovModel treats
// its own lookup failure (§4.4): degrade — here, back to the process-env key —
// with a WARN, never a hard failure and never a dial with a stale id.
type AuxCredentialLookup func(ctx context.Context, credentialID string) (apiKey string, err error)

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
	rows, err := s.db.QueryContext(ctx, auxSelectAll)
	if err != nil {
		return fmt.Errorf("keepercfg: load aux settings: %w", err)
	}
	next, err := scanAuxRows(rows)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.cur = next
	s.mu.Unlock()
	return nil
}

// auxSelectAll is shared by the boot-time load and the transactional refresh, so
// the two can never drift into reading different columns.
const auxSelectAll = `
	SELECT slot, provider, model, timeout_ms, COALESCE(credential_id, ''), updated_at, COALESCE(updated_by, '')
	  FROM keeper_aux_settings`

// scanAuxRows builds the cache map from a query over auxSelectAll and closes
// rows. Rows for slots the build no longer knows are dropped here.
func scanAuxRows(rows *sql.Rows) (map[string]AuxOverride, error) {
	defer rows.Close()

	next := map[string]AuxOverride{}
	for rows.Next() {
		var slot, provider, model, credentialID, updatedAt, updatedBy string
		var timeoutMS sql.NullInt64
		if err := rows.Scan(&slot, &provider, &model, &timeoutMS, &credentialID, &updatedAt, &updatedBy); err != nil {
			return nil, fmt.Errorf("keepercfg: scan aux setting: %w", err)
		}
		if !KnownAuxSlot(slot) {
			continue
		}
		o := AuxOverride{
			Provider: provider, Model: model, CredentialID: credentialID,
			UpdatedAt: updatedAt, UpdatedBy: updatedBy,
		}
		if timeoutMS.Valid {
			d := time.Duration(timeoutMS.Int64) * time.Millisecond
			o.Timeout = &d
		}
		next[slot] = o
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("keepercfg: iterate aux settings: %w", err)
	}
	return next, nil
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
	if s == nil {
		return resolveAuxSlot(slot, llm.DefaultAuxiliaryModels(), llm.DefaultAuxiliaryModels(), AuxOverride{})
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveSlotLocked(slot)
}

// effectiveSlotLocked is EffectiveSlot without taking the mutex, for the write
// path — which already holds it, and would deadlock re-entering.
func (s *AuxStore) effectiveSlotLocked(slot string) AuxEffective {
	return resolveAuxSlot(slot, s.dflt, s.builtin, s.cur[slot])
}

// resolveAuxSlot layers one slot: instance override over the configured value
// over what we shipped, with the provenance that distinguishes them.
func resolveAuxSlot(slot string, dflt, builtin llm.AuxiliaryModels, o AuxOverride) AuxEffective {
	base := auxBaseFor(dflt, slot)
	shipped := auxBaseFor(builtin, slot)

	eff := AuxEffective{
		Slot:       slot,
		Label:      AuxLabels[slot],
		Overridden: !o.empty(),
		UpdatedAt:  o.UpdatedAt,
		UpdatedBy:  o.UpdatedBy,
	}
	eff.Provider = pickAux(o.Provider, base.Provider, shipped.Provider)
	eff.Model = pickAux(o.Model, base.Model, shipped.Model)
	// No env layer to inherit from — see AuxEffective.CredentialID.
	if o.CredentialID != "" {
		eff.CredentialID = Field[string]{Value: o.CredentialID, Source: SourceInstance}
	} else {
		eff.CredentialID = Field[string]{Source: SourceDefault}
	}

	switch {
	case o.Timeout != nil:
		eff.TimeoutMS = Field[int64]{Value: o.Timeout.Milliseconds(), Source: SourceInstance}
	case base.Timeout != shipped.Timeout:
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

	// Read-modify-write inside ONE transaction, against the DATABASE rather than
	// the in-memory cache.
	//
	// The cache used to be the base, and it is only refreshed after the lock is
	// released. Two Apply calls patching DIFFERENT fields of the same slot could
	// therefore both read the pre-first-write cache, and the second would persist
	// the first's field at its old value — the row itself losing a committed
	// change, not merely a stale read.
	//
	// A transaction rather than only moving the cache write inside the lock: the
	// mutex serialises goroutines in THIS process, and the same interleaving
	// across two connections would still lose the update.
	s.mu.Lock()
	eff, err := s.applyLocked(ctx, slot, p, actor)
	s.mu.Unlock()
	if err != nil {
		return AuxEffective{}, err
	}
	// Callbacks after the lock is dropped: they are free to read the store, and
	// firing them under the write lock would deadlock the first one that did.
	s.fireOnChange()
	return eff, nil
}

// applyLocked does the transactional read-modify-write. Caller holds s.mu.
func (s *AuxStore) applyLocked(ctx context.Context, slot string, p AuxPatch, actor string) (AuxEffective, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AuxEffective{}, fmt.Errorf("keepercfg: begin aux update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	next, err := readAuxSlotTx(ctx, tx, slot)
	if err != nil {
		return AuxEffective{}, err
	}
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
	if p.CredentialID != nil {
		next.CredentialID = strings.TrimSpace(*p.CredentialID)
	}
	if err := validateAux(next); err != nil {
		return AuxEffective{}, err
	}
	if err := persistAuxTx(ctx, tx, slot, next, actor); err != nil {
		return AuxEffective{}, err
	}
	// Read back inside the same transaction, so updated_at/updated_by are the
	// values the database wrote rather than a guess about its defaults.
	saved, err := readAuxSlotTx(ctx, tx, slot)
	if err != nil {
		return AuxEffective{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuxEffective{}, fmt.Errorf("keepercfg: commit aux update: %w", err)
	}

	// Cached under the SAME lock as the write, so the next Apply cannot start
	// from a value this one has already superseded.
	if saved.empty() {
		delete(s.cur, slot)
	} else {
		s.cur[slot] = saved
	}
	return s.effectiveSlotLocked(slot), nil
}

// readAuxSlotTx reads one slot's stored override inside a transaction. A missing
// row is the zero AuxOverride — "inherits everything" — not an error.
func readAuxSlotTx(ctx context.Context, tx *sql.Tx, slot string) (AuxOverride, error) {
	var (
		o         AuxOverride
		timeoutMS sql.NullInt64
		credID    sql.NullString
		updatedBy sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
		SELECT provider, model, timeout_ms, credential_id, updated_at, updated_by
		  FROM keeper_aux_settings WHERE slot = ?`, slot).
		Scan(&o.Provider, &o.Model, &timeoutMS, &credID, &o.UpdatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return AuxOverride{}, nil
	}
	if err != nil {
		return AuxOverride{}, fmt.Errorf("keepercfg: read aux slot %s: %w", slot, err)
	}
	if timeoutMS.Valid {
		d := time.Duration(timeoutMS.Int64) * time.Millisecond
		o.Timeout = &d
	}
	o.CredentialID = credID.String
	o.UpdatedBy = updatedBy.String
	return o, nil
}

// Reset drops the override for one slot, or every slot when slot is empty.
//
// It takes the same lock Apply does, for the same reason. Reset used to delete
// and then reload outside it: a concurrent Apply could commit its override and
// update the cache in the gap, and this reload — already stale by then — would
// put the pre-apply state back. The row survived in the database while the
// process resolved the slot as inherited, so a paid evaluator quietly ran on a
// different model until the next reload.
func (s *AuxStore) Reset(ctx context.Context, slot string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("keepercfg: no aux settings store configured")
	}
	if slot != "" && !KnownAuxSlot(slot) {
		return newValidation(fmt.Sprintf("unknown evaluator slot %q", slot))
	}

	s.mu.Lock()
	err := s.resetLocked(ctx, slot)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	// Callbacks after the lock is dropped — same contract as Apply.
	s.fireOnChange()
	return nil
}

// resetLocked deletes and re-reads inside ONE transaction, so the cache is
// refreshed from the same database state the delete left behind. Caller holds
// s.mu.
func (s *AuxStore) resetLocked(ctx context.Context, slot string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("keepercfg: begin aux reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if slot == "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM keeper_aux_settings`)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM keeper_aux_settings WHERE slot = ?`, slot)
	}
	if err != nil {
		return fmt.Errorf("keepercfg: reset aux settings: %w", err)
	}

	rows, err := tx.QueryContext(ctx, auxSelectAll)
	if err != nil {
		return fmt.Errorf("keepercfg: reload aux settings: %w", err)
	}
	next, err := scanAuxRows(rows)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("keepercfg: commit aux reset: %w", err)
	}
	s.cur = next
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

// persistAuxTx writes one slot inside a transaction.
//
// A row whose every field is back to inherit is a DELETED row, not a row of
// empty strings — otherwise `overridden` would stay true forever after the last
// field was cleared.
func persistAuxTx(ctx context.Context, tx *sql.Tx, slot string, o AuxOverride, actor string) error {
	if o.empty() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM keeper_aux_settings WHERE slot = ?`, slot); err != nil {
			return fmt.Errorf("keepercfg: clear aux slot %s: %w", slot, err)
		}
		return nil
	}
	var timeout any
	if o.Timeout != nil {
		timeout = o.Timeout.Milliseconds()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO keeper_aux_settings (slot, provider, model, timeout_ms, credential_id, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(slot) DO UPDATE SET
			provider      = excluded.provider,
			model         = excluded.model,
			timeout_ms    = excluded.timeout_ms,
			credential_id = excluded.credential_id,
			updated_by    = excluded.updated_by,
			updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		slot, o.Provider, o.Model, timeout, nullIfEmpty(o.CredentialID), nullIfEmpty(actor))
	if err != nil {
		return fmt.Errorf("keepercfg: persist aux slot %s: %w", slot, err)
	}
	return nil
}

func validateAux(o AuxOverride) error {
	if o.Provider != "" && !KnownAuxProvider(o.Provider) {
		if o.Provider == "google" || o.Provider == "gemini" {
			return newValidation(fmt.Sprintf(
				"google models cannot back an evaluator — this build has no Gemini provider; use one of %s",
				strings.Join(auxProviders(), ", ")))
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
	// Shape only. Whether the id names a credential that EXISTS, is active, is an
	// API_KEY and belongs to the caller's workspace is checked by the admin
	// handler, which has the vault; this package deliberately has no notion of a
	// workspace and must not grow one to store a string.
	if o.CredentialID != "" {
		if len(o.CredentialID) > maxAuxCredentialIDLen {
			return newValidation(fmt.Sprintf("the credential id is too long (%d characters, limit %d)",
				len(o.CredentialID), maxAuxCredentialIDLen))
		}
		for _, r := range o.CredentialID {
			if r < 0x20 || r == 0x7f {
				return newValidation("the credential id contains a control character")
			}
		}
	}
	return nil
}

// CredentialFor is the vault key a slot's evaluator should be built with, or ""
// for "read it from the process environment" — the pre-#1554 behaviour, and
// what an untouched instance resolves to.
//
// It mirrors llm.ResolveAux's fall-through rather than reading the slot's row
// blindly: a slot with no provider of its own resolves its MODEL through the
// fallback slot, so it has to spend the fallback's key too. A slot that does
// have a provider keeps its own credential (possibly empty) — borrowing the
// fallback's key there would hand an anthropic key to an openai endpoint.
//
// Nil-receiver safe: a process with no store names no credential.
func (s *AuxStore) CredentialFor(slot string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	eff := s.effectiveSlotLocked(slot)
	if eff.CredentialID.Value != "" || eff.Provider.Value != "" || slot == SlotFallback {
		return eff.CredentialID.Value
	}
	return s.effectiveSlotLocked(SlotFallback).CredentialID.Value
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
	case SlotFallback:
		return &cfg.Fallback
	default:
		return nil
	}
}
