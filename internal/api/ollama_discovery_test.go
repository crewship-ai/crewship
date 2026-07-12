package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/llm"
)

// #988: the daemon-side OLLAMA discovery dial must refuse a tenant endpoint
// resolving to a private/loopback IP unless the instance cap is on, and must
// ALWAYS refuse metadata/link-local. httptest binds to 127.0.0.1 (private
// tier), so it's the loopback case.
func TestOllamaDiscoveryClient_SSRFGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"x"}]}`))
	}))
	defer srv.Close()

	// Cap OFF (default): loopback dial must be blocked.
	t.Setenv("CREWSHIP_ALLOW_PRIVATE_ENDPOINTS", "")
	if _, err := ollamaDiscoveryClient().Get(srv.URL + "/api/tags"); err == nil {
		t.Error("cap off: discovery dial to loopback must be blocked (SSRF guard)")
	}

	// Cap ON: loopback dial allowed (operator opted in instance-wide).
	t.Setenv("CREWSHIP_ALLOW_PRIVATE_ENDPOINTS", "true")
	resp, err := ollamaDiscoveryClient().Get(srv.URL + "/api/tags")
	if err != nil {
		t.Fatalf("cap on: loopback dial should be allowed, got %v", err)
	}
	_ = resp.Body.Close()
}

// Metadata/link-local is blocked regardless of the cap — the guard composes
// httpsafe.IsBlockedIPForEndpoint, whose hard tier ignores allowPrivate.
func TestOllamaDiscovery_MetadataAlwaysBlocked(t *testing.T) {
	for _, ipStr := range []string{"169.254.169.254", "::ffff:169.254.169.254", "fe80::1"} {
		ip := net.ParseIP(ipStr)
		if !httpsafe.IsBlockedIPForEndpoint(ip, true) {
			t.Errorf("metadata/link-local %s must be blocked even with the instance cap on", ipStr)
		}
	}
}

// resolveModels for OLLAMA lists against the workspace ENDPOINT_URL via the
// (injected) workspace lister, and fails open to the server-global path when
// that lister errors.
func TestResolveModels_OllamaWorkspaceEndpoint(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Seed a workspace ENDPOINT_URL credential.
	enc, _ := encryption.Encrypt("http://ws-ollama.internal:11434/v1")
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-ep', ?, 'ep', ?, 'ENDPOINT_URL', 'OLLAMA', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, enc, userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := NewModelsHandler(db, newTestLogger(), "http://server-global:11434")

	// Happy path: workspace lister is used with the endpoint's base URL.
	var gotURL string
	h.workspaceOllamaLister = func(baseURL string) (llm.ModelLister, bool) {
		gotURL = baseURL
		return &fakeLister{models: []llm.ModelInfo{{ID: "ollama/ws-model"}}}, true
	}
	models, source := h.resolveModels(context.Background(), wsID, "OLLAMA")
	if gotURL != "http://ws-ollama.internal:11434/v1" {
		t.Errorf("workspace lister built with %q, want the ENDPOINT_URL", gotURL)
	}
	if source != "live" || len(models) != 1 || models[0].ID != "ollama/ws-model" {
		t.Errorf("got %v/%q, want the workspace endpoint's live model", models, source)
	}

	// Fail-open: workspace lister errors → falls back to the server-global path.
	h.workspaceOllamaLister = func(string) (llm.ModelLister, bool) {
		return &fakeLister{err: errLiveDown}, true
	}
	var globalURL string
	h.buildLister = func(provider, apiKey, ollamaURL string) (llm.ModelLister, bool) {
		globalURL = ollamaURL
		return &fakeLister{models: []llm.ModelInfo{{ID: "ollama/global"}}}, true
	}
	models, source = h.resolveModels(context.Background(), wsID, "OLLAMA")
	if globalURL != "http://server-global:11434" {
		t.Errorf("fail-open should use the server-global URL, built with %q", globalURL)
	}
	if source != "live" || len(models) != 1 || models[0].ID != "ollama/global" {
		t.Errorf("fail-open got %v/%q, want the server-global model", models, source)
	}
}
