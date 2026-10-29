package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
)

var (
	ErrOnboardingAlreadyCompleted = errors.New("onboarding already completed")
	ErrOnboardingDenied           = errors.New("onboarding requires current crew creation authority")
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
	db      *sql.DB
	logger  *slog.Logger
	idFunc  func() string
	journal journal.Emitter
}

// NewOnboardingService creates a new OnboardingService.
// idFunc generates unique IDs (e.g. CUIDs).
func NewOnboardingService(db *sql.DB, logger *slog.Logger, idFunc func() string) *OnboardingService {
	return &OnboardingService{db: db, logger: logger, idFunc: idFunc}
}

// SetJournal uses the router's configured transactional audit writer.
// A nil emitter persists through the native default-key AppendTx path.
func (s *OnboardingService) SetJournal(emitter journal.Emitter) { s.journal = emitter }

func onboardingAuthority(ctx context.Context, tx *sql.Tx, p SetupParams) error {
	var role string
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT m.role,
 u.suspended_at IS NULL AND w.deleted_at IS NULL AND NOT EXISTS(
 SELECT 1 FROM workspace_members g WHERE g.user_id=m.user_id AND g.access_mode='restricted')
 FROM workspace_members m JOIN users u ON u.id=m.user_id
 JOIN workspaces w ON w.id=m.workspace_id WHERE m.workspace_id=? AND m.user_id=?`,
		p.WorkspaceID, p.UserID).Scan(&role, &allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrWorkspaceNotFound
	}
	if err != nil {
		return err
	}
	if !allowed || (role != "OWNER" && role != "ADMIN" && role != "MANAGER") {
		return ErrOnboardingDenied
	}
	return nil
}

// Setup creates the initial crew, agent, and credential for a user's workspace.
// It atomically claims onboarding (CAS guard) to prevent TOCTOU races.
func (s *OnboardingService) Setup(ctx context.Context, p SetupParams) (*SetupResult, error) {
	var notify func()
	result, err := database.WithTxResult(ctx, s.db, func(tx *sql.Tx) (*SetupResult, error) {
		if err := onboardingAuthority(ctx, tx, p); err != nil {
			return nil, err
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
			INSERT INTO crews (id, workspace_id, name, slug, network_mode, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, crewID, p.WorkspaceID, p.CrewName, p.CrewSlug, database.DefaultCrewNetworkMode, p.Now, p.Now); err != nil {
			s.logger.Error("insert crew", "error", err)
			return nil, err
		}

		// Add user as crew member, preserving the native NULL inherited role.
		memberID := s.idFunc()
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO crew_members (id, crew_id, user_id, created_at)
			VALUES (?, ?, ?, ?)
		`, memberID, crewID, p.UserID, p.Now); err != nil {
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

			// Onboarding only accepts CLI tokens (the value the user
			// gets out of `claude setup-token`, `gemini auth print-token`,
			// etc.) — NOT raw provider API keys. The wizard's UI says
			// so explicitly and the handler validates the prefix
			// before we get here. Always store as AI_CLI_TOKEN so the
			// runtime picks the matching auth mode.
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

		entry := journal.Entry{WorkspaceID: p.WorkspaceID, CrewID: crewID,
			ActorType: journal.ActorUser, ActorID: p.UserID, Type: journal.EntryCrewMemberChanged,
			Summary: "Added onboarding crew member", Payload: map[string]any{
				"subject_user_id": p.UserID, "member_id": memberID, "action": "added",
				"previous_membership": nil, "membership": map[string]any{"role": nil}}}
		if s.journal == nil {
			notify, err = journal.AppendTx(ctx, tx, entry)
		} else if writer, ok := s.journal.(journal.TransactionEmitter); ok {
			notify, err = writer.AppendTx(ctx, tx, entry)
		} else {
			return nil, fmt.Errorf("onboarding journal does not support atomic writes")
		}
		if err != nil {
			return nil, err
		}
		// Audit triggers run before these final business receipts. Check the
		// membership business key as well as its generated identity, and the CAS
		// and newly created resource links before allowing the transaction out.
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT
          (SELECT COUNT(*) FROM crew_members WHERE crew_id=? AND user_id=?)=1
          AND EXISTS(SELECT 1 FROM crew_members WHERE id=? AND crew_id=? AND user_id=? AND role IS NULL)
          AND EXISTS(SELECT 1 FROM users WHERE id=? AND onboarding_completed=1)
          AND EXISTS(SELECT 1 FROM crews WHERE id=? AND workspace_id=? AND name=? AND slug=? AND deleted_at IS NULL)
          AND EXISTS(SELECT 1 FROM agents WHERE id=? AND crew_id=? AND workspace_id=? AND name=? AND slug=? AND cli_adapter=? AND llm_provider=? AND llm_model IS ? AND deleted_at IS NULL)`,
			crewID, p.UserID, memberID, crewID, p.UserID, p.UserID,
			crewID, p.WorkspaceID, p.CrewName, p.CrewSlug, agentID, crewID, p.WorkspaceID, p.AgentName, p.AgentSlug, p.CliAdapter, p.LLMProvider, p.LLMModel).Scan(&valid)
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, fmt.Errorf("onboarding final business state changed")
		}
		if err = onboardingAuthority(ctx, tx, p); err != nil {
			return nil, err
		}

		return &SetupResult{
			WorkspaceID:  p.WorkspaceID,
			CrewID:       crewID,
			AgentID:      agentID,
			CredentialID: credentialID,
		}, nil
	})
	if err == nil && notify != nil {
		notify()
	}
	return result, err
}
