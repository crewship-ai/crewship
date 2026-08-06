package api

// Autonomy gate for the sidecar-facing /api/v1/internal/* creation routes
// (#1768).
//
// Six routes let an agent inside a crew container create a STANDING thing —
// a crew, a persistent agent, a mission, a cron schedule, a skill — and none
// of them consulted the crew's autonomy_level. Each of the handlers serving
// them justified skipping the check with a claim about its caller
// ("enforced upstream", "the handler is the authoritative gate") rather than
// a check; internal/sidecar/memory_routes_coverage_test.go records the whole
// belief loop. This file is the enforcement those comments described.
//
// It lives here, not in the sidecar, because the sidecar has no transport for
// autonomy_level: the value is absent from IPCConfig and from every
// /api/v1/internal route, and the only surface exposing it is the JWT-authed
// public GET /api/v1/crews/{id}/policy. A boot-time copy inside the sidecar
// could never be invalidated when an operator lowers the level — which is
// exactly what policy.Resolver.Invalidate exists for. Gating at the backend
// adapters also closes the hole for every internal-token holder, not just for
// the sidecar that prompted it.
//
// Three decision arms, following the model AgentHandler.resolveHirePolicy
// (agents_hire.go) already set for /agents/hire:
//
//	DecisionRejected      → structured 403 naming the autonomy level, so the
//	                        CLI can suggest the `crewship policy set` that
//	                        would unblock it.
//	DecisionInboxApprove  → the row IS created, but INERT: a status/enabled/
//	                        autonomy sentinel that keeps it from doing
//	                        anything, plus a blocking inbox waitpoint and an
//	                        approvals_queue row that releases the sentinel
//	                        when an operator decides. "Created but inert"
//	                        beats "refused" because guided is the DEFAULT
//	                        autonomy level — refusing there would remove a
//	                        capability every crew has today.
//	auto/journal arms     → proceed, with the logging the decision names.
//
// Never a silent allow: every arm writes an audit row carrying the decision
// and the autonomy level it came from.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/policy"
)

// autonomyHoldTimeoutSecs is how long a staged creation stays decidable on
// the approvals queue. harbormaster's one-hour default is sized for a
// synchronous tool-call gate where an agent is blocked waiting; these holds
// are asynchronous — nobody is waiting, and an operator who is away for the
// weekend should still find the row decidable on Monday. Timing out does not
// release anything: the sentinel stays in place and the artefact stays inert,
// so a lapsed window fails closed.
const autonomyHoldTimeoutSecs = 7 * 24 * 60 * 60

// autonomyDecision is the resolved gate outcome for one internal creation
// call. CrewID is the crew whose policy was consulted — recorded so the audit
// row and the 403 body name a crew the operator can act on.
type autonomyDecision struct {
	Decision policy.Decision
	Level    policy.AutonomyLevel
	CrewID   string
	Action   policy.Action
}

// held reports whether the caller must stage the creation inert instead of
// letting it act.
func (d autonomyDecision) held() bool { return d.Decision == policy.DecisionInboxApprove }

// wantsInbox reports whether the decision asks for an inbox row at all —
// blocking for InboxApprove, informational for AutoLogInbox. The journal-only
// arms (AutoLogJournal / AutoJournal) deliberately write no inbox row: a
// trusted or full crew does not want one item per created thing.
func (d autonomyDecision) wantsInbox() bool {
	return d.Decision == policy.DecisionInboxApprove || d.Decision == policy.DecisionAutoLogInbox
}

// auditFields returns the (decision, autonomy_level) pair every gated route
// stamps onto its audit row, so an operator reading the log can tell a
// creation that was allowed from one that was held.
func (d autonomyDecision) auditFields() map[string]interface{} {
	return map[string]interface{}{
		"decision":       string(d.Decision),
		"autonomy_level": string(d.Level),
		"policy_action":  string(d.Action),
		"policy_crew_id": d.CrewID,
	}
}

