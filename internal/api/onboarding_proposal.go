package api

// The onboarding proposal store and its apply path
// (docs/prd/conversational-onboarding.md §5.6, §8.2).
//
// Design constraint that dissolves the PRD's open questions: the setup agent
// that will eventually drive the onboarding conversation gets NO write
// permission — not an internal token scoped to a nominal crew, not anything.
// It (or, today, a plain authenticated call) produces a proposal via Create;
// only a human's click on Apply writes anything, and that click runs under
// the human's own session, gated exactly like every other crew-creating
// mutation (roleCreate, the same tier CrewTemplateHandler.Deploy requires).
//
// The integrity property (§5.6, pinned by the test in
// onboarding_proposal_test.go): the card and the mutation come from the same
// struct, captured at propose time. Apply's request body carries nothing but
// the id — there is no field in it to re-derive from, by construction. What
// Apply executes is read from the STORED payload_json column and nothing
// else; the id in the path is a lookup key, not content.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
)

// journalEntryOnboardingProposalApplied marks the one journal entry Apply
// writes. journal.EntryType is a plain string type with no central registry
// to extend (see internal/journal/types.go), so a new call site can mint its
// own constant.
const journalEntryOnboardingProposalApplied journal.EntryType = "onboarding.proposal_applied"

const (
	onboardingProposalStatusPending = "PENDING"
	onboardingProposalStatusApplied = "APPLIED"
)

var errOnboardingProposalAlreadyApplied = errors.New("onboarding proposal already applied")

// onboardingProposalAgent is one agent in a proposal's resolved roster —
// exactly the fields the created `agents` row will carry, computed at
// propose time from the named template plus the model override. Not
// agent-authored prose: every field here is a plain column value.
type onboardingProposalAgent struct {
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	RoleTitle    string `json:"role_title"`
	AgentRole    string `json:"agent_role"`
	CLIAdapter   string `json:"cli_adapter"`
	LLMProvider  string `json:"llm_provider"`
	LLMModel     string `json:"llm_model"`
	ToolProfile  string `json:"tool_profile"`
	SystemPrompt string `json:"system_prompt"`
}

// onboardingProposalPayload is the structured object §5.6 requires: a crew
// name/slug/icon, the template slug the whole roster derives from (Phase 1
// scope is one template + one model override, per §7), and the fully
// resolved agent list that deploying that template with that override would
// produce. Apply executes these stored values directly, in the same
// transaction that marks the proposal APPLIED. TemplateSlug remains audit
// provenance; it is never re-read after the human has approved the card.
type onboardingProposalPayload struct {
	CrewName     string                    `json:"crew_name"`
	CrewSlug     string                    `json:"crew_slug"`
	CrewIcon     *string                   `json:"crew_icon,omitempty"`
	CrewColor    *string                   `json:"crew_color,omitempty"`
	TemplateSlug string                    `json:"template_slug"`
	LLMProvider  string                    `json:"llm_provider,omitempty"`
	LLMModel     string                    `json:"llm_model,omitempty"`
	Agents       []onboardingProposalAgent `json:"agents"`
}

// onboardingProposalResponse is the wire shape for Create and Get.
type onboardingProposalResponse struct {
	ID            string                    `json:"id"`
	WorkspaceID   string                    `json:"workspace_id"`
	CreatedBy     string                    `json:"created_by"`
	CreatedAt     string                    `json:"created_at"`
	AppliedAt     *string                   `json:"applied_at"`
	Status        string                    `json:"status"`
	Payload       onboardingProposalPayload `json:"payload"`
	AppliedCrewID *string                   `json:"applied_crew_id,omitempty"`
}

// onboardingProposalApplyResponse is Apply's response shape. AlreadyApplied
// is true when this call found the proposal already APPLIED and returned
// the stored first result rather than writing anything (the idempotency the
// task requires).
type onboardingProposalApplyResponse struct {
	ProposalID     string           `json:"proposal_id"`
	Status         string           `json:"status"`
	AlreadyApplied bool             `json:"already_applied"`
	Crew           deployCrewResult `json:"crew"`
}

