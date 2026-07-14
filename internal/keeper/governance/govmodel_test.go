package governance

import (
	"context"
	"errors"
	"testing"
)

// fakeVault is a test CredentialLookup: it returns a fixed type/value, or an
// error to simulate a revoked/undecryptable credential.
type fakeVault struct {
	credType string
	value    string
	err      error
	gotWS    string
	gotID    string
}

func (f *fakeVault) LookupCredential(_ context.Context, workspaceID, credentialID string) (string, string, error) {
	f.gotWS, f.gotID = workspaceID, credentialID
	if f.err != nil {
		return "", "", f.err
	}
	return f.credType, f.value, nil
}

var testDefault = OllamaDefault{URL: "http://localhost:11434", Model: "qwen2.5:3b-instruct"}

func TestResolveGovModel_Unconfigured(t *testing.T) {
	_, found := ResolveGovModel(context.Background(), Settings{}, "ws1", &fakeVault{}, testDefault)
	if found {
		t.Fatal("empty provider must resolve found=false (caller uses env default)")
	}
}

func TestResolveGovModel_ConfiguredNoCredential(t *testing.T) {
	s := Settings{GovModelProvider: ProviderOllama, GovModelID: "llama3.2:3b-instruct"}
	got, found := ResolveGovModel(context.Background(), s, "ws1", &fakeVault{}, testDefault)
	if !found || got.Degraded {
		t.Fatalf("want found & not degraded, got found=%v degraded=%v", found, got.Degraded)
	}
	if got.Provider != ProviderOllama || got.Model != "llama3.2:3b-instruct" {
		t.Fatalf("resolved = %+v", got)
	}
	if got.EndpointURL != "" || got.APIKey != "" {
		t.Fatalf("no credential should leave secrets empty (env fallback), got %+v", got)
	}
}

func TestResolveGovModel_EndpointURLCredential(t *testing.T) {
	s := Settings{GovModelProvider: ProviderOpenAICompat, GovModelID: "gpt-4o-mini", GovModelCredentialID: "c1"}
	v := &fakeVault{credType: CredTypeEndpointURL, value: "https://llm.internal/v1"}
	got, found := ResolveGovModel(context.Background(), s, "ws9", v, testDefault)
	if !found || got.Degraded {
		t.Fatalf("want found & not degraded, got %+v", got)
	}
	if got.EndpointURL != "https://llm.internal/v1" {
		t.Errorf("EndpointURL = %q", got.EndpointURL)
	}
	if v.gotWS != "ws9" || v.gotID != "c1" {
		t.Errorf("vault called with ws=%q id=%q, want ws9/c1", v.gotWS, v.gotID)
	}
}

func TestResolveGovModel_APIKeyCredential(t *testing.T) {
	s := Settings{GovModelProvider: ProviderAnthropic, GovModelID: "claude-haiku-4-5", GovModelCredentialID: "c1"}
	v := &fakeVault{credType: CredTypeAPIKey, value: "sk-secret"}
	got, _ := ResolveGovModel(context.Background(), s, "ws1", v, testDefault)
	if got.Degraded {
		t.Fatalf("valid API_KEY credential should not degrade: %+v", got)
	}
	if got.APIKey != "sk-secret" {
		t.Errorf("APIKey = %q", got.APIKey)
	}
}

// TestResolveGovModel_RevokedCredentialDegrades is the core §4.4 revoke-safety
// assertion: a broken credential must degrade to a WORKING default OLLAMA judge,
// never a nil/broken provider.
func TestResolveGovModel_RevokedCredentialDegrades(t *testing.T) {
	s := Settings{GovModelProvider: ProviderAnthropic, GovModelID: "claude-haiku-4-5", GovModelCredentialID: "c1"}
	v := &fakeVault{err: errors.New("credential not found")}
	got, found := ResolveGovModel(context.Background(), s, "ws1", v, testDefault)
	if !found {
		t.Fatal("a configured-but-degraded row must still return found=true (a provider is built)")
	}
	if !got.Degraded || got.DegradeReason == "" {
		t.Fatalf("want Degraded with a reason, got %+v", got)
	}
	if got.Provider != ProviderOllama || got.Model != testDefault.Model || got.EndpointURL != testDefault.URL {
		t.Fatalf("degrade must target the default OLLAMA judge, got %+v", got)
	}
}

