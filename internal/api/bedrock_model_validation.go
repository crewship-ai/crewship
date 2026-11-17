package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

// bedrockPolicyError is a caller-correctable refusal (400). Any other error
// from the validators below is a server fault and must be reported as 500.
type bedrockPolicyError struct{ msg string }

func (e bedrockPolicyError) Error() string { return e.msg }

func policyErr(msg string) error { return bedrockPolicyError{msg: msg} }

// isBedrockPolicyError reports whether err is a refusal the caller can fix.
func isBedrockPolicyError(err error) bool {
	var p bedrockPolicyError
	return errors.As(err, &p)
}

// validateBedrockAgentModel runs in the same transaction as an agent PATCH.
// Catalog membership is advisory; the assigned account's policy is mandatory.
func validateBedrockAgentModel(ctx context.Context, tx *sql.Tx, ws, agent string) error {
	return validateBedrockAgent(ctx, tx, ws, agent, true)
}

// validateAssignedBedrockModel checks a Bedrock agent's model against its
// account only when an account is already assigned. Creation and crew moves
// use it: an agent created on Bedrock gets its account in a follow-up
// assignment, but one that inherited a crew account must not be stored on a
// model that account refuses.
func validateAssignedBedrockModel(ctx context.Context, tx *sql.Tx, ws, agent string) error {
	return validateBedrockAgent(ctx, tx, ws, agent, false)
}

func validateBedrockAgent(ctx context.Context, tx *sql.Tx, ws, agent string, requireAccount bool) error {
	var provider, adapter, model string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(llm_provider,''),cli_adapter,COALESCE(llm_model,'') FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, agent, ws).Scan(&provider, &adapter, &model); err != nil {
		return err
	}
	if provider != bedrock.Provider {
		if strings.HasPrefix(model, bedrock.NativeProvider+"/") {
			return policyErr("Bedrock model IDs require the AWS_BEDROCK provider")
		}
		return nil
	}
	if adapter != "OPENCODE" || !strings.HasPrefix(model, bedrock.NativeProvider+"/") {
		return policyErr("Bedrock requires OpenCode and a full amazon-bedrock/ model ID")
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
			return policyErr("exactly one assigned Bedrock account required")
		}
		selected = &delivered[i]
	}
	if selected == nil && !requireAccount {
		return nil
	}
	if selected == nil || selected.HandleOnly || selected.Type != CredTypeProviderLogin {
		return policyErr("assigned Bedrock account unavailable")
	}
	clear, err := decryptCredential(selected.EncryptedValue)
	if err != nil {
		return policyErr("assigned Bedrock account unavailable")
	}
	account, err := bedrock.Parse(clear)
	if err != nil || account.Expired(time.Now()) || !account.Allows(strings.TrimPrefix(model, bedrock.NativeProvider+"/")) {
		return policyErr("Bedrock account expired or model not permitted")
	}
	return nil
}
