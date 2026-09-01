package api

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Who else in the crew holds this credential? — the agent dimension the sidecar
// CredStore needs (#2052).
//
// One sidecar serves a whole crew container, so its CredStore is crew-wide.
// Until #2052 that store answered "which credential for this provider?" with a
// round-robin counter and nothing about who was asking. For the fixed-host
// providers that only decided whose key paid. OPENAI_COMPAT takes its UPSTREAM
// from the credential, so it also decided which gateway received the prompt —
// and the #2051 allowlist union means the peer's host is allowlisted, so there
// is not even a 403 to notice.
//
// The fix needs the store to know which members a credential belongs to. The
// obvious value — "the agent this delivery is for" — is the wrong one, and the
// reason is the shared sidecar's restart logic. sidecarConfigFingerprint hashes
// the boot payload; sidecarNeedsRestart restarts the crew's sidecar whenever the
// fingerprint of the incoming exec differs from the running one. A field that
// held a different value in every member's payload would therefore restart the
// sidecar on every alternation between members — the thrash #1160 removed,
// through a new door. And crews are FULL of credentials granted explicitly to
// every member: autoAssignCredentials writes an agent_credentials row per agent
// for every crew created from a template.
//
// So the value has to be a property of the CREDENTIAL within the crew, computed
// identically no matter who asks:
//
//   - a credential reachable by the whole crew — a credential_crews link, or a
//     binding at CREW/WORKSPACE scope — is crew-wide, and delivers with NO agent
//     ids at all. That keeps its bytes (and the crew's fingerprint) identical to
//     before this existed.
//   - otherwise the grantees are the crew's members holding an explicit
//     agent-scoped claim: an agent_credentials row, or a binding at AGENT scope.
//     If that set is the whole crew, it is crew-wide in effect and delivers
//     empty too — which is the autoAssignCredentials case, i.e. most crews.
//   - anything narrower delivers the sorted member list, and the CredStore
//     refuses to serve it to anyone else.
//
// A crew-less agent has a sidecar of its own with no peers to cross over to;
// every credential it receives is crew-wide by definition and this file does no
// work for it.
//
// THE LOSING CASE, named rather than hidden. A binding at CREW scope that LOSES
// its slot to a more specific binding for one member is still counted crew-wide
// here, so that member could select through the sidecar a credential its own
// delivery did not include. That is exactly today's behaviour, so it is not a
// regression, and a crew already shares a container and therefore a trust
// domain (#2052's own framing). Narrowing it would mean re-running the whole
// delivery query once per member, which is the cost the design above exists to
// avoid.

// crewCredentialGrantees is the agent-scoped ownership of every credential in
// one crew: credential id -> the member ids holding an explicit claim on it.
// A credential absent from the map, or present with a nil value, is crew-wide.
type crewCredentialGrantees struct {
	// crewMembers is every non-deleted agent in the crew, used to collapse "all
	// of them" back to crew-wide.
	crewMembers map[string]struct{}
	// crewWide are credentials with a crew/workspace-scoped route into this
	// crew. They outrank any explicit grant: the credential reaches every
	// member regardless.
	crewWide map[string]struct{}
	// byCredential holds the explicit agent-scoped claims.
	byCredential map[string]map[string]struct{}
}

// grantedTo returns the sorted grantee list to deliver for credentialID, or nil
// when it is crew-wide. actingAgentID is always included in a non-nil result:
// this delivery is proof that the agent holds the credential, so a set that
// somehow omitted it would load a credential into the sidecar that even the
// booting agent cannot select — a self-inflicted 503 in place of a crossover.
func (g *crewCredentialGrantees) grantedTo(credentialID, actingAgentID string) []string {
	if g == nil || len(g.crewMembers) == 0 {
		return nil // crew-less agent: no peers, nothing to scope against
	}
	if _, ok := g.crewWide[credentialID]; ok {
		return nil
	}
	holders := g.byCredential[credentialID]
	out := make([]string, 0, len(holders)+1)
	seen := make(map[string]struct{}, len(holders)+1)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for id := range holders {
		add(id)
	}
	add(actingAgentID)
	if len(out) >= len(g.crewMembers) {
		// Every member holds it: crew-wide in effect, and delivering it as such
		// keeps the payload (and the crew's config fingerprint) unchanged. The
		// comparison is a length one because every grantee is a crew member by
		// construction — both source queries join through the crew.
		return nil
	}
	sort.Strings(out)
	return out
}

