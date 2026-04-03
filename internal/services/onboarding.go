package services

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
)

var (
	ErrOnboardingAlreadyCompleted = errors.New("onboarding already completed")
	ErrWorkspaceNotFound          = errors.New("workspace membership not found")
)

// SetupParams holds the validated input for onboarding setup.
type SetupParams struct {
	UserID          string
	WorkspaceID     string // caller resolves this before calling Setup
	WorkspaceName   string
	CrewName        string
	CrewSlug        string
	AgentName       string
	AgentSlug       string
	CliAdapter      string
	LLMProvider     string
	LLMModel        *string // nil for nullable column
	EnvVarName      string
	CredentialName  string
	CredentialValue string
	Now             string // RFC3339 timestamp
}

// SetupResult holds the IDs created during onboarding.
type SetupResult struct {
	WorkspaceID  string `json:"workspace_id"`
	CrewID       string `json:"crew_id"`
	AgentID      string `json:"agent_id"`
	CredentialID string `json:"credential_id"`
}

// OnboardingService handles the business logic for user onboarding.
type OnboardingService struct {
	db       *sql.DB
	logger   *slog.Logger
	idFunc   func() string
}

// NewOnboardingService creates a new OnboardingService.
// idFunc generates unique IDs (e.g. CUIDs).
func NewOnboardingService(db *sql.DB, logger *slog.Logger, idFunc func() string) *OnboardingService {
	return &OnboardingService{db: db, logger: logger, idFunc: idFunc}
}

// Setup creates the initial crew, agent, and credential for a user's workspace.
// It atomically claims onboarding (CAS guard) to prevent TOCTOU races.
func (s *OnboardingService) Setup(ctx context.Context, p SetupParams) (*SetupResult, error) {
	return database.WithTxResult(ctx, s.db, func(tx *sql.Tx) (*SetupResult, error) {
		// Re-verify workspace membership inside transaction (prevents TOCTOU with membership removal)
		var memberExists int
		if err := tx.QueryRowContext(ctx,
			"SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
			p.WorkspaceID, p.UserID).Scan(&memberExists); err != nil {
			return nil, ErrWorkspaceNotFound
		}

		// Atomic guard: claim onboarding (prevents TOCTOU race)
		guardRes, err := tx.ExecContext(ctx,
			"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ? AND onboarding_completed = 0",
			p.Now, p.UserID)
		if err != nil {
			s.logger.Error("lock onboarding", "error", err)
			return nil, err
		}
		guardRows, err := guardRes.RowsAffected()
		if err != nil {
			return nil, err
		}
		if guardRows == 0 {
			return nil, ErrOnboardingAlreadyCompleted
		}

		// Update workspace name if provided
		if p.WorkspaceName != "" {
			if _, err = tx.ExecContext(ctx,
				"UPDATE workspaces SET name = ?, updated_at = ? WHERE id = ?",
				p.WorkspaceName, p.Now, p.WorkspaceID); err != nil {
				s.logger.Error("update workspace name", "error", err)
				return nil, err
			}
		}

		// Create crew
		crewID := s.idFunc()
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, crewID, p.WorkspaceID, p.CrewName, p.CrewSlug, p.Now, p.Now); err != nil {
			s.logger.Error("insert crew", "error", err)
			return nil, err
		}

		// Add user as crew member
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO crew_members (id, crew_id, user_id, created_at)
			VALUES (?, ?, ?, ?)
		`, s.idFunc(), crewID, p.UserID, p.Now); err != nil {
			s.logger.Error("insert crew member", "error", err)
			return nil, err
		}

		// Create agent
		agentID := s.idFunc()
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO agents (id, crew_id, workspace_id, name, slug, cli_adapter, llm_provider, llm_model, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, agentID, crewID, p.WorkspaceID, p.AgentName, p.AgentSlug, p.CliAdapter,
			p.LLMProvider, p.LLMModel, p.Now, p.Now); err != nil {
			s.logger.Error("insert agent", "error", err)
			return nil, err
		}

		// Create and assign credential if provided
		var credentialID string
		if p.CredentialValue != "" {
			credentialID = s.idFunc()

			encryptedValue, encErr := encryption.Encrypt(p.CredentialValue)
			if encErr != nil {
				s.logger.Error("encrypt credential", "error", encErr)
				return nil, encErr
			}

			if _, err = tx.ExecContext(ctx, `
				INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, created_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'AI_CLI_TOKEN', ?, 'WORKSPACE', ?, ?, ?)
			`, credentialID, p.WorkspaceID, p.CredentialName, encryptedValue, p.LLMProvider, p.UserID, p.Now, p.Now); err != nil {
				s.logger.Error("insert credential", "error", err)
				return nil, err
			}

			// Assign credential to agent
			if _, err = tx.ExecContext(ctx, `
				INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
				VALUES (?, ?, ?, ?, 0, ?)
			`, s.idFunc(), agentID, credentialID, p.EnvVarName, p.Now); err != nil {
				s.logger.Error("assign credential", "error", err)
				return nil, err
			}
		}

		return &SetupResult{
			WorkspaceID:  p.WorkspaceID,
			CrewID:       crewID,
			AgentID:      agentID,
			CredentialID: credentialID,
		}, nil
	})
}
