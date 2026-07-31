package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// Which KEY a live evaluator dials with (#1554).
//
// Before this, a hosted evaluator read ANTHROPIC_API_KEY from the server
// process environment, which produced the failure the issue was filed for: an
// instance whose env key is stale has five dead background evaluators and says
// nothing, because the slots ARE configured — the inherited key is the broken
// part. Naming the credential per slot is what makes the broken thing nameable.
//
// The revoke-safety contract is copied from governance.ResolveGovModel §4.4: a
// revoked credential is a SOFT delete, so the id survives in the row and the FK
// never fires. The resolver has to notice at BUILD time and degrade — never dial
// with a stale id, never take the evaluator down.

// credLookup is a recording stub for the vault seam.
type credLookup struct {
	mu    sync.Mutex
	key   string
	err   error
	calls []string
}

func (c *credLookup) lookup(_ context.Context, id string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, id)
	return c.key, c.err
}

func (c *credLookup) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func setAuxCredential(t *testing.T, store *keepercfg.AuxStore, slot, provider, model, credID string) {
	t.Helper()
	if _, err := store.Apply(context.Background(), slot, keepercfg.AuxPatch{
		Provider: &provider, Model: &model, CredentialID: &credID,
	}, ""); err != nil {
		t.Fatalf("apply %s: %v", slot, err)
	}
}

// The point of the feature: the evaluator is built from the vault key, with
// nothing usable in the environment at all. Without the credential this build
// would fail — which is exactly what it did on dev1.
func TestAuxLiveResolver_BuildsFromTheNamedVaultKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := auxStoreFor(t)
	setAuxCredential(t, store, "behavior", "anthropic", "claude-opus-5", "cred_paid")

	vault := &credLookup{key: "sk-ant-from-the-vault"}
	resolve := newAuxLiveResolver("behavior", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), vault.lookup, nil, nil, slog.Default())

	p, m := resolve(context.Background(), "ws-1")
	if p == nil {
		t.Fatal("no provider — the vault key did not reach the builder")
	}
	if m != "claude-opus-5" {
		t.Errorf("model = %q, want claude-opus-5", m)
	}
	if vault.callCount() == 0 {
		t.Error("the credential was never looked up")
	}
}

// A slot that names no credential must not touch the vault at all, and must
// build exactly as it did before the column existed.
func TestAuxLiveResolver_NoCredentialIsTheEnvPath(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-the-env")
	store := auxStoreFor(t)
	setAuxCredential(t, store, "curator", "anthropic", "claude-haiku-4-5", "")

	vault := &credLookup{err: fmt.Errorf("the vault must not be consulted")}
	resolve := newAuxLiveResolver("curator", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), vault.lookup, nil, nil, slog.Default())

	if p, _ := resolve(context.Background(), "ws-1"); p == nil {
		t.Fatal("an un-credentialed slot stopped building from the environment")
	}
	if vault.callCount() != 0 {
		t.Errorf("looked the vault up %d time(s) for a slot that names no credential", vault.callCount())
	}
}

// Revoke-safety. A revoke is a soft delete, so the id is still in the row; the
// lookup is what fails. The slot must fall back to the process env — a working
// evaluator — rather than dialling with the stale id or going dark.
func TestAuxLiveResolver_RevokedCredentialDegradesToTheEnvKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-the-env")
	store := auxStoreFor(t)
	setAuxCredential(t, store, "memory_health", "anthropic", "claude-haiku-4-5", "cred_revoked")

	vault := &credLookup{err: fmt.Errorf("credential %q not found, inactive, or revoked", "cred_revoked")}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	resolve := newAuxLiveResolver("memory_health", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), vault.lookup, nil, nil, logger)

	p, m := resolve(context.Background(), "ws-1")
	if p == nil {
		t.Fatal("a revoked credential took the evaluator down instead of degrading it")
	}
	if m != "claude-haiku-4-5" {
		t.Errorf("model = %q, want the configured model to survive the degrade", m)
	}
	if !strings.Contains(logs.String(), "credential is unusable") {
		t.Errorf("the degrade was silent; logs = %q", logs.String())
	}

	// The sweeps and the sampled behaviour hook call this often, so a stuck
	// revoked credential must warn ONCE, not once per evaluation.
	for range 3 {
		if p, _ := resolve(context.Background(), "ws-1"); p == nil {
			t.Fatal("the degraded provider stopped being served")
		}
	}
	if n := strings.Count(logs.String(), "credential is unusable"); n != 1 {
		t.Errorf("warned %d times about one stuck credential, want 1", n)
	}
}

