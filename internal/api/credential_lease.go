package api

// Credential lease ISSUANCE (#1373, second increment).
//
// v149 gave agent_credentials an `expires_at` and every injection path a
// fail-closed gate on it — the grant could CARRY and ENFORCE a lease. Nothing
// ever MINTED one except an operator typing `crewship credential assign --ttl`,
// so in practice every grant stayed standing and the ephemerality guarantee the
// issue asks for ("agents/sidecar hold only a lease, not the durable secret")
// was unmet.
//
// This file is the minting side: on a Keeper ALLOW, and on the approval of an
// agent-proposed CREDENTIAL escalation, the requesting agent's grant is
// re-issued as a short-lived lease whose TTL comes from the per-workspace
// keeper_governance_settings.auto_lease_seconds knob.
//
// Three deliberate constraints, all of which exist so turning this on cannot
// break a working deployment:
//
//   - OPT-IN. auto_lease_seconds defaults to 0 (off) and every pre-migration
//     workspace backfills to 0, so behaviour is unchanged until an OWNER/ADMIN
//     sets it.
//   - L3/L4 ONLY. L1/L2 are the boot-delivered self-service tier whose whole
//     point is that the agent holds the value for the run; expiring those
//     mid-run would break the agent's own LLM calls. Only Keeper-mediated
//     (L3) and human-escalation (L4) credentials are auto-leased.
//   - NEVER SHORTENS AN EXPLICIT LONGER LEASE. A standing grant (NULL) is
//     narrowed to now+TTL — that IS the feature — but a grant an operator
//     already leased for longer wins, so `--ttl 7d` is not silently rewritten
//     to 15 minutes on the next ALLOW.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// Lease provenance values written to agent_credentials.lease_source. They answer
// "why is this grant expiring?" in an incident review, which a bare expires_at
// cannot.
const (
	// leaseSourceManual: an operator set the TTL explicitly
	// (`crewship credential assign --ttl`, POST .../credentials ttl_seconds).
	leaseSourceManual = "manual"
	// leaseSourceKeeperAllow: the Keeper gatekeeper ALLOWed a request/execute
	// for an L3/L4 credential and the workspace has auto-issuance configured.
	leaseSourceKeeperAllow = "keeper_allow"
	// leaseSourceEscalationApprove: a human approved an agent-proposed
	// CREDENTIAL escalation, which grants the proposing agent access.
	leaseSourceEscalationApprove = "escalation_approve"
)

// leaseIssueInput identifies the grant to lease and the approval that authorised
// it. Everything here is already resolved by the caller (the Keeper handlers all
// hold the agent/credential names for their audit rows), so issuance costs one
// UPDATE plus the two best-effort audit writes.
type leaseIssueInput struct {
	WorkspaceID    string
	CrewID         string
	AgentID        string
	AgentName      string
	CredentialID   string
	CredentialName string
	// SecurityLevel gates issuance: below L3 the credential is boot-delivered
	// self-service and must not start expiring underneath a running agent.
	SecurityLevel int
	// Source is one of the leaseSource* constants.
	Source string
	// RequestID is the keeper_requests.id (Keeper ALLOW) or escalations.id
	// (escalation approve) that authorised the lease. Recorded on the grant so
	// the audit trail links the lease back to its decision.
	RequestID string
	// TTLSeconds is the resolved auto-lease TTL. <= 0 means auto-issuance is off
	// for this workspace and the call is a no-op.
	TTLSeconds int
}

// leaseEligible reports whether auto-issuance applies at all. Split out so both
// the issuer and its tests can assert the gate without a DB.
func (in leaseIssueInput) leaseEligible() bool {
	return in.TTLSeconds > 0 &&
		in.AgentID != "" && in.CredentialID != "" &&
		in.SecurityLevel >= int(keeper.SecurityLevelL3)
}

