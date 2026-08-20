package keepercfg

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llm"
)

// Which VAULT KEY an evaluator spends (#1554).
//
// The slots could already name a provider and a model; they could not name the
// key. So every hosted evaluator dialled with whatever ANTHROPIC_API_KEY the
// server process happened to boot with, and on an instance holding several keys
// the console offered no way to say which one a sweep bills.
//
// The properties worth pinning are the same ones the rest of this store is
// pinned on, because they are what make a two-level config tolerable: an
// untouched instance still resolves to nothing (the process env, exactly as
// before), a written credential actually reaches the resolution the evaluators
// are built from, and clearing it returns the slot to that inherited behaviour
// rather than to a guess.

func credPtr(s string) *string { return &s }

// The backward-compatibility guarantee, stated as a test rather than as a
// comment: nothing in a fresh store names a credential, so every slot resolves
// its key the way it did before this column existed.
func TestAuxCredential_UntouchedInstanceNamesNoCredential(t *testing.T) {
	s := newAuxStore(t)

	for _, eff := range s.Effective() {
		if eff.CredentialID.Value != "" {
			t.Errorf("slot %s: credential = %q, want empty on an untouched instance", eff.Slot, eff.CredentialID.Value)
		}
		if eff.CredentialID.Source != SourceDefault {
			t.Errorf("slot %s: credential source = %q, want %q", eff.Slot, eff.CredentialID.Source, SourceDefault)
		}
		if eff.Overridden {
			t.Errorf("slot %s: reported as overridden on an untouched instance", eff.Slot)
		}
		if got := s.CredentialFor(eff.Slot); got != "" {
			t.Errorf("slot %s: CredentialFor = %q, want empty", eff.Slot, got)
		}
	}
}

// A credential written for one slot is readable, resolvable and clearable, and
// clearing it takes the row with it when nothing else is overridden — otherwise
// "overridden" would stay true forever after the last field was cleared.
func TestAuxCredential_RoundTripsAndClears(t *testing.T) {
	ctx := context.Background()
	s := newAuxStore(t)

	eff, err := s.Apply(ctx, "behavior", AuxPatch{CredentialID: credPtr("cred_abc123")}, "u1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if eff.CredentialID.Value != "cred_abc123" {
		t.Fatalf("credential = %q, want cred_abc123", eff.CredentialID.Value)
	}
	if eff.CredentialID.Source != SourceInstance {
		t.Errorf("credential source = %q, want %q", eff.CredentialID.Source, SourceInstance)
	}
	// A credential alone is an override: the slot now spends a key the operator
	// picked rather than the one in the process environment.
	if !eff.Overridden {
		t.Error("a slot with a credential is not reported as overridden")
	}
	// And it does NOT invent a provider or a model — those still inherit.
	if eff.Provider.Source == SourceInstance || eff.Model.Source == SourceInstance {
		t.Errorf("naming a key overrode the model too: provider=%v model=%v", eff.Provider, eff.Model)
	}
	if got := s.CredentialFor("behavior"); got != "cred_abc123" {
		t.Errorf("CredentialFor = %q, want cred_abc123", got)
	}

	// The row survives a reload, i.e. it is in the database and not only in the
	// cache — an evaluator built after a restart must spend the same key.
	if err := s.Load(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s.CredentialFor("behavior"); got != "cred_abc123" {
		t.Errorf("after reload CredentialFor = %q, want cred_abc123", got)
	}

	// "" is the documented clear, and with nothing else set the row goes away.
	if _, err := s.Apply(ctx, "behavior", AuxPatch{CredentialID: credPtr("")}, "u1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if eff := s.EffectiveSlot("behavior"); eff.Overridden || eff.CredentialID.Value != "" {
		t.Errorf("after clearing: overridden=%v credential=%q, want a fully inherited slot", eff.Overridden, eff.CredentialID.Value)
	}
	var n int
	if err := auxRowCount(s, "behavior", &n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Errorf("cleared slot still has %d row(s); an all-inherit row must be deleted", n)
	}
}

// Clearing the credential must not take the provider/model override with it —
// the fields are independent, and an operator dropping a key expects the slot to
// go back to the process env, not back to the shipped model.
func TestAuxCredential_ClearingLeavesTheModelAlone(t *testing.T) {
	ctx := context.Background()
	s := newAuxStore(t)

	provider, model := "anthropic", "claude-opus-5"
	if _, err := s.Apply(ctx, "curator", AuxPatch{
		Provider: &provider, Model: &model, CredentialID: credPtr("cred_x"),
	}, "u1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := s.Apply(ctx, "curator", AuxPatch{CredentialID: credPtr("")}, "u1"); err != nil {
		t.Fatalf("clear credential: %v", err)
	}

	eff := s.EffectiveSlot("curator")
	if eff.Model.Value != "claude-opus-5" || eff.Model.Source != SourceInstance {
		t.Errorf("model = %v, want the instance override to survive clearing the key", eff.Model)
	}
	if eff.CredentialID.Value != "" {
		t.Errorf("credential = %q, want empty", eff.CredentialID.Value)
	}
	if !eff.Overridden {
		t.Error("the slot still overrides its model, so it is still overridden")
	}
}

// Bad input is refused with a sentence the person who typed it can read, and it
// is refused as validation (400) rather than as an infrastructure failure.
func TestAuxCredential_Validation(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"too long", strings.Repeat("c", maxAuxCredentialIDLen+1), "too long"},
		{"control character", "cred\x00abc", "control character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newAuxStore(t)
			_, err := s.Apply(ctx, "behavior", AuxPatch{CredentialID: credPtr(tt.id)}, "u1")
			if err == nil {
				t.Fatalf("accepted %q", tt.name)
			}
			if !IsValidation(err) {
				t.Errorf("err = %v, want a validation error (400, not 500)", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tt.want)
			}
		})
	}
}

