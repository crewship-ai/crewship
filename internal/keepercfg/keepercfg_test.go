package keepercfg

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// tableDDL mirrors internal/database/migrations/20260730063951_keeper_runtime_settings.sql.
// Duplicated here so the store unit tests don't drag in the full migration
// stack; the backup totality guard and the migration test keep the real schema
// honest. The users stub exists only so the updated_by foreign key resolves if
// a caller has foreign_keys=ON.
const tableDDL = `
CREATE TABLE users (id TEXT PRIMARY KEY);
CREATE TABLE keeper_runtime_settings (
    id                 TEXT PRIMARY KEY CHECK (id = 'singleton'),
    enabled            INTEGER CHECK (enabled IN (0, 1)),
    judge_provider     TEXT NOT NULL DEFAULT '',
    judge_endpoint_url TEXT NOT NULL DEFAULT '',
    judge_wire         TEXT NOT NULL DEFAULT '',
    judge_model        TEXT NOT NULL DEFAULT '',
    updated_by         TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);`

func newTestStore(t *testing.T, dflt Defaults) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(tableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	s := New(db, dflt)
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

func strp(s string) *string { return &s }

// envDefaults is a server started the old way: KEEPER_ENABLED=true plus a
// loopback Ollama.
var envDefaults = Defaults{Enabled: true, EndpointURL: "http://127.0.0.1:11434", Model: "qwen2.5:7b"}

// --- inheritance ------------------------------------------------------------

// An untouched instance must behave EXACTLY as it did before this table
// existed: every field reads through to cfg.Keeper, provenance says so.
func TestEffective_EmptyRowInheritsEnv(t *testing.T) {
	s := newTestStore(t, envDefaults)
	eff := s.Effective()

	if !eff.Enabled.Value || eff.Enabled.Source != SourceEnv {
		t.Errorf("enabled = %v/%s, want true/env", eff.Enabled.Value, eff.Enabled.Source)
	}
	if eff.EndpointURL.Value != envDefaults.EndpointURL || eff.EndpointURL.Source != SourceEnv {
		t.Errorf("endpoint = %q/%s, want %q/env", eff.EndpointURL.Value, eff.EndpointURL.Source, envDefaults.EndpointURL)
	}
	if eff.Model.Value != envDefaults.Model || eff.Model.Source != SourceEnv {
		t.Errorf("model = %q/%s, want %q/env", eff.Model.Value, eff.Model.Source, envDefaults.Model)
	}
	// Provider and wire have no env equivalent — cfg.Keeper builds an Ollama
	// judge by construction, so the built-in default names that.
	if eff.Provider.Value != ProviderOllama || eff.Provider.Source != SourceDefault {
		t.Errorf("provider = %q/%s, want ollama/default", eff.Provider.Value, eff.Provider.Source)
	}
	if eff.Wire.Value != WireOllama || eff.Wire.Source != SourceDefault {
		t.Errorf("wire = %q/%s, want ollama/default", eff.Wire.Value, eff.Wire.Source)
	}
	if eff.Overridden {
		t.Error("Overridden = true with no override stored")
	}
}

// A stock server (no KEEPER_* env at all) reports the built-in default, not a
// phantom env value.
func TestEffective_NoEnvNoRow(t *testing.T) {
	s := newTestStore(t, Defaults{})
	eff := s.Effective()

	if eff.Enabled.Value {
		t.Error("enabled = true on a server with no keeper config")
	}
	if eff.EndpointURL.Value != "" || eff.EndpointURL.Source != SourceDefault {
		t.Errorf("endpoint = %q/%s, want empty/default", eff.EndpointURL.Value, eff.EndpointURL.Source)
	}
	if eff.Model.Source != SourceDefault {
		t.Errorf("model source = %s, want default", eff.Model.Source)
	}
}

func TestApply_InstanceOverridesEnvPerField(t *testing.T) {
	s := newTestStore(t, envDefaults)

	eff, err := s.Apply(context.Background(), Patch{Model: strp("qwen3:4b")}, "user-1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if eff.Model.Value != "qwen3:4b" || eff.Model.Source != SourceInstance {
		t.Errorf("model = %q/%s, want qwen3:4b/instance", eff.Model.Value, eff.Model.Source)
	}
	// Untouched fields keep inheriting — a partial update is partial.
	if eff.EndpointURL.Value != envDefaults.EndpointURL || eff.EndpointURL.Source != SourceEnv {
		t.Errorf("endpoint = %q/%s, want the env value still inherited", eff.EndpointURL.Value, eff.EndpointURL.Source)
	}
	if !eff.Overridden {
		t.Error("Overridden = false after a model override")
	}
	if eff.UpdatedBy != "user-1" {
		t.Errorf("updated_by = %q, want user-1", eff.UpdatedBy)
	}
	if eff.UpdatedAt == "" {
		t.Error("updated_at empty after a write")
	}
}

// Clearing a field returns it to the env value, not to a hardcoded guess.
func TestApply_ClearingRestoresInheritance(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	if _, err := s.Apply(ctx, Patch{Model: strp("qwen3:4b")}, "user-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff, err := s.Apply(ctx, Patch{Model: strp("")}, "user-1")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if eff.Model.Value != envDefaults.Model || eff.Model.Source != SourceEnv {
		t.Errorf("after clear model = %q/%s, want %q/env", eff.Model.Value, eff.Model.Source, envDefaults.Model)
	}
}

// The whole reason `enabled` is nullable: "not touched" and "turned off" must
// stay distinguishable, so KEEPER_ENABLED is honoured until someone overrides
// it — and an explicit off is not mistaken for silence.
func TestApply_EnabledIsThreeState(t *testing.T) {
	s := newTestStore(t, envDefaults) // env says enabled
	ctx := context.Background()

	off := TriOff
	eff, err := s.Apply(ctx, Patch{Enabled: &off}, "user-1")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if eff.Enabled.Value || eff.Enabled.Source != SourceInstance {
		t.Errorf("enabled = %v/%s, want false/instance", eff.Enabled.Value, eff.Enabled.Source)
	}

	inherit := TriInherit
	eff, err = s.Apply(ctx, Patch{Enabled: &inherit}, "user-1")
	if err != nil {
		t.Fatalf("inherit: %v", err)
	}
	if !eff.Enabled.Value || eff.Enabled.Source != SourceEnv {
		t.Errorf("enabled = %v/%s, want true/env after reverting to inherit", eff.Enabled.Value, eff.Enabled.Source)
	}
}

// The case this whole slice exists for: a box booted with KEEPER_ENABLED unset
// turns Keeper on from the API, in one call, with the endpoint and model that
// make it work.
func TestApply_EnableFromScratch(t *testing.T) {
	s := newTestStore(t, Defaults{})

	on := TriOn
	eff, err := s.Apply(context.Background(), Patch{
		Enabled:     &on,
		EndpointURL: strp("http://192.168.1.40:11434"),
		Model:       strp("qwen2.5:7b"),
	}, "user-1")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !eff.Enabled.Value {
		t.Error("enabled = false after turning it on")
	}
	if eff.EndpointURL.Value != "http://192.168.1.40:11434" {
		t.Errorf("endpoint = %q", eff.EndpointURL.Value)
	}
	if eff.Model.Value != "qwen2.5:7b" {
		t.Errorf("model = %q", eff.Model.Value)
	}
}

func TestReset_DropsEveryOverride(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	off := TriOff
	if _, err := s.Apply(ctx, Patch{Enabled: &off, Model: strp("bogus:1b"), EndpointURL: strp("http://10.0.0.5:11434")}, "user-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff, err := s.Reset(ctx, "user-2")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if eff.Overridden {
		t.Error("Overridden = true after reset")
	}
	if !eff.Enabled.Value || eff.Enabled.Source != SourceEnv {
		t.Errorf("enabled = %v/%s after reset, want the env value back", eff.Enabled.Value, eff.Enabled.Source)
	}
	if eff.Model.Value != envDefaults.Model {
		t.Errorf("model = %q after reset, want %q", eff.Model.Value, envDefaults.Model)
	}
}

// Reads must survive a process restart: Load rebuilds the cache from the row.
func TestLoad_ReadsPersistedRow(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()
	if _, err := s.Apply(ctx, Patch{Model: strp("qwen3:4b"), EndpointURL: strp("http://10.0.0.5:11434")}, "user-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	fresh := New(s.db, envDefaults)
	if err := fresh.Load(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	eff := fresh.Effective()
	if eff.Model.Value != "qwen3:4b" || eff.Model.Source != SourceInstance {
		t.Errorf("reloaded model = %q/%s", eff.Model.Value, eff.Model.Source)
	}
	if eff.EndpointURL.Value != "http://10.0.0.5:11434" || eff.EndpointURL.Source != SourceInstance {
		t.Errorf("reloaded endpoint = %q/%s", eff.EndpointURL.Value, eff.EndpointURL.Source)
	}
}

// --- validation -------------------------------------------------------------

func TestApply_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		dflt  Defaults
		patch Patch
		want  string // substring of the operator-facing message
	}{
		{
			name:  "unknown provider",
			dflt:  envDefaults,
			patch: Patch{Provider: strp("bedrock")},
			want:  "provider",
		},
		{
			// anthropic needs a key, and the instance row deliberately has no
			// credential field — that lives in the vault at workspace scope.
			name:  "provider needing a key",
			dflt:  envDefaults,
			patch: Patch{Provider: strp(ProviderAnthropic)},
			want:  "API key",
		},
		{
			// openai_compat is a legal governance provider, but the instance
			// judge builds its request URL from the stored endpoint, and doing
			// that unambiguously is the endpoint contract's job (#1528). Until
			// then, say so instead of storing a value that would 404.
			name:  "provider the instance judge cannot build yet",
			dflt:  envDefaults,
			patch: Patch{Provider: strp(ProviderOpenAICompat)},
			want:  "workspace",
		},
		{
			name:  "unknown wire",
			dflt:  envDefaults,
			patch: Patch{Wire: strp("grpc")},
			want:  "wire",
		},
		{
			name:  "wire the instance judge cannot speak yet",
			dflt:  envDefaults,
			patch: Patch{Wire: strp(WireOpenAIChat)},
			want:  "wire",
		},
		{
			// A bare host is the most likely paste. url.Parse accepts it, so
			// the message has to be the one that fixes it.
			name:  "endpoint with no scheme",
			dflt:  envDefaults,
			patch: Patch{EndpointURL: strp("192.168.1.40:11434")},
			want:  "scheme",
		},
		{
			name:  "endpoint that does not parse",
			dflt:  envDefaults,
			patch: Patch{EndpointURL: strp("http://[::1:11434")},
			want:  "not a valid URL",
		},
		{
			name:  "endpoint with a non-http scheme",
			dflt:  envDefaults,
			patch: Patch{EndpointURL: strp("ftp://host:11434")},
			want:  "http",
		},
		{
			// Credentials in a URL would be stored unencrypted in a
			// non-secret table and echoed back by the admin GET.
			name:  "endpoint carrying userinfo",
			dflt:  envDefaults,
			patch: Patch{EndpointURL: strp("http://user:pass@host:11434")},
			want:  "credentials",
		},
		{
			name:  "endpoint with no host",
			dflt:  envDefaults,
			patch: Patch{EndpointURL: strp("http:///v1")},
			want:  "host",
		},
		{
			name:  "endpoint over the length limit",
			dflt:  envDefaults,
			patch: Patch{EndpointURL: strp("http://h/" + strings.Repeat("a", maxEndpointLen))},
			want:  "too long",
		},
		{
			name:  "model over the length limit",
			dflt:  envDefaults,
			patch: Patch{Model: strp(strings.Repeat("m", maxModelLen+1))},
			want:  "too long",
		},
		{
			name:  "model with a control character",
			dflt:  envDefaults,
			patch: Patch{Model: strp("qwen\n2.5")},
			want:  "model",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t, tc.dflt)
			_, err := s.Apply(context.Background(), tc.patch, "user-1")
			if err == nil {
				t.Fatal("apply accepted an invalid patch")
			}
			if !IsValidation(err) {
				t.Errorf("IsValidation = false for %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q does not mention %q", err.Error(), tc.want)
			}
			// A rejected patch must leave the row untouched.
			if s.Effective().Overridden {
				t.Error("a rejected patch still wrote an override")
			}
		})
	}
}

