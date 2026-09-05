package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
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
	       COALESCE(c.provider, '')    AS provider,
	       COALESCE(c.username, '')    AS username,
	       COALESCE(ac.expires_at, '') AS lease_expires_at,
	       c.handle_only     AS handle_only,
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
	       COALESCE(c.provider, '') AS provider,
	       COALESCE(c.username, '') AS username,
	       ''  AS lease_expires_at,
	       c.handle_only AS handle_only,
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
	       COALESCE(c.provider, '') AS provider,
	       COALESCE(c.username, '') AS username,
	       ''  AS lease_expires_at,
	       c.handle_only AS handle_only,
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
	  -- Suppress the crew-link ONLY when a binding actually delivers this
	  -- credential (it is in resolved_bindings), not merely when it has one.
	  -- A binding that LOSES its slot to a more specific binding leaves the
	  -- credential undelivered by the binding arm; suppressing here against
	  -- applicable_bindings — every binding, winner or not — dropped it from
	  -- the crew arm too, so a crew-linked secret with a losing binding
	  -- reached the container under no variable at all. The crew link is
	  -- independent of the binding's slot contest and must survive it.
	  AND NOT EXISTS (
	      SELECT 1 FROM resolved_bindings rb WHERE rb.credential_id = c.id
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
	// Provider is credentials.provider — the free-form service label
	// ("ANTHROPIC", "OPENROUTER", "GITHUB", "NONE"). It is the only thing that
	// identifies a provider whose agent-facing env-var name does not
	// (credTypeToProvider's switch). Selected as COALESCE(c.provider, '')
	// because the column is TEXT NOT NULL DEFAULT 'NONE' with no CHECK, and a
	// row written before that default existed can still be NULL.
	Provider       string
	Username       string
	LeaseExpiresAt string
	// HandleOnly is credentials.handle_only (#2376): the agent may USE this
	// value — through /keeper/execute or the sidecar proxy — but never read
	// it. Every loader that turns a row into a delivered credential leaves the
	// value empty when this is set, and does so regardless of Keeper state:
	// it is a property of the secret, not of the instance's configuration.
	HandleOnly bool
	// Source is one of the credSource* ranks: where this row came from, and
	// how specific it is. Nothing branches on it today; it exists so a caller
	// that needs to distinguish an explicit grant from a workspace-wide
	// binding does not have to re-derive it from the shape of the row.
	Source int
	// Fields are the credential's additional named parts (credential_fields,
	// PRD §2.2), with their env-var names already derived from THIS row's
	// EnvVar and already checked against every other name in the delivery.
	// Nil for a credential with no parts, which is every credential that
	// exists today — see credential_field_delivery.go.
	Fields []deliveredCredentialField
	// GrantedAgentIDs is the set of crew members holding this credential
	// (#2052), or nil when it reaches the whole crew. It is what lets the
	// crew-wide sidecar CredStore refuse to serve one member's endpoint
	// credential to another. Computed per CREDENTIAL, so every member of the
	// crew sees the same value — see credential_grantees.go for why that is not
	// optional.
	GrantedAgentIDs []string
	// FieldConflicts are the parts that were refused a name. Carried rather
	// than logged at the source so the caller, which knows the agent and has a
	// logger, can report them; a part that vanishes with no trace is the exact
	// failure this design is built to avoid.
	FieldConflicts []deliveredFieldConflict
}

// deliveredSlotNotice records what happened to one credential's delivery slot:
// it was normalised onto a legal environment variable name, or it could not be
// and the credential is not delivered at all.
//
// Delivered is the name the container will actually see, or "" when the
// credential is not delivered. Requested is what the operator's row said — a
// display name from the crew link, a binding's slot, or an explicit grant's
// env_var_name — so the report can name the thing the operator typed rather
// than the thing we derived from it. Never a value: this is a map, not a
// reveal (§2.6 L9).
type deliveredSlotNotice struct {
	CredentialID string
	Requested    string
	Delivered    string
	Reason       string
}

// logHandleOnlyWithheld is the one line every delivery path writes when it
// leaves a handle-only credential's value behind (#2376). Env-var name only,
// never the value — the same discipline as the Keeper withhold log, and for
// the same reason: this is where the plaintext would have been, so it is the
// place an operator looks when an agent says the credential is "missing".
func logHandleOnlyWithheld(logger *slog.Logger, agentID, envVar string) {
	if logger == nil {
		return
	}
	logger.Warn("handle-only credential withheld from delivery — usable through /keeper/execute only",
		"agent_id", agentID, "env_var", envVar)
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
//
// The second return is every credential whose slot was NOT the string the query
// produced — normalised onto a legal environment variable name, or refused one
// and dropped from the set entirely (#1657). It is a return value rather than a
// field on the rows because a dropped credential has no row left to hang it on:
// leaving it in the slice with a blank slot would ship its ciphertext to the
// sidecar under no name at all. Every caller must do something with it; the
// three boot/delegation paths log it, the resolution view reports it to the
// operator.
func loadDeliveredCredentials(ctx context.Context, db *sql.DB, agentID string) ([]deliveredCredential, []deliveredSlotNotice, error) {
	rows, err := db.QueryContext(ctx, agentDeliveredCredentialsSQL,
		agentID, agentID, leaseComparisonNow())
	if err != nil {
		return nil, nil, fmt.Errorf("query delivered credentials: %w", err)
	}
	defer rows.Close()

	var out []deliveredCredential
	for rows.Next() {
		var d deliveredCredential
		// Scan order follows the SELECT list column-for-column. The three UNION
		// arms must stay aligned: inserting a column into one arm only would
		// shift every later field into the wrong struct member silently, with
		// no error from database/sql.
		if err := rows.Scan(&d.ID, &d.EnvVar, &d.Priority, &d.EncryptedValue,
			&d.Type, &d.Provider, &d.Username, &d.LeaseExpiresAt, &d.HandleOnly, &d.Source); err != nil {
			return nil, nil, fmt.Errorf("scan delivered credential: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Slots are settled BEFORE the parts are attached, because a part's name is
	// derived from its credential's slot (<SLOT>_<KEY>). Attaching first would
	// derive AWS_REGION off a slot that is about to change, and seed the field
	// claim table with names no container will ever see.
	out, notices := resolveDeliverySlots(out)

	// The parts of a multi-part credential hang off the row's resolved EnvVar,
	// so they are attached HERE — after the set and its slots are final and
	// before any consumer sees it. A consumer that derived part names itself
	// would be the second resolution path, and the second path is always the one
	// that misses a filter (see the notes above on how many resolvers had to be
	// fixed for the ACTIVE status and the #1373 lease gate).
	//
	// rows must be closed before this runs: SQLite serialises writes against an
	// open read cursor, and holding one across a second query is how a delivery
	// path acquires a deadlock nobody can reproduce.
	rows.Close()
	if err := attachDeliveredCredentialFields(ctx, db, out); err != nil {
		return nil, nil, err
	}

	// The agent dimension the crew-wide sidecar CredStore needs (#2052).
	// Attached HERE, at the same chokepoint the set itself is derived, for the
	// reason this file's header gives: a second derivation elsewhere is always
	// the one that misses a source. Skipped entirely when nothing was delivered.
	if len(out) > 0 {
		grantees, err := loadCrewCredentialGrantees(ctx, db, agentID)
		if err != nil {
			return nil, nil, err
		}
		for i := range out {
			out[i].GrantedAgentIDs = grantees.grantedTo(out[i].ID, agentID)
		}
	}
	return out, notices, nil
}