// CredentialFor mirrors llm.ResolveAux's own fall-through: a slot with no
// provider of its own resolves through the fallback slot, so the key has to
// follow the model it is paying for. A slot that DOES have a provider keeps its
// own (empty) credential rather than quietly spending the fallback's key on a
// different vendor's endpoint.
func TestAuxCredentialFor_FollowsTheFallbackOnlyWhenTheModelDoes(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(auxTableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	// A config with NOTHING configured per slot, so the fallback is the layer
	// that actually resolves — the shape llm.ResolveAux falls through in.
	s := NewAuxStore(db, llm.AuxiliaryModels{})
	if err := s.Load(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}

	fp, fm := "anthropic", "claude-haiku-4-5"
	if _, err := s.Apply(ctx, "fallback", AuxPatch{
		Provider: &fp, Model: &fm, CredentialID: credPtr("cred_fallback"),
	}, "u1"); err != nil {
		t.Fatalf("apply fallback: %v", err)
	}

	if got := s.CredentialFor("behavior"); got != "cred_fallback" {
		t.Errorf("CredentialFor(behavior) = %q, want the fallback's key — that is the model it will use", got)
	}

	// Now give the slot its own provider. It no longer resolves through the
	// fallback, so it must no longer borrow the fallback's key either.
	op, om := "openai", "gpt-4o-mini"
	if _, err := s.Apply(ctx, "behavior", AuxPatch{Provider: &op, Model: &om}, "u1"); err != nil {
		t.Fatalf("apply behavior: %v", err)
	}
	if got := s.CredentialFor("behavior"); got != "" {
		t.Errorf("CredentialFor(behavior) = %q, want empty — an openai slot must not spend the fallback's anthropic key", got)
	}
}

// auxRowCount counts the stored rows for one slot through the store's own DB
// handle, so a test can tell "cleared" from "stored as empty strings".
func auxRowCount(s *AuxStore, slot string, out *int) error {
	return s.db.QueryRow(`SELECT COUNT(*) FROM keeper_aux_settings WHERE slot = ?`, slot).Scan(out)
}

// A pinned credential is a vendor choice, so availability must not overrule it.
//
// #1554 lets an operator move a key out of the process environment into the
// vault, pinned per slot. #2001 then made an unconfigured slot follow whichever
// provider's key IS in the environment. Composed naively, an instance holding
// an Anthropic key in the vault and an OPENAI_API_KEY in the env — which is a
// perfectly ordinary shape, the env key being there for agent runs — has its
// evaluator slots retargeted to openai while still being built with the
// Anthropic vault key. Every evaluator 401s on first use and nothing says the
// vendor moved under it.
func TestAuxCredential_PinnedKeyIsNotRetargetedByAvailability(t *testing.T) {
	shipped := llm.AuxiliaryModels{
		Behavior: llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
	}
	// What LoadAuxiliaryModels resolves on a box whose env holds only an
	// OpenAI key: every slot retargeted away from the shipped vendor.
	available := llm.AuxiliaryModels{
		Behavior: llm.AuxModel{Provider: "openai", Model: "gpt-5.4-mini"},
	}

	t.Run("credential pinned, no provider: keeps the shipped vendor", func(t *testing.T) {
		eff := resolveAuxSlot(string(llm.SlotBehavior), available, shipped, AuxOverride{
			CredentialID: "cred_anthropic_vault_key",
		})
		if eff.Provider.Value != "anthropic" {
			t.Errorf("provider = %q, want anthropic — a pinned key is only valid at one vendor, so availability must not move the slot out from under it", eff.Provider.Value)
		}
		if eff.CredentialID.Value != "cred_anthropic_vault_key" {
			t.Errorf("credential = %q, want it preserved", eff.CredentialID.Value)
		}
	})

	t.Run("credential pinned WITH a provider: the operator's provider wins", func(t *testing.T) {
		eff := resolveAuxSlot(string(llm.SlotBehavior), available, shipped, AuxOverride{
			CredentialID: "cred_openai_vault_key",
			Provider:     "openai",
		})
		if eff.Provider.Value != "openai" {
			t.Errorf("provider = %q, want openai — an explicit provider is the operator saying it outright", eff.Provider.Value)
		}
	})

	t.Run("no credential: availability still chooses", func(t *testing.T) {
		eff := resolveAuxSlot(string(llm.SlotBehavior), available, shipped, AuxOverride{})
		if eff.Provider.Value != "openai" {
			t.Errorf("provider = %q, want openai — a slot expressing no preference is exactly what availability is for", eff.Provider.Value)
		}
	})
}
