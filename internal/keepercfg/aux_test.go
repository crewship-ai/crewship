package keepercfg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/llm"
	_ "modernc.org/sqlite"
)

// The aux slots are the PAID models in the Keeper stack, so the properties worth
// pinning are the ones that decide what gets billed: an untouched instance bills
// exactly as before, an override actually reaches the resolved config the
// evaluators are built from, and clearing one returns the slot to the inherited
// value rather than to a guess.

// auxTableDDL mirrors internal/database/migrations/20260730111147_keeper_aux_settings.sql
// plus 20260730205811_keeper_aux_credential.sql.
const auxTableDDL = `
CREATE TABLE users (id TEXT PRIMARY KEY);
CREATE TABLE credentials (id TEXT PRIMARY KEY);
CREATE TABLE keeper_aux_settings (
    slot          TEXT PRIMARY KEY,
    provider      TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    timeout_ms    INTEGER CHECK (timeout_ms IS NULL OR timeout_ms > 0),
    updated_by    TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    credential_id TEXT REFERENCES credentials(id) ON DELETE SET NULL
);`

func newAuxStore(t *testing.T) *AuxStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// One connection: a bare :memory: DSN gives each pooled connection its own
	// empty database, so an Apply → Load could land where the table is absent.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(auxTableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	// DefaultAuxiliaryModels, not LoadAuxiliaryModels: the latter reads
	// CREWSHIP_AUX_* from the environment, which would make these assertions
	// depend on the developer's shell.
	s := NewAuxStore(db, llm.DefaultAuxiliaryModels())
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