// And with no env key either, a revoked credential leaves the slot on its
// construction-time default — the existing fall-through, not a broken dial.
func TestAuxLiveResolver_RevokedCredentialAndNoEnvKeyFallsThrough(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := auxStoreFor(t)
	setAuxCredential(t, store, "negative", "anthropic", "claude-haiku-4-5", "cred_revoked")

	vault := &credLookup{err: fmt.Errorf("revoked")}
	resolve := newAuxLiveResolver("negative", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), vault.lookup, nil, nil, slog.Default())

	if p, _ := resolve(context.Background(), "ws-1"); p != nil {
		t.Error("built an anthropic provider with neither a vault key nor an env key")
	}
}

// A rotated key must reach the evaluator. The provider is cached on the wiring
// (otherwise every sampled tool call opens a fresh connection), and the wiring
// string cannot contain the key — so the cache has to compare the key itself,
// the way the governance model's build cache does.
func TestAuxLiveResolver_RotatedKeyRebuildsTheProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := auxStoreFor(t)
	setAuxCredential(t, store, "behavior", "anthropic", "claude-haiku-4-5", "cred_rot")

	vault := &credLookup{key: "sk-ant-first"}
	resolve := newAuxLiveResolver("behavior", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), vault.lookup, nil, nil, slog.Default())

	first, _ := resolve(context.Background(), "ws-1")
	if first == nil {
		t.Fatal("no provider on the first resolve")
	}
	if same, _ := resolve(context.Background(), "ws-1"); same != first {
		t.Error("an unchanged key rebuilt the provider")
	}

	vault.mu.Lock()
	vault.key = "sk-ant-rotated"
	vault.mu.Unlock()

	rotated, _ := resolve(context.Background(), "ws-1")
	if rotated == nil {
		t.Fatal("no provider after the rotation")
	}
	if rotated == first {
		t.Error("a rotated vault key kept serving the provider built from the old one")
	}
}

// An "ollama" slot dials the instance judge endpoint and needs no key, so a
// credential left over from when the slot was hosted must be ignored rather
// than looked up (and rather than breaking the local slot).
func TestAuxLiveResolver_LocalSlotIgnoresACredential(t *testing.T) {
	store := auxStoreFor(t)
	setAuxCredential(t, store, "curator", "ollama", "qwen2.5:7b", "cred_leftover")

	vault := &credLookup{err: fmt.Errorf("the vault must not be consulted for a local slot")}
	resolve := newAuxLiveResolver("curator", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), vault.lookup, nil, nil, slog.Default())

	if p, _ := resolve(context.Background(), "ws-1"); p == nil {
		t.Fatal("a local slot with a leftover credential stopped building")
	}
	if vault.callCount() != 0 {
		t.Errorf("looked the vault up %d time(s) for an ollama slot", vault.callCount())
	}
}

// No vault seam wired (test and embedded builds) means the credential is simply
// not resolvable, and the slot keeps the env behaviour rather than panicking.
func TestAuxLiveResolver_NilVaultSeamKeepsTheEnvPath(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-the-env")
	store := auxStoreFor(t)
	setAuxCredential(t, store, "behavior", "anthropic", "claude-haiku-4-5", "cred_unresolvable")

	resolve := newAuxLiveResolver("behavior", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), nil, nil, nil, slog.Default())
	if p, _ := resolve(context.Background(), "ws-1"); p == nil {
		t.Error("a nil vault seam took the evaluator down instead of leaving it on the env key")
	}
}
