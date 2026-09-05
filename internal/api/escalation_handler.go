package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// maxJournalEscalationFieldLen bounds each agent-supplied text field
// (reason, context, metadata) written into the peer.escalation journal
// payload. Unlike the escalations row and the inbox projection, a journal
// entry is permanent (append-only, hash-chained) and explicitly excluded
// from the GDPR erasure cascade (admin_gdpr.go), and this route bypasses
// BodyCap entirely (router.go ~813-818) — so without a cap here, an agent
// can put an unbounded blob into `context` and it survives every erasure
// forever. 4096 runes is generous enough to keep genuine multi-paragraph
// escalation detail (the summary line, separately, is bounded to 140
// chars for the single-line UI) while giving the pathological case — a
// megabyte of tool output stuffed into context — a real ceiling rather
// than a nominally-large one. See #2238.
const maxJournalEscalationFieldLen = 4096

// PendingEscalationCount returns the number of unresolved escalations workspace-wide.
func (h *QueryHandler) PendingEscalationCount(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Settle any question whose deadline has passed before counting. This is
	// the "computed on read" half of the expiry design (see
	// escalation_lifecycle.go): the background sweeper is what guarantees it
	// eventually happens, and this is what guarantees no surface can show a
	// stale number in the meantime. Both go through the same CAS-guarded
	// transition, so the row still flips exactly once.
	h.sweepExpiredEscalationsBestEffort(r.Context(), workspaceID)
	var count int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM escalations e
		 JOIN crews c ON c.id = e.crew_id
		 WHERE c.workspace_id = ? AND e.status = 'PENDING' AND c.deleted_at IS NULL`,
		workspaceID).Scan(&count)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

// CreateEscalation handles POST /api/v1/internal/escalations.
// Auth: protected by internalAuth middleware (X-Internal-Token) in router.go.
func (h *QueryHandler) CreateEscalation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromSlug    string `json:"from_slug"`
		Reason      string `json:"reason"`
		Context     string `json:"context"`
		Type        string `json:"type"`
		Metadata    string `json:"metadata"`
		CrewID      string `json:"crew_id"`
		WorkspaceID string `json:"workspace_id"`
		ChatID      string `json:"chat_id"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if body.FromSlug == "" || body.Reason == "" || body.CrewID == "" || body.WorkspaceID == "" || body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "from_slug, reason, crew_id, workspace_id, chat_id required",
		})
		return
	}
	// PR-F24 F-4: a bound token may only raise escalations in its own
	// workspace (body workspace_id is the insert scope; the auth
	// middleware can't inspect bodies).
	if !assertInternalTokenWorkspace(w, r, body.WorkspaceID) {
		return
	}
	// PR-F24 foreign-ID closure: crew_id and chat_id are independent of the
	// workspace_id checked above — prove they belong to the bound workspace
	// before inserting the escalation so a ws-A token can't raise one
	// attributed to a ws-B crew/chat.
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &body.CrewID) {
		return
	}
	if !assertBoundChatWorkspaceDB(w, r, h.db, h.logger, body.ChatID) {
		return
	}

	// Look up the from agent
	var fromAgentID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id FROM agents WHERE slug = ? AND crew_id = ? AND deleted_at IS NULL
	`, body.FromSlug, body.CrewID).Scan(&fromAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "from agent not found")
			return
		}
		replyInternalError(w, h.logger, "lookup from agent for escalation", err)
		return
	}

	escalationID := generateCUID()
	raisedAt := time.Now().UTC()
	now := raisedAt.Format(time.RFC3339)
	// Two clocks, written here, because they answer two different questions
	// (see escalation_lifecycle.go):
	//
	//   deadlineAt       how long the AGENT waits. Published in the response so
	//                    the sidecar's poll is bounded by the SERVER's clock —
	//                    it used to pick its own 300 s that merely happened to
	//                    match, and the two now come from one place.
	//   answerDeadlineAt how long a HUMAN may still answer. Days, not minutes:
	//                    this is the one an operator who walked off to fetch an
	//                    API key is racing, and making it the agent's number is
	//                    what turned "Approve" into 409.
	deadlineAt := raisedAt.Add(escalationAgentWait).Format(time.RFC3339)
	answerDeadlineAt := raisedAt.Add(escalationAnswerTTL).Format(time.RFC3339)

	escalationType := body.Type
	if escalationType == "" {
		escalationType = "TEXT"
	}
	if escalationType != "TEXT" && escalationType != "CREDENTIAL" && escalationType != "LINK" {
		replyError(w, http.StatusBadRequest, "type must be TEXT, CREDENTIAL, or LINK")
		return
	}

	if escalationType == "LINK" {
		if body.Metadata == "" {
			replyError(w, http.StatusBadRequest, "metadata (https URL) required for LINK type")
			return
		}
		u, parseErr := url.ParseRequestURI(body.Metadata)
		if parseErr != nil || u.Scheme != "https" || u.Host == "" {
			replyError(w, http.StatusBadRequest, "metadata must be a valid https URL")
			return
		}
	}

	// CREDENTIAL escalations may carry a structured proposal in `metadata`:
	// {"name","type","provider","value"}. When present we create the proposed
	// credential up front in PENDING_APPROVAL state (value encrypted) and link
	// it here, so a human can approve it with one click. SECURITY: the metadata
	// carries a secret, so once a proposal is detected we ALWAYS replace
	// body.Metadata with a redacted blob (no value) before it is stored on the
	// escalation row or emitted to the journal — even on the fallback path where
	// no pending row was created (e.g. name collision, no workspace owner).
	var credentialID interface{}
	var credentialName string
	// credentialRequested marks an ASK (#2376): the agent has no value and is
	// asking a human for one. The inbox card must then offer a masked input
	// that goes to the supply endpoint, not a one-click Approve.
	var credentialRequested bool
	var credentialHosts []string
	var credentialPurpose string
	if escalationType == "CREDENTIAL" {
		if proposal, ok := parseCredentialProposal(body.Metadata); ok {
			// Keeper judges the ASK before anything is staged (#2392). Only an
			// ASK — a PROPOSE carries a value the agent already holds and is the
			// human approving a generated secret, not a request to grant one.
			// DENY stages nothing and interrupts no one; the agent gets the
			// reason. ESCALATE (and a judge outage) still stages and routes to a
			// human, with the judge's note attached so they see why.
			if proposal.IsAsk() {
				dec := h.judgeAsk(r.Context(), CredentialAskInput{
					WorkspaceID:    body.WorkspaceID,
					CrewID:         body.CrewID,
					AgentID:        fromAgentID,
					AgentName:      body.FromSlug,
					CredentialName: proposal.Name,
					Purpose:        proposal.Purpose,
					SecurityLevel:  int(proposal.SecurityLevel),
				})
				if dec.deny {
					h.logger.Info("credential ask denied by keeper",
						"from", body.FromSlug, "credential", proposal.Name, "crew_id", body.CrewID)
					// Best-effort journal so a denied ask is not invisible to the
					// operator — a spike of these is exactly what four-eyes and the
					// watchdog exist to surface. No credential, no inbox row.
					_, _ = h.journal.Emit(r.Context(), journal.Entry{
						WorkspaceID: body.WorkspaceID,
						CrewID:      body.CrewID,
						AgentID:     fromAgentID,
						Type:        journal.EntryKeeperDecision,
						Severity:    journal.SeverityNotice,
						ActorType:   journal.ActorKeeper,
						ActorID:     "keeper",
						Summary: fmt.Sprintf("keeper denied credential ask %q from %s",
							proposal.Name, body.FromSlug),
						Payload: map[string]any{
							"decision":        "deny",
							"credential_name": proposal.Name,
							"security_level":  int(proposal.SecurityLevel),
							"reason":          dec.reason,
							"from_slug":       body.FromSlug,
						},
						Refs: map[string]any{"chat_id": body.ChatID},
					})
					writeJSON(w, http.StatusOK, map[string]any{
						"status":          "DENIED",
						"decision":        "deny",
						"keeper_judged":   true,
						"reason":          dec.reason,
						"credential_name": proposal.Name,
					})
					return
				}
				if dec.note != "" {
					body.Context = prependEscalationNote(body.Context, dec.note)
				}
			}
			cid, outcome := h.createPendingCredential(r.Context(), body.WorkspaceID, fromAgentID, proposal)
			switch outcome {
			case pendingCredStaged:
				credentialID = cid
				credentialName = proposal.Name
				credentialRequested = proposal.IsAsk()
				credentialHosts = proposal.Hosts
				credentialPurpose = proposal.Purpose
			case pendingCredNameConflict:
				// Recoverable: no credential was staged, but a human should still
				// be told. Lead the escalation with a note so the reporter isn't
				// left thinking a one-click approval is waiting. (Secret already
				// discarded; redaction below still runs.)
				body.Context = prependEscalationNote(body.Context,
					fmt.Sprintf("A credential named %q already exists, so the agent's proposed value was NOT staged and has been discarded — supply or rotate it manually.", proposal.Name))
			case pendingCredInvalidType:
				body.Context = prependEscalationNote(body.Context,
					fmt.Sprintf("The proposed credential type %q is not recognized, so the value was NOT staged and has been discarded — a human must supply the credential.", proposal.Type))
			case pendingCredValueTooLarge:
				body.Context = prependEscalationNote(body.Context,
					fmt.Sprintf("The proposed credential value (%d bytes) exceeds the %d-byte limit, so it was NOT staged and has been discarded — a human must supply the credential.", len(proposal.Value), maxCredentialValueLen))
			case pendingCredNoApprover:
				// Hard failure: nothing can approve the credential and the secret
				// is gone. Fail LOUD instead of recording a PENDING escalation that
				// falsely claims a proposal is waiting (doctrine: never fake success).
				h.logger.Warn("escalation credential proposal rejected: no workspace owner",
					"workspace_id", body.WorkspaceID, "from", body.FromSlug)
				replyError(w, http.StatusServiceUnavailable,
					"credential proposal could not be stored: no workspace owner is configured to approve it")
				return
			case pendingCredVaultError:
				h.logger.Warn("escalation credential proposal rejected: vault error",
					"workspace_id", body.WorkspaceID, "from", body.FromSlug)
				replyError(w, http.StatusServiceUnavailable,
					"credential proposal could not be stored: vault error — retry")
				return
			}
			body.Metadata = proposal.redactedMetadata(cid) // cid=="" on the note paths
		} else if metadataCarriesValue(body.Metadata) {
			// Malformed proposal that still embeds a secret (e.g. missing name) —
			// never persist or journal it raw. Drop to a redacted marker.
			body.Metadata = `{"redacted":true}`
		}
	}

	var contextVal interface{}
	if body.Context != "" {
		contextVal = body.Context
	}

	var metadataVal interface{}
	if body.Metadata != "" {
		metadataVal = body.Metadata
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, context, type, metadata, credential_id, status, created_at, deadline_at, answer_deadline_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?, ?, ?)
	`, escalationID, body.WorkspaceID, body.CrewID, body.ChatID, fromAgentID, body.Reason, contextVal, escalationType, metadataVal, credentialID, now, deadlineAt, answerDeadlineAt)
	if err != nil {
		h.logger.Error("create escalation", "error", err)
		// Don't leave an orphaned PENDING_APPROVAL credential with no escalation
		// to approve it through — roll it back. (createPendingCredential commits
		// before this insert; this is the compensating delete.)
		if cid, ok := credentialID.(string); ok && cid != "" {
			if _, delErr := h.db.ExecContext(r.Context(),
				`DELETE FROM credentials WHERE id = ? AND workspace_id = ? AND status = 'PENDING_APPROVAL'`,
				cid, body.WorkspaceID); delErr != nil {
				h.logger.Error("rollback orphaned pending credential", "error", delErr, "credential_id", cid)
			}
		}
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Write-through to inbox_items so the escalation surfaces in the
	// unified Inbox without a fan-out query at read time. Best-effort:
	// failure here is logged + swallowed; the escalations table stays
	// the source of truth and a future inbox-rebuild job can backfill.
	// The inbox row is a projection broadcast to every MANAGER in the
	// workspace; the escalations table (access-controlled) stays the
	// source of truth for the real value. So redact any credential
	// material an agent put in reason/context before it lands in body_md.
	// For CREDENTIAL escalations, lead with an explicit note that the
	// secret is handled in the credential flow, not shown here.
	inboxBody := inbox.RedactSecrets(body.Context)
	if escalationType == "CREDENTIAL" {
		inboxBody = "🔒 A credential is being requested — the secret is handled in the credential flow and is not shown here.\n\n" + inboxBody
		if credentialRequested {
			inboxBody = "🔒 The agent is asking you for a credential it does not have. " +
				"Supply the value from the escalation card: it goes straight into the vault, " +
				"and the agent receives a name to use it by — never the value.\n\n" + inboxBody
		}
	}
	inboxPayload := map[string]interface{}{
		"crew_id":         body.CrewID,
		"chat_id":         body.ChatID,
		"reason":          inbox.RedactSecrets(body.Reason),
		"escalation_type": escalationType,
	}
	// What is being approved travels WITH the request. The inbox is the
	// surface with the Approve button, and it used to show "Escalation type
	// LINK" and nothing else — the URL lived only on the escalations row, so
	// a person approved an address they could not see. Same for a credential
	// proposal, whose name was only in the title. The LINK metadata was
	// validated as an https URL above; the credential NAME is operator
	// metadata, never the value (redacted before it is stored, see above).
	if escalationType == "LINK" && body.Metadata != "" {
		inboxPayload["link_url"] = body.Metadata
	}
	if credentialName != "" {
		inboxPayload["credential_name"] = credentialName
	}
	// Signal to the inbox UI that this CREDENTIAL escalation already has a
	// proposed credential waiting in the vault, so it can show a one-click
	// Approve (vs the legacy human-supplies-the-secret flow that routes to the
	// crew escalations panel).
	if cid, ok := credentialID.(string); ok && cid != "" {
		inboxPayload["credential_id"] = cid
		if credentialRequested {
			// An ASK: the card needs an input, not an Approve. Hosts and purpose
			// are agent-authored review metadata — redacted like every other
			// agent field on this projection, and never enforced.
			inboxPayload["needs_credential_value"] = true
			if len(credentialHosts) > 0 {
				inboxPayload["credential_hosts"] = credentialHosts
			}
			if credentialPurpose != "" {
				inboxPayload["credential_purpose"] = inbox.RedactSecrets(credentialPurpose)
			}
		} else {
			inboxPayload["has_pending_credential"] = true
		}
	}
	// A credential proposal reads more naturally in the inbox as a credential
	// approval than as a generic "Agent escalation". The proposal name is
	// operator-supplied metadata, not the secret. For the generic fallback,
	// redact BEFORE truncating: a secret whose closing delimiter falls past the
	// 80-char cut would otherwise leave an unmatched (= unredacted) prefix.
	inboxTitle := fmt.Sprintf("Agent escalation: %s", truncate(inbox.RedactSecrets(body.Reason), 80))
	if credentialName != "" {
		inboxTitle = fmt.Sprintf("Credential approval: %s", credentialName)
		if credentialRequested {
			inboxTitle = fmt.Sprintf("Credential requested: %s", credentialName)
		}
	}
	inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
		WorkspaceID: body.WorkspaceID,
		Kind:        inbox.KindEscalation,
		SourceID:    escalationID,
		TargetRole:  "MANAGER",
		Title:       inboxTitle,
		BodyMD:      inboxBody,
		SenderType:  "agent",
		SenderID:    fromAgentID,
		SenderName:  body.FromSlug,
		Priority:    "high",
		Blocking:    true,
		Payload:     inboxPayload,
	})

	// Dual-write the escalation into the journal. Severity=warn because
	// an unresolved escalation should surface in the default "things
	// needing attention" filter (severity IN (warn, error)).
	//
	// Unlike the escalations row (access-controlled source of truth) and
	// like the inbox projection above, the journal is a projection too —
	// and a permanent one (append-only, hash-chained, excluded from GDPR
	// erasure) that this internal-IPC route can fill with an unbounded
	// agent-supplied blob (no BodyCap on this path). So every
	// agent-supplied field here gets the same treatment as the inbox
	// copy — inbox.RedactSecrets, then bounded — rather than a second
	// redaction policy. See #2238.
	journalReason := truncate(inbox.RedactSecrets(body.Reason), maxJournalEscalationFieldLen)
	journalContext := truncate(inbox.RedactSecrets(body.Context), maxJournalEscalationFieldLen)
	journalMetadata := truncate(inbox.RedactSecrets(body.Metadata), maxJournalEscalationFieldLen)
	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     fromAgentID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorAgent,
		ActorID:     fromAgentID,
		// Redact BEFORE truncating for the same reason the inbox title
		// does above: a secret whose closing delimiter falls past the
		// 140-char cut would otherwise leave an unmatched (= unredacted)
		// prefix in this permanent, human-visible summary line.
		Summary: fmt.Sprintf("escalation from %s: %s", body.FromSlug, truncate(inbox.RedactSecrets(body.Reason), 140)),
		Payload: map[string]any{
			"reason":          journalReason,
			"context":         journalContext,
			"escalation_type": escalationType,
			"metadata":        journalMetadata,
			"from_slug":       body.FromSlug,
			"state":           "pending",
		},
		Refs: map[string]any{"escalation_id": escalationID, "chat_id": body.ChatID},
	})

	// Broadcast escalation event. Ephemeral (not stored), but distributed
	// wider than either projection above: the workspace-scoped broadcast
	// reaches every connected member, not just this escalation's audience
	// — so `reason` gets the same redaction as everywhere else in this
	// function even though, unlike the journal, it isn't the retention
	// risk. See #2238.
	broadcastReason := inbox.RedactSecrets(body.Reason)
	broadcastChannelEvent(h.hub, "session", body.ChatID, "escalation_created",
		map[string]string{
			"id":     escalationID,
			"from":   body.FromSlug,
			"reason": broadcastReason,
		})
	broadcastWorkspaceEvent(h.hub, body.WorkspaceID, "escalation.created",
		map[string]string{
			"id":        escalationID,
			"crew_id":   body.CrewID,
			"from_slug": body.FromSlug,
			"reason":    broadcastReason,
		})

	h.logger.Info("escalation created",
		"escalation_id", escalationID,
		"from", body.FromSlug,
		"crew_id", body.CrewID,
	)

	created := map[string]any{
		"escalation_id": escalationID,
		"status":        escalationStatusPending,
		// The contract the sidecar bounds its long poll on. Publishing both
		// the absolute instant and the window means a caller with a skewed
		// clock can still use the duration, and one without can use the
		// instant — but neither has to guess, which is what the old hardcoded
		// 300 s amounted to.
		"deadline_at":     deadlineAt,
		"timeout_seconds": int(escalationAgentWait.Seconds()),
		// The human's clock, published for the console and the CLI rather than
		// for the sidecar — an operator looking at an inbox item needs to know
		// how long it stays actionable, and the number they must NOT be shown is
		// the agent's poll window.
		"answer_deadline_at": answerDeadlineAt,
	}
	// The name the agent will be able to use the credential by, so it can say
	// so in its report before the human has even answered. Never a value.
	if cid, ok := credentialID.(string); ok && cid != "" {
		created["credential_id"] = cid
		created["credential_name"] = credentialName
		created["credential_requested"] = credentialRequested
	}
	writeJSON(w, http.StatusCreated, created)
}