// Keeper is fail-closed: enabling it without a judge would turn every
// credential request into a DENY. Refuse at configure time instead.
func TestApply_RefusesEnableWithoutJudge(t *testing.T) {
	on := TriOn
	cases := []struct {
		name  string
		dflt  Defaults
		patch Patch
	}{
		{"nothing configured", Defaults{}, Patch{Enabled: &on}},
		{"endpoint but no model", Defaults{}, Patch{Enabled: &on, EndpointURL: strp("http://127.0.0.1:11434")}},
		{"model but no endpoint", Defaults{}, Patch{Enabled: &on, Model: strp("qwen2.5:7b")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t, tc.dflt)
			_, err := s.Apply(context.Background(), tc.patch, "user-1")
			if err == nil {
				t.Fatal("enabled Keeper with no judge configured")
			}
			if !IsValidation(err) {
				t.Errorf("IsValidation = false for %v", err)
			}
			if !strings.Contains(err.Error(), "endpoint") && !strings.Contains(err.Error(), "model") {
				t.Errorf("message %q names neither the endpoint nor the model", err.Error())
			}
		})
	}
}

// The complement: enabling is fine when the same call supplies the judge, and
// fine when the env already did.
func TestApply_EnableAllowedWhenJudgeResolves(t *testing.T) {
	on := TriOn
	t.Run("supplied in the same call", func(t *testing.T) {
		s := newTestStore(t, Defaults{})
		if _, err := s.Apply(context.Background(), Patch{
			Enabled: &on, EndpointURL: strp("http://127.0.0.1:11434"), Model: strp("qwen2.5:7b"),
		}, "user-1"); err != nil {
			t.Fatalf("apply: %v", err)
		}
	})
	t.Run("inherited from env", func(t *testing.T) {
		s := newTestStore(t, Defaults{EndpointURL: "http://127.0.0.1:11434", Model: "qwen2.5:7b"})
		if _, err := s.Apply(context.Background(), Patch{Enabled: &on}, "user-1"); err != nil {
			t.Fatalf("apply: %v", err)
		}
	})
}

