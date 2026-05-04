package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/skills"
)

// SkillGenerateHandler exposes POST /api/v1/workspaces/{wsID}/skills/generate
// which calls Anthropic with a stripped-down skill-creator prompt and writes
// the result back as a fresh row with source='GENERATED'. The user can then
// edit the body via the skills detail page or re-import a hand-tuned version.
type SkillGenerateHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSkillGenerateHandler wires the handler against an open *sql.DB. Workspace
// resolution and credential decryption are done per-request to honour the
// "credentials are workspace-scoped" rule from the keeper architecture notes.
func NewSkillGenerateHandler(db *sql.DB, logger *slog.Logger) *SkillGenerateHandler {
	return &SkillGenerateHandler{db: db, logger: logger}
}

type skillGenerateRequest struct {
	Slug   string `json:"slug"`
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
}

type skillGenerateResponse struct {
	SkillID    string `json:"skill_id"`
	Slug       string `json:"slug"`
	Content    string `json:"content"`
	ScanStatus string `json:"scan_status"`
	ScanReason string `json:"scan_reason,omitempty"`
	Quality    string `json:"description_quality,omitempty"`
}

// skillCreatorSystemPrompt is a condensed adaptation of Anthropic's
// skill-creator stage-2 instructions (github.com/anthropics/skills/skills/
// skill-creator). The condensed form preserves the essentials — description
// must begin with a trigger phrase, body uses the canonical sections, output
// is JUST the SKILL.md — without dragging in eval-loop scaffolding that v0.1
// can't run server-side anyway. Multi-stage interactive authoring (with
// sample test prompts and qualitative review) is parked for v0.2.
const skillCreatorSystemPrompt = `You write SKILL.md files for AI coding agents.

OUTPUT REQUIREMENTS:
- Output ONLY the SKILL.md content, no preamble or commentary.
- Begin with YAML frontmatter delimited by --- on its own line.
- Frontmatter MUST contain: name (kebab-case slug), description (one line, ≤1024 chars).
- Frontmatter MAY contain: license, tags (yaml list), credential_requirements (yaml list of env-var names).
- description is THE field that controls when the skill activates — it MUST begin with one of:
  "Use when ...", "Use this when ...", "Useful for ...", "Useful when ...", "To <verb> ...".
  Bad description: "Helps with PDFs"  →  Good: "Use when the user asks to extract tables, forms, or text from PDF files."
- Description must NOT exceed one paragraph; no newlines.

BODY STRUCTURE:
- Markdown after the closing --- delimiter.
- Sections (use ## headings): When to use, Inputs, Steps, Output format, Guardrails, Verification.
- Total body ≤500 lines. Be concise — every line costs the model context.
- Do NOT include backtick-prefixed dynamic-context blocks (!"cmd"); they will be stripped.
- Do NOT reference external scripts/ or references/ folders unless you also bundle them.

QUALITY:
- Concrete, specific instructions. Avoid generic boilerplate ("be helpful", "do your best").
- Include at least one negative example or guardrail.
- If the skill needs credentials, list the env-var names in frontmatter credential_requirements.

You will receive the user's intent in the next message. Respond with the SKILL.md.`

