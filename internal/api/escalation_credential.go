package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// credentialProposal is the structured payload an agent sends in a CREDENTIAL
// escalation's `metadata` field. Two shapes share it, and the presence of
// `value` is what tells them apart:
//
//   - a PROPOSAL carries `value`. The agent generated the secret (a DB
//     password for infra it just set up); the server stores it as a
//     PENDING_APPROVAL credential and a human approves it.
//   - an ASK carries no `value`. The agent needs a secret only a human has.
//     The server stores a REQUESTED credential — name, type, tier, purpose,
//     no value — and the human supplies the value through
//     POST /escalations/{id}/supply. The agent is answered with a GRANT
//     (#2376): the name it may use the credential by, never the value.
type credentialProposal struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Value    string `json:"value"`
	// SecurityLevel is the tier the agent proposes for the credential (1..4).
	// Advisory: the approver sees it and the human who supplies the value can
	// override it. 0 means unset.
	SecurityLevel flexInt `json:"security_level"`
	// Purpose is what the credential is for, in the agent's words. Stored as
	// the credential's description so it outlives the escalation that asked.
	Purpose string `json:"purpose"`
	// Hosts are the destinations the agent says it will use the credential
	// against. Review metadata for the approver, NOT an enforced binding —
	// there is no per-credential egress policy (the model-scoped credentials
	// PRD records the same honest limit), and a list that looks enforced but
	// is not would be worse than none.
	Hosts []string `json:"hosts"`
}

// IsAsk reports whether the proposal carries no value — the agent is asking a
// human to supply one.
func (p credentialProposal) IsAsk() bool { return p.Value == "" }

// flexInt decodes a JSON number or a numeric string. Agents assemble the
// metadata blob by hand inside a shell heredoc, and "security_level":"3" is a
// far more likely slip than a wrong number; refusing the whole proposal for
// it would silently downgrade the ask to a free-text escalation.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

const (
	// maxProposalPurposeLen bounds the agent-authored purpose. It lands in a
	// credential's description and in the inbox card, both bounded surfaces.
	maxProposalPurposeLen = 500
	// maxProposalHosts bounds the declared destinations. A hostname is at most
	// 253 characters; twenty of them is more than any real ask names.
	maxProposalHosts = 20
)

// redactedMetadata returns the proposal as JSON with the secret value stripped,
// safe to persist on the escalation row, surface in ListEscalations, and emit to
// the journal. The raw proposal (with `value`) must NEVER be written anywhere
// except the encrypted credentials.encrypted_value column.
func (p credentialProposal) redactedMetadata(credentialID string) string {
	m := map[string]any{
		"name":          p.Name,
		"type":          p.Type,
		"provider":      p.Provider,
		"credential_id": credentialID,
	}
	if p.IsAsk() {
		m["requested"] = true
	}
	if p.SecurityLevel > 0 {
		m["security_level"] = int(p.SecurityLevel)
	}
	if p.Purpose != "" {
		m["purpose"] = p.Purpose
	}
	if len(p.Hosts) > 0 {
		m["hosts"] = p.Hosts
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// metadataCarriesValue reports whether the escalate metadata is JSON with a
// non-empty "value" field — i.e. it embeds a secret. Used to redact defensively
// even when the proposal is malformed (missing name, bad type, ...), so a
// secret can never reach escalations.metadata, ListEscalations, or the journal.
func metadataCarriesValue(metadata string) bool {
	s := strings.TrimSpace(metadata)
	if s == "" || s[0] != '{' {
		return false
	}
	var m struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		// Malformed JSON object — we cannot trust it doesn't embed a secret
		// (e.g. an unterminated `{"value":"secret"`). Fail closed: if it so
		// much as mentions a "value" key, treat it as secret-bearing so the
		// caller redacts instead of persisting/journaling the raw string. A
		// false positive here only over-redacts a non-secret; the inverse
		// would leak.
		return strings.Contains(s, `"value"`)
	}
	return strings.TrimSpace(m.Value) != ""
}

// parseCredentialProposal decodes the escalate `metadata` JSON. ok=false (no
// error) when the metadata is absent or not a usable credential proposal — the
// caller then records a plain CREDENTIAL escalation with no credential behind
// it. A proposal needs a name; a value makes it a PROPOSAL, its absence an ASK
// (see credentialProposal). Defaults type→SECRET, provider→NONE.
func parseCredentialProposal(metadata string) (credentialProposal, bool) {
	s := strings.TrimSpace(metadata)
	if s == "" || s[0] != '{' {
		return credentialProposal{}, false
	}
	var p credentialProposal
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return credentialProposal{}, false
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Type = strings.TrimSpace(p.Type)
	p.Provider = strings.TrimSpace(p.Provider)
	if p.Name == "" {
		return credentialProposal{}, false
	}
	if p.Type == "" {
		p.Type = "SECRET"
	}
	if p.Provider == "" {
		p.Provider = "NONE"
	}
	if p.SecurityLevel < 0 || p.SecurityLevel > 4 {
		p.SecurityLevel = 0
	}
	p.Purpose = truncate(strings.TrimSpace(p.Purpose), maxProposalPurposeLen)
	hosts := p.Hosts[:0]
	for _, h := range p.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || len(h) > 253 {
			continue
		}
		hosts = append(hosts, h)
		if len(hosts) == maxProposalHosts {
			break
		}
	}
	p.Hosts = hosts
	return p, true
}

