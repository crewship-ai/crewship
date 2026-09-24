package api

// Consumable Keeper approvals (#2574).
//
// A human resolving an escalation as ALLOW used to be a dead end. The ruling
// landed on keeper_requests, the inbox item and the ledger — and nothing else.
// The sidecar exposed no route to learn the outcome, and when the agent
// retried, the request was judged afresh: ApplyTierPolicyFloor re-escalated
// every L4 read, so with the default profile an approved production credential
// could never execute. The approval said "yes" and the system kept saying
// "ask again".
//
// This file is the other half. The agent presents the approved request id on
// its retry (approval_request_id, or the X-Keeper-Approval header the sidecar
// translates), and consumeKeeperApproval decides whether that approval may be
// spent on THIS request — then spends it, exactly once, atomically.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// keeperApprovalTTL bounds how long a human approval stays consumable after
// the resolve click. Long enough for an agent polling a request to notice and
// retry (the poll is seconds; this covers a wedged agent), short enough that a
// leaked request id is not a standing grant. The window starts at decided_at —
// written by the same resolve transaction that recorded the approval — so it
// measures the age of the DECISION, not of the retry.
const keeperApprovalTTL = 15 * time.Minute

// consumedApproval is what a successfully spent approval tells the caller.
type consumedApproval struct {
	// RequestID is the escalation row the approval came from — referenced by
	// the retry's audit trail so the chain survives the projection being
	// pruned.
	RequestID string
	// ApproverID is the user who resolved the escalation, carried onto the
	// retry's ledger transition: an ALLOW exercised by a human decision should
	// say WHO decided, not record the keeper as its author.
	ApproverID string
	// Reason carries the human's resolve reason onto the retry's own record:
	// the ALLOW the agent receives should say WHY it was allowed, and the why
	// lives on the approval, not on the retry.
	Reason string
	// RiskScore is the approval row's risk score, kept so the audit of the
	// retry reflects the decision actually exercised rather than a fabricated
	// low number.
	RiskScore int
}

// approvalFailure is a refusal to honour an approval, with the HTTP status it
// should be answered with. Kept as a value rather than a bool+code pair
// because every branch has a message the agent needs to act on — the codes
// are for the transport, the messages are for the retry loop.
type approvalFailure struct {
	status int
	body   string
}

func (f approvalFailure) write(w http.ResponseWriter) {
	writeJSON(w, f.status, map[string]string{"error": f.body})
}

