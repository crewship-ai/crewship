package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// anthropicLLMCredentialFilter is the SQL predicate (a WHERE fragment, minus the
// workspace_id clause) identifying a vault row that can satisfy an agent step's
// Anthropic LLM auth. It is the SINGLE source of truth shared by
// LLMRunner.providerForWorkspace (which decrypts and returns the value) and the
// run-time credential gate's NewAnthropicLLMCredentialProbe (which only checks
// existence). One predicate for both is what guarantees the gate can never 422 a
// run the runner would have completed — the parity the #1418 gate was missing.
//
// Both API_KEY (Messages API) and AI_CLI_TOKEN (Claude Code OAuth) are valid
// Anthropic auth surfaces; the seed picks AI_CLI_TOKEN for an sk-ant-oat token
// and API_KEY otherwise. The match is workspace-wide (no crew pin) exactly as an
// agent resolves its provider — a key pinned to any crew still powers the run.
const anthropicLLMCredentialFilter = `provider = 'ANTHROPIC' AND type IN ('API_KEY', 'AI_CLI_TOKEN') AND status = 'ACTIVE' AND deleted_at IS NULL`

// AnthropicLLMCredentialTypes are the vault credential types the filter above
// accepts, exposed so the gate can tell whether a declared requirement type is
// one the Anthropic LLM provider path would satisfy. Keep in sync with
// anthropicLLMCredentialFilter.
var AnthropicLLMCredentialTypes = []string{"API_KEY", "AI_CLI_TOKEN"}

// IsAnthropicLLMCredentialType reports whether credType (case-insensitive) is a
// type an Anthropic LLM credential can present — one that providerForWorkspace
// would resolve. The gate uses it to decide whether an otherwise-unsatisfied
// exact-type requirement may instead be met by a workspace-wide Anthropic key.
func IsAnthropicLLMCredentialType(credType string) bool {
	credType = strings.ToUpper(strings.TrimSpace(credType))
	for _, t := range AnthropicLLMCredentialTypes {
		if t == credType {
			return true
		}
	}
	return false
}

// NewAnthropicLLMCredentialProbe reports whether a workspace holds an ACTIVE
// Anthropic LLM credential resolvable by LLMRunner.providerForWorkspace. The run
// gate falls back to this probe so a routine whose agent steps need an Anthropic
// key is never blocked when the vault holds a usable key of EITHER accepted type
// (API_KEY or AI_CLI_TOKEN), pinned to any crew or none — matching exactly what
// the runner will resolve. It never reads encrypted_value, so it holds no secret.
func NewAnthropicLLMCredentialProbe(db *sql.DB) func(ctx context.Context, workspaceID string) (bool, error) {
	return func(ctx context.Context, workspaceID string) (bool, error) {
		if workspaceID == "" {
			return false, fmt.Errorf("anthropic credential probe requires a workspace")
		}
		var one int
		err := db.QueryRowContext(ctx,
			`SELECT 1 FROM credentials WHERE workspace_id = ? AND `+anthropicLLMCredentialFilter+` LIMIT 1`,
			workspaceID,
		).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("anthropic credential probe: %w", err)
		}
		return true, nil
	}
}
