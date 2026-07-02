package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// NewVaultCredentialResolver builds the production credential resolver
// for http steps: credential_ref.type → the decrypted value of a
// matching credential in the running workspace's vault (credentials
// table + encryption.Decrypt — the same query/decrypt pattern as
// LLMRunner.providerForWorkspace and the agent-config resolver in
// internal/api/agent_config.go).
//
// Matching contract (mirrors CredReq / `crewship routine doctor`):
// credential_ref points at a credential by TYPE — the vault type enum
// (API_KEY, GENERIC_SECRET, ...) — never by ID, so a marketplace
// routine runs against ANY workspace that holds a credential of the
// right type. Comparison is case-insensitive (authors write
// `type: api_key`, the vault stores `API_KEY`).
//
// Selection rules, in order:
//
//   - workspace-scoped: only rows of the run's workspace are ever
//     considered — a routine can never read another workspace's vault.
//   - crew isolation: rows pinned to a crew (crew_id set) match only
//     when that crew is the routine's author crew; unpinned rows are
//     workspace-shared and always eligible.
//   - status = 'ACTIVE' + not deleted: PENDING rows carry encrypted
//     placeholder sentinels (see internal/api/credentials_types.go) —
//     the status filter here is the load-bearing guard that keeps a
//     placeholder from ever being injected as a real token.
//   - author-crew rows win over workspace-shared rows; within a bucket
//     the newest row wins (created_at DESC), so a rotated credential
//     takes over as soon as it lands — same rotation rule as
//     LLMRunner.providerForWorkspace.
//
// No match → ("", error). runHTTPStep treats that as "skip injection"
// (public endpoints must keep working), never as a step failure.
// The decrypted value is returned to the caller ONLY — this function
// must never log it.
func NewVaultCredentialResolver(db *sql.DB) func(ctx context.Context, scope RunScope, credType string) (string, error) {
	return func(ctx context.Context, scope RunScope, credType string) (string, error) {
		credType = strings.TrimSpace(credType)
		if scope.WorkspaceID == "" {
			return "", fmt.Errorf("credential resolution requires a workspace scope")
		}
		if credType == "" {
			return "", fmt.Errorf("credential_ref.type is empty")
		}
		var encryptedValue string
		err := db.QueryRowContext(ctx, `
SELECT encrypted_value FROM credentials
WHERE workspace_id = ?
  AND UPPER(type) = UPPER(?)
  AND status = 'ACTIVE'
  AND deleted_at IS NULL
  AND (crew_id IS NULL OR crew_id = '' OR crew_id = ?)
ORDER BY CASE WHEN crew_id = ? THEN 0 ELSE 1 END, created_at DESC, id
LIMIT 1`,
			scope.WorkspaceID, credType, scope.AuthorCrewID, scope.AuthorCrewID,
		).Scan(&encryptedValue)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("no active credential of type %q in workspace vault", credType)
		}
		if err != nil {
			return "", fmt.Errorf("credential lookup for type %q: %w", credType, err)
		}
		plain, err := encryption.Decrypt(encryptedValue)
		if err != nil {
			// Deliberately NOT wrapping the raw decrypt error detail
			// beyond its message — and never the value.
			return "", fmt.Errorf("decrypt credential of type %q: %w", credType, err)
		}
		return plain, nil
	}
}