// ResolveEscalation handles PATCH /api/v1/escalations/{escalationId}/resolve.
func (h *QueryHandler) ResolveEscalation(w http.ResponseWriter, r *http.Request) {
	escalationID := r.PathValue("escalationId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	// The authenticated human resolving the escalation — recorded as the actor
	// on the resolution journal entry (audit: who approved/rejected/redirected).
	resolverUserID := ""
	if u := UserFromContext(r.Context()); u != nil {
		resolverUserID = u.ID
	}
	// Require at least MANAGER to resolve escalations (data-modifying operation)
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var body struct {
		Resolution string `json:"resolution"`
		Action     string `json:"action"`
		RedirectTo string `json:"redirect_to"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	// "resolution required" is decided below, once the escalation's type is
	// known: a CREDENTIAL escalation takes none at all (#2376).

	// Default action to "approve" for backward compatibility.
	if body.Action == "" {
		body.Action = "approve"
	}
	if body.Action != "approve" && body.Action != "reject" && body.Action != "redirect" {
		replyError(w, http.StatusBadRequest, "action must be approve, reject, or redirect")
		return
	}
	if body.Action == "redirect" && body.RedirectTo == "" {
		replyError(w, http.StatusBadRequest, "redirect_to required when action is redirect")
		return
	}
	if body.Action != "redirect" {
		body.RedirectTo = ""
	}

	var status, chatID, crewID, fromSlug, escalationType string
	var credentialID, initiatorUserID sql.NullString
	// agentGaveUpAt is set when the agent's wait window closed before a human
	// answered. It does NOT block the resolve — that is the whole point of the
	// two clocks — but it changes what the operator is told about the effect.
	var agentGaveUpAt sql.NullString
	// fromAgentID/fromAgentName: the agent that raised the escalation. Needed by
	// the #1373 lease mint below — approving an agent-proposed credential is an
	// approval, and an approval is what issues a lease.
	var fromAgentID, fromAgentName string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT e.status, e.chat_id, e.crew_id, a.slug, e.type, e.credential_id, a.created_by_user_id,
		       e.from_agent_id, COALESCE(a.name, ''), e.agent_gave_up_at
		FROM escalations e
		JOIN agents a ON a.id = e.from_agent_id
		WHERE e.id = ? AND e.workspace_id = ?
	`, escalationID, workspaceID).Scan(&status, &chatID, &crewID, &fromSlug, &escalationType, &credentialID, &initiatorUserID,
		&fromAgentID, &fromAgentName, &agentGaveUpAt)

	// Validate redirect_to agent exists in the same crew (after we know crew_id).
	if err == nil && body.Action == "redirect" && body.RedirectTo != "" {
		var exists int
		if scanErr := h.db.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM agents WHERE slug = ? AND crew_id = ? AND deleted_at IS NULL
		`, body.RedirectTo, crewID).Scan(&exists); scanErr != nil {
			h.logger.Error("resolve escalation redirect lookup", "error", scanErr)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if exists == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("redirect_to agent %q not found in crew", body.RedirectTo),
			})
			return
		}
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "escalation not found")
			return
		}
		replyInternalError(w, h.logger, "resolve escalation lookup", err)
		return
	}
	// Terminal is terminal. The message names which terminal state, because
	// "already resolved" told an operator whose escalation had EXPIRED that
	// somebody had decided it — the opposite of what happened.
	if status != escalationStatusPending {
		replyError(w, http.StatusConflict, escalationTerminalError(status))
		return
	}

	// What this channel may carry depends on the type (#2376). A CREDENTIAL
	// escalation takes NO text: the only thing a human ever typed into this
	// field for one was the secret, and that now goes through the supply
	// endpoint into the vault. Refusing the field outright, rather than
	// redacting it, is what makes "paste it into the resolution box" stop
	// being a path at all. For every other type the text IS the answer.
	if escalationType == "CREDENTIAL" {
		if body.Resolution != "" {
			replyError(w, http.StatusBadRequest,
				"a CREDENTIAL escalation takes no resolution text — supply the value with "+
					"POST /api/v1/escalations/{id}/supply (crewship escalation supply), "+
					"which stores it in the vault and hands the agent a name, never the value")
			return
		}
		if body.Action == "approve" && credentialID.Valid && credentialID.String != "" {
			var credStatus string
			if err := h.db.QueryRowContext(r.Context(),
				`SELECT status FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
				credentialID.String, workspaceID).Scan(&credStatus); err == nil && credStatus == credentialStatusRequested {
				replyError(w, http.StatusConflict,
					"the agent is asking you for a value it does not have — approving records nothing; "+
						"supply it with POST /api/v1/escalations/{id}/supply (crewship escalation supply)")
				return
			}
		}
	} else if body.Resolution == "" {
		replyError(w, http.StatusBadRequest, "resolution required")
		return
	}

	// Segregation of duties (issue #1084): a workspace can opt in to a strict
	// four-eyes rule for CREDENTIAL escalations — the human recorded as the
	// initiating agent's owner (agents.created_by_user_id, v100) may not also
	// be the one who resolves it. This is checked BEFORE any mutation and
	// applies to every action (approve/reject/redirect), not just approve:
	// the point is that the same person can't unilaterally close out a
	// credential request their own agent raised. Deliberately independent of
	// role — canRole above already gated MANAGER+, and this is a strict
	// approver-must-differ-from-initiator rule with NO OWNER bypass. If the
	// agent has no recorded owner (legacy pre-v99 rows), the rule cannot be
	// enforced and resolution proceeds as before.
	//
	// KNOWN SCOPE LIMIT: "initiator" is proxied by agent *ownership*
	// (created_by_user_id), not the human who actually drove the agent to raise
	// the escalation. If user A owns the agent but user B drives it via chat to
	// propose a credential, the rule blocks A, not B — so B could still
	// self-approve. Tightening this to the actual requester needs the escalation
	// to record the driving user at raise time (follow-up); until then this is a
	// four-eyes control keyed on ownership, which covers the common case.
	if h.refuseCredentialSelfApproval(w, r, secondApproverInput{
		workspaceID: workspaceID, crewID: crewID, chatID: chatID, escalationID: escalationID,
		escalationType: escalationType, fromSlug: fromSlug, action: body.Action,
		credentialID: credentialID, initiatorUserID: initiatorUserID,
	}) {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// A CREDENTIAL escalation stores no resolution at all (#2376). This column
	// used to hold the human-typed secret, encrypted — a second vault with no
	// tier, lease, rotation or audit timeline, read by exactly one caller,
	// which handed the plaintext to the agent. NULL, not a marker: a marker
	// would be a value that pretends to be a decision.
	var storedResolution interface{}
	if escalationType != "CREDENTIAL" {
		storedResolution = body.Resolution
	}

	var redirectToVal interface{}
	if body.RedirectTo != "" {
		redirectToVal = body.RedirectTo
	}

	result, err := h.db.ExecContext(r.Context(), `
		UPDATE escalations SET status = 'RESOLVED', resolution = ?, action = ?, redirect_to = ?, resolved_at = ?, resolved_by = 'user'
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING'
	`, storedResolution, body.Action, redirectToVal, now, escalationID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve escalation update", err)
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "resolve escalation rows affected", err)
		return
	}
	if n == 0 {
		// Lost the compare-and-swap: a cancel, an expiry or another resolve
		// landed between the read above and this UPDATE.
		replyError(w, http.StatusConflict, "escalation is no longer pending")
		return
	}

	// Agent-proposed credential: a CREDENTIAL escalation may link a credential
	// already sitting in the vault as PENDING_APPROVAL. Approve activates it —
	// attributed to the human approver, which is the named-human gate that makes
	// the credential usable; reject soft-deletes it. Idempotent + best-effort:
	// the escalation has already transitioned, so a missing/already-flipped
	// credential is logged, never a 500.
	if credentialID.Valid && credentialID.String != "" {
		approverID := ""
		if user := UserFromContext(r.Context()); user != nil {
			approverID = user.ID
		}
		switch body.Action {
		case "approve":
			res, credErr := h.db.ExecContext(r.Context(), `
				UPDATE credentials
				SET status = 'ACTIVE', approved_by_user_id = ?, approved_at = ?, created_by = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ? AND status = 'PENDING_APPROVAL' AND deleted_at IS NULL
			`, approverID, now, approverID, now, credentialID.String, workspaceID)
			if credErr != nil {
				h.logger.Error("approve pending credential", "error", credErr, "credential_id", credentialID.String)
			} else if rows, _ := res.RowsAffected(); rows == 0 {
				h.logger.Warn("approve pending credential: no pending row to activate", "credential_id", credentialID.String)
			} else {
				recordCredentialEventBestEffort(r.Context(), h.db, h.logger, credentialID.String,
					AuditEventApproved, "", "", map[string]any{"approved_by": approverID})
				// #1373: the approval is the moment access is granted, so it is the
				// moment to issue a LEASE. Only runs when the workspace has opted
				// in (auto_lease_seconds > 0) — see grantLeasedCredentialOnApprove
				// for why the opt-in gate is load-bearing here and not merely a
				// feature flag.
				h.grantLeasedCredentialOnApprove(r.Context(), workspaceID, crewID,
					fromAgentID, fromAgentName, credentialID.String, escalationID)
			}
		case "reject":
			res, credErr := h.db.ExecContext(r.Context(), `
				UPDATE credentials SET status = 'REJECTED', deleted_at = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ? AND status IN ('PENDING_APPROVAL', 'REQUESTED') AND deleted_at IS NULL
			`, now, now, credentialID.String, workspaceID)
			if credErr != nil {
				h.logger.Error("reject pending credential", "error", credErr, "credential_id", credentialID.String)
			} else if rows, _ := res.RowsAffected(); rows == 0 {
				h.logger.Warn("reject pending credential: no pending row to delete", "credential_id", credentialID.String)
			} else {
				recordCredentialEventBestEffort(r.Context(), h.db, h.logger, credentialID.String,
					AuditEventRejected, "", "", map[string]any{"rejected_by": approverID})
			}
		}
	}

	// Mirror the resolution into the unified inbox so the row drops
	// from "needs action" into the resolved feed in real time. Done
	// after the source UPDATE so we don't flip the inbox row before
	// the source actually transitions.
	if user := UserFromContext(r.Context()); user != nil {
		inbox.ResolveBySource(r.Context(), h.db, h.logger,
			"escalation", escalationID, body.Action, user.ID)
	} else {
		inbox.ResolveBySource(r.Context(), h.db, h.logger,
			"escalation", escalationID, body.Action, "")
	}

	// Resolution closes the escalation thread in the journal. Severity
	// stays at notice (not warn) because the ongoing-attention signal
	// ended — filters on "warn+ only" will drop this correctly.
	//
	// CRITICAL: CREDENTIAL escalations carry secret material in
	// body.Resolution (that's why the storage path above encrypts it
	// before writing to the escalations table). Never write the raw
	// value into the journal payload — the journal is a broadcast
	// stream visible to every workspace reader. Replace with an
	// opaque marker instead; the encrypted value in `escalations.
	// resolution` stays the canonical record.
	//
	// Every OTHER type still writes body.Resolution — an operator-supplied
	// free-text field — straight into the same permanent, hash-chained
	// entry. The type check above only catches a secret the escalation
	// itself was ABOUT; it says nothing about a secret the resolving human
	// happens to paste into a TOOL or TEXT resolution ("used token ghp_...
	// to unblock it"), so it needs the same redact-then-bound treatment
	// CreateEscalation gives the agent-supplied fields (#2238) rather than a
	// second policy that only fires on one escalation type.
	resolutionForJournal := truncate(inbox.RedactSecrets(body.Resolution), maxJournalEscalationFieldLen)
	if escalationType == "CREDENTIAL" {
		// Refused above when non-empty. The marker this used to write
		// described a secret that no longer passes through here at all.
		resolutionForJournal = ""
	}
	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     resolverUserID,
		Summary:     fmt.Sprintf("escalation %s resolved (%s)", escalationID, body.Action),
		Payload: map[string]any{
			"resolution":      resolutionForJournal,
			"action":          body.Action,
			"redirect_to":     body.RedirectTo,
			"state":           "resolved",
			"escalation_type": escalationType,
			"credential_id":   credentialID.String,
			// Whether the decision reached the run that asked for it. An
			// operator reconstructing "why did that agent proceed without my
			// approval" needs this on the resolution entry, not only on the
			// give-up that happened hours earlier.
			"agent_still_waiting": agentGaveUpAt.String == "",
			"agent_gave_up_at":    agentGaveUpAt.String,
		},
		Refs: map[string]any{"escalation_id": escalationID},
	})

	// Notify any waiting sidecar that the escalation has been resolved. A
	// CREDENTIAL answer is a HANDLE (#2376): the name the agent may use the
	// credential by and how, never a value — there is none on this path.
	waiterResult := escalationResult{
		Action:               body.Action,
		RedirectTo:           body.RedirectTo,
		CredentialEscalation: escalationType == "CREDENTIAL",
	}
	if escalationType == "CREDENTIAL" {
		if body.Action == "approve" {
			waiterResult.Credential = h.credentialHandleFor(r.Context(), workspaceID, credentialID.String, fromAgentID)
		}
	} else {
		waiterResult.Resolution = body.Resolution
	}
	h.notifyEscalationWaiter(escalationID, waiterResult)

	broadcastResolution := body.Resolution
	if escalationType == "CREDENTIAL" {
		broadcastResolution = "[credential " + body.Action + "d]"
		if body.Action == "redirect" {
			broadcastResolution = "[credential request redirected]"
		}
	}
	broadcastChannelEvent(h.hub, "session", chatID, "escalation_resolved",
		map[string]string{
			"id":         escalationID,
			"resolution": broadcastResolution,
			"action":     body.Action,
		})
	broadcastWorkspaceEvent(h.hub, workspaceID, "escalation.resolved",
		map[string]string{
			"id":        escalationID,
			"crew_id":   crewID,
			"from_slug": fromSlug,
			"action":    body.Action,
		})

	h.logger.Info("escalation resolved",
		"escalation_id", escalationID,
		"crew_id", crewID,
		"action", body.Action,
	)

	// What a LATE answer means, said out loud.
	//
	// The agent's wait and the human's answerability are two clocks now, so a
	// decision can legitimately arrive after the run that asked has already
	// continued without it. That resolve succeeds — refusing it was the
	// regression — but the operator must not walk away believing they unblocked
	// that run. They did not: it was told in writing, minutes or days ago, that
	// it was proceeding without the answer, and it has since finished.
	//
	// What a late approval DOES accomplish is not nothing, and this is why the
	// answer is still worth giving: the staged credential is now ACTIVE in the
	// vault (above), so the next run has it and the agent does not have to ask
	// again. For a TEXT or LINK escalation the record of the decision is the
	// value — the next agent to ask the same question finds it answered.
	agentStillWaiting := !agentGaveUpAt.Valid || agentGaveUpAt.String == ""
	resp := map[string]any{
		"id":                  escalationID,
		"status":              "RESOLVED",
		"action":              body.Action,
		"agent_still_waiting": agentStillWaiting,
	}
	if !agentStillWaiting {
		resp["agent_gave_up_at"] = agentGaveUpAt.String
		note := fmt.Sprintf(
			"Recorded, but %s stopped waiting at %s and continued without this answer — that run will not receive it.",
			fromSlug, agentGaveUpAt.String)
		if credentialID.Valid && credentialID.String != "" && body.Action == "approve" {
			note += " The credential is now active in the vault, so the next run has it."
		} else {
			note += " The decision stands on the record for the next time it is asked."
		}
		resp["note"] = note
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListEscalations handles GET /api/v1/crews/{crewId}/escalations.
func (h *QueryHandler) ListEscalations(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	h.sweepExpiredEscalationsBestEffort(r.Context(), workspaceID)

	limit, offset := parsePagination(r, 50, 100)

	// ?status= narrows to one lifecycle state. The CLI's `--status` flag and
	// docs/cli/escalation.mdx have advertised this filter since they were
	// written; the server ignored it, so `escalation list --status PENDING`
	// quietly returned everything. With four states in the vocabulary that
	// stopped being a cosmetic gap.
	//
	// An unrecognised value returns nothing rather than everything: a typo
	// that silently widens a filter is how an operator concludes there are no
	// expired escalations when there are.
	statusFilter := r.URL.Query().Get("status")

	type escalationItem struct {
		ID                 string  `json:"id"`
		Type               string  `json:"type"`
		FromName           string  `json:"from_name"`
		FromSlug           string  `json:"from_slug"`
		Reason             string  `json:"reason"`
		Context            *string `json:"context"`
		Metadata           *string `json:"metadata"`
		PeerConversationID *string `json:"peer_conversation_id"`
		Status             string  `json:"status"`
		Resolution         *string `json:"resolution"`
		Action             *string `json:"action"`
		RedirectTo         *string `json:"redirect_to"`
		ResolvedBy         *string `json:"resolved_by"`
		ResolvedAt         *string `json:"resolved_at"`
		CreatedAt          string  `json:"created_at"`
		// CredentialID links an agent-proposed CREDENTIAL escalation to the
		// PENDING_APPROVAL credential it created; non-null means "approve here
		// activates it" (no secret to type).
		CredentialID *string `json:"credential_id"`
		// CredentialStatus is the linked credential's status: REQUESTED means
		// the agent is asking for a value the console must collect (supply),
		// PENDING_APPROVAL means the agent proposed one (approve). Absent when
		// no credential is linked (#2376).
		CredentialStatus *string `json:"credential_status,omitempty"`

		// The two clocks, and the stamp that says which of them has run out.
		//
		// DeadlineAt bounds the AGENT's wait — the console should never present
		// it as the operator's countdown, which is what the single-deadline
		// version of this branch effectively did.
		// AnswerDeadlineAt is when THIS question stops being answerable and the
		// Approve button really will start refusing.
		// AgentGaveUpAt, when set, means the asking run already continued
		// without an answer: still answerable, but answering it will not reach
		// that run. A console that shows the two as the same thing recreates the
		// bug in the UI after it was fixed in the server.
		//
		// All pointers: NULL on rows raised before the columns existed, and "no
		// deadline" and "the epoch" are very different claims.
		DeadlineAt       *string `json:"deadline_at"`
		AnswerDeadlineAt *string `json:"answer_deadline_at"`
		AgentGaveUpAt    *string `json:"agent_gave_up_at"`

		// The four-eyes rule as it will be applied to THIS row (issue #1559).
		// ResolveEscalation decides it from two inputs the console could not
		// see — the workspace toggle and the tier of the linked credential —
		// so an Approve button that was going to 403 looked exactly like one
		// that was not, and the refusal was the first anyone heard of it.
		//
		// SecondApproverRequired is the answer; the two `By` flags are the
		// reason, and they are independent: a workspace can have the rule on
		// while the tier would have forced it anyway. Read live rather than
		// stored, because both inputs can change after the escalation is
		// raised.
		SecondApproverRequired    bool `json:"second_approver_required"`
		SecondApproverByWorkspace bool `json:"second_approver_by_workspace"`
		SecondApproverByTier      bool `json:"second_approver_by_tier"`
		// SecurityLevelLabel is the linked credential's tier ("L4 · critical"),
		// from keeper's table so the console does not keep its own copy. Empty
		// when the escalation has no credential behind it.
		SecurityLevelLabel string `json:"security_level_label,omitempty"`

		// initiatorUserID / securityLevel back the fields above and are not
		// serialised: the owning user id is not the console's business, and
		// the raw level is already carried as its label.
		initiatorUserID sql.NullString
		securityLevel   sql.NullInt64
	}

	// LEFT JOIN, not JOIN: a CREDENTIAL escalation may carry no credential row
	// (the legacy flow where the human supplies the secret), and those rows
	// must still list.
	query := `
		SELECT e.id, e.type, e.reason, e.context, e.metadata, e.peer_conversation_id, e.status,
		       e.resolution, e.action, e.redirect_to, e.resolved_by, e.resolved_at, e.created_at,
		       e.credential_id, e.deadline_at, e.answer_deadline_at, e.agent_gave_up_at,
		       from_a.name, from_a.slug, from_a.created_by_user_id, c.security_level, c.status
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		LEFT JOIN credentials c ON c.id = e.credential_id AND c.workspace_id = e.workspace_id
		WHERE e.crew_id = ? AND e.workspace_id = ?`
	args := []interface{}{crewID, workspaceID}
	if statusFilter != "" {
		query += ` AND e.status = ?`
		args = append(args, statusFilter)
	}
	query += ` ORDER BY e.created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list escalations", err)
		return
	}
	defer rows.Close()

	items := make([]escalationItem, 0, capacityHint(limit))
	for rows.Next() {
		var item escalationItem
		if err := rows.Scan(
			&item.ID, &item.Type, &item.Reason, &item.Context, &item.Metadata,
			&item.PeerConversationID, &item.Status, &item.Resolution, &item.Action,
			&item.RedirectTo, &item.ResolvedBy, &item.ResolvedAt, &item.CreatedAt,
			&item.CredentialID, &item.DeadlineAt, &item.AnswerDeadlineAt, &item.AgentGaveUpAt,
			&item.FromName, &item.FromSlug,
			&item.initiatorUserID, &item.securityLevel, &item.CredentialStatus,
		); err != nil {
			replyInternalError(w, h.logger, "scan escalation", err)
			return
		}
		// Never expose credential material to the list response. The mask
		// names what actually happened: only a RESOLVED row carries a secret
		// (encrypted at rest by ResolveEscalation), and saying "[credential
		// submitted]" over an EXPIRED or CANCELLED row would report a
		// submission that never took place — the resolution text on those is
		// the lifecycle's own plaintext note, which is safe to show.
		if item.Type == "CREDENTIAL" && item.Resolution != nil && item.Status == escalationStatusResolved {
			masked := "[credential submitted]"
			item.Resolution = &masked
		}
		if item.securityLevel.Valid {
			item.SecurityLevelLabel = keeper.SecurityLevel(item.securityLevel.Int64).Label()
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration", err)
		return
	}

	// Four-eyes (#1559), mirroring ResolveEscalation's own reasoning:
	//
	//   - CREDENTIAL escalations only.
	//   - The rule compares the approver against the agent's recorded owner, so
	//     an agent with no owner (legacy pre-v99 row) cannot have it enforced —
	//     and a row that claimed otherwise would be worse than saying nothing.
	//   - The workspace toggle opts every tier in; the credential's own tier
	//     forces it on the top tier regardless. Either is sufficient.
	//
	// The governance row is read once for the whole page, not once per item.
	needsGovernance := false
	for i := range items {
		if items[i].Type == "CREDENTIAL" && items[i].initiatorUserID.Valid && items[i].initiatorUserID.String != "" {
			needsGovernance = true
			break
		}
	}
	if needsGovernance {
		gov := governance.Resolve(r.Context(), h.db, h.logger, workspaceID)
		for i := range items {
			it := &items[i]
			if it.Type != "CREDENTIAL" || !it.initiatorUserID.Valid || it.initiatorUserID.String == "" {
				continue
			}
			it.SecondApproverByWorkspace = gov.RequireSecondApprover
			if it.securityLevel.Valid {
				it.SecondApproverByTier = keeper.SecurityLevel(it.securityLevel.Int64).Tier().SecondApprover
			}
			it.SecondApproverRequired = it.SecondApproverByWorkspace || it.SecondApproverByTier
		}
	}

	writeJSON(w, http.StatusOK, items)
}

// PurgeEscalations handles DELETE /api/v1/crews/{crewId}/escalations —
// HARD-delete every escalation row for one crew in the context workspace. A
// teardown/reset primitive for seed --nuke: the escalations table carries a
// workspace_id but NO foreign key, so a crew delete never cascades to it and
// the rows survive a nuke as orphans (and keep surfacing in the inbox as
// synthetic escalation items). Crew-scoped to mirror the ListEscalations route
// and its wsCtx boundary, and gated on "manage" (OWNER/ADMIN) because it's
// destructive and cross-user. Returns {"deleted": N}.
func (h *QueryHandler) PurgeEscalations(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew id required")
		return
	}

	// Scope by BOTH crew_id and workspace_id: crewId comes from the path and a
	// caller must not reach another workspace's rows by guessing a crew id.
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM escalations WHERE crew_id = ? AND workspace_id = ?`, crewID, workspaceID)
	if err != nil {
		h.logger.Error("purge escalations", "error", err)
		replyError(w, http.StatusInternalServerError, "purge failed")
		return
	}
	deleted, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": deleted})
}