// autonomySubjectCrew picks the crew whose autonomy_level governs this call.
//
// The token's crew binding wins: for a crew-bound (crwv1) sidecar token it is
// cryptographic and unforgeable, while a body/query crew_id is caller-
// supplied. #1186 already forces those two to agree for the routes that carry
// a crew reference (assertBoundCrewWorkspaceDB), so preferring the binding
// changes nothing there and closes the gap on routes where the caller could
// otherwise name a more permissive sibling.
//
// The fallback is the request-supplied crew, which covers a workspace-bound
// (wsv1) token — a crew-less run — naming its target crew explicitly. When
// neither is available the caller has no crew subject and policy.Resolver
// answers with its documented safe default (guided/warn), i.e. the call is
// held rather than waved through.
func autonomySubjectCrew(r *http.Request, requestCrewID string) string {
	if bound := InternalTokenCrewFromContext(r.Context()); bound != "" {
		return bound
	}
	return requestCrewID
}

// gateInternalAction resolves the autonomy policy for an internal creation
// call and returns the decision. On DecisionRejected (or a resolver failure)
// the 403/500 has been written and ok is false — callers must return
// immediately, exactly like resolveHirePolicy.
//
// A nil resolver defaults to guided/InboxApprove rather than to "allow". That
// is the same conservative fallback agents_hire.go uses for handlers built
// before the router calls SetPolicyResolver, and it is what keeps a wiring
// mistake from silently reopening the hole this gate closes.
//
// THAT DEFAULT IS A LITERAL AND MUST STAY ONE. It looks like duplication —
// `policy.Policy{AutonomyLevel: policy.AutonomyGuided}.DecideAction(action)`
// would "derive it from the matrix" — and it is not. A nil resolver means THE
// GATE IS NOT WIRED, which is a different question from what a guided crew is
// allowed to do, and the two must not be made to share a source. Since the
// #1768 rebalance, guided answers AutoLogInbox for mission_create and
// routine_schedule_create; deriving the fallback would turn an unwired
// resolver from "hold everything" into "wave those two through" — a fail-open
// on a wiring bug, in the file whose whole job is not to have one.
// TestAutonomyGate_NilResolver_HoldsEvenWhatGuidedAllows pins it.
//
// The resolver-error branch below answers 500 for the same reason: an
// unanswerable policy question is not an allow.
func gateInternalAction(
	w http.ResponseWriter,
	r *http.Request,
	resolver *policy.Resolver,
	logger *slog.Logger,
	requestCrewID string,
	action policy.Action,
	what string,
) (autonomyDecision, bool) {
	d := autonomyDecision{
		Decision: policy.DecisionInboxApprove,
		Level:    policy.AutonomyGuided,
		CrewID:   autonomySubjectCrew(r, requestCrewID),
		Action:   action,
	}
	if resolver != nil {
		pol, err := resolver.Resolve(r.Context(), d.CrewID)
		if err != nil {
			if logger != nil {
				logger.Error("autonomy gate: resolve policy",
					"crew_id", d.CrewID, "action", string(action), "error", err)
			}
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return d, false
		}
		d.Decision = pol.DecideAction(action)
		d.Level = pol.AutonomyLevel
	}
	if d.Decision == policy.DecisionRejected {
		// Same structured shape agents_hire.go:418 returns, because the
		// CLI already renders it: the autonomy level is in the body so the
		// error can name the `crewship policy set` that would unblock it
		// instead of just bouncing the caller.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":          what + " rejected by policy",
			"reason":         "autonomy_level=" + string(d.Level) + " forbids " + string(action),
			"crew_id":        d.CrewID,
			"autonomy_level": string(d.Level),
			"policy_action":  string(action),
		})
		return d, false
	}
	return d, true
}

// autonomyHold describes the blocking record a held creation leaves behind:
// an approvals_queue row (decidable with `crewship approvals approve/deny`
// and on the /approvals UI) paired with a blocking inbox item keyed to the
// created row.
//
// Target/TargetID are what applyAutonomyGateDecisionTx uses to release the
// sentinel when the operator approves. ReleaseValue carries the per-target
// value needed for that release (the autonomy level a held crew is restored
// to); it is unused by targets whose release is a fixed transition.
type autonomyHold struct {
	WorkspaceID  string
	CrewID       string
	AgentID      string
	MissionID    string
	Target       string
	TargetID     string
	ReleaseValue string
	InboxKind    string
	Title        string
	BodyMD       string
	Reason       string
	RequestedBy  string
}

