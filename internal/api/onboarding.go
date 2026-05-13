package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/services"
)

var dashRegex = regexp.MustCompile(`-+`)

type llmProviderInfo struct {
	provider   string
	envVarName string
}

// resolveLLMProvider maps a user-supplied provider string to its canonical
// (provider, envVarName) pair. Mirrors the validLLMProviders registry in
// internal/api/agents.go (and lib/cli-adapters.ts on the frontend) — the
// accepted set MUST stay aligned across onboarding, agent create, and
// agent update; otherwise users hit the "wizard accepted my provider but
// the agent endpoint rejects it" inconsistency CR called out.
//
// Empty input yields the Anthropic default (matches the wizard's
// pre-multi-CLI behaviour for the unset case). Explicit but unknown
// values return ok=false so the caller can return 400 instead of silently
// provisioning the wrong provider — that bug used to store keys under
// ANTHROPIC_API_KEY for any typo'd provider value, masking the real
// failure until first agent run.
//
// OLLAMA is a special case: there is no API key (local models, no auth)
// so envVarName is "" — credential creation is skipped by the onboarding
// service when envVarName is empty.
func resolveLLMProvider(provider string) (llmProviderInfo, bool) {
	switch strings.ToUpper(strings.TrimSpace(provider)) {
	case "":
		return llmProviderInfo{provider: "ANTHROPIC", envVarName: "ANTHROPIC_API_KEY"}, true
	case "ANTHROPIC":
		return llmProviderInfo{provider: "ANTHROPIC", envVarName: "ANTHROPIC_API_KEY"}, true
	case "OPENAI":
		return llmProviderInfo{provider: "OPENAI", envVarName: "OPENAI_API_KEY"}, true
	case "GOOGLE":
		return llmProviderInfo{provider: "GOOGLE", envVarName: "GOOGLE_API_KEY"}, true
	case "CURSOR":
		return llmProviderInfo{provider: "CURSOR", envVarName: "CURSOR_API_KEY"}, true
	case "FACTORY":
		return llmProviderInfo{provider: "FACTORY", envVarName: "FACTORY_API_KEY"}, true
	case "OLLAMA":
		return llmProviderInfo{provider: "OLLAMA", envVarName: ""}, true
	default:
		return llmProviderInfo{}, false
	}
}

// OnboardingHandler guides new users through workspace setup (runtime detection, crew creation).
type OnboardingHandler struct {
	db     *sql.DB
	svc    *services.OnboardingService
	logger *slog.Logger
}

// NewOnboardingHandler creates an OnboardingHandler with the given dependencies.
func NewOnboardingHandler(db *sql.DB, svc *services.OnboardingService, logger *slog.Logger) *OnboardingHandler {
	return &OnboardingHandler{db: db, svc: svc, logger: logger}
}

