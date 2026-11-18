package server

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func codexSandboxPolicyResolver(db *sql.DB) orchestrator.CodexSandboxPolicyResolver {
	launch := managedLaunchResolverFor(db, true)
	return func(ctx context.Context, workspace, crew string) (orchestrator.CodexSandboxPolicy, error) {
		var policy orchestrator.CodexSandboxPolicy
		if workspace == "" || crew == "" {
			return policy, errors.New("workspace-scoped crew required")
		}
		if err := db.QueryRowContext(ctx, `SELECT allow_codex_outer_sandbox FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, crew, workspace).Scan(&policy.Enabled); err != nil {
			return policy, err
		}
		if !policy.Enabled {
			return policy, nil
		}
		var err error
		policy.Launch, err = launch(ctx, workspace, crew, "CODEX_CLI")
		return policy, err
	}
}