// issueCredentialLease re-issues (agent, credential) as a short-lived lease and
// returns the resulting expiry in RFC3339 UTC. issued is false when auto-lease
// is off, the credential is below L3, no grant row exists, or the grant already
// carries a lease that outlives the one we would mint.
//
// The expires_at written here is the SAME fixed-width RFC3339 UTC form every
// enforcement site compares against (keeper_execute's injection gate, the
// crew-scoped internal listing, the boot credential resolvers), so the TEXT
// comparison those queries do orders correctly.
//
// Best-effort by contract: the approval it accompanies has already been decided
// and recorded, so a failed lease write is logged and reported as not-issued
// rather than failing the caller's request. Failing an ALLOW because the lease
// bookkeeping hiccuped would turn a security nicety into an availability
// incident — and the un-leased outcome is the pre-#1373 status quo, not an
// escalation of privilege.
func issueCredentialLease(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	j journal.Emitter,
	in leaseIssueInput,
) (expiresAt string, issued bool) {
	if db == nil || !in.leaseEligible() {
		return "", false
	}

	now := time.Now().UTC()
	newExpiry := now.Add(time.Duration(in.TTLSeconds) * time.Second).Format(time.RFC3339)

	// The `expires_at IS NULL OR expires_at < ?` guard is what implements
	// "narrow a standing grant, extend a shorter lease, never shorten a longer
	// one". It also makes repeated ALLOWs idempotent-ish: a second ALLOW inside
	// the same second is a no-op rather than a churn of audit rows.
	res, err := db.ExecContext(ctx, `
		UPDATE agent_credentials
		   SET expires_at = ?, lease_source = ?, lease_issued_at = ?, lease_request_id = ?
		 WHERE agent_id = ? AND credential_id = ?
		   AND (expires_at IS NULL OR expires_at < ?)`,
		newExpiry, in.Source, now.Format(time.RFC3339), nullIfEmpty(in.RequestID),
		in.AgentID, in.CredentialID, newExpiry)
	if err != nil {
		if logger != nil {
			logger.Error("credential lease: issue failed",
				"error", err, "agent_id", in.AgentID, "credential_id", in.CredentialID)
		}
		return "", false
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		// n == 0 is the normal "operator's longer lease wins" / "grant was
		// concurrently unassigned" outcome, not an error.
		return "", false
	}

	// Durable trail. A grant that silently starts expiring is the kind of change
	// that shows up as an unexplained agent failure hours later, so record both
	// in the credential's own audit timeline and in the hash-chained journal.
	recordCredentialEventBestEffort(ctx, db, logger, in.CredentialID,
		AuditEventLeased, in.AgentID, "", map[string]any{
			"lease_source":     in.Source,
			"lease_expires_at": newExpiry,
			"ttl_seconds":      in.TTLSeconds,
			"security_level":   in.SecurityLevel,
			"request_id":       in.RequestID,
		})

	if j != nil {
		if _, jerr := j.Emit(ctx, journal.Entry{
			WorkspaceID: in.WorkspaceID,
			CrewID:      in.CrewID,
			AgentID:     in.AgentID,
			Type:        journal.EntryCredentialLeaseIssued,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorKeeper,
			ActorID:     "keeper",
			Summary: fmt.Sprintf("credential %s leased to %s until %s",
				in.CredentialName, in.AgentName, newExpiry),
			Payload: map[string]any{
				"credential_id":    in.CredentialID,
				"credential_name":  in.CredentialName,
				"lease_source":     in.Source,
				"lease_expires_at": newExpiry,
				"ttl_seconds":      in.TTLSeconds,
				"security_level":   in.SecurityLevel,
				"request_id":       in.RequestID,
			},
			Refs: map[string]any{
				"credential_id":     in.CredentialID,
				"keeper_request_id": in.RequestID,
			},
		}); jerr != nil && logger != nil {
			logger.Warn("credential lease: journal emit failed",
				"error", jerr, "credential_id", in.CredentialID)
		}
	}

	if logger != nil {
		logger.Info("credential lease issued",
			"credential", in.CredentialName, "agent", in.AgentName,
			"source", in.Source, "expires_at", newExpiry)
	}
	return newExpiry, true
}

