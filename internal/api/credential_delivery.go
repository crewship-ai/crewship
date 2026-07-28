package api

import (
	"context"
	"database/sql"
	"fmt"
)

// Which credentials does an agent receive? — one definition, three consumers.
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
// This query is the chokepoint: the delivery paths must not spell the
// derivation out by hand. #1373's first increment is the cautionary tale — the
// lease gate was written into /keeper/execute and three other resolvers kept
// reading agent_credentials with no expiry filter at all.

// Source ranks for a delivered row, most specific first. They double as the
// tie-break in ORDER BY, so the numbering is load-bearing and not decorative:
// the orchestrator resolves an env-var collision by taking the FIRST match.
//
// The legacy crew link ranks last on purpose. It delivers under
// credentials.name — a name, not a slot — so anything that made a deliberate
// statement about the env var outranks it.
const (
	credSourceAgentGrant       = 0 // explicit agent_credentials row
	credSourceBindingAgent     = 1 // credential_bindings, scope=AGENT
	credSourceBindingCrew      = 2 // credential_bindings, scope=CREW
	credSourceBindingWorkspace = 3 // credential_bindings, scope=WORKSPACE
	credSourceCrewLink         = 4 // credential_crews, delivered under credentials.name
)

// agentDeliveredCredentialsSQL is the whole delivery set for one agent.
//
// Three sources, in the order the compound SELECT declares them:
//
//  1. agent_credentials — an explicit, per-agent grant. Authoritative for its
//     credential (see the NOT EXISTS notes on loadDeliveredCredentials) and
//     lease-gated.
//  2. credential_bindings — (scope, slot) → credential, PRD §2.5b. This is what
//     makes ten GitHub accounts in one workspace possible: the env var is a
//     property of the binding, so ten crews can each bind GH_TOKEN to a
//     different account. Resolution is agent > crew > workspace.
//  3. credential_crews — the pre-binding model, delivering under
//     credentials.name. Kept, not migrated: this is the backward-compatibility
//     guarantee. A credential with no binding delivers exactly as it did
//     before, which is the state every existing workspace is in.
//
// Built with Sprintf rather than written out so the rank literals in the SQL
// cannot drift from the credSource* constants the Go side compares against —
// nothing would catch two numbers disagreeing across a language boundary.
//
// Bind order is (agentID /* CTE */, agentID /* explicit arm */,
// leaseComparisonNow()). The WITH clause is first in the text, so its parameter
// is first in the sequence — moving the CTE would silently shift every bind.
var agentDeliveredCredentialsSQL = fmt.Sprintf(`
	WITH agent_scope AS (
		SELECT a.id AS agent_id, a.workspace_id AS workspace_id, a.crew_id AS crew_id
		FROM agents a
		WHERE a.id = ? AND a.deleted_at IS NULL
	),
	applicable_bindings AS (
		SELECT s.agent_id      AS agent_id,
		       s.workspace_id  AS workspace_id,
		       b.credential_id AS credential_id,
		       b.slot          AS slot,
		       CASE b.scope WHEN 'AGENT' THEN %d WHEN 'CREW' THEN %d ELSE %d END AS spec
		FROM agent_scope s
		JOIN credential_bindings b
		  ON b.workspace_id = s.workspace_id
		 AND (   (b.scope = 'AGENT' AND b.agent_id = s.agent_id)
		      OR (b.scope = 'CREW'  AND s.crew_id IS NOT NULL AND b.crew_id = s.crew_id)
		      OR  b.scope = 'WORKSPACE')
	),
	resolved_bindings AS (
		SELECT ab.agent_id, ab.workspace_id, ab.credential_id, ab.slot, ab.spec
		FROM applicable_bindings ab
		WHERE ab.spec = (SELECT MIN(x.spec) FROM applicable_bindings x WHERE x.slot = ab.slot)
	)

	SELECT ac.credential_id AS credential_id,
	       ac.env_var_name  AS env_var_name,
	       ac.priority      AS priority,
	       c.encrypted_value AS encrypted_value,
	       c.type            AS cred_type,
	       COALESCE(c.username, '')    AS username,
	       COALESCE(ac.expires_at, '') AS lease_expires_at,
	       %d AS source_rank
	FROM agent_credentials ac
	JOIN credentials c ON c.id = ac.credential_id
	WHERE ac.agent_id = ? AND c.deleted_at IS NULL AND c.status = 'ACTIVE'
	  AND %s

	UNION ALL

	SELECT rb.credential_id AS credential_id,
	       rb.slot          AS env_var_name,
	       0                AS priority,
	       c.encrypted_value AS encrypted_value,
	       c.type            AS cred_type,
	       COALESCE(c.username, '') AS username,
	       ''  AS lease_expires_at,
	       rb.spec AS source_rank
	FROM resolved_bindings rb
	JOIN credentials c ON c.id = rb.credential_id
	WHERE c.deleted_at IS NULL AND c.status = 'ACTIVE'
	  AND c.workspace_id = rb.workspace_id
	  AND NOT EXISTS (
	      SELECT 1 FROM agent_credentials ac2
	      WHERE ac2.agent_id = rb.agent_id AND ac2.credential_id = rb.credential_id
	  )

	UNION ALL

	SELECT c.id   AS credential_id,
	       c.name AS env_var_name,
	       0      AS priority,
	       c.encrypted_value AS encrypted_value,
	       c.type            AS cred_type,
	       COALESCE(c.username, '') AS username,
	       ''  AS lease_expires_at,
	       %d AS source_rank
	FROM agent_scope s
	JOIN credential_crews cc ON cc.crew_id = s.crew_id
	JOIN credentials c ON c.id = cc.credential_id
	WHERE s.crew_id IS NOT NULL
	  AND c.deleted_at IS NULL AND c.status = 'ACTIVE'
	  AND c.workspace_id = s.workspace_id
	  AND NOT EXISTS (
	      SELECT 1 FROM agent_credentials ac2
	      WHERE ac2.agent_id = s.agent_id AND ac2.credential_id = c.id
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM applicable_bindings ab WHERE ab.credential_id = c.id
	  )

	ORDER BY priority ASC, source_rank ASC
`,
	credSourceBindingAgent, credSourceBindingCrew, credSourceBindingWorkspace,
	credSourceAgentGrant, credentialLeaseGateSQL, credSourceCrewLink)

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
	// Source is one of the credSource* ranks: where this row came from, and
	// how specific it is. Nothing branches on it today; it exists so a caller
	// that needs to distinguish an explicit grant from a workspace-wide
	// binding does not have to re-derive it from the shape of the row.
	Source int
}