func TestResolveGovModel_InvalidProviderDegrades(t *testing.T) {
	s := Settings{GovModelProvider: "gemini", GovModelID: "x"}
	got, found := ResolveGovModel(context.Background(), s, "ws1", &fakeVault{}, testDefault)
	if !found || !got.Degraded || got.Provider != ProviderOllama {
		t.Fatalf("unknown provider must degrade to OLLAMA, got found=%v %+v", found, got)
	}
}

func TestResolveGovModel_WrongCredentialTypeDegrades(t *testing.T) {
	s := Settings{GovModelProvider: ProviderAnthropic, GovModelID: "x", GovModelCredentialID: "c1"}
	v := &fakeVault{credType: "SECRET", value: "nope"}
	got, _ := ResolveGovModel(context.Background(), s, "ws1", v, testDefault)
	if !got.Degraded || got.Provider != ProviderOllama {
		t.Fatalf("unusable credential type must degrade to OLLAMA, got %+v", got)
	}
}

func TestKnownGovProvider(t *testing.T) {
	for _, p := range []string{ProviderOllama, ProviderAnthropic, ProviderOpenAICompat} {
		if !KnownGovProvider(p) {
			t.Errorf("%q should be known", p)
		}
	}
	for _, p := range []string{"", "gemini", "OLLAMA", "openai"} {
		if KnownGovProvider(p) {
			t.Errorf("%q should NOT be known", p)
		}
	}
}

// TestGovModelFields_RoundTripAndHardDelete exercises the v142 columns end-to-end
// on a migrated DB: Upsert/Get round-trip, plus the ON DELETE SET NULL FK on a
// HARD delete of the credential row (the same-name recreation purge path) — it
// nulls the ref rather than leaving it dangling. NOTE: the normal revoke path is a
// SOFT delete (credentials.deleted_at) which does NOT fire this FK; that path's
// revoke-safety is enforced at resolve time and is covered by
// TestResolveGovModel_RevokedCredentialDegrades.
func TestGovModelFields_RoundTripAndHardDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by)
		 VALUES ('cred1', 'ws1', 'gov-endpoint', 'enc', 'ENDPOINT_URL', 'u1')`); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	in := Settings{
		Enabled:              true,
		DenyNotifyMinRisk:    5,
		GovModelProvider:     ProviderOpenAICompat,
		GovModelID:           "gpt-4o-mini",
		GovModelCredentialID: "cred1",
	}
	if err := Upsert(ctx, db, "ws1", in, "u1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, found, err := Get(ctx, db, "ws1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got.GovModelProvider != ProviderOpenAICompat || got.GovModelID != "gpt-4o-mini" || got.GovModelCredentialID != "cred1" {
		t.Fatalf("gov fields round-trip mismatch: %+v", got)
	}

	// HARD-delete the credential row (the purge path) → ON DELETE SET NULL nulls
	// the ref. A real revoke is a soft delete and would NOT null it here (see the
	// function doc); resolve-time degrade covers that path.
	if _, err := db.Exec(`DELETE FROM credentials WHERE id = 'cred1'`); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	got, _, err = Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get after hard delete: %v", err)
	}
	if got.GovModelCredentialID != "" {
		t.Fatalf("hard-deleted credential ref must be NULL, got %q", got.GovModelCredentialID)
	}
	// Provider/model config survives — the resolver degrades at build time.
	if got.GovModelProvider != ProviderOpenAICompat {
		t.Fatalf("provider config should survive credential revoke, got %q", got.GovModelProvider)
	}
}