// Targets understood by applyAutonomyGateDecisionTx. The string lands in the
// approvals_queue payload, so renaming one strands rows written by an older
// build — add rather than rename.
const (
	autonomyTargetAgent    = "agent"
	autonomyTargetCrew     = "crew"
	autonomyTargetSchedule = "schedule"
	autonomyTargetMission  = "mission"
)

// writeAutonomyHold records a held creation: the approvals_queue row first
// (it is the decision point), then the blocking inbox item that surfaces it.
//
// Returns the approval id. An error means the hold is NOT recorded — the
// caller must treat that as fatal and roll its creation back, because a
// sentinel with no way to release it is a bricked row, which is precisely the
// failure mode agents_hire.go's compensating delete exists to avoid.
func writeAutonomyHold(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	j journal.Emitter,
	d autonomyDecision,
	h autonomyHold,
) (string, error) {
	requestedBy := h.RequestedBy
	if requestedBy == "" {
		requestedBy = "agent"
	}
	payload := map[string]any{
		// tool/args follow the Gate() payload convention so
		// harbormaster's reward-history hook keys the outcome under a
		// stable tool name instead of warn-logging every hold.
		"tool": "autonomy." + string(d.Action),
		"args": map[string]any{
			"target":    h.Target,
			"target_id": h.TargetID,
		},
		"target":          h.Target,
		"target_id":       h.TargetID,
		"release_value":   h.ReleaseValue,
		"inbox_kind":      h.InboxKind,
		"policy_action":   string(d.Action),
		"autonomy_level":  string(d.Level),
		"policy_crew_id":  d.CrewID,
		"policy_decision": string(d.Decision),
	}
	approvalID, err := harbormaster.Enqueue(ctx, db, j, harbormaster.Request{
		WorkspaceID: h.WorkspaceID,
		CrewID:      h.CrewID,
		AgentID:     h.AgentID,
		MissionID:   h.MissionID,
		RequestedBy: requestedBy,
		Kind:        harbormaster.KindAutonomyGate,
		Reason:      h.Reason,
		Payload:     payload,
		TimeoutSecs: autonomyHoldTimeoutSecs,
	})
	if err != nil {
		return "", fmt.Errorf("autonomy hold: enqueue approval: %w", err)
	}

	// ADMIN, not MANAGER: POST /approvals/{id}/decide requires OWNER or
	// ADMIN, so a MANAGER-addressed row could only ever hand its reader a
	// 403. Address what can act — the visibility clause is hierarchical, so
	// OWNER sees it too. Same call skills_author_handler.go makes, and
	// TestInboxTargetRoleMatchesDecider pins the rule.
	if err := inbox.Insert(ctx, db, logger, inbox.Item{
		WorkspaceID: h.WorkspaceID,
		Kind:        h.InboxKind,
		SourceID:    h.TargetID,
		TargetRole:  "ADMIN",
		Title:       h.Title,
		BodyMD:      h.BodyMD,
		SenderType:  "agent",
		SenderName:  "Agent autonomy gate",
		Priority:    "high",
		Blocking:    true,
		Payload: map[string]interface{}{
			"kind":           "autonomy_gate",
			"target":         h.Target,
			"target_id":      h.TargetID,
			"approval_id":    approvalID,
			"crew_id":        d.CrewID,
			"autonomy_level": string(d.Level),
			"policy_action":  string(d.Action),
		},
	}); err != nil {
		// Compensate the approvals row so the operator is not left with a
		// pending decision that has no inbox surface, then let the caller
		// undo its own creation.
		if cerr := harbormaster.Cancel(ctx, db, nil, h.WorkspaceID, approvalID,
			"inbox waitpoint write failed"); cerr != nil && logger != nil {
			logger.Error("autonomy hold: cancel approval after inbox failure",
				"approval_id", approvalID, "error", cerr)
		}
		return "", fmt.Errorf("autonomy hold: write inbox waitpoint: %w", err)
	}
	return approvalID, nil
}