// grantLeasedCredentialOnApprove issues the proposing agent a LEASED grant when
// a human approves the agent-proposed CREDENTIAL escalation that carried it
// (#1373: "issue a lease on … escalation approve").
//
// Why this is gated on auto_lease_seconds > 0 rather than always running:
// approving such an escalation currently activates the credential but creates NO
// agent_credentials row, so the proposing agent still cannot reach the value
// through any delivery path. Creating one unconditionally would therefore be a
// silent privilege GRANT layered onto an unrelated security PR — the wrong way
// to change an authorization default. With the workspace opted in, the grant we
// create is time-boxed by construction, which is the behaviour the issue asks
// for; with it off, nothing changes and the pre-existing gap stays as it is
// (tracked separately).
//
// Best-effort: the escalation has already transitioned, so a failure here is
// logged and the caller continues. The agent is then simply un-granted, which is
// exactly the status quo.
func (h *QueryHandler) grantLeasedCredentialOnApprove(
	ctx context.Context,
	workspaceID, crewID, agentID, agentName, credentialID, escalationID string,
) {
	if agentID == "" || credentialID == "" {
		return
	}
	ttl := governance.Resolve(ctx, h.db, h.logger, workspaceID).AutoLeaseSeconds
	if ttl <= 0 {
		return
	}

	var credName string
	var secLevel int
	if err := h.db.QueryRowContext(ctx, `
		SELECT name, COALESCE(security_level, 1) FROM credentials
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credentialID, workspaceID).Scan(&credName, &secLevel); err != nil {
		h.logger.Warn("credential lease on approve: credential lookup failed",
			"error", err, "credential_id", credentialID)
		return
	}
	// L1/L2 are the boot-delivered self-service tier: leasing them would expire
	// a key underneath a running agent. issueCredentialLease enforces the same
	// floor, but bail early so we don't create a grant we would not lease.
	if secLevel < int(keeper.SecurityLevelL3) {
		return
	}

	// Derive a safe env var name from the credential name, using the same
	// sanitizer /keeper/execute falls back to, so the two agree on what a
	// credential called "Deploy Key" is called in the environment.
	envVar := envVarSanitizePattern.ReplaceAllString(strings.ToUpper(credName), "_")
	if envVar == "" || !envVarNamePattern.MatchString(envVar) {
		envVar = "KEEPER_SECRET"
	}

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttl) * time.Second).Format(time.RFC3339)

	// INSERT OR IGNORE, then let issueCredentialLease do the lease write. The
	// two-step keeps ONE place responsible for lease semantics (narrow standing,
	// extend shorter, never shorten longer) and for the audit/journal trail,
	// instead of duplicating that logic in an upsert here. The UNIQUE constraint
	// on (agent_id, credential_id) makes the INSERT a no-op when the operator had
	// already assigned the credential.
	if _, err := h.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO agent_credentials
			(id, agent_id, credential_id, env_var_name, priority, created_at, expires_at, lease_source, lease_issued_at, lease_request_id)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		generateCUID(), agentID, credentialID, envVar, now.Format(time.RFC3339),
		expiresAt, leaseSourceEscalationApprove, now.Format(time.RFC3339), escalationID,
	); err != nil {
		h.logger.Warn("credential lease on approve: grant insert failed",
			"error", err, "agent_id", agentID, "credential_id", credentialID)
		return
	}

	issueCredentialLease(ctx, h.db, h.logger, h.journal, leaseIssueInput{
		WorkspaceID:    workspaceID,
		CrewID:         crewID,
		AgentID:        agentID,
		AgentName:      agentName,
		CredentialID:   credentialID,
		CredentialName: credName,
		SecurityLevel:  secLevel,
		Source:         leaseSourceEscalationApprove,
		RequestID:      escalationID,
		TTLSeconds:     ttl,
	})
}

// credentialLeaseGateSQL is the single canonical spelling of the lease gate,
// shared by every path that hands a per-agent grant's plaintext to an agent.
// Duplicating this predicate by hand is how #1373's first increment ended up
// enforcing the lease at /keeper/execute but not at boot: three other resolvers
// were reading agent_credentials with no expiry filter at all.
//
// The bound parameter is the caller's `now` in RFC3339 UTC (see
// leaseComparisonNow). NULL expires_at is a standing grant and always passes.
const credentialLeaseGateSQL = `(ac.expires_at IS NULL OR ac.expires_at > ?)`

// leaseComparisonNow returns the value to bind against credentialLeaseGateSQL.
// It exists so every site formats the comparison timestamp identically —
// fixed-width RFC3339 UTC, matching how issueCredentialLease and AddCredential
// WRITE the column, which is what makes the TEXT comparison a valid ordering.
func leaseComparisonNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}