// Clearing the model out from under an enabled judge is the same fail-closed
// hazard arriving by the back door.
func TestApply_RefusesClearingTheJudgeWhileEnabled(t *testing.T) {
	s := newTestStore(t, Defaults{})
	ctx := context.Background()
	on := TriOn
	if _, err := s.Apply(ctx, Patch{Enabled: &on, EndpointURL: strp("http://127.0.0.1:11434"), Model: strp("qwen2.5:7b")}, "u"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := s.Apply(ctx, Patch{Model: strp("")}, "u"); err == nil {
		t.Fatal("cleared the model while Keeper was enabled")
	}
}

// Accepted endpoint shapes. Normalization to a bare root is internal/llm's job
// (#1528); the store's contract is that it stores what an operator plausibly
// pastes rather than rejecting it.
func TestApply_AcceptsPasteShapes(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:11434",
		"http://127.0.0.1:11434/",
		"http://127.0.0.1:11434/v1",
		"http://127.0.0.1:11434/v1/chat/completions",
		"http://127.0.0.1:11434/api/chat",
		"http://ollama.lan:11434",
		"https://llm.example.com",
		"http://[::1]:11434",
		"HTTP://127.0.0.1:11434",
	} {
		t.Run(raw, func(t *testing.T) {
			s := newTestStore(t, envDefaults)
			if _, err := s.Apply(context.Background(), Patch{EndpointURL: strp(raw)}, "u"); err != nil {
				t.Fatalf("rejected %q: %v", raw, err)
			}
		})
	}
}