// writeAutonomyNotice records a creation that PROCEEDED under a decision that
// still wants the operator to see it (DecisionAutoLogInbox at full autonomy).
// Non-blocking: the thing is already live, this is after-the-fact visibility.
// A failed write is a missed audit row, not a broken creation, so it is
// logged and swallowed — the same call agents_hire.go's AutoLogInbox arm
// makes.
func writeAutonomyNotice(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	d autonomyDecision,
	workspaceID, inboxKind, sourceID, title, bodyMD string,
) {
	if err := inbox.Insert(ctx, db, logger, inbox.Item{
		WorkspaceID: workspaceID,
		Kind:        inboxKind,
		SourceID:    sourceID,
		TargetRole:  "ADMIN",
		Title:       title,
		BodyMD:      bodyMD,
		SenderType:  "agent",
		SenderName:  "Agent autonomy gate",
		Priority:    "medium",
		Blocking:    false,
		Payload: map[string]interface{}{
			"kind":           "autonomy_notice",
			"target_id":      sourceID,
			"crew_id":        d.CrewID,
			"autonomy_level": string(d.Level),
			"policy_action":  string(d.Action),
		},
	}); err != nil && logger != nil {
		logger.Warn("autonomy notice: inbox write failed",
			"source_id", sourceID, "action", string(d.Action), "error", err)
	}
}