// planOnboardingProposal loads templateSlug's CURRENT definition and computes
// exactly the crew + agent fields deployCrewTemplate would write for
// (crewName, crewSlugInput, overrides) — without writing anything. It
// mirrors deployCrewTemplate's own derivation (crew slug fallback, per-agent
// slug suffix, model override resolution) so that what Create stores here
// and what Apply later executes are guaranteed to agree as long as both read
// the same template row and the same override. Deliberately duplicated
// rather than factored into crew_templates.go: this file owns the proposal
// surface end to end and must not require touching the deploy path it calls.
func planOnboardingProposal(ctx context.Context, db *sql.DB, wsID, templateSlug, crewName, crewSlugInput string, overrides deployOverrides) (*onboardingProposalPayload, error) {
	crewSlug := crewSlugInput
	if crewSlug == "" {
		crewSlug = slugify(crewName)
	} else {
		crewSlug = slugify(crewSlug)
	}
	if crewSlug == "" {
		return nil, fmt.Errorf("%w: crew_slug must contain only lowercase letters, numbers, and hyphens", errCrewSlugConflict)
	}

	var agentsJSON string
	var icon, color *string
	err := db.QueryRowContext(ctx, `
		SELECT agents_json, icon, color FROM crew_templates`+crewTemplateBySlugScope, templateSlug, wsID).Scan(&agentsJSON, &icon, &color)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errTemplateNotFound
		}
		return nil, fmt.Errorf("load template: %w", err)
	}

	var agents []database.CrewTemplateAgent
	if err := json.Unmarshal([]byte(agentsJSON), &agents); err != nil {
		return nil, fmt.Errorf("parse template agents: %w", err)
	}

	planned := make([]onboardingProposalAgent, 0, len(agents))
	for _, a := range agents {
		planned = append(planned, onboardingProposalAgent{
			Name:         a.Name,
			Slug:         a.Slug + "-" + crewSlug,
			RoleTitle:    a.RoleTitle,
			AgentRole:    a.AgentRole,
			CLIAdapter:   a.CLIAdapter,
			LLMProvider:  a.LLMProvider,
			LLMModel:     overrides.modelFor(a.LLMProvider, a.LLMModel),
			ToolProfile:  a.ToolProfile,
			SystemPrompt: a.SystemPrompt,
		})
	}

	return &onboardingProposalPayload{
		CrewName:     crewName,
		CrewSlug:     crewSlug,
		CrewIcon:     icon,
		CrewColor:    color,
		TemplateSlug: templateSlug,
		LLMProvider:  overrides.Provider,
		LLMModel:     overrides.LLMModel,
		Agents:       planned,
	}, nil
}

// OnboardingProposalHandler serves the onboarding proposal store: Create,
// Get, and the one write path, Apply.
type OnboardingProposalHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