// pendingCredResult classifies the outcome of staging an agent-proposed
// credential so the caller can react honestly. The distinction matters: a
// recoverable mismatch (name collision, unknown type) should still surface a
// plain escalation to a human, but a hard failure (no approver, vault error)
// must NOT be recorded as a PENDING escalation that falsely claims a proposal
// is waiting for one-click approval when the secret has already been discarded.
type pendingCredResult int

const (
	// pendingCredStaged: the credential row was created PENDING_APPROVAL; the
	// caller links it, redacts the metadata, and records the escalation.
	pendingCredStaged pendingCredResult = iota
	// pendingCredNameConflict: a live credential already uses the proposed name
	// (we never auto-rename). Recoverable — record a plain escalation with a
	// human-readable note, no credential link.
	pendingCredNameConflict
	// pendingCredInvalidType: the proposal's Type is unknown. Recoverable —
	// record a plain escalation with a note, no credential link.
	pendingCredInvalidType
	// pendingCredValueTooLarge: the proposed value exceeds
	// maxCredentialValueLen — the same cap create/update/rotate enforce;
	// without it the escalation flow would be the one uncapped path into
	// the vault. Recoverable — plain escalation with a note.
	pendingCredValueTooLarge
	// pendingCredNoApprover: no workspace OWNER exists to attribute/approve the
	// credential. Hard failure — the caller must NOT record an escalation.
	pendingCredNoApprover
	// pendingCredVaultError: encrypt or insert failed. Hard failure — the caller
	// must NOT record an escalation (the agent should retry).
	pendingCredVaultError
)

// prependEscalationNote puts a human-readable note at the top of an
// escalation's context, keeping any agent-supplied body below a blank line. Used
// when a credential proposal could not be staged but a plain escalation is still
// warranted, so the reporter isn't left thinking a one-click approval is waiting.
func prependEscalationNote(existing, note string) string {
	if strings.TrimSpace(existing) == "" {
		return note
	}
	return note + "\n\n" + existing
}