// loadDeliveredCredentials runs the shared derivation for one agent.
//
// Notes on the SQL that are easy to lose in review:
//
//   - env_var_name comes from three different places, and that is the point of
//     §2.5b: the explicit grant's chosen env_var_name, the binding's slot, or —
//     only when neither exists — credentials.name. The last one is today's
//     convention and the reason a workspace could hold exactly one GitHub
//     account.
//   - resolution is per SLOT, not per scope: a slot is claimed by the most
//     specific scope that named it, and the claim stands even if that
//     credential then turns out undeliverable (revoked, soft-deleted, or
//     superseded by an explicit grant). The alternative — falling through to
//     the next binding down — would hand the crew an identity nobody chose, at
//     the exact moment someone was revoking one. Fail closed: a missing
//     GH_TOKEN is diagnosable, the wrong GH_TOKEN is not.
//   - both NOT EXISTS clauses on agent_credentials suppress on ANY explicit
//     grant for the credential, including a lapsed one. The explicit grant is
//     the authoritative binding for that pair, so an expired lease removes the
//     credential outright instead of quietly falling back to the crew's
//     standing copy — which would defeat the TTL with no trace anywhere.
//   - the crew-link arm additionally drops any credential that has an
//     applicable binding. Without it a bound-and-linked credential would arrive
//     TWICE, once as GH_TOKEN and once as "github-acme", doubling the blast
//     radius of a leak and making "the token is in the container" and "the tool
//     works" stop being the same statement.
//   - credentialLeaseGateSQL applies to the explicit arm only: neither
//     credential_crews nor credential_bindings has an expires_at column,
//     exactly as internal_credentials.go already documents for the sidecar
//     listing.
//   - a crew-less agent (crew_id IS NULL) matches nothing in the crew arm and
//     nothing in the CREW branch of the binding join. Left implicit it would be
//     a NULL comparison and still match nothing, but the explicit predicate is
//     what a reader needs to see, because the failure mode is handing every
//     crew-scoped credential in the workspace to an unassigned agent.
//   - c.workspace_id = <scope>.workspace_id on both derived arms is defence in
//     depth behind trg_credential_bindings_workspace_check and its
//     credential_crews twin. Tested directly (the triggers are dropped in those
//     tests) so it cannot be deleted as "obviously redundant".
func loadDeliveredCredentials(ctx context.Context, db *sql.DB, agentID string) ([]deliveredCredential, error) {
	rows, err := db.QueryContext(ctx, agentDeliveredCredentialsSQL,
		agentID, agentID, leaseComparisonNow())
	if err != nil {
		return nil, fmt.Errorf("query delivered credentials: %w", err)
	}
	defer rows.Close()

	var out []deliveredCredential
	for rows.Next() {
		var d deliveredCredential
		if err := rows.Scan(&d.ID, &d.EnvVar, &d.Priority, &d.EncryptedValue,
			&d.Type, &d.Username, &d.LeaseExpiresAt, &d.Source); err != nil {
			return nil, fmt.Errorf("scan delivered credential: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
