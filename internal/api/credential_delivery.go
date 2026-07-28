package api

import (
	"context"
	"database/sql"
	"fmt"
)

// Which credentials does an agent receive? — one definition, two consumers.
//
// Delivery used to read agent_credentials and nothing else, so a credential
// linked to a crew (credential_crews) reached the UI listing and the sidecar
// metadata listing and stopped there. "Assign the secret to crew 1" meant crew
// 1's members could SEE it; the agents in crew 1 booted without it. That is
// PRD-CREDENTIALS-V2 §1.2, blocker #1.
//
// The fanout is derived at READ time rather than materialised into
// agent_credentials rows, for two reasons:
//
//   - agents are created at five call sites (agents_create.go, crew_templates.go,
//     internal_status.go, agents_hire.go, services/onboarding.go). A write-side
//     hook would have to be added to all five and would be missed by the sixth;
//     autoAssignCredentials is the existing example and it covers exactly one of
//     them, for exactly one provider.
//   - nothing ever moves an agent between crews (no `UPDATE agents SET crew_id`
//     exists), so the agent's own agents.crew_id is a complete answer at read
//     time — and an unlink takes effect on the next boot instead of leaving
//     orphaned rows delivering a revoked credential forever.
//
// This constant is the chokepoint: the delivery paths must not spell the
// derivation out by hand. #1373's first increment is the cautionary tale — the
// lease gate was written into /keeper/execute and three other resolvers kept
// reading agent_credentials with no expiry filter at all.
//
// Bind order is (agentID, leaseComparisonNow(), agentID).
const agentDeliveredCredentialsSQL = `
	SELECT ac.credential_id AS credential_id,
	       ac.env_var_name  AS env_var_name,
	       ac.priority      AS priority,
	       c.encrypted_value AS encrypted_value,
	       c.type            AS cred_type,
	       COALESCE(c.username, '')    AS username,
	       COALESCE(ac.expires_at, '') AS lease_expires_at,
	       0 AS crew_derived
	FROM agent_credentials ac
	JOIN credentials c ON c.id = ac.credential_id
	WHERE ac.agent_id = ? AND c.deleted_at IS NULL AND c.status = 'ACTIVE'
	  AND ` + credentialLeaseGateSQL + `

	UNION ALL

	SELECT c.id   AS credential_id,
	       c.name AS env_var_name,
	       0      AS priority,
	       c.encrypted_value AS encrypted_value,
	       c.type            AS cred_type,
	       COALESCE(c.username, '') AS username,
	       ''  AS lease_expires_at,
	       1   AS crew_derived
	FROM agents a
	JOIN credential_crews cc ON cc.crew_id = a.crew_id
	JOIN credentials c ON c.id = cc.credential_id
	WHERE a.id = ? AND a.deleted_at IS NULL AND a.crew_id IS NOT NULL
	  AND c.deleted_at IS NULL AND c.status = 'ACTIVE'
	  AND c.workspace_id = a.workspace_id
	  AND NOT EXISTS (
	      SELECT 1 FROM agent_credentials ac2
	      WHERE ac2.agent_id = a.id AND ac2.credential_id = c.id
	  )

	ORDER BY priority ASC, crew_derived ASC
`

// deliveredCredential is one row of agentDeliveredCredentialsSQL, still
// encrypted. Decryption stays with the caller so each delivery path keeps its
// own failure policy (boot logs and continues; the delegation loader does the
// same) rather than having one imposed here.
type deliveredCredential struct {
	ID             string
	EnvVar         string
	Priority       int
	EncryptedValue string
	Type           string
	Username       string
	LeaseExpiresAt string
	// CrewDerived marks a row that came from credential_crews rather than an
	// explicit grant. Nothing branches on it today; it exists so a caller that
	// needs to distinguish the two sources does not have to re-derive it.
	CrewDerived bool
}

// loadDeliveredCredentials runs the shared derivation for one agent.
//
// Notes on the SQL that are easy to lose in review:
//
//   - env_var_name for a crew-derived row is credentials.name. That is today's
//     convention, already written by autoAssignCredentials into
//     agent_credentials.env_var_name; a crew link has no per-agent row to carry
//     a chosen name.
//   - the NOT EXISTS suppresses the crew row whenever ANY explicit grant for the
//     same credential exists, including a lapsed one. The explicit grant is the
//     authoritative binding for that pair, so a lease that has expired removes
//     the credential outright instead of quietly falling back to the crew's
//     standing copy — which would defeat the TTL with no trace anywhere.
//   - credentialLeaseGateSQL applies to the explicit half only: credential_crews
//     has no expires_at column, exactly as internal_credentials.go already
//     documents for the sidecar listing.
//   - a crew-less agent (crew_id IS NULL) matches nothing in the second half.
//     Left implicit it would be a NULL join and still match nothing, but the
//     explicit predicate is what a reader needs to see, because the failure mode
//     is handing every crew-linked credential in the workspace to an unassigned
//     agent.
//   - the crew half orders after explicit grants at equal priority, so an
//     env-var collision resolves to the grant someone configured by hand (the
//     orchestrator takes the first match).
func loadDeliveredCredentials(ctx context.Context, db *sql.DB, agentID string) ([]deliveredCredential, error) {
	rows, err := db.QueryContext(ctx, agentDeliveredCredentialsSQL,
		agentID, leaseComparisonNow(), agentID)
	if err != nil {
		return nil, fmt.Errorf("query delivered credentials: %w", err)
	}
	defer rows.Close()

	var out []deliveredCredential
	for rows.Next() {
		var d deliveredCredential
		var crewDerived int
		if err := rows.Scan(&d.ID, &d.EnvVar, &d.Priority, &d.EncryptedValue,
			&d.Type, &d.Username, &d.LeaseExpiresAt, &crewDerived); err != nil {
			return nil, fmt.Errorf("scan delivered credential: %w", err)
		}
		d.CrewDerived = crewDerived == 1
		out = append(out, d)
	}
	return out, rows.Err()
}
