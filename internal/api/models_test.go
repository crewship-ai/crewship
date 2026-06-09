package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/llm"
)

// fakeLister is an injectable llm.ModelLister for the models endpoint tests.
type fakeLister struct {
	models []llm.ModelInfo
	err    error
}

func (f *fakeLister) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return f.models, f.err
}

// newModelsHandler builds a ModelsHandler over a fresh DB with a seeded
// owner+workspace and an injected buildLister.
func newModelsHandler(t *testing.T, build func(provider, apiKey, ollamaURL string) (llm.ModelLister, bool)) (*ModelsHandler, string, string) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewModelsHandler(db, newTestLogger(), "http://ollama.test:11434")
	if build != nil {
		h.buildLister = build
	}
	return h, wsID, userID
}

func seedProviderCredential(t *testing.T, db *sql.DB, wsID, userID, provider, value string) {
	t.Helper()
	enc, err := encryption.Encrypt(value)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'API_KEY', ?, 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		"cred-"+provider, wsID, "key-"+provider, enc, provider, userID)
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

func doModelsList(t *testing.T, h *ModelsHandler, wsID, query string) (*httptest.ResponseRecorder, modelsListResponse) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/models"+query, nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	var body modelsListResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, rr.Body.String())
		}
	}
	return rr, body
}

func TestModelsList_BadRequest(t *testing.T) {
	h, wsID, _ := newModelsHandler(t, nil)

	t.Run("missing provider", func(t *testing.T) {
		rr, _ := doModelsList(t, h, wsID, "")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unsupported provider", func(t *testing.T) {
		rr, _ := doModelsList(t, h, wsID, "?provider=COHERE")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rr.Code)
		}
	})
}

func TestModelsList_Live(t *testing.T) {
	build := func(provider, apiKey, ollamaURL string) (llm.ModelLister, bool) {
		if apiKey != "sk-live" {
			t.Errorf("apiKey = %q, want decrypted credential", apiKey)
		}
		return &fakeLister{models: []llm.ModelInfo{
			{ID: "claude-opus-4-8", Provider: "anthropic"},
		}}, true
	}
	h, wsID, userID := newModelsHandler(t, build)
	seedProviderCredential(t, h.db, wsID, userID, "ANTHROPIC", "sk-live")

	rr, body := doModelsList(t, h, wsID, "?provider=anthropic")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if body.Source != "live" {
		t.Errorf("source = %q, want live", body.Source)
	}
	if len(body.Models) != 1 || body.Models[0].ID != "claude-opus-4-8" {
		t.Errorf("models = %+v", body.Models)
	}
}