// --- change notification ----------------------------------------------------

// The runtime enable story depends on this: the orchestrator's keeper gate and
// the lazy judge learn about a change without a restart.
func TestOnChange_FiresWithTheNewEffectiveConfig(t *testing.T) {
	s := newTestStore(t, envDefaults)
	var got []Effective
	s.OnChange(func(e Effective) { got = append(got, e) })

	ctx := context.Background()
	if _, err := s.Apply(ctx, Patch{Model: strp("qwen3:4b")}, "u"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := s.Reset(ctx, "u"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("callback fired %d times, want 2", len(got))
	}
	if got[0].Model.Value != "qwen3:4b" {
		t.Errorf("first callback saw model %q", got[0].Model.Value)
	}
	if got[1].Model.Value != envDefaults.Model {
		t.Errorf("second callback saw model %q, want the env value back", got[1].Model.Value)
	}
}

// A rejected patch must not fire the callback — a listener that reconfigures a
// live judge on every call would otherwise churn on bad input.
func TestOnChange_SilentOnRejection(t *testing.T) {
	s := newTestStore(t, envDefaults)
	fired := 0
	s.OnChange(func(Effective) { fired++ })
	if _, err := s.Apply(context.Background(), Patch{Wire: strp("grpc")}, "u"); err == nil {
		t.Fatal("expected rejection")
	}
	if fired != 0 {
		t.Errorf("callback fired %d times on a rejected patch", fired)
	}
}

// --- fingerprint ------------------------------------------------------------

// The lazy judge rebuilds when — and only when — the wiring it was built from
// changed. Provenance and audit columns must not participate.
func TestJudgeFingerprint(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()
	base := s.Effective().JudgeFingerprint()

	if _, err := s.Apply(ctx, Patch{Model: strp("qwen3:4b")}, "u"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if changed := s.Effective().JudgeFingerprint(); changed == base {
		t.Error("fingerprint unchanged after the model changed")
	}

	// Same wiring, different actor: no rebuild.
	before := s.Effective().JudgeFingerprint()
	if _, err := s.Apply(ctx, Patch{Model: strp("qwen3:4b")}, "someone-else"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if after := s.Effective().JudgeFingerprint(); after != before {
		t.Error("fingerprint changed when only the acting user did")
	}

	// The endpoint participates: a judge left pointing at the old host is the
	// failure this whole slice exists to make impossible.
	if _, err := s.Apply(ctx, Patch{EndpointURL: strp("http://10.0.0.5:11434")}, "u"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if after := s.Effective().JudgeFingerprint(); after == before {
		t.Error("fingerprint unchanged after the endpoint changed")
	}

	// And so does the wire, which is not yet writable but is already part of
	// what a built judge depends on — see validate().
	withWire := Effective{Wire: Field[string]{Value: WireOpenAIChat}}
	if withWire.JudgeFingerprint() == (Effective{Wire: Field[string]{Value: WireOllama}}).JudgeFingerprint() {
		t.Error("fingerprint ignores the wire")
	}
}

// --- nil safety -------------------------------------------------------------

// CLI processes and unit tests run without a store. Reading must not panic and
// must report "nothing configured" rather than a phantom enabled judge.
func TestNilStore(t *testing.T) {
	var s *Store
	eff := s.Effective()
	if eff.Enabled.Value {
		t.Error("nil store reports Keeper enabled")
	}
	if eff.Enabled.Source != SourceDefault {
		t.Errorf("nil store source = %s, want default", eff.Enabled.Source)
	}
	if _, err := s.Apply(context.Background(), Patch{Model: strp("x")}, "u"); err == nil {
		t.Error("nil store accepted a write")
	}
}
