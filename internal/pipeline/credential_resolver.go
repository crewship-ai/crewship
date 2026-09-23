package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/llmroute"
)

// endpointBackedProviders lists the provider column values whose stored
// credential value is a {baseURL, apiKey, headers} object rather than a bare
// token. Computed once at init: llmroute.Specs() deep-copies the whole table
// and these resolvers run per http step.
var endpointBackedProviders = func() []string {
	var out []string
	for _, s := range llmroute.Specs() {
		if s.UpstreamFromCredential {
			out = append(out, s.ID)
		}
	}
	sort.Strings(out)
	return out
}()

// excludeEndpointProviders returns a WHERE fragment (and its args) that keeps
// endpoint-backed credentials out of both resolvers below.
//
// Both of them select by TYPE alone, which was sound while `type = 'API_KEY'`
// meant "a bare token". It stopped meaning that: an OPENAI_COMPAT credential is
// stored as API_KEY and holds the whole endpoint object. Without this filter,
// creating one made it the newest API_KEY row in the workspace, so the next
// routine step declaring `credential_ref: {type: API_KEY}` would have had that
// entire object — base URL, custom headers and key — injected into whatever
// third-party endpoint the routine dials. That is the same leak the delivery
// split fixed on the agent path, arriving by a route that has nothing to do
// with agents.
//
// It EXCLUDES rather than splits deliberately. A step asking for "an API key"
// wants a bare token, and the value it should get is the next-newest row that
// actually is one — not the apiKey field prised out of a credential the author
// never asked for. OPENAI_COMPAT is new in this change, so nothing that
// resolved before can stop resolving because of it.
func excludeEndpointProviders() (string, []any) {
	if len(endpointBackedProviders) == 0 {
		return "", nil
	}
	placeholders := make([]string, len(endpointBackedProviders))
	args := make([]any, len(endpointBackedProviders))
	for i, p := range endpointBackedProviders {
		placeholders[i] = "?"
		args[i] = p
	}
	return "  AND UPPER(COALESCE(provider, '')) NOT IN (" + strings.Join(placeholders, ", ") + ")\n", args
}

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
//   - crew isolation: credential_crews is authoritative for crew grants;
//     unlinked WORKSPACE rows are shared. The legacy crew_id is not a grant.
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
		err := selectedVaultCredential(db, ctx, scope, credType, true).Scan(new(string), &encryptedValue)
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

// NewVaultCredentialIDResolver previews the exact runtime selection without
// reading or decrypting its value. A selected ID is not proof of past use.
func NewVaultCredentialIDResolver(db *sql.DB) func(context.Context, RunScope, string) (string, error) {
	return func(ctx context.Context, scope RunScope, credType string) (string, error) {
		if scope.WorkspaceID == "" || strings.TrimSpace(credType) == "" {
			return "", fmt.Errorf("credential selection requires workspace and type")
		}
		var id string
		err := selectedVaultCredential(db, ctx, scope, credType, false).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("credential selection: %w", err)
		}
		return id, nil
	}
}

func selectedVaultCredential(db *sql.DB, ctx context.Context, scope RunScope, credType string, includeValue bool) *sql.Row {
	query, args := vaultCredentialCandidates(scope, credType)
	projection := "v.id"
	join := ""
	if includeValue {
		projection += ", c.encrypted_value"
		join = " JOIN credentials c ON c.id = v.id"
	}
	return db.QueryRowContext(ctx, query+`SELECT `+projection+` FROM candidates v`+join+`
WHERE v.crew_linked OR (v.scope = 'WORKSPACE' AND NOT v.has_links AND COALESCE(v.crew_id, '') = '')
ORDER BY v.crew_linked DESC, v.created_at DESC, v.id
LIMIT 1`, args...)
}

// NewVaultCredentialProbe builds the availability check behind
// credentials_required enforcement (#1418): reports whether an ACTIVE
// credential of a given type EXISTS in the run scope, WITHOUT decrypting
// or returning the value. It applies the exact same workspace + author-crew
// + status='ACTIVE' + not-deleted filter as NewVaultCredentialResolver, so
// "declared credential resolves" means precisely "the runtime would inject
// it" — no drift between the enforcement gate and the actual resolution.
//
// A missing scope workspace is an error (a probe that can't scope must not
// silently report "available"); no matching row → (false, nil). It never
// reads encrypted_value, so it holds no secret material.
func NewVaultCredentialProbe(db *sql.DB) func(ctx context.Context, scope RunScope, credType string) (bool, error) {
	return func(ctx context.Context, scope RunScope, credType string) (bool, error) {
		credType = strings.TrimSpace(credType)
		if scope.WorkspaceID == "" {
			return false, fmt.Errorf("credential probe requires a workspace scope")
		}
		if credType == "" {
			return false, fmt.Errorf("credential type is empty")
		}
		// The same filter as the resolver, and for the reason the doc comment
		// above states: "declared credential resolves" must mean exactly "the
		// runtime would inject it". A probe that counted an endpoint-backed row
		// the resolver now skips would report a credentials_required gate
		// satisfied by a credential the step will never receive.
		query, args := vaultCredentialCandidates(scope, credType)
		var one int
		err := db.QueryRowContext(ctx, query+`
SELECT 1 FROM candidates
WHERE crew_linked OR (scope = 'WORKSPACE' AND NOT has_links AND COALESCE(crew_id, '') = '')
LIMIT 1`, args...,
		).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("credential probe for type %q: %w", credType, err)
		}
		return true, nil
	}
}

// vaultCredentialCandidates keeps the resolver and availability probe on the
// same selection contract. A crew link must point to a live crew in the run's
// workspace; stale or foreign links cannot grant a routine access. A row with
// any crew links cannot silently fall back to workspace scope.
func vaultCredentialCandidates(scope RunScope, credType string) (string, []any) {
	notEndpoint, endpointArgs := excludeEndpointProviders()
	args := append([]any{scope.AuthorCrewID, scope.WorkspaceID, credType}, endpointArgs...)
	return `WITH candidates AS (
SELECT c.created_at, c.id, c.scope, c.crew_id,
  EXISTS (
    SELECT 1 FROM credential_crews cc
    JOIN crews cr ON cr.id = cc.crew_id AND cr.workspace_id = c.workspace_id AND cr.deleted_at IS NULL
    WHERE cc.credential_id = c.id AND cc.crew_id = ?
  ) AS crew_linked,
  EXISTS (SELECT 1 FROM credential_crews cc WHERE cc.credential_id = c.id) AS has_links
FROM credentials c
WHERE c.workspace_id = ? AND UPPER(c.type) = UPPER(?)
` + notEndpoint + `  AND c.status = 'ACTIVE' AND c.deleted_at IS NULL
)
`, args
}