// createPendingCredential inserts the credential an agent's escalation
// metadata describes, in a state no delivery path serves:
//
//   - a PROPOSAL (value present) as PENDING_APPROVAL — a human flips it to
//     ACTIVE via ResolveEscalation;
//   - an ASK (no value) as REQUESTED, holding a sentinel instead of a value and
//     marked handle_only — a human fills it through SupplyEscalationCredential,
//     and the agent is answered with a grant, never the value (#2376).
//
// Either way the row is filtered out of every credential delivery path
// (agent_config, keeper, models, auto-assign) until a human acts.
//
// Returns (credentialID, pendingCredStaged) on success. On failure the second
// return classifies why (see pendingCredResult); the credentialID is "". The
// caller decides — from the class — whether a plain escalation is still
// warranted (recoverable) or the whole request must fail loud (hard failure).
func (h *QueryHandler) createPendingCredential(ctx context.Context, wsID, fromAgentID string, p credentialProposal) (string, pendingCredResult) {
	if msg := validateCredentialType(p.Type); msg != "" {
		h.logger.Warn("pending credential: invalid type, falling back to plain escalation",
			"type", p.Type, "agent_id", fromAgentID)
		return "", pendingCredInvalidType
	}
	if !p.IsAsk() && len(p.Value) > maxCredentialValueLen {
		h.logger.Warn("pending credential: value exceeds cap, falling back to plain escalation",
			"bytes", len(p.Value), "agent_id", fromAgentID)
		return "", pendingCredValueTooLarge
	}

	// credentials.created_by is NOT NULL → users(id); an agent has no human
	// identity, so attribute the proposal to the workspace OWNER. The approver
	// overwrites created_by on activation; created_by_actor_* preserves the
	// agent as the original proposer for the audit trail.
	var ownerID string
	if err := h.db.QueryRowContext(ctx, `
		SELECT user_id FROM workspace_members
		WHERE workspace_id = ? AND role = 'OWNER'
		ORDER BY created_at ASC LIMIT 1
	`, wsID).Scan(&ownerID); err != nil {
		// No OWNER row is a permanent config problem (fail loud, don't stage);
		// any other DB error is transient — surface it as a vault error so the
		// agent retries instead of being told "no approver" forever.
		if errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("pending credential: no workspace owner to attribute",
				"workspace_id", wsID)
			return "", pendingCredNoApprover
		}
		h.logger.Error("pending credential: owner lookup failed", "workspace_id", wsID, "error", err)
		return "", pendingCredVaultError
	}

	// Retire any DEAD proposal holding this name before deciding whether the
	// name is taken.
	//
	// A PENDING_APPROVAL credential is reachable through exactly one route: the
	// resolve path of the escalation that staged it (approve → ACTIVE, reject →
	// REJECTED + deleted_at). Once that escalation is no longer PENDING, the
	// route answers 409 forever, so the row can never be activated and never
	// rejected — it is an encrypted secret nobody can act on. Counting it as a
	// live name meant one unanswered question jammed auto-staging for that name
	// permanently: every later proposal came back as a conflict and the agent
	// was told to have a human type the value in by hand.
	//
	// The predicate is reachability (no PENDING escalation links it), not a list
	// of terminal statuses, because reachability is the property that makes the
	// row dead — and it keeps the rule symmetrical with the probe below: a
	// proposal whose question is STILL OPEN is genuinely live and must still
	// conflict, or two rows would race the UNIQUE(workspace_id, name) constraint
	// with no way to tell which one a human was looking at.
	//
	// The disposal is the same disposal expiry and cancellation now perform
	// (disposeStagedCredential); this pass is the net for rows stranded before
	// those paths learned to clean up, which no forward-looking fix can reach.
	if _, err := h.db.ExecContext(ctx, `
		UPDATE credentials
		   SET status = 'REJECTED', deleted_at = ?, updated_at = ?
		 WHERE workspace_id = ? AND name = ? AND status IN ('PENDING_APPROVAL', 'REQUESTED') AND deleted_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM escalations e
		        WHERE e.credential_id = credentials.id AND e.status = 'PENDING'
		   )`,
		time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), wsID, p.Name); err != nil {
		h.logger.Warn("pending credential: retire stranded proposal", "name", p.Name, "error", err)
	}

	// Clear any soft-deleted same-name row so the INSERT can't trip the
	// UNIQUE(workspace_id, name) constraint (mirrors credentials_mutate.go).
	// Runs after the retirement above so rows it just soft-deleted are cleared
	// in the same pass.
	if _, err := h.db.ExecContext(ctx,
		"DELETE FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NOT NULL",
		wsID, p.Name); err != nil {
		h.logger.Warn("pending credential: cleanup soft-deleted", "name", p.Name, "error", err)
	}
	// A live credential with this name already exists → do not auto-rename.
	var existing int
	if err := h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL",
		wsID, p.Name).Scan(&existing); err == nil && existing > 0 {
		h.logger.Warn("pending credential: name already in use, falling back to plain escalation",
			"name", p.Name)
		return "", pendingCredNameConflict
	}

	// An ASK has no value to keep. The sentinel is what every PENDING-shaped
	// row carries (isPendingSentinel): defence in depth behind the status
	// filter, so a query that ever forgot `status = 'ACTIVE'` would deliver a
	// placeholder rather than nothing at all — and never a real secret,
	// because there is none.
	//
	// handle_only is set on the ASK and only the ask. The whole point of the
	// row is that the agent gets to USE the value a human types and never to
	// read it; a proposal's value the agent already knows.
	toStore, status, handleOnly := p.Value, "PENDING_APPROVAL", 0
	if p.IsAsk() {
		toStore, status, handleOnly = pendingSentinelRequested, "REQUESTED", 1
	}
	enc, err := encryption.Encrypt(toStore)
	if err != nil {
		h.logger.Error("pending credential: encrypt", "error", err)
		return "", pendingCredVaultError
	}
	level := 1
	if p.SecurityLevel > 0 {
		level = int(p.SecurityLevel)
	}

	credID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(ctx, `
		INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
			type, provider, scope, security_level, status, created_by, created_at, updated_at,
			created_by_actor_type, created_by_actor_id, handle_only)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'WORKSPACE', ?, ?, ?, ?, ?, 'agent', ?, ?)`,
		credID, wsID, p.Name, p.Purpose, enc, p.Type, p.Provider, level, status, ownerID, now, now,
		fromAgentID, handleOnly); err != nil {
		h.logger.Error("pending credential: insert", "error", err, "name", p.Name)
		return "", pendingCredVaultError
	}

	recordCredentialEventBestEffort(ctx, h.db, h.logger, credID,
		AuditEventCreated, fromAgentID, "", map[string]any{
			"status":      status,
			"actor_type":  "agent",
			"proposed":    !p.IsAsk(),
			"requested":   p.IsAsk(),
			"handle_only": handleOnly == 1,
		})

	if p.IsAsk() {
		h.logger.Info("agent requested credential (awaiting a human-supplied value)",
			"credential_id", credID, "name", p.Name, "agent_id", fromAgentID)
	} else {
		h.logger.Info("agent proposed credential (pending approval)",
			"credential_id", credID, "name", p.Name, "agent_id", fromAgentID)
	}
	return credID, pendingCredStaged
}