// crewMembersSQL lists the non-deleted agents sharing one agent's crew. A
// crew-less agent (crew_id IS NULL) matches nothing, which is the answer: it
// has no peers on its sidecar.
const crewMembersSQL = `
	SELECT peer.id
	FROM agents self
	JOIN agents peer
	  ON peer.crew_id = self.crew_id
	 AND peer.deleted_at IS NULL
	WHERE self.id = ? AND self.deleted_at IS NULL AND self.crew_id IS NOT NULL
`

// crewCredentialGranteesSQL returns, for one agent's crew, every agent-scoped
// claim on a credential (crew_wide = 0) and every credential with a
// crew/workspace-scoped route into that crew (crew_wide = 1).
//
// The arms mirror agentDeliveredCredentialsSQL's sources one for one — explicit
// grant, AGENT binding, CREW/WORKSPACE binding, crew link — because a source
// that exists there and not here would classify a credential as narrower than
// it is delivered, and the CredStore would refuse a member the credential it
// was actually given.
const crewCredentialGranteesSQL = `
	WITH agent_scope AS (
		SELECT a.id AS agent_id, a.workspace_id AS workspace_id, a.crew_id AS crew_id
		FROM agents a
		WHERE a.id = ? AND a.deleted_at IS NULL AND a.crew_id IS NOT NULL
	)

	SELECT ac.credential_id AS credential_id, ac.agent_id AS agent_id, 0 AS crew_wide
	FROM agent_credentials ac
	JOIN agents peer ON peer.id = ac.agent_id AND peer.deleted_at IS NULL
	JOIN agent_scope s ON peer.crew_id = s.crew_id

	UNION ALL

	SELECT b.credential_id, b.agent_id, 0
	FROM credential_bindings b
	JOIN agents peer ON peer.id = b.agent_id AND peer.deleted_at IS NULL
	JOIN agent_scope s ON peer.crew_id = s.crew_id AND b.workspace_id = s.workspace_id
	WHERE b.scope = 'AGENT'

	UNION ALL

	SELECT cc.credential_id, '', 1
	FROM credential_crews cc
	JOIN agent_scope s ON cc.crew_id = s.crew_id

	UNION ALL

	SELECT b2.credential_id, '', 1
	FROM credential_bindings b2
	JOIN agent_scope s ON b2.workspace_id = s.workspace_id
	WHERE b2.scope = 'WORKSPACE'
	   OR (b2.scope = 'CREW' AND b2.crew_id = s.crew_id)
`

// loadCrewCredentialGrantees reads both halves in two small queries. It is
// called once per delivery, alongside the delivery query itself.
//
// A failure is returned rather than swallowed: the caller decides, and
// loadDeliveredCredentials treats it as fatal for the delivery. Guessing
// "crew-wide" on an error would re-open the crossover silently, which is the
// failure mode this whole change is about; guessing "agent-scoped" would 503 a
// working crew. Neither is a guess worth making when the alternative is one
// clear error at boot.
func loadCrewCredentialGrantees(ctx context.Context, db *sql.DB, agentID string) (*crewCredentialGrantees, error) {
	g := &crewCredentialGrantees{
		crewMembers:  map[string]struct{}{},
		crewWide:     map[string]struct{}{},
		byCredential: map[string]map[string]struct{}{},
	}

	memberRows, err := db.QueryContext(ctx, crewMembersSQL, agentID)
	if err != nil {
		return nil, fmt.Errorf("query crew members for credential scope: %w", err)
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var id string
		if err := memberRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan crew member for credential scope: %w", err)
		}
		g.crewMembers[id] = struct{}{}
	}
	if err := memberRows.Err(); err != nil {
		return nil, err
	}
	// rows must be closed before the next query: SQLite serialises writes
	// against an open read cursor, the same reason loadDeliveredCredentials
	// closes its own before attaching fields.
	memberRows.Close()

	if len(g.crewMembers) == 0 {
		return g, nil // crew-less agent; grantedTo answers nil for everything
	}

	rows, err := db.QueryContext(ctx, crewCredentialGranteesSQL, agentID)
	if err != nil {
		return nil, fmt.Errorf("query credential grantees: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var credentialID, granteeID string
		var crewWide int
		if err := rows.Scan(&credentialID, &granteeID, &crewWide); err != nil {
			return nil, fmt.Errorf("scan credential grantee: %w", err)
		}
		if crewWide == 1 {
			g.crewWide[credentialID] = struct{}{}
			continue
		}
		if granteeID == "" {
			continue
		}
		if g.byCredential[credentialID] == nil {
			g.byCredential[credentialID] = map[string]struct{}{}
		}
		g.byCredential[credentialID][granteeID] = struct{}{}
	}
	return g, rows.Err()
}