func TestModelsList_CuratedFallback(t *testing.T) {
	t.Run("no credential falls back to curated", func(t *testing.T) {
		// buildLister would build a real lister, but with no credential the
		// handler short-circuits to curated before ever calling it.
		called := false
		build := func(string, string, string) (llm.ModelLister, bool) {
			called = true
			return &fakeLister{}, true
		}
		h, wsID, _ := newModelsHandler(t, build)
		rr, body := doModelsList(t, h, wsID, "?provider=ANTHROPIC")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if body.Source != "curated" {
			t.Errorf("source = %q, want curated", body.Source)
		}
		if len(body.Models) == 0 {
			t.Errorf("expected curated models, got none")
		}
		if called {
			t.Errorf("buildLister called despite missing credential")
		}
	})

	t.Run("live error falls back to curated", func(t *testing.T) {
		build := func(string, string, string) (llm.ModelLister, bool) {
			return &fakeLister{err: errLiveDown}, true
		}
		h, wsID, userID := newModelsHandler(t, build)
		seedProviderCredential(t, h.db, wsID, userID, "ANTHROPIC", "sk-x")
		rr, body := doModelsList(t, h, wsID, "?provider=ANTHROPIC")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if body.Source != "curated" {
			t.Errorf("source = %q, want curated", body.Source)
		}
	})

	t.Run("credential decrypt error logs and falls back to curated", func(t *testing.T) {
		// A non-ErrNoRows credential failure (undecryptable ciphertext)
		// must hit the warn-log branch and still degrade to curated.
		h, wsID, userID := newModelsHandler(t, func(string, string, string) (llm.ModelLister, bool) {
			t.Fatalf("buildLister should not be reached on credential error")
			return nil, false
		})
		if _, err := h.db.Exec(`INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
			VALUES ('cred-bad', ?, 'bad', 'not-valid-ciphertext', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
			wsID, userID); err != nil {
			t.Fatalf("seed bad credential: %v", err)
		}
		rr, body := doModelsList(t, h, wsID, "?provider=ANTHROPIC")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if body.Source != "curated" {
			t.Errorf("source = %q, want curated", body.Source)
		}
	})

	t.Run("no lister (GOOGLE) uses curated", func(t *testing.T) {
		h, wsID, _ := newModelsHandler(t, nil) // default lister: GOOGLE has no live path
		rr, body := doModelsList(t, h, wsID, "?provider=GOOGLE")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if body.Source != "curated" || len(body.Models) == 0 {
			t.Errorf("GOOGLE: source=%q models=%d", body.Source, len(body.Models))
		}
	})
}

func TestModelsList_OllamaLiveAndUnreachable(t *testing.T) {
	t.Run("ollama live needs no credential", func(t *testing.T) {
		build := func(provider, apiKey, ollamaURL string) (llm.ModelLister, bool) {
			if provider != "OLLAMA" {
				t.Errorf("provider = %s", provider)
			}
			if ollamaURL == "" {
				t.Errorf("ollamaURL not passed through")
			}
			return &fakeLister{models: []llm.ModelInfo{{ID: "llama3.2", Provider: "ollama"}}}, true
		}
		h, wsID, _ := newModelsHandler(t, build)
		rr, body := doModelsList(t, h, wsID, "?provider=OLLAMA")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if body.Source != "live" || len(body.Models) != 1 {
			t.Errorf("ollama live: %+v", body)
		}
	})

	t.Run("ollama unreachable -> 502", func(t *testing.T) {
		build := func(string, string, string) (llm.ModelLister, bool) {
			return &fakeLister{err: errLiveDown}, true
		}
		h, wsID, _ := newModelsHandler(t, build)
		rr, _ := doModelsList(t, h, wsID, "?provider=OLLAMA")
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rr.Code)
		}
	})
}

// TestDefaultModelLister exercises the production provider-construction switch.
func TestDefaultModelLister(t *testing.T) {
	tests := []struct {
		provider  string
		ollamaURL string
		wantOK    bool
	}{
		{"ANTHROPIC", "", true},
		{"OPENAI", "", true},
		{"OLLAMA", "http://x:11434", true},
		{"OLLAMA", "", false},
		{"GOOGLE", "", false},
		{"NOPE", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			lister, ok := defaultModelLister(tc.provider, "k", tc.ollamaURL)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && lister == nil {
				t.Errorf("ok but nil lister")
			}
		})
	}
}

// TestProviderModelIDs covers the ModelValidator path used by agents_update.
func TestProviderModelIDs(t *testing.T) {
	h, wsID, userID := newModelsHandler(t, func(string, string, string) (llm.ModelLister, bool) {
		return &fakeLister{models: []llm.ModelInfo{{ID: "m1"}, {ID: "m2"}}}, true
	})
	seedProviderCredential(t, h.db, wsID, userID, "ANTHROPIC", "k")

	set, ok := h.providerModelIDs(context.Background(), wsID, "anthropic")
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if !set["m1"] || set["nope"] {
		t.Errorf("set = %+v", set)
	}

	// Unsupported provider -> not determinable.
	if _, ok := h.providerModelIDs(context.Background(), wsID, "COHERE"); ok {
		t.Errorf("COHERE should be undeterminable")
	}

	// Provider with empty resolved set (OLLAMA unreachable) -> undeterminable.
	hEmpty, wsE, _ := newModelsHandler(t, func(string, string, string) (llm.ModelLister, bool) {
		return &fakeLister{err: errLiveDown}, true
	})
	if _, ok := hEmpty.providerModelIDs(context.Background(), wsE, "OLLAMA"); ok {
		t.Errorf("unreachable OLLAMA should be undeterminable")
	}
}

var errLiveDown = errTest("live provider down")

type errTest string

func (e errTest) Error() string { return string(e) }
