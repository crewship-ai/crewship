package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/llm"
)

// ModelsHandler answers GET /api/v1/models?provider=<PROVIDER>, enumerating the
// models a provider can serve. It resolves the workspace's active credential
// for that provider, builds the matching llm.Provider, and — if that provider
// implements llm.ModelLister — lists live. On any failure (no credential, the
// provider can't list, or the live call errors) it falls back to the curated
// model set (internal/llm/models_curated.go). OLLAMA has no curated set: its
// models are whatever the local daemon has pulled, so a live failure there is
// terminal.
type ModelsHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	ollamaURL string
	// buildLister constructs a ModelLister for (provider, apiKey). It is a
	// field so tests can inject a fake without standing up real Anthropic /
	// OpenAI / Ollama HTTP clients. ok=false means "this provider has no
	// live-listing path" — the caller then uses the curated set.
	buildLister func(provider, apiKey, ollamaURL string) (llm.ModelLister, bool)
}

// modelsListResponse is the GET /api/v1/models payload.
type modelsListResponse struct {
	Provider string          `json:"provider"`
	Source   string          `json:"source"` // "live" or "curated"
	Models   []llm.ModelInfo `json:"models"`
}

// supportedModelProviders is the set of provider identifiers /api/v1/models
// will answer for. Mirrors validLLMProviders but only the ones that have
// either a live lister or a curated fallback — CURSOR / FACTORY route through
// CLI adapters and have no model-discovery surface of their own.
var supportedModelProviders = map[string]bool{
	"ANTHROPIC": true,
	"OPENAI":    true,
	"GOOGLE":    true,
	"OLLAMA":    true,
}

// NewModelsHandler builds a ModelsHandler. ollamaURL is the daemon URL used
// when listing OLLAMA models (no credential required); pass "" to disable
// OLLAMA live discovery.
func NewModelsHandler(db *sql.DB, logger *slog.Logger, ollamaURL string) *ModelsHandler {
	return &ModelsHandler{
		db:          db,
		logger:      logger,
		ollamaURL:   ollamaURL,
		buildLister: defaultModelLister,
	}
}

// defaultModelLister wires the concrete providers. OPENAI / ANTHROPIC need an
// API key; OLLAMA needs only the daemon URL.
func defaultModelLister(provider, apiKey, ollamaURL string) (llm.ModelLister, bool) {
	switch provider {
	case "ANTHROPIC":
		return llm.NewAnthropic(apiKey), true
	case "OPENAI":
		return llm.NewOpenAI(apiKey), true
	case "OLLAMA":
		if ollamaURL == "" {
			return nil, false
		}
		return llm.NewOllama(ollamaURL, ""), true
	default:
		// GOOGLE and anything else have no live lister yet — curated only.
		return nil, false
	}
}

// List handles GET /api/v1/models?provider=<PROVIDER>.
func (h *ModelsHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	provider := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("provider")))
	if provider == "" {
		writeProblem(w, r, http.StatusBadRequest, "provider query parameter is required")
		return
	}
	if !supportedModelProviders[provider] {
		writeProblem(w, r, http.StatusBadRequest,
			"unsupported provider; expected one of ANTHROPIC, OPENAI, GOOGLE, OLLAMA")
		return
	}

	models, source := h.resolveModels(r.Context(), wsID, provider)

	// OLLAMA has no curated fallback. An empty live result with no curated
	// backstop is a real failure state worth surfacing distinctly so callers
	// don't silently treat "daemon unreachable" as "no models installed".
	if provider == "OLLAMA" && source == "curated" && len(models) == 0 {
		writeProblem(w, r, http.StatusBadGateway,
			"could not reach the Ollama daemon to list models")
		return
	}

	writeJSON(w, http.StatusOK, modelsListResponse{
		Provider: provider,
		Source:   source,
		Models:   models,
	})
}

// resolveModels returns the model set and the source ("live"|"curated").
// It never returns an error: any failure degrades to the curated fallback.
// The returned slice is always non-nil (CuratedModels may yield an empty set,
// e.g. for OLLAMA — the caller distinguishes that case), so the response
// always serializes "models" as a JSON array rather than null.
func (h *ModelsHandler) resolveModels(ctx context.Context, wsID, provider string) ([]llm.ModelInfo, string) {
	apiKey, err := h.activeCredential(ctx, wsID, provider)
	// OLLAMA needs no credential; everything else does to list live.
	if err != nil && provider != "OLLAMA" {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("models: credential lookup failed", "provider", provider, "error", err)
		}
		return curatedOrEmpty(provider), "curated"
	}

	lister, ok := h.buildLister(provider, apiKey, h.ollamaURL)
	if !ok {
		return curatedOrEmpty(provider), "curated"
	}

	live, err := lister.ListModels(ctx)
	if err != nil {
		h.logger.Warn("models: live listing failed, using curated fallback",
			"provider", provider, "error", err)
		return curatedOrEmpty(provider), "curated"
	}
	return live, "live"
}

// curatedOrEmpty returns the curated set for a provider, normalising a nil
// (no curated list, e.g. OLLAMA) to an empty non-nil slice so callers never
// have to nil-guard and the JSON response is always an array.
func curatedOrEmpty(provider string) []llm.ModelInfo {
	if m := llm.CuratedModels(provider); m != nil {
		return m
	}
	return []llm.ModelInfo{}
}

// activeCredential returns the decrypted value of the workspace's first active
// API_KEY credential for the given provider, or sql.ErrNoRows when none exists.
func (h *ModelsHandler) activeCredential(ctx context.Context, wsID, provider string) (string, error) {
	var encryptedValue string
	err := h.db.QueryRowContext(ctx, `
		SELECT encrypted_value FROM credentials
		WHERE workspace_id = ?
		  AND provider = ?
		  AND type = 'API_KEY'
		  AND status = 'ACTIVE'
		  AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1`, wsID, provider).Scan(&encryptedValue)
	if err != nil {
		return "", err
	}
	return encryption.Decrypt(encryptedValue)
}

// providerModelIDs returns the set of valid model IDs for a provider in a
// workspace, drawn from the same live-or-curated resolution the HTTP endpoint
// uses. Returned ok=false means the set could not be determined (no live
// lister and no curated set, e.g. an OLLAMA daemon that's unreachable) — the
// caller should then NOT reject an unknown model, since it cannot prove the
// model invalid. Used by the agent update path to validate llm_model.
func (h *ModelsHandler) providerModelIDs(ctx context.Context, wsID, provider string) (map[string]bool, bool) {
	provider = strings.ToUpper(strings.TrimSpace(provider))
	if !supportedModelProviders[provider] {
		return nil, false
	}
	models, _ := h.resolveModels(ctx, wsID, provider)
	if len(models) == 0 {
		return nil, false
	}
	set := make(map[string]bool, len(models))
	for _, m := range models {
		set[m.ID] = true
	}
	return set, true
}