// newAuxStoreMultiConn is newAuxStore over a FILE database with the connection
// pool left open. The :memory: helper has to pin the pool to one connection
// (each pooled connection would otherwise get its own empty database), which
// also serialises everything the store does — so it cannot exercise two
// connections racing, the very case the transactional write path exists for.
func newAuxStoreMultiConn(t *testing.T) *AuxStore {
	t.Helper()
	// The pragmas that matter here are the ones database.Open sets in production:
	// WAL so a reader and a writer can hold the database at once, a busy timeout
	// so a contended write waits instead of returning SQLITE_BUSY, and
	// _txlock=immediate so BeginTx takes the write lock up front. Without the
	// last one a read-modify-write transaction starts on a read snapshot and
	// fails with "database is locked (517)" when it upgrades — a failure mode the
	// real database does not have, which would make this test lie about it.
	dsn := fmt.Sprintf("file:%s/aux.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=immediate", t.TempDir())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(auxTableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	s := NewAuxStore(db, llm.DefaultAuxiliaryModels())
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

func slotOf(t *testing.T, list []AuxEffective, slot string) AuxEffective {
	t.Helper()
	for _, e := range list {
		if e.Slot == slot {
			return e
		}
	}
	t.Fatalf("slot %q missing from %d entries", slot, len(list))
	return AuxEffective{}
}

// An instance nobody has configured must bill exactly as it did before this
// table existed.
func TestAux_UntouchedInheritsTheShippedDefaults(t *testing.T) {
	s := newAuxStore(t)
	eff := s.Effective()

	if len(eff) != len(AuxSlots) {
		t.Fatalf("got %d slots, want %d", len(eff), len(AuxSlots))
	}
	behavior := slotOf(t, eff, string(llm.SlotBehavior))
	// default, not env: nothing was configured, and telling the operator "env"
	// would claim they set something they never touched.
	if behavior.Provider.Value != "anthropic" || behavior.Provider.Source != SourceDefault {
		t.Errorf("provider = %q/%s, want anthropic/default", behavior.Provider.Value, behavior.Provider.Source)
	}
	if behavior.TimeoutMS.Source != SourceDefault {
		t.Errorf("timeout source = %s, want default", behavior.TimeoutMS.Source)
	}
	if behavior.Model.Value != "claude-haiku-4-5" {
		t.Errorf("model = %q, want the shipped default", behavior.Model.Value)
	}
	if behavior.Overridden {
		t.Error("Overridden = true with nothing stored")
	}
	if behavior.Label == "" {
		t.Error("slot has no human-facing label")
	}
	// And the config the evaluators are actually built from is untouched.
	if got := s.Resolved().Behavior.Model; got != "claude-haiku-4-5" {
		t.Errorf("resolved model = %q", got)
	}
}

// The point of the feature: an override reaches the config the evaluators build
// from, not just the display.
func TestAux_OverrideReachesTheResolvedConfig(t *testing.T) {
	s := newAuxStore(t)
	provider, model := "anthropic", "claude-opus-5"

	eff, err := s.Apply(context.Background(), string(llm.SlotCurator),
		AuxPatch{Provider: &provider, Model: &model}, "u")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if eff.Model.Value != "claude-opus-5" || eff.Model.Source != SourceInstance {
		t.Errorf("model = %q/%s, want claude-opus-5/instance", eff.Model.Value, eff.Model.Source)
	}
	if !eff.Overridden {
		t.Error("Overridden = false right after an override")
	}
	resolved := s.Resolved()
	if resolved.Curator.Model != "claude-opus-5" {
		t.Errorf("resolved curator model = %q", resolved.Curator.Model)
	}
	// Untouched slots keep the shipped default — a per-slot override is per slot.
	if resolved.Behavior.Model != "claude-haiku-4-5" {
		t.Errorf("an unrelated slot changed: %q", resolved.Behavior.Model)
	}
	// The timeout was not part of the patch, so it still inherits.
	if eff.TimeoutMS.Source == SourceInstance {
		t.Errorf("timeout source = %s, want an inherited source (untouched)", eff.TimeoutMS.Source)
	}
}

func TestAux_TimeoutOverrideAndClear(t *testing.T) {
	s := newAuxStore(t)
	ctx := context.Background()
	provider, model := "anthropic", "claude-haiku-4-5"
	twenty := int64(20000)

	eff, err := s.Apply(ctx, string(llm.SlotMemoryHealth),
		AuxPatch{Provider: &provider, Model: &model, TimeoutMS: &twenty}, "u")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if eff.TimeoutMS.Value != 20000 || eff.TimeoutMS.Source != SourceInstance {
		t.Errorf("timeout = %d/%s", eff.TimeoutMS.Value, eff.TimeoutMS.Source)
	}
	if got := s.Resolved().MemoryHealth.Timeout; got != 20*time.Second {
		t.Errorf("resolved timeout = %s, want 20s", got)
	}

	// 0 is the documented clear — and it must go back to the inherited deadline,
	// not to "no deadline".
	zero := int64(0)
	eff, err = s.Apply(ctx, string(llm.SlotMemoryHealth), AuxPatch{TimeoutMS: &zero}, "u")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if eff.TimeoutMS.Source == SourceInstance {
		t.Errorf("after clear source = %s, want an inherited source", eff.TimeoutMS.Source)
	}
	if got := s.Resolved().MemoryHealth.Timeout; got == 0 {
		t.Error("clearing the timeout left the slot with no deadline")
	}
}

// Clearing every field is a deleted row, not a row of empty strings — otherwise
// the slot reads as overridden forever.
func TestAux_ClearingEveryFieldDropsTheOverride(t *testing.T) {
	s := newAuxStore(t)
	ctx := context.Background()
	provider, model := "anthropic", "claude-opus-5"
	if _, err := s.Apply(ctx, string(llm.SlotNegative), AuxPatch{Provider: &provider, Model: &model}, "u"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	empty := ""
	eff, err := s.Apply(ctx, string(llm.SlotNegative), AuxPatch{Provider: &empty, Model: &empty}, "u")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if eff.Overridden {
		t.Error("Overridden = true after clearing every field")
	}
	if eff.Model.Source == SourceInstance {
		t.Errorf("model source = %s, want the inherited value back", eff.Model.Source)
	}
	if eff.Model.Value != "claude-haiku-4-5" {
		t.Errorf("model = %q, want the inherited value back", eff.Model.Value)
	}
}

// The one-click cost decision: every evaluator onto the local judge.
func TestAux_UseJudgeForAll(t *testing.T) {
	s := newAuxStore(t)
	if err := s.UseJudgeForAll(context.Background(), ProviderOllama, "qwen2.5:7b", "u"); err != nil {
		t.Fatalf("use judge for all: %v", err)
	}

	for _, e := range s.Effective() {
		if e.Provider.Value != "ollama" || e.Model.Value != "qwen2.5:7b" {
			t.Errorf("slot %s = %s/%s, want ollama/qwen2.5:7b", e.Slot, e.Provider.Value, e.Model.Value)
		}
		if !e.Overridden {
			t.Errorf("slot %s not marked overridden", e.Slot)
		}
	}
	// Explicit per-slot rows, not a mode flag — so Reset still works per slot and
	// the console keeps showing what each slot resolves to.
	resolved := s.Resolved()
	if resolved.Curator.Model != "qwen2.5:7b" || resolved.RunSummary.Model != "qwen2.5:7b" {
		t.Errorf("resolved config did not follow: %+v", resolved)
	}
}

// Pointing the evaluators at a judge that has no model would write a provider
// with no model — the one combination that cannot resolve.
func TestAux_UseJudgeForAllRefusesWithoutAModel(t *testing.T) {
	s := newAuxStore(t)
	if err := s.UseJudgeForAll(context.Background(), ProviderOllama, "  ", "u"); err == nil {
		t.Fatal("accepted an empty judge model")
	} else if !IsValidation(err) {
		t.Errorf("IsValidation = false for %v", err)
	}
}

func TestAux_ResetOneAndAll(t *testing.T) {
	s := newAuxStore(t)
	ctx := context.Background()
	if err := s.UseJudgeForAll(ctx, ProviderOllama, "qwen2.5:7b", "u"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.Reset(ctx, string(llm.SlotBehavior)); err != nil {
		t.Fatalf("reset one: %v", err)
	}
	if slotOf(t, s.Effective(), string(llm.SlotBehavior)).Overridden {
		t.Error("the reset slot is still overridden")
	}
	if !slotOf(t, s.Effective(), string(llm.SlotCurator)).Overridden {
		t.Error("resetting one slot cleared another")
	}

	if err := s.Reset(ctx, ""); err != nil {
		t.Fatalf("reset all: %v", err)
	}
	for _, e := range s.Effective() {
		if e.Overridden {
			t.Errorf("slot %s still overridden after a full reset", e.Slot)
		}
	}
}

func TestAux_Rejects(t *testing.T) {
	model := "claude-opus-5"
	provider := "anthropic"
	huge := int64((30 * time.Minute).Milliseconds())
	negative := int64(-1)

	for _, tc := range []struct {
		name  string
		slot  string
		patch AuxPatch
		want  string
	}{
		{"unknown slot", "not_a_slot", AuxPatch{Model: &model}, "unknown evaluator slot"},
		{"unknown provider", string(llm.SlotCurator), AuxPatch{Provider: strPtrAux("bedrock"), Model: &model}, "unknown evaluator provider"},
		// The catalogue offers Gemini ids but this build cannot construct one, so
		// the rejection has to say why rather than read as a typo.
		{"google has no provider", string(llm.SlotCurator), AuxPatch{Provider: strPtrAux("google"), Model: strPtrAux("gemini-2.0-flash")}, "no Gemini provider"},
		{"provider without a model", string(llm.SlotCurator), AuxPatch{Provider: &provider}, "needs a model"},
		{"timeout over the cap", string(llm.SlotCurator), AuxPatch{TimeoutMS: &huge}, "at most"},
		{"negative timeout", string(llm.SlotCurator), AuxPatch{TimeoutMS: &negative}, "positive"},
		{"model with a control character", string(llm.SlotCurator), AuxPatch{Model: strPtrAux("claude\nopus")}, "control character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAuxStore(t)
			_, err := s.Apply(context.Background(), tc.slot, tc.patch, "u")
			if err == nil {
				t.Fatal("accepted an invalid patch")
			}
			if !IsValidation(err) {
				t.Errorf("IsValidation = false for %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q does not mention %q", err.Error(), tc.want)
			}
			// A rejected patch must leave the slot inheriting.
			if slotOf(t, s.Effective(), string(llm.SlotCurator)).Overridden {
				t.Error("a rejected patch still wrote an override")
			}
		})
	}
}

// A row for a slot the build no longer knows must not resurrect an evaluator.
func TestAux_IgnoresRowsForUnknownSlots(t *testing.T) {
	s := newAuxStore(t)
	if _, err := s.db.Exec(
		`INSERT INTO keeper_aux_settings (slot, provider, model) VALUES ('retired_slot', 'anthropic', 'x')`); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, e := range s.Effective() {
		if e.Overridden {
			t.Errorf("stale row leaked into slot %s", e.Slot)
		}
	}
}

// Every slot the resolver knows must be writable, or an evaluator exists that the
// console cannot configure — the gap this store closes.
func TestAux_EverySlotIsAddressable(t *testing.T) {
	s := newAuxStore(t)
	ctx := context.Background()
	provider, model := "anthropic", "claude-haiku-4-5"

	for _, slot := range AuxSlots {
		if _, err := s.Apply(ctx, slot, AuxPatch{Provider: &provider, Model: &model}, "u"); err != nil {
			t.Errorf("slot %s is not writable: %v", slot, err)
		}
		if AuxLabels[slot] == "" {
			t.Errorf("slot %s has no label", slot)
		}
		// A slot with no applies-at would let the console imply a live change
		// where a restart is needed, which reads as a broken feature.
		switch auxAppliesAt[slot] {
		case AppliesImmediately, AppliesOnRestart:
		default:
			t.Errorf("slot %s has no applies-at (%q)", slot, auxAppliesAt[slot])
		}
	}
	// llm.SlotKeeper is excluded on purpose — nothing resolves it, so offering it
	// would be a knob wired to nothing (same reason /system/aux-status omits it).
	if KnownAuxSlot(string(llm.SlotKeeper)) {
		t.Error("the keeper slot is writable, but nothing consumes it")
	}
	// And each one lands on its own field rather than aliasing another.
	r := s.Resolved()
	for name, got := range map[string]string{
		"curator": r.Curator.Model, "behavior": r.Behavior.Model,
		"memory_health": r.MemoryHealth.Model, "negative": r.Negative.Model,
		"run_summary": r.RunSummary.Model, "fallback": r.Fallback.Model,
	} {
		if got != model {
			t.Errorf("%s did not receive its override (got %q)", name, got)
		}
	}
}

func TestAux_NilStore(t *testing.T) {
	var s *AuxStore
	eff := s.Effective()
	if len(eff) != len(AuxSlots) {
		t.Fatalf("nil store returned %d slots", len(eff))
	}
	if eff[0].Overridden {
		t.Error("nil store reports an override")
	}
	if _, err := s.Apply(context.Background(), string(llm.SlotCurator), AuxPatch{}, "u"); err == nil {
		t.Error("nil store accepted a write")
	}
}

// A CREWSHIP_AUX_* value must read as `env`, so the console can distinguish
// "we shipped this" from "someone on this box configured it" — the difference
// decides whether Reset is safe to press.
func TestAux_EnvConfiguredValueIsAttributedToEnv(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(auxTableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}

	configured := llm.AuxiliaryModelsFromEnv(llm.DefaultAuxiliaryModels(), func(k string) string {
		switch k {
		case "CREWSHIP_AUX_BEHAVIOR_MODEL":
			return "claude-sonnet-5"
		case "CREWSHIP_AUX_BEHAVIOR_TIMEOUT":
			return "12s"
		default:
			return ""
		}
	})
	s := NewAuxStore(db, configured)
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}

	behavior := slotOf(t, s.Effective(), string(llm.SlotBehavior))
	if behavior.Model.Value != "claude-sonnet-5" || behavior.Model.Source != SourceEnv {
		t.Errorf("model = %q/%s, want claude-sonnet-5/env", behavior.Model.Value, behavior.Model.Source)
	}
	if behavior.TimeoutMS.Value != 12000 || behavior.TimeoutMS.Source != SourceEnv {
		t.Errorf("timeout = %d/%s, want 12000/env", behavior.TimeoutMS.Value, behavior.TimeoutMS.Source)
	}
	// The provider was not in the environment, so it stays a shipped default even
	// on a slot whose model was configured.
	if behavior.Provider.Source != SourceDefault {
		t.Errorf("provider source = %s, want default", behavior.Provider.Source)
	}

	// And an override on top of env still returns to the ENV value on reset, not
	// to the built-in.
	model := "claude-opus-5"
	if _, err := s.Apply(context.Background(), string(llm.SlotBehavior),
		AuxPatch{Provider: strPtrAux("anthropic"), Model: &model}, "u"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := s.Reset(context.Background(), string(llm.SlotBehavior)); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := s.Resolved().Behavior.Model; got != "claude-sonnet-5" {
		t.Errorf("after reset the resolved model = %q, want the env value back", got)
	}
}

func strPtrAux(s string) *string { return &s }

// Two operators patching DIFFERENT fields of the same slot at the same time.
//
// The read-modify-write used to start from the in-memory cache, which is only
// refreshed after the lock is released — so the second Apply could read the
// pre-first-write value and persist the first's field at its old value. Not a
// stale read: the ROW lost a committed change. Reported as Critical on #1530.
//
// Serial here rather than with goroutines, because the interleaving that broke it
// is "B reads before A's refresh lands", and reproducing that reliably needs the
// refresh to be observable. What the fix guarantees — the base is always the
// database, never a cache that a concurrent writer has already superseded — holds
// for both shapes, and the concurrent case below covers the racy one.
func TestAux_ConcurrentPatchesToDifferentFieldsBothSurvive(t *testing.T) {
	s := newAuxStore(t)
	ctx := context.Background()
	slot := string(llm.SlotCurator)

	provider, model := "anthropic", "claude-opus-5"
	if _, err := s.Apply(ctx, slot, AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// One patches the timeout, the other the model. Neither mentions the other's
	// field, so both must survive.
	forty := int64(40000)
	if _, err := s.Apply(ctx, slot, AuxPatch{TimeoutMS: &forty}, ""); err != nil {
		t.Fatalf("timeout patch: %v", err)
	}
	next := "claude-sonnet-5"
	if _, err := s.Apply(ctx, slot, AuxPatch{Model: &next}, ""); err != nil {
		t.Fatalf("model patch: %v", err)
	}

	eff := s.EffectiveSlot(slot)
	if eff.Model.Value != "claude-sonnet-5" {
		t.Errorf("model = %q, want the later patch", eff.Model.Value)
	}
	if eff.TimeoutMS.Value != 40000 {
		t.Errorf("timeout = %d, want the earlier patch to have survived", eff.TimeoutMS.Value)
	}
	if eff.Provider.Value != "anthropic" {
		t.Errorf("provider = %q, want the seeded value to have survived both", eff.Provider.Value)
	}
}

// The same, run concurrently and under -race. Every patch names one field; when
// they all return, every field must hold one of the values that was actually
// written — never a value an earlier Apply had already replaced.
func TestAux_ConcurrentAppliesDoNotLoseAField(t *testing.T) {
	s := newAuxStore(t)
	ctx := context.Background()
	slot := string(llm.SlotBehavior)

	provider, model := "anthropic", "claude-haiku-4-5"
	if _, err := s.Apply(ctx, slot, AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Alternating fields: half move the timeout, half the model.
			if i%2 == 0 {
				ms := int64(10000 + i*1000)
				if _, err := s.Apply(ctx, slot, AuxPatch{TimeoutMS: &ms}, ""); err != nil {
					errs <- err
				}
				return
			}
			m := fmt.Sprintf("claude-opus-%d", i)
			if _, err := s.Apply(ctx, slot, AuxPatch{Model: &m}, ""); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent apply: %v", err)
	}

	// The provider was written once and never patched again, so it is the field
	// a lost update would silently revert to "" — and "" would be a slot that
	// resolves to the inherited provider, i.e. a different model entirely.
	eff := s.EffectiveSlot(slot)
	if eff.Provider.Value != "anthropic" || eff.Provider.Source != SourceInstance {
		t.Errorf("provider = %q/%s after concurrent patches — a write was lost",
			eff.Provider.Value, eff.Provider.Source)
	}
	if eff.TimeoutMS.Source != SourceInstance {
		t.Errorf("timeout source = %s, want an override to have survived", eff.TimeoutMS.Source)
	}
	// And the cache agrees with the database.
	if err := s.Load(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded := s.EffectiveSlot(slot); reloaded.Provider.Value != eff.Provider.Value ||
		reloaded.Model.Value != eff.Model.Value || reloaded.TimeoutMS.Value != eff.TimeoutMS.Value {
		t.Errorf("cache and database disagree: cached %+v, stored %+v", eff, reloaded)
	}
}

// storedAuxRow reads one slot straight from the table, bypassing the cache — so a
// test can assert the two agree rather than asking the cache about itself.
func storedAuxRow(t *testing.T, db *sql.DB, slot string) (provider, model string, present bool) {
	t.Helper()
	err := db.QueryRow(`SELECT provider, model FROM keeper_aux_settings WHERE slot = ?`, slot).
		Scan(&provider, &model)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		t.Fatalf("read stored row: %v", err)
	}
	return provider, model, true
}

// Reset used to delete the row and then reload the cache OUTSIDE the lock that
// Apply holds. A concurrent Apply could commit its override and update the cache
// in that window, and the reset's already-stale reload would then overwrite the
// cache with the pre-apply state: the row survives in the database, but every
// evaluator built in this process resolves the slot as inherited — a paid model
// silently reverting to a different one — until something reloads.
//
// The invariant, whichever of the two lands last: the cache says what the table
// says. Reported by CodeRabbit on #1530.
func TestAux_ConcurrentResetAndApplyKeepCacheAgreeingWithTheDatabase(t *testing.T) {
	s := newAuxStoreMultiConn(t)
	ctx := context.Background()
	slot := string(llm.SlotCurator)

	for round := range 200 {
		provider := "anthropic"
		model := fmt.Sprintf("claude-opus-%d", round)

		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			if err := s.Reset(ctx, slot); err != nil {
				t.Errorf("reset: %v", err)
			}
		}()
		for range 3 {
			go func() {
				defer wg.Done()
				if _, err := s.Apply(ctx, slot, AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
					t.Errorf("apply: %v", err)
				}
			}()
		}
		wg.Wait()

		wantProvider, wantModel, present := storedAuxRow(t, s.db, slot)
		eff := s.EffectiveSlot(slot)
		if eff.Overridden != present {
			t.Fatalf("round %d: cache says overridden=%v, table says row present=%v",
				round, eff.Overridden, present)
		}
		if present && (eff.Provider.Value != wantProvider || eff.Model.Value != wantModel) {
			t.Fatalf("round %d: cache has %s/%s, table has %s/%s",
				round, eff.Provider.Value, eff.Model.Value, wantProvider, wantModel)
		}
	}
}