// secondApproverInput is what the four-eyes rule needs to know about the
// escalation being decided. Both deciders — ResolveEscalation and
// SupplyEscalationCredential — hand it the same row, so the rule is written
// once and cannot drift between "approve the agent's value" and "type the
// value yourself", which are the same act of trust.
type secondApproverInput struct {
	workspaceID, crewID, chatID, escalationID string
	escalationType, fromSlug, action          string
	credentialID, initiatorUserID             sql.NullString
}

// refuseCredentialSelfApproval enforces segregation of duties on a CREDENTIAL
// escalation (issue #1084): the human recorded as the initiating agent's owner
// may not also be the one who decides it, when the workspace opted in or the
// credential's tier forces it. Writes the 403 and journals the blocked attempt;
// returns true when the caller must stop. See the comment block at its call
// site in ResolveEscalation for the known scope limit (ownership as a proxy
// for the initiating human).
func (h *QueryHandler) refuseCredentialSelfApproval(w http.ResponseWriter, r *http.Request, in secondApproverInput) bool {
	if in.escalationType == "CREDENTIAL" && in.initiatorUserID.Valid && in.initiatorUserID.String != "" {
		gov := governance.Resolve(r.Context(), h.db, h.logger, in.workspaceID)
		// The workspace toggle is the opt-in. The credential's tier can also demand
		// it: an L4 credential is one an operator marked as production-critical, and
		// "one person can close out a request their own agent raised" is exactly the
		// hole four-eyes exists to close — so the tier forces the rule on whether or
		// not the workspace opted in. A workspace that has NOT opted in still gets
		// the rule for L4 only, which is the narrowest reading of what the level
		// means (internal/keeper/tier.go).
		// The tier is read here and the escalation is resolved by the
		// compare-and-swap UPDATE further down, so a tier change committed in
		// between would be decided against. Left as a read rather than folded into
		// a transaction spanning the resolve, deliberately:
		//
		// changing a credential's tier needs roleManage — the same right this
		// handler already requires — so an admin who wanted to escape their own
		// four-eyes requirement can lower the tier and approve SERIALLY. The race
		// grants nothing the actor cannot do without it, and the workspace toggle
		// is unaffected either way. Making it atomic means a transaction around
		// the resolve, the credential activation and the lease mint; worth doing,
		// not worth doing inside this branch.
		tierForces := false
		if in.credentialID.Valid && in.credentialID.String != "" {
			var lvl int
			if err := h.db.QueryRowContext(r.Context(),
				`SELECT COALESCE(security_level, 1) FROM credentials WHERE id = ? AND workspace_id = ?`,
				in.credentialID.String, in.workspaceID).Scan(&lvl); err == nil {
				tierForces = keeper.SecurityLevel(lvl).Tier().SecondApprover
			} else if !errors.Is(err, sql.ErrNoRows) {
				// A read failure must not silently drop a security control: treat an
				// unreadable tier as the strict case, the same fail-closed default the
				// tier table itself takes for an unknown level.
				h.logger.Warn("resolve escalation: credential tier unreadable; enforcing four-eyes",
					"error", err, "credential_id", in.credentialID.String)
				tierForces = true
			}
		}
		if gov.RequireSecondApprover || tierForces {
			forcedBy := "workspace policy"
			if tierForces {
				// The tier is the more specific reason, so it is the one named.
				forcedBy = "critical credential tier"
			}
			approverID := ""
			if user := UserFromContext(r.Context()); user != nil {
				approverID = user.ID
			}
			if approverID != "" && approverID == in.initiatorUserID.String {
				// Audit the blocked self-approval. A user attempting to resolve a
				// credential escalation their own agent raised is exactly the
				// security event a four-eyes control exists to catch, so it must
				// leave a trail — the successful resolution is journaled downstream,
				// but the denial otherwise left none.
				_, _ = h.journal.Emit(r.Context(), journal.Entry{
					WorkspaceID: in.workspaceID,
					CrewID:      in.crewID,
					Type:        journal.EntryKeeperDecision,
					Severity:    journal.SeverityError,
					ActorType:   journal.ActorUser,
					ActorID:     approverID,
					Summary: fmt.Sprintf(
						"blocked self-approval: user tried to %s a credential escalation raised by agent %s they own",
						in.action, in.fromSlug),
					Payload: map[string]any{
						"rule":            "segregation_of_duties",
						"action":          in.action,
						"escalation_type": in.escalationType,
						"from_slug":       in.fromSlug,
						// Which of the two rules fired. An incident review asking "why
						// was this blocked" should not have to infer it from the
						// workspace settings as they stand weeks later.
						"forced_by":         forcedBy,
						"initiator_user_id": in.initiatorUserID.String,
					},
					Refs: map[string]any{"escalation_id": in.escalationID, "chat_id": in.chatID},
				})
				replyError(w, http.StatusForbidden,
					"a second approver is required ("+forcedBy+"): you cannot resolve a credential escalation raised by an agent you own")
				return true
			}
		}
	}

	return false
}