// consumeKeeperApproval validates an approval against the retry presenting it
// and marks it consumed, atomically. On success the caller MUST treat the
// request as ALLOWed by a human without re-judging it. On failure it returns
// an approvalFailure for the caller to write; nothing has been consumed.
//
// The binding set — what makes THIS approval answer THIS request:
//
//   - the row was resolved by a human (resolved_by_user_id IS NOT NULL) — the
//     column is set only by HandleResolve, so a judge ALLOW can never be spent
//     as an approval and a chain of retries can never mint fresh ones
//   - the ruling is ALLOW (DENY refuses; ESCALATE/PENDING means nobody ruled)
//   - same agent, same credential, same request type as the escalation
//   - for /execute, the identical command: the human approved running THAT
//     command with THAT credential, not a class of commands
//   - within keeperApprovalTTL of decided_at
//   - not yet consumed (approval_consumed_at IS NULL), enforced by the same
//     UPDATE that consumes — two concurrent retries race on the write, and
//     exactly one changes a row
//
// Consumption precedes the retry's own execution, deliberately: if the exec
// later fails, the approval is spent. The alternative — refunding on failure —
// would turn a flaky container into an unlimited retry oracle, and single-use
// is the property the whole design rests on.
func (h *KeeperHandler) consumeKeeperApproval(
	ctx context.Context,
	approvalID, workspaceID, agentID, credentialID string,
	requestType keeper.RequestType,
	command string,
) (*consumedApproval, *approvalFailure) {
	if approvalID == "" {
		return nil, &approvalFailure{
			status: http.StatusBadRequest,
			body:   "approval_request_id is empty",
		}
	}

	var (
		decision     sql.NullString
		decidedAt    sql.NullString
		consumedAt   sql.NullString
		resolvedBy   sql.NullString
		rowAgentID   sql.NullString
		rowCredID    sql.NullString
		rowType      sql.NullString
		rowCommand   sql.NullString
		rowReason    sql.NullString
		rowRisk      sql.NullInt64
		rowWorkspace string
	)
	// Scoped through the credential's workspace, the same way HandleResolve
	// scopes: keeper_requests carries no workspace of its own, and a request
	// id from another tenant must learn nothing from the difference.
	err := h.db.QueryRowContext(ctx, `
		SELECT kr.decision, kr.decided_at, kr.approval_consumed_at, kr.resolved_by_user_id,
		       kr.requesting_agent_id, kr.credential_id, kr.request_type, kr.command,
		       kr.reason, kr.risk_score, c.workspace_id
		  FROM keeper_requests kr
		  JOIN credentials c ON c.id = kr.credential_id
		 WHERE kr.id = ?`, approvalID).
		Scan(&decision, &decidedAt, &consumedAt, &resolvedBy,
			&rowAgentID, &rowCredID, &rowType, &rowCommand,
			&rowReason, &rowRisk, &rowWorkspace)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &approvalFailure{
				status: http.StatusNotFound,
				body:   "approval request not found",
			}
		}
		return nil, &approvalFailure{
			status: http.StatusInternalServerError,
			body:   "failed to load the approval",
		}
	}
	if rowWorkspace != workspaceID {
		// 404, not 403: the caller must not learn that the id exists in
		// another tenant. Same convention as GetRequest's cross-tenant scope.
		return nil, &approvalFailure{
			status: http.StatusNotFound,
			body:   "approval request not found",
		}
	}

	// State before provenance: the message the caller can act on depends on
	// where the escalation stands, and "still waiting for a person" is the
	// answer for both PENDING and ESCALATE long before WHO resolved it could
	// matter.
	switch {
	case !decision.Valid || decision.String == string(keeper.DecisionEscalate) || decision.String == keeperStatePending:
		return nil, &approvalFailure{
			status: http.StatusForbidden,
			body:   "the escalation is still awaiting a human decision — poll it and retry when it is resolved",
		}
	case decision.String != string(keeper.DecisionAllow):
		return nil, &approvalFailure{
			status: http.StatusForbidden,
			body:   fmt.Sprintf("the escalation was resolved as %s — the credential will not be released for this request", decision.String),
		}
	}

	// Provenance: only a HUMAN resolve makes an approval. A judge ALLOW has
	// decided_at and decision='ALLOW' too, and without this check every past
	// ALLOW on this table would be a mintable approval.
	if !resolvedBy.Valid || resolvedBy.String == "" {
		return nil, &approvalFailure{
			status: http.StatusForbidden,
			body:   "this request was not resolved by a human — only an approved escalation can be presented as an approval",
		}
	}

	// Binding. A generic refusal on mismatch: the caller knows which ids it
	// sent, so there is nothing to protect by distinguishing the branches, and
	// one message keeps the agent's recovery simple (be judged afresh).
	if rowAgentID.String != agentID ||
		rowCredID.String != credentialID ||
		rowType.String != string(requestType) ||
		(requestType == keeper.RequestTypeExecute && rowCommand.String != command) {
		return nil, &approvalFailure{
			status: http.StatusForbidden,
			body:   "the approval does not bind to this request (agent, credential, request type or command differ) — submit without approval_request_id to be judged anew",
		}
	}

	// Expiry, from the resolve time. Unparseable/missing decided_at is not
	// consumable: an approval that cannot say when it was given cannot say it
	// is still fresh.
	if !decidedAt.Valid {
		return nil, &approvalFailure{
			status: http.StatusGone,
			body:   "the approval carries no decision time and cannot be honoured — submit without approval_request_id to be judged anew",
		}
	}
	decided, perr := time.Parse(time.RFC3339, decidedAt.String)
	if perr != nil {
		return nil, &approvalFailure{
			status: http.StatusGone,
			body:   "the approval's decision time is unreadable and cannot be honoured — submit without approval_request_id to be judged anew",
		}
	}
	if time.Since(decided) > keeperApprovalTTL {
		return nil, &approvalFailure{
			status: http.StatusGone,
			body: fmt.Sprintf("the approval expired (%s old, TTL %s) — submit without approval_request_id to raise a fresh escalation",
				time.Since(decided).Round(time.Second), keeperApprovalTTL),
		}
	}

	if consumedAt.Valid && consumedAt.String != "" {
		return nil, &approvalFailure{
			status: http.StatusConflict,
			body:   "the approval has already been used — submit without approval_request_id to raise a fresh escalation",
		}
	}

	// The spend. The WHERE clause re-asserts every precondition the read
	// checked, because the read happened outside any transaction: two
	// concurrent retries can both pass it and both arrive here, and the
	// conditional UPDATE is what turns that race into exactly one winner.
	// RowsAffected is the verdict.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(ctx, `
		UPDATE keeper_requests
		   SET approval_consumed_at = ?
		 WHERE id = ?
		   AND decision = ?
		   AND resolved_by_user_id IS NOT NULL
		   AND approval_consumed_at IS NULL`,
		now, approvalID, string(keeper.DecisionAllow))
	if err != nil {
		return nil, &approvalFailure{
			status: http.StatusInternalServerError,
			body:   "failed to consume the approval",
		}
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, &approvalFailure{
			status: http.StatusConflict,
			body:   "the approval was consumed by another request — submit without approval_request_id to raise a fresh escalation",
		}
	}

	risk := 0
	if rowRisk.Valid {
		risk = int(rowRisk.Int64)
	}
	return &consumedApproval{
		RequestID:  approvalID,
		ApproverID: resolvedBy.String,
		Reason:     rowReason.String,
		RiskScore:  risk,
	}, nil
}
