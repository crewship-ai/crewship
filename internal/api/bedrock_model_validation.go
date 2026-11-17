package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

// validateBedrockAgentModel runs in the same transaction as an agent PATCH.
// Catalog membership is advisory; the assigned account's policy is mandatory.
func validateBedrockAgentModel(ctx context.Context, tx *sql.Tx, ws, agent string) error {
	var provider, adapter, model string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(llm_provider,''),cli_adapter,COALESCE(llm_model,'') FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, agent, ws).Scan(&provider, &adapter, &model); err != nil {
		return err
	}
	if provider != bedrock.Provider {
		if strings.HasPrefix(model, bedrock.NativeProvider+"/") {
			return errors.New("Bedrock model IDs require the AWS_BEDROCK provider")
		}
		return nil
	}
	if adapter != "OPENCODE" || !strings.HasPrefix(model, bedrock.NativeProvider+"/") {
		return errors.New("Bedrock requires OpenCode and a full amazon-bedrock/ model ID")
	}
	delivered, _, err := loadDeliveredCredentialSnapshot(ctx, tx, agent)
	if err != nil {
		return err
	}
	var selected *deliveredCredential
	for i := range delivered {
		if delivered[i].Provider != bedrock.Provider {
			continue
		}
		if selected != nil {
			return errors.New("exactly one assigned Bedrock account required")
		}
		selected = &delivered[i]
	}
	if selected == nil || selected.HandleOnly || selected.Type != CredTypeProviderLogin {
		return errors.New("assigned Bedrock account unavailable")
	}
	clear, err := decryptCredential(selected.EncryptedValue)
	if err != nil {
		return errors.New("assigned Bedrock account unavailable")
	}
	account, err := bedrock.Parse(clear)
	if err != nil || account.Expired(time.Now()) || !account.Allows(strings.TrimPrefix(model, bedrock.NativeProvider+"/")) {
		return errors.New("Bedrock account expired or model not permitted")
	}
	return nil
}