// Generate handles the request, calls Anthropic, validates the output, and
// upserts it into the skills table. Errors are surfaced verbatim because the
// CLI is the primary caller and a precise message saves a round-trip.
func (h *SkillGenerateHandler) Generate(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspaceId")
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	var body skillGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Slug = strings.TrimSpace(body.Slug)
	body.Prompt = strings.TrimSpace(body.Prompt)
	if body.Slug == "" || body.Prompt == "" {
		writeProblem(w, r, http.StatusBadRequest, "slug and prompt are required")
		return
	}
	body.Slug = skills.Slugify(body.Slug)

	model := body.Model
	if model == "" {
		// Sonnet is the v0.1 default — fast enough for an interactive CLI
		// command, smart enough to write a usable SKILL.md without the
		// Opus tax.
		model = "claude-sonnet-4-6"
	}

	provider, err := h.resolveAnthropicProvider(r.Context(), wsID)
	if err != nil {
		if errors.Is(err, errNoActiveAnthropicCredential) {
			writeProblem(w, r, http.StatusPreconditionFailed,
				"workspace has no active Anthropic API key — add one under /settings/credentials and retry")
			return
		}
		h.logger.Error("resolve anthropic provider", "error", err, "ws_id", wsID)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	resp, err := provider.Complete(ctx, llm.Request{
		Model:     model,
		System:    skillCreatorSystemPrompt,
		MaxTokens: 4096,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("Slug: %s\n\nIntent:\n%s", body.Slug, body.Prompt),
		}},
	})
	if err != nil {
		h.logger.Warn("skill generate llm call", "error", err, "slug", body.Slug)
		writeProblem(w, r, http.StatusBadGateway, "skill generation failed: "+err.Error())
		return
	}

	rawSkill := strings.TrimSpace(resp.Content)
	if !strings.HasPrefix(rawSkill, "---") {
		writeProblem(w, r, http.StatusBadGateway,
			"generated content does not start with YAML frontmatter — model output: "+truncateForError(rawSkill, 200))
		return
	}

	parsed, err := skills.ParseSKILLMD(rawSkill)
	if err != nil {
		writeProblem(w, r, http.StatusBadGateway, "generated SKILL.md failed parser: "+err.Error())
		return
	}
	// The parser slugifies the name from frontmatter; force it back to
	// the user's requested slug so the UI / DB key matches what the CLI
	// asked for.
	parsed.Meta.Name = body.Slug

	scan := skills.ScanContent(parsed.Content)

	skillID := generateGeneratedSkillID()
	now := time.Now().UTC().Format(time.RFC3339)

	tagsJSON := "[]"
	if len(parsed.Meta.Tags) > 0 {
		if b, err := json.Marshal(parsed.Meta.Tags); err == nil {
			tagsJSON = string(b)
		}
	}
	credReqJSON := "[]"
	if len(parsed.Meta.CredentialRequirements) > 0 {
		if b, err := json.Marshal(parsed.Meta.CredentialRequirements); err == nil {
			credReqJSON = string(b)
		}
	}

	descQuality := parsed.DescriptionQuality
	if scan.Status == "FLAGGED" {
		descQuality = scan.Reason
	}

	displayName := parsed.Meta.DisplayName
	if displayName == "" {
		displayName = body.Slug
	}

	_, insertErr := h.db.ExecContext(r.Context(), `
		INSERT INTO skills (
			id, name, slug, display_name, description, version, author,
			category, source, content, tags, credential_requirements,
			vendor, spdx_license, runtime, maturity, scan_status,
			description_quality, license,
			created_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, 'GENERATED', ?, ?, ?,
			'workspace', NULL, 'INSTRUCTIONS', 'EXPERIMENTAL', ?,
			?, ?,
			?, ?
		)`,
		skillID, body.Slug, body.Slug, displayName,
		nullableStrIfc(parsed.Meta.Description), firstNonEmpty(parsed.Meta.Version, "0.1.0"),
		nullableStrIfc(parsed.Meta.Author),
		firstNonEmpty(parsed.Meta.Category, "CUSTOM"), parsed.Content, tagsJSON, credReqJSON,
		scan.Status,
		nullableStrIfc(descQuality), nullableStrIfc(parsed.Meta.License),
		now, now,
	)
	if insertErr != nil {
		h.logger.Error("insert generated skill", "error", insertErr, "slug", body.Slug)
		writeProblem(w, r, http.StatusConflict, "could not insert generated skill — slug may already exist: "+insertErr.Error())
		return
	}

	writeJSON(w, http.StatusCreated, skillGenerateResponse{
		SkillID:    skillID,
		Slug:       body.Slug,
		Content:    rawSkill,
		ScanStatus: scan.Status,
		ScanReason: scan.Reason,
		// Echo the description_quality value we actually persisted —
		// when scan flagged the skill we replaced descQuality with the
		// scan reason; the client must see the same string so
		// re-fetches don't show a different value than the create-time
		// response.
		Quality: nullableString(descQuality),
	})
}

func nullableString(v interface{}) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func (h *SkillGenerateHandler) resolveAnthropicProvider(ctx context.Context, wsID string) (llm.Provider, error) {
	var encryptedValue string
	err := h.db.QueryRowContext(ctx, `
		SELECT encrypted_value FROM credentials
		WHERE workspace_id = ?
		  AND provider = 'ANTHROPIC'
		  AND type = 'API_KEY'
		  AND status = 'ACTIVE'
		  AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1`, wsID).Scan(&encryptedValue)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errNoActiveAnthropicCredential
		}
		return nil, fmt.Errorf("query credential: %w", err)
	}
	plain, err := encryption.Decrypt(encryptedValue)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	return llm.NewAnthropic(plain), nil
}

func generateGeneratedSkillID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("skills.generate: crypto/rand unavailable: " + err.Error())
	}
	return "sk_" + hex.EncodeToString(b)
}

func nullableStrIfc(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
