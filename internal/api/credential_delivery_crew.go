package api

// The CREW-scoped delivery set — what a crew's own infrastructure gets, as
// opposed to what one of its agents gets.
//
// credential_delivery.go says, with feeling, "Three arrivals, three near-misses,
// one shared definition now. Do not spell the query out here again." This is not
// a fourth copy of that query: it is a different SET, and the difference is the
// point.
//
// A sidecar belongs to the CREW, not to whichever agent happened to trigger the
// start. Its environment feeds the docker provider's spec hash, so resolving
// `env_refs` against an agent's delivered set gives the same crew a different
// Postgres per agent and recreates it — dropping every connection — whenever the
// trigger changes. So the agent-specific arms are deliberately absent:
//
//   - agent_credentials (explicit per-agent grants) — excluded.
//   - credential_bindings scope=AGENT — excluded.
//   - credential_bindings scope=CREW / WORKSPACE — included, resolved per slot
//     with CREW winning over WORKSPACE, exactly as the agent query resolves its
//     three scopes.
//   - credential_crews — included, delivered under credentials.name, and
//     suppressed when a binding already delivers that credential. Same rule and
//     same reason as the agent query: a bound-and-linked credential must not
//     arrive twice under two names.
//
// The filters that arrived late to every OTHER loader (status = 'ACTIVE', the
// soft-delete check, the workspace match on both derived arms) are here from the
// start. The #1373 lease gate is not: it applies to the explicit agent arm only,
// because neither credential_bindings nor credential_crews has an expires_at
// column — the same note credential_delivery.go and internal_credentials.go
// already carry.

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// crewDeliveredCredentialsSQL is the crew-scoped twin of
// agentDeliveredCredentialsSQL. Built with Sprintf for the same reason: the rank
// literals cannot be allowed to drift from the credSource* constants.
//
// Bind order is (crewID /* CTE */) — the WITH clause is first in the text, and
// every arm reads the crew from it.
var crewDeliveredCredentialsSQL = fmt.Sprintf(`
	WITH crew_scope AS (
		SELECT c.id AS crew_id, c.workspace_id AS workspace_id
		FROM crews c
		WHERE c.id = ? AND c.deleted_at IS NULL
	),
	applicable_bindings AS (
		SELECT s.workspace_id  AS workspace_id,
		       b.credential_id AS credential_id,
		       b.slot          AS slot,
		       CASE b.scope WHEN 'CREW' THEN %d ELSE %d END AS spec
		FROM crew_scope s
		JOIN credential_bindings b
		  ON b.workspace_id = s.workspace_id
		 AND (   (b.scope = 'CREW' AND b.crew_id = s.crew_id)
		      OR  b.scope = 'WORKSPACE')
	),
	resolved_bindings AS (
		SELECT ab.workspace_id, ab.credential_id, ab.slot, ab.spec
		FROM applicable_bindings ab
		WHERE ab.spec = (SELECT MIN(x.spec) FROM applicable_bindings x WHERE x.slot = ab.slot)
	)

	SELECT rb.slot          AS env_var_name,
	       c.encrypted_value AS encrypted_value,
	       rb.spec           AS source_rank
	FROM resolved_bindings rb
	JOIN credentials c ON c.id = rb.credential_id
	WHERE c.deleted_at IS NULL AND c.status = 'ACTIVE'
	  AND c.workspace_id = rb.workspace_id

	UNION ALL

	SELECT c.name           AS env_var_name,
	       c.encrypted_value AS encrypted_value,
	       %d                AS source_rank
	FROM crew_scope s
	JOIN credential_crews cc ON cc.crew_id = s.crew_id
	JOIN credentials c ON c.id = cc.credential_id
	WHERE c.deleted_at IS NULL AND c.status = 'ACTIVE'
	  AND c.workspace_id = s.workspace_id
	  AND NOT EXISTS (
	      SELECT 1 FROM resolved_bindings rb WHERE rb.credential_id = c.id
	  )

	ORDER BY source_rank ASC
`, credSourceBindingCrew, credSourceBindingWorkspace, credSourceCrewLink)

// loadCrewDeliveredEnv returns env-var name → plaintext value for one crew.
//
// First writer wins, and the ORDER BY is what makes that meaningful: a binding
// (the deliberate statement about an env var) outranks a crew link (a name that
// happens to look like one), exactly as it does for an agent.
//
// A credential that will not decrypt, or that carries a PENDING sentinel, is
// dropped rather than delivered: half a secret in a sidecar's environment is
// worse than none, because the container starts and then misbehaves.
func loadCrewDeliveredEnv(ctx context.Context, db *sql.DB, crewID string) (map[string]string, error) {
	out := map[string]string{}
	if db == nil || crewID == "" {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, crewDeliveredCredentialsSQL, crewID)
	if err != nil {
		return nil, fmt.Errorf("query crew delivered credentials: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var envVar, encrypted string
		var rank int
		if err := rows.Scan(&envVar, &encrypted, &rank); err != nil {
			return nil, fmt.Errorf("scan crew delivered credential: %w", err)
		}
		if envVar == "" {
			continue
		}
		if _, taken := out[envVar]; taken {
			continue
		}
		plain, err := encryption.Decrypt(encrypted)
		if err != nil || isPendingSentinel(plain) {
			continue
		}
		out[envVar] = plain
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