// NewOnboardingProposalHandler creates an OnboardingProposalHandler. Journal
// emitter defaults to noopEmitter — call SetJournal to wire up the real one,
// same convention as CrewTemplateHandler.
func NewOnboardingProposalHandler(db *sql.DB, logger *slog.Logger) *OnboardingProposalHandler {
	return &OnboardingProposalHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal attaches the canonical event-stream emitter.
func (h *OnboardingProposalHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

type onboardingProposalCreateRequest struct {
	CrewName     string `json:"crew_name"`
	CrewSlug     string `json:"crew_slug"`
	TemplateSlug string `json:"template_slug"`
	LLMProvider  string `json:"llm_provider"`
	LLMModel     string `json:"llm_model"`
}

// Create handles POST /api/v1/onboarding/proposals. Resolves the named
// template + model override into a full proposal payload (§5.6) and stores
// it — no crew or agent row is written here. Auth: the normal authenticated
// user path (task #2), gated at the same MANAGER+ tier CrewTemplateHandler's
// own Deploy requires, since a proposal previews exactly that mutation.
func (h *OnboardingProposalHandler) Create(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req onboardingProposalCreateRequest
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	req.CrewName = strings.TrimSpace(req.CrewName)
	req.TemplateSlug = strings.TrimSpace(req.TemplateSlug)
	if req.CrewName == "" {
		writeProblem(w, r, http.StatusBadRequest, "crew_name is required")
		return
	}
	if req.TemplateSlug == "" {
		writeProblem(w, r, http.StatusBadRequest, "template_slug is required")
		return
	}

	llm, ok := resolveLLMProvider(req.LLMProvider)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "llm_provider must be ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, or OLLAMA")
		return
	}
	overrides := deployOverrides{LLMModel: strings.TrimSpace(req.LLMModel), Provider: llm.provider}

	// Builtin templates may not be seeded yet if this is the very first
	// wizard call in a fresh workspace — same lazy-seed CrewTemplateHandler
	// List and onboarding.setupFromTemplate both rely on.
	if err := database.SeedBuiltinCrewTemplates(r.Context(), h.db, h.logger); err != nil {
		h.logger.Warn("onboarding proposal create: seed builtin templates", "error", err)
	}

	payload, err := planOnboardingProposal(r.Context(), h.db, wsID, req.TemplateSlug, req.CrewName, req.CrewSlug, overrides)
	if err != nil {
		switch {
		case errors.Is(err, errTemplateNotFound):
			writeProblem(w, r, http.StatusNotFound, "Template not found")
		case errors.Is(err, errCrewSlugConflict):
			writeProblem(w, r, http.StatusBadRequest, err.Error())
		default:
			internalError(w, r, h.logger, "plan onboarding proposal", err)
		}
		return
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		internalError(w, r, h.logger, "marshal onboarding proposal payload", err)
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO onboarding_proposals (id, workspace_id, created_by, created_at, status, payload_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, wsID, user.ID, now, onboardingProposalStatusPending, string(payloadJSON)); err != nil {
		internalError(w, r, h.logger, "insert onboarding proposal", err)
		return
	}

	writeJSON(w, http.StatusCreated, onboardingProposalResponse{
		ID:          id,
		WorkspaceID: wsID,
		CreatedBy:   user.ID,
		CreatedAt:   now,
		AppliedAt:   nil,
		Status:      onboardingProposalStatusPending,
		Payload:     *payload,
	})
}

// loadProposalRow reads one proposal row scoped to wsID, decoding its stored
// payload. Shared by Get and Apply so both read the identical stored state.
func (h *OnboardingProposalHandler) loadProposalRow(ctx context.Context, wsID, id string) (row onboardingProposalResponse, payload onboardingProposalPayload, err error) {
	var (
		payloadJSON      string
		appliedAt        sql.NullString
		appliedCrewID    sql.NullString
		appliedResultRaw sql.NullString
	)
	err = h.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, created_by, created_at, applied_at, status, payload_json, applied_crew_id, applied_result_json
		FROM onboarding_proposals WHERE id = ? AND workspace_id = ?`,
		id, wsID).Scan(&row.ID, &row.WorkspaceID, &row.CreatedBy, &row.CreatedAt, &appliedAt, &row.Status, &payloadJSON, &appliedCrewID, &appliedResultRaw)
	if err != nil {
		return row, payload, err
	}
	if appliedAt.Valid {
		row.AppliedAt = &appliedAt.String
	}
	if appliedCrewID.Valid {
		row.AppliedCrewID = &appliedCrewID.String
	}
	if unmarshalErr := json.Unmarshal([]byte(payloadJSON), &payload); unmarshalErr != nil {
		return row, payload, fmt.Errorf("parse stored onboarding proposal payload: %w", unmarshalErr)
	}
	row.Payload = payload
	return row, payload, nil
}

// Get handles GET /api/v1/onboarding/proposals/{id}.
func (h *OnboardingProposalHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("id")

	row, _, err := h.loadProposalRow(r.Context(), wsID, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Proposal not found")
			return
		}
		internalError(w, r, h.logger, "get onboarding proposal", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// deployStoredOnboardingProposal performs the crew write and the proposal's
// PENDING -> APPLIED transition in one transaction. The payload is re-read
// inside that transaction and the template catalogue is deliberately not
// consulted: the card the human approved is the immutable execution plan.
// This both survives a template update/removal after proposal time and avoids
// the old partial-success state where crew creation committed but recording
// APPLIED failed, leaving a permanently conflicting PENDING proposal.
func (h *OnboardingProposalHandler) deployStoredOnboardingProposal(ctx context.Context, wsID, id string) (*deployCrewResult, onboardingProposalPayload, string, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, onboardingProposalPayload{}, "", fmt.Errorf("begin proposal apply: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var status, payloadJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT status, payload_json FROM onboarding_proposals
		WHERE id = ? AND workspace_id = ?`, id, wsID).Scan(&status, &payloadJSON); err != nil {
		return nil, onboardingProposalPayload{}, "", err
	}
	if status == onboardingProposalStatusApplied {
		return nil, onboardingProposalPayload{}, "", errOnboardingProposalAlreadyApplied
	}

	var payload onboardingProposalPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, payload, "", fmt.Errorf("parse stored onboarding proposal payload: %w", err)
	}

	var existing int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM crews
		WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, payload.CrewSlug).Scan(&existing); err != nil {
		return nil, payload, "", fmt.Errorf("check proposal crew slug uniqueness: %w", err)
	}
	if existing > 0 {
		return nil, payload, "", fmt.Errorf("%w: '%s'", errCrewSlugConflict, payload.CrewSlug)
	}

	crewID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, network_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, wsID, payload.CrewName, payload.CrewSlug, payload.CrewIcon, payload.CrewColor,
		database.DefaultCrewNetworkMode, now, now); err != nil {
		return nil, payload, "", fmt.Errorf("create proposal crew: %w", err)
	}

	agentIDs := make([]string, 0, len(payload.Agents))
	for _, agent := range payload.Agents {
		agentID := generateCUID()
		storedSecret, _, encErr := encryption.EncryptAtRest(generateWebhookSecret())
		if encErr != nil {
			return nil, payload, "", fmt.Errorf("encrypt webhook secret for %s: %w", agent.Name, encErr)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agents (id, workspace_id, crew_id, name, slug, role_title, agent_role,
				cli_adapter, llm_provider, llm_model, tool_profile, system_prompt_legacy,
				timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1800, 1, ?, ?, ?)`,
			agentID, wsID, crewID, agent.Name, agent.Slug, agent.RoleTitle, agent.AgentRole,
			agent.CLIAdapter, agent.LLMProvider, agent.LLMModel, agent.ToolProfile,
			agent.SystemPrompt, storedSecret, now, now); err != nil {
			return nil, payload, "", fmt.Errorf("create proposal agent %s: %w", agent.Name, err)
		}
		agentIDs = append(agentIDs, agentID)
	}

	result := &deployCrewResult{
		CrewID:     crewID,
		CrewName:   payload.CrewName,
		CrewSlug:   payload.CrewSlug,
		AgentCount: len(agentIDs),
		AgentIDs:   agentIDs,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, payload, "", fmt.Errorf("marshal onboarding proposal apply result: %w", err)
	}
	update, err := tx.ExecContext(ctx, `
		UPDATE onboarding_proposals
		SET status = ?, applied_at = ?, applied_crew_id = ?, applied_result_json = ?
		WHERE id = ? AND workspace_id = ? AND status = ?`,
		onboardingProposalStatusApplied, now, crewID, string(resultJSON),
		id, wsID, onboardingProposalStatusPending)
	if err != nil {
		return nil, payload, "", fmt.Errorf("record onboarding proposal apply: %w", err)
	}
	if affected, err := update.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return nil, payload, "", fmt.Errorf("read onboarding proposal apply rows affected: %w", err)
		}
		return nil, payload, "", errOnboardingProposalAlreadyApplied
	}
	if err := tx.Commit(); err != nil {
		return nil, payload, "", fmt.Errorf("commit onboarding proposal apply: %w", err)
	}
	return result, payload, now, nil
}

// Apply handles POST /api/v1/onboarding/proposals/{id}/apply — the ONLY
// write path in this file, and by construction the only one that can create
// a crew from a proposal at all.
//
// It deliberately never parses a request body: task #3 requires it to
// "execute from the STORED payload, never from anything in the request body
// beyond the id", and the simplest enforcement of that is to give the
// request body nothing to read. The id in the path is a lookup key, not
// content — it names which stored row to execute, and nothing it points to
// can be overridden by the caller.
func (h *OnboardingProposalHandler) Apply(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id := r.PathValue("id")

	row, payload, err := h.loadProposalRow(r.Context(), wsID, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Proposal not found")
			return
		}
		internalError(w, r, h.logger, "load onboarding proposal for apply", err)
		return
	}

	// Idempotent: a proposal already APPLIED returns the FIRST result again
	// rather than creating a second crew (task #3). The result is read back
	// from applied_result_json rather than re-derived from the crews/agents
	// tables, so a later independent edit to those rows can't make a replay
	// of Apply answer differently than the original call did.
	if row.Status == onboardingProposalStatusApplied {
		result, replayErr := h.replayAppliedResult(r.Context(), wsID, id)
		if replayErr != nil {
			internalError(w, r, h.logger, "replay applied onboarding proposal", replayErr)
			return
		}
		writeJSON(w, http.StatusOK, onboardingProposalApplyResponse{
			ProposalID:     id,
			Status:         onboardingProposalStatusApplied,
			AlreadyApplied: true,
			Crew:           *result,
		})
		return
	}

	result, payload, createdAt, err := h.deployStoredOnboardingProposal(r.Context(), wsID, id)
	if err != nil {
		if errors.Is(err, errOnboardingProposalAlreadyApplied) {
			result, replayErr := h.replayAppliedResult(r.Context(), wsID, id)
			if replayErr != nil {
				internalError(w, r, h.logger, "replay concurrently applied onboarding proposal", replayErr)
				return
			}
			writeJSON(w, http.StatusOK, onboardingProposalApplyResponse{
				ProposalID: id, Status: onboardingProposalStatusApplied, AlreadyApplied: true, Crew: *result,
			})
			return
		}
		if errors.Is(err, errCrewSlugConflict) {
			// A concurrent Apply on the SAME proposal may have already won:
			// deployCrewTemplate's own slug-uniqueness check inside its
			// transaction is what would surface that as this exact error
			// (SQLite serializes writers, so this is the only way two Apply
			// calls on one proposal can observe each other). Re-check this
			// proposal's own status before treating the conflict as final —
			// if it is APPLIED now, return the winner's stored result
			// instead of a spurious 409.
			if row2, _, reErr := h.loadProposalRow(r.Context(), wsID, id); reErr == nil && row2.Status == onboardingProposalStatusApplied {
				result2, replayErr := h.replayAppliedResult(r.Context(), wsID, id)
				if replayErr == nil {
					writeJSON(w, http.StatusOK, onboardingProposalApplyResponse{
						ProposalID:     id,
						Status:         onboardingProposalStatusApplied,
						AlreadyApplied: true,
						Crew:           *result2,
					})
					return
				}
			}
			writeProblem(w, r, http.StatusConflict, err.Error())
			return
		}
		internalError(w, r, h.logger, "apply onboarding proposal", err)
		return
	}

	// Credential linking is intentionally after commit and best-effort, as it
	// is for ordinary template deploys. The structural crew/proposal mutation
	// above is atomic; external observability work below must not roll it back.
	for _, agentID := range result.AgentIDs {
		autoAssignCredentials(r.Context(), h.db, h.logger, h.journal, wsID, agentID, createdAt)
	}

	WriteAuditLog(r.Context(), h.db, h.journal, "onboarding.proposal_apply", "CREW", result.CrewID, user.ID, wsID, map[string]interface{}{
		"proposal_id":   id,
		"template_slug": payload.TemplateSlug,
		"agent_count":   result.AgentCount,
	})
	if _, jErr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: wsID,
		CrewID:      result.CrewID,
		Type:        journalEntryOnboardingProposalApplied,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     user.ID,
		Summary:     fmt.Sprintf("onboarding proposal %s applied", id),
		Payload: map[string]any{
			"proposal_id":   id,
			"template_slug": payload.TemplateSlug,
			"crew_id":       result.CrewID,
			"agent_count":   result.AgentCount,
		},
	}); jErr != nil {
		h.logger.Warn("onboarding proposal apply: journal emit failed", "proposal_id", id, "error", jErr)
	}

	h.logger.Info("onboarding proposal applied",
		"proposal_id", id, "crew_id", result.CrewID, "template", payload.TemplateSlug, "agents", result.AgentCount)

	writeJSON(w, http.StatusCreated, onboardingProposalApplyResponse{
		ProposalID:     id,
		Status:         onboardingProposalStatusApplied,
		AlreadyApplied: false,
		Crew:           *result,
	})
}

// replayAppliedResult decodes the deployCrewResult snapshot Apply stored the
// first time this proposal was applied.
func (h *OnboardingProposalHandler) replayAppliedResult(ctx context.Context, wsID, id string) (*deployCrewResult, error) {
	var raw sql.NullString
	if err := h.db.QueryRowContext(ctx,
		`SELECT applied_result_json FROM onboarding_proposals WHERE id = ? AND workspace_id = ?`,
		id, wsID).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid || raw.String == "" {
		return nil, fmt.Errorf("onboarding proposal %s is APPLIED with no stored result", id)
	}
	var result deployCrewResult
	if err := json.Unmarshal([]byte(raw.String), &result); err != nil {
		return nil, fmt.Errorf("parse stored onboarding proposal apply result: %w", err)
	}
	return &result, nil
}
