package server

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

func newSubscriptionUsageRecorder(db *sql.DB, j journal.Emitter) func(context.Context, orchestrator.SubscriptionUsage) error {
	return func(ctx context.Context, usage orchestrator.SubscriptionUsage) error {
		_, err := paymaster.Record(ctx, db, j, paymaster.Call{
			Scope:        paymaster.Scope{WorkspaceID: usage.WorkspaceID, CrewID: usage.CrewID, AgentID: usage.AgentID, MissionID: usage.MissionID},
			CredentialID: usage.CredentialID, Provider: usage.Provider, Model: usage.Model,
			BillingMode: paymaster.BillingFlatRate, SubscriptionPlan: usage.Plan,
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			CachedInputTokens: usage.CachedInputTokens, CacheCreationTokens: usage.CacheCreationTokens,
			Tags: map[string]any{"source": "subscription_cli", "run_id": usage.RunID},
		})
		return err
	}
}