// Status returns whether the current user has completed onboarding.
// GET /api/v1/onboarding/status
func (h *OnboardingHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var completed bool
	err := h.db.QueryRowContext(r.Context(),
		"SELECT onboarding_completed FROM users WHERE id = ?", user.ID).Scan(&completed)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "User not found"})
		return
	}
	if err != nil {
		h.logger.Error("query onboarding status", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Smart detect: if user already has agents (e.g. provisioned via CLI), treat as completed
	if !completed {
		var agentCount int
		err = h.db.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM agents a
			JOIN workspace_members wm ON wm.workspace_id = a.workspace_id
			WHERE wm.user_id = ? AND a.deleted_at IS NULL
		`, user.ID).Scan(&agentCount)
		if err == nil && agentCount > 0 {
			completed = true
			// Persist the flag so we don't re-query next time
			if _, err := h.db.ExecContext(r.Context(),
				"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ?",
				time.Now().UTC().Format(time.RFC3339), user.ID); err != nil {
				h.logger.Warn("persist onboarding flag", "error", err, "user_id", user.ID)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"completed": completed,
	})
}

// Complete marks onboarding as finished for the current user.
// POST /api/v1/onboarding/complete
func (h *OnboardingHandler) Complete(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ?",
		time.Now().UTC().Format(time.RFC3339), user.ID)
	if err != nil {
		h.logger.Error("complete onboarding", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	rows, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("complete onboarding rows affected", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if rows == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "User not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
}

type onboardingSetupRequest struct {
	WorkspaceName   string `json:"workspace_name"`
	CrewName        string `json:"crew_name"`
	AgentName       string `json:"agent_name"`
	CliAdapter      string `json:"cli_adapter"`
	LlmProvider     string `json:"llm_provider"`
	LlmModel        string `json:"llm_model"`
	CredentialName  string `json:"credential_name"`
	CredentialValue string `json:"credential_value"`
	// CrewTemplateSlug, when non-empty, branches the setup into the
	// "deploy a full crew from a builtin template" path. The template
	// supplies all agent metadata (names, roles, system prompts,
	// LLM model defaults), so CrewName / AgentName / CliAdapter are
	// optional in this mode. Slug must match a row in crew_templates
	// where is_builtin = 1 (or workspace-scoped).
	CrewTemplateSlug string `json:"crew_template_slug"`
	// PairingMode signals the user picked "Pair my local CLI" in
	// step 3 of the wizard. The setup still creates the workspace +
	// (optionally) the templated crew, but skips credential creation
	// — the CLI redeem flow lands the auth via a separate cli_token
	// row, not as a workspace credential.
	PairingMode bool `json:"pairing_mode"`
	// PreferredLanguage is what the user picked in the workspace
	// step. Stored as workspaces.preferred_language so the
	// orchestrator can inject it into every agent's system prompt
	// (see internal/api/assignments_run.go). Free-form text so the
	// orchestrator can pass it through verbatim ("Czech", "English",
	// "Português", etc.) without us maintaining an ISO-code map.
	PreferredLanguage string `json:"preferred_language"`
}

var slugRegex = regexp.MustCompile(`[^a-z0-9-]`)

func makeSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRegex.ReplaceAllString(s, "-")
	s = dashRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "default"
	}
	return s
}

// Setup handles the onboarding wizard submission (crew creation, agent provisioning, credential setup).
// POST /api/v1/onboarding/setup
func (h *OnboardingHandler) Setup(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var req onboardingSetupRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Get user's first workspace
	var workspaceID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT wm.workspace_id FROM workspace_members wm
		WHERE wm.user_id = ? ORDER BY wm.created_at ASC LIMIT 1
	`, user.ID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No workspace found for user"})
		return
	}
	if err != nil {
		h.logger.Error("find workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Persist preferred_language for both forks (template + blank).
	// Doing it before the branch means the orchestrator gets the
	// setting regardless of which crew shape the user picked, and a
	// failed write here doesn't take down the rest of the flow.
	if lang := strings.TrimSpace(req.PreferredLanguage); lang != "" {
		if _, err := h.db.ExecContext(r.Context(),
			"UPDATE workspaces SET preferred_language = ?, updated_at = ? WHERE id = ?",
			lang, time.Now().UTC().Format(time.RFC3339), workspaceID); err != nil {
			h.logger.Warn("set preferred_language", "error", err)
		}
	}

	// CLI-token-only contract: reject raw API keys with a clear
	// message that points users at `claude setup-token` etc. The
	// orchestrator's OAuth CONNECT-tunnel auth mode can't use a
	// plain sk-ant-api… key — agents in the container can't see
	// any on-disk credentials and Claude Code refuses to start.
	if err := validateOnboardingCredential(req.LlmProvider, req.CredentialValue); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Template branch — when crew_template_slug is set, the wizard
	// deploys a full crew (multiple agents, system prompts, model
	// defaults) from a builtin template. Single-agent fields
	// (CrewName, AgentName, CliAdapter) become optional inputs.
	if strings.TrimSpace(req.CrewTemplateSlug) != "" {
		h.setupFromTemplate(w, r, user.ID, workspaceID, req)
		return
	}

	// Blank / single-agent branch — preserves the pre-template
	// onboarding shape so users who pick "Start blank" still get a
	// workable initial agent. CrewName + AgentName are required here.
	if req.CrewName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_name is required"})
		return
	}
	if req.AgentName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_name is required"})
		return
	}

	cliAdapter := req.CliAdapter
	if cliAdapter == "" {
		cliAdapter = "CLAUDE_CODE"
	}
	llm, ok := resolveLLMProvider(req.LlmProvider)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "llm_provider must be ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, or OLLAMA"})
		return
	}

	// IMPORTANT: pair mode does NOT skip credential creation any
	// more. The CLI token from /pair/redeem authenticates the user
	// to crewshipd; it does NOT give the agents in containers a way
	// to call Claude. Agents always need a workspace-scoped provider
	// credential (Anthropic key, OpenAI key, etc.) — that's
	// independent of how the user drives Crewship.
	//
	// Previously this branch zeroed CredentialValue when
	// pairing_mode=true. The result was a workspace full of agents
	// with env_var_name set but no row in agent_credentials —
	// agents booted, hit Claude, got "ANTHROPIC_API_KEY missing",
	// and went silent. We caught this on the first end-to-end smoke.
	credentialValue := req.CredentialValue
	credName := req.CredentialName
	if credName == "" && credentialValue != "" {
		credName = "API Key"
	}

	result, err := h.svc.Setup(r.Context(), services.SetupParams{
		UserID:          user.ID,
		WorkspaceID:     workspaceID,
		WorkspaceName:   req.WorkspaceName,
		CrewName:        req.CrewName,
		CrewSlug:        makeSlug(req.CrewName),
		AgentName:       req.AgentName,
		AgentSlug:       makeSlug(req.AgentName),
		CliAdapter:      cliAdapter,
		LLMProvider:     llm.provider,
		LLMModel:        stringPtr(req.LlmModel),
		EnvVarName:      llm.envVarName,
		CredentialName:  credName,
		CredentialValue: credentialValue,
		Now:             time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		switch {
		case errors.Is(err, services.ErrOnboardingAlreadyCompleted):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Onboarding already completed"})
		case errors.Is(err, services.ErrWorkspaceNotFound):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No workspace found for user"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		}
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// setupFromTemplate handles the crew-template path of onboarding.
// Atomically claims onboarding (CAS guard like the service-layer
// path), renames the workspace if requested, deploys the template
// via the shared deployCrewTemplate helper (which seeds the full
// agent roster and auto-assigns workspace credentials), and stores
// an Anthropic credential when the user pasted an API key.
func (h *OnboardingHandler) setupFromTemplate(w http.ResponseWriter, r *http.Request, userID, workspaceID string, req onboardingSetupRequest) {
	now := time.Now().UTC().Format(time.RFC3339)

	// CAS guard: only proceed if onboarding hasn't been claimed yet.
	// Race-safe equivalent of services.OnboardingService.Setup's
	// guard. The template branch lives in the handler (not the
	// service) because crew_templates loading + deployment is itself
	// a handler-level helper already.
	guardRes, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ? AND onboarding_completed = 0",
		now, userID)
	if err != nil {
		h.logger.Error("onboarding template: lock", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	rows, _ := guardRes.RowsAffected()
	if rows == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Onboarding already completed"})
		return
	}

	if strings.TrimSpace(req.WorkspaceName) != "" {
		if _, err := h.db.ExecContext(r.Context(),
			"UPDATE workspaces SET name = ?, updated_at = ? WHERE id = ?",
			req.WorkspaceName, now, workspaceID); err != nil {
			h.logger.Error("onboarding template: update workspace name", "error", err)
			// Soft failure: continue to template deployment so a
			// failed rename doesn't abort the whole onboarding.
		}
	}
	// (preferred_language is set in the parent Setup() before this
	// fork — applies to both template + blank paths.)

	// Always store the API key when one was provided, regardless of
	// pairing mode. The CLI token from /pair/redeem authenticates
	// the user to crewshipd, but agents in containers still need a
	// workspace-scoped provider credential to actually call Claude.
	// Storing BEFORE deploy lets the template's auto-assign hook
	// wire it onto every freshly-created agent in one step.
	if strings.TrimSpace(req.CredentialValue) != "" {
		credName := req.CredentialName
		if credName == "" {
			credName = "API Key"
		}
		// Default the provider to ANTHROPIC; the wizard collects the
		// matching adapter so the credential maps onto the right
		// env var by the template's deploy hook.
		llm, _ := resolveLLMProvider(req.LlmProvider)
		if llm.provider == "" {
			llm.provider = "ANTHROPIC"
			llm.envVarName = "ANTHROPIC_API_KEY"
		}
		if err := insertOnboardingCredential(r.Context(), h.db, userID, workspaceID, credName, llm.provider, llm.envVarName, req.CredentialValue, now); err != nil {
			h.logger.Error("onboarding template: store credential", "error", err)
			// Continue — template deploys but with no creds, the
			// auto-assign hook emits credential.auto_assign_empty
			// into the journal so the operator can see why.
		}
	}

	// Make sure the builtin templates are seeded — the List handler
	// does this lazily on first call, but a user who hits the wizard
	// before ever loading the templates page (i.e. signs up → walks
	// straight into onboarding) would otherwise face an empty
	// crew_templates table and a 400 "Unknown crew template". Seeding
	// is idempotent (UPSERT on slug) so calling it on every wizard
	// submission is cheap and removes the ordering dependency.
	if err := database.SeedBuiltinCrewTemplates(r.Context(), h.db, h.logger); err != nil {
		h.logger.Warn("onboarding template: seed builtin templates", "error", err)
	}

	// Default the crew display name to the template's name when the
	// user didn't supply one. The deploy helper accepts an empty
	// crew slug input and derives it from the name.
	crewName := strings.TrimSpace(req.CrewName)
	if crewName == "" {
		// Look up template's display name for a sane default.
		_ = h.db.QueryRowContext(r.Context(),
			"SELECT name FROM crew_templates WHERE slug = ? AND (is_builtin = 1 OR workspace_id = ?)",
			req.CrewTemplateSlug, workspaceID).Scan(&crewName)
		if crewName == "" {
			crewName = req.CrewTemplateSlug
		}
	}

	result, err := deployCrewTemplate(r.Context(), h.db, h.logger, noopEmitter{}, workspaceID, req.CrewTemplateSlug, crewName, "")
	if err != nil {
		// Roll back the onboarding_completed=1 flag we claimed above
		// so the user can retry the wizard (with a corrected
		// template slug, for example) instead of being stuck in a
		// half-finished state forever.
		if _, rbErr := h.db.ExecContext(r.Context(),
			"UPDATE users SET onboarding_completed = 0, updated_at = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339), userID); rbErr != nil {
			h.logger.Error("onboarding template: rollback completion flag", "error", rbErr, "deploy_error", err)
		}
		switch {
		case errors.Is(err, errTemplateNotFound):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unknown crew template"})
		case errors.Is(err, errCrewSlugConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			h.logger.Error("onboarding template: deploy", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		}
		return
	}

	// Return the same shape as the single-agent path so the frontend
	// can route in one place. AgentID points to the first agent in
	// the deployed roster (typically the lead).
	var firstAgentID string
	if len(result.AgentIDs) > 0 {
		firstAgentID = result.AgentIDs[0]
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"workspace_id": workspaceID,
		"crew_id":      result.CrewID,
		"agent_id":     firstAgentID,
		"agent_ids":    result.AgentIDs,
		"agent_count":  result.AgentCount,
	})
}

// insertOnboardingCredential stores a workspace-scoped CLI token the
// user pasted during onboarding. Same shape as the row that
// internal/services/onboarding.go produces in the blank path, so the
// auto-assign hook called by deployCrewTemplate finds it.
//
// Onboarding only accepts CLI tokens (output of `claude setup-token`
// etc.), never raw API keys. The wizard UI says so explicitly and
// validateCliToken() upstream rejects the call when the prefix
// doesn't match. Always stored as AI_CLI_TOKEN.
func insertOnboardingCredential(ctx context.Context, db *sql.DB, userID, workspaceID, name, provider, _ /*envVarName*/, value, now string) error {
	encrypted, err := encryption.Encrypt(value)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'AI_CLI_TOKEN', ?, 'WORKSPACE', ?, ?, ?)`,
		generateCUID(), workspaceID, name, encrypted, provider, userID, now, now); err != nil {
		return fmt.Errorf("insert credential: %w", err)
	}
	return nil
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// validateOnboardingCredential rejects the obvious wrong-shape values
// (most importantly: a raw Anthropic API key when the runtime expects
// a Claude Code OAuth token). Empty value is allowed — pair mode and
// users who skip credential setup land here without a value to check.
//
// Two layers of checking:
//
//  1. Shape check: cheap regex / prefix gate that catches the
//     "pasted the wrong thing from the wrong page" case before we
//     even bother the upstream API.
//  2. Live probe: a real authenticated request to the provider's
//     API with the token. Catches "right shape, wrong / expired
//     token" — the case that was leaving users with broken chat
//     and silent empty bubbles. Optional via the `probe` flag so
//     tests can skip it.
//
// Per-provider checks are intentionally narrow: we only reject shapes
// we know will fail downstream, never anything ambiguous. A future
// adapter whose token shape we don't yet recognise falls through.
func validateOnboardingCredential(provider, value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(provider)) {
	case "ANTHROPIC":
		// Claude Code OAuth tokens look like `sk-ant-oat01-…`. Raw
		// account-level API keys start with `sk-ant-api…` and are
		// what users mistakenly grab from console.anthropic.com.
		// Reject the latter loudly with a fix-it pointer.
		if strings.HasPrefix(v, "sk-ant-api") {
			return errors.New(
				"That looks like a raw Anthropic API key (sk-ant-api…). " +
					"Crewship onboarding needs a Claude Code CLI token instead — " +
					"run `claude setup-token` on your machine and paste the resulting " +
					"sk-ant-oat… value here.",
			)
		}
		if !strings.HasPrefix(v, "sk-ant-oat") {
			return errors.New(
				"This doesn't look like a Claude Code CLI token (expected prefix `sk-ant-oat`). " +
					"Run `claude setup-token` on your machine and paste the resulting value.",
			)
		}
		return probeAnthropicOAuthToken(v)
	}
	return nil
}

// probeAnthropicOAuthToken hits api.anthropic.com with the token to
// confirm the upstream actually accepts it. The whole point of this
// check is to fail the onboarding submit BEFORE the user spends
// 5 minutes wondering why chat goes silent — see the
// CLAUDE_CODE_OAUTH_TOKEN regression we caught the hard way.
//
// Uses /v1/messages with max_tokens=1 — cheapest call that still
// exercises the auth path. 401 → token bad. Anything 2xx → token
// works. Other failures (network, 5xx) are treated as soft — we
// log + accept rather than block onboarding on Anthropic flaking.
func probeAnthropicOAuthToken(token string) error {
	const url = "https://api.anthropic.com/v1/messages"
	const body = `{"model":"claude-3-5-haiku-latest","max_tokens":1,"messages":[{"role":"user","content":"ok"}]}`
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		// Caller never sees this — request construction is local.
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Network blip / DNS issue / Anthropic outage. Don't block
		// the user — the runtime will surface a real error if the
		// token still doesn't work when chat runs.
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New(
			"Anthropic rejected your CLI token (401). The most common cause is pasting just part of the value " +
				"or copying from a page that truncates it — run `claude setup-token` again and paste the entire " +
				"sk-ant-oat… string in one go.",
		)
	}
	return nil
}