// applyAutonomyGateDecisionTx releases (or leaves in place) the sentinel a
// held creation left behind. It rides the caller's transaction so the
// approvals-queue CAS and the release commit together or not at all — the
// #1247 rule: a terminal approval describing a transition that never happened
// is worse than no approval.
//
// DENY is deliberately a no-op on the target. Every sentinel used here is
// "inert until released", so refusing simply leaves the artefact inert: a
// held agent stays PENDING_REVIEW, a held schedule stays disabled, a held
// crew stays strict, a held mission stays unstartable. That keeps the deny
// path from needing a destructive counterpart (which would have to decide
// whether to delete rows an operator may still want to inspect).
//
// A denied hold writes NO second marker on the target — in particular no
// agents.expired_at, the way applyEphemeralHireDecisionTx's deny arm does.
// That was reconsidered when the #1768 widening let approve-hire resurrect a
// denied agent, and kept: expired_at is the EPHEMERAL ghost marker. It
// reorders the roster (agents_query.go), frees a crew's hire quota
// (agents_hire.go) and is the one field `crewship rehire` resets — so
// stamping it on a persistent agent would file a refused creation as a
// lapsed hire and offer `rehire` as the way back, which for an agent the
// gate staged means nothing.
//
// The refusal is durable structurally instead: the approvals_queue row is
// the ONLY door to this function, harbormaster.DecideTx CASes it out of
// `pending` in the same transaction, and no other surface flips a gate-held
// artefact. TestApproveHire_CannotResurrectATerminalAutonomyHold and
// TestAutonomyGate_Decide_IsTerminal hold that from both sides. If a second
// release door is ever added, it must key off the approvals row too — or
// this decision has to be revisited, not worked around.
func applyAutonomyGateDecisionTx(
	ctx context.Context,
	tx harbormaster.DBTX,
	workspaceID string,
	row *harbormaster.Request,
	approved bool,
	decidedBy string,
) error {
	if row == nil || row.Kind != harbormaster.KindAutonomyGate {
		return nil
	}
	target, _ := row.Payload["target"].(string)
	targetID, _ := row.Payload["target_id"].(string)
	inboxKind, _ := row.Payload["inbox_kind"].(string)
	releaseValue, _ := row.Payload["release_value"].(string)
	if targetID == "" {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if approved {
		var err error
		switch target {
		case autonomyTargetAgent:
			// Same conditional UPDATE ApproveHire uses, for the same
			// reason: two operators deciding at once must not both think
			// they were the one who released it.
			//
			// The expired_at / deleted_at guards match it too, and they are
			// the fail-closed direction rather than decoration: this arm is
			// the ONLY door that releases a gate-held agent (approve-hire
			// refuses permanent rows outright), so if anything ever ghosts
			// or soft-deletes one, a late approve must leave it dead
			// instead of resurrecting it. A no-op here is safe by
			// construction — the sentinel simply stays.
			_, err = tx.ExecContext(ctx, `
				UPDATE agents SET status = 'IDLE', updated_at = ?
				WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'
				  AND expired_at IS NULL AND deleted_at IS NULL`,
				now, targetID, workspaceID)
		case autonomyTargetSchedule:
			// Since the #1768 rebalance no autonomy LEVEL holds a schedule —
			// strict refuses and everything below it notices. Two things
			// still reach this arm and both matter: the fail-closed
			// nil-resolver fallback (gateInternalAction holds when the gate
			// is unwired, whatever the action), and rows written by a build
			// that predates the rebalance and are still pending on a running
			// instance. Deleting this would strand those permanently
			// disabled with no release path.
			_, err = tx.ExecContext(ctx, `
				UPDATE pipeline_schedules SET enabled = 1, updated_at = ?
				WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
				now, targetID, workspaceID)
		case autonomyTargetCrew:
			// A held crew is created pinned to strict so it cannot host an
			// agent or a cron entry (both are DecisionRejected at strict).
			// Approving restores the level the creating crew ran at, which
			// is the most permissive the child is ever allowed to be.
			if releaseValue == "" {
				releaseValue = string(policy.AutonomyGuided)
			}
			_, err = tx.ExecContext(ctx, `
				UPDATE crews SET autonomy_level = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
				releaseValue, now, targetID, workspaceID)
		case autonomyTargetMission:
			// Nothing to flip: the mission is already PLANNING, and
			// InternalMissionHandler.Start reads this approvals row
			// directly (missionCreateApproved). Approving IS the release.
		}
		if err != nil {
			return fmt.Errorf("autonomy gate: release %s %s: %w", target, targetID, err)
		}
	}

	if inboxKind == "" {
		return nil
	}
	action := "approved"
	if !approved {
		action = "denied"
	}
	if err := inbox.ResolveBySourceTx(ctx, tx, inboxKind, targetID, action, decidedBy); err != nil {
		return fmt.Errorf("autonomy gate: resolve inbox waitpoint for %s: %w", targetID, err)
	}
	return nil
}

// capturedResponse buffers a handler's response so an adapter can read the
// created row's id out of the body before deciding what to send on. Used by
// the routine-schedule gate: the public CreateSchedule owns the INSERT (and
// the cron parsing, slug resolution and audit emit that go with it), so
// intercepting its response is the only way to learn the schedule id without
// forking that logic — the duplication internal_routines.go's docstring
// exists to avoid.
type capturedResponse struct {
	header http.Header
	body   *bytes.Buffer
	status int
}

func newCapturedResponse() *capturedResponse {
	return &capturedResponse{header: http.Header{}, body: &bytes.Buffer{}, status: http.StatusOK}
}

func (c *capturedResponse) Header() http.Header { return c.header }

func (c *capturedResponse) Write(p []byte) (int, error) { return c.body.Write(p) }

func (c *capturedResponse) WriteHeader(code int) { c.status = code }

// flush replays the captured response verbatim onto the real writer. Used
// when the gate has nothing to add — an error status, or a body we could not
// parse an id out of (in which case failing the request open with the
// original response is honest: no hold was recorded and the caller sees
// exactly what the underlying handler said).
func (c *capturedResponse) flush(w http.ResponseWriter) {
	for k, vs := range c.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(c.status)
	_, _ = w.Write(c.body.Bytes())
}

// autonomyGateApproved reports whether the newest autonomy-gate approval for
// (workspace, target) reached the `approved` status.
//
// Fail-closed by construction: it answers false for pending, denied,
// cancelled AND timed-out rows. That last one matters — harbormaster's
// timeout sweeper flips a lapsed row out of `pending`, so a check written as
// "no pending row blocks me" would let a hold expire into a green light.
//
// A target with no gate row at all is not held (it was created under a
// decision that did not stage it), so hasRow=false means "proceed".
func autonomyGateApproved(ctx context.Context, db *sql.DB, workspaceID, targetID string) (approved, hasRow bool, err error) {
	var status string
	qerr := db.QueryRowContext(ctx, `
		SELECT status FROM approvals_queue
		WHERE workspace_id = ? AND kind = ?
		  AND json_extract(payload, '$.target_id') = ?
		ORDER BY created_at DESC LIMIT 1`,
		workspaceID, string(harbormaster.KindAutonomyGate), targetID).Scan(&status)
	if errors.Is(qerr, sql.ErrNoRows) {
		return false, false, nil
	}
	if qerr != nil {
		return false, false, qerr
	}
	return status == string(harbormaster.StatusApproved), true, nil
}
