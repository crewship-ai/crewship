package api

// e2e_fixtures.go — the browser-test seed door (#2403).
//
// A run_needs_human card is written by exactly one producer: a run that
// reports outcome NEEDS_HUMAN (createOutcomeInboxItem, B6). That needs a
// provider-backed agent, which the Playwright PR gate does not have — so
// until this file the gate could only route-mock the inbox and prove the
// web half of the contract. It could not assert the server half (the
// `inbox_acted` receipt on the issue's event log) because there was no card
// to act on.
//
// POST /api/v1/e2e/fixtures/run-needs-human plants what a NEEDS_HUMAN run
// leaves behind — the (issue, agent) session parked in awaiting_input, the
// COMPLETED assignment that reported the outcome, and the card — and it
// writes the card through the SAME producer the real path uses, so what the
// browser acts on is shaped exactly like production data rather than a
// hand-built row that agrees with whatever the UI happens to expect.
//
// THIS SURFACE DOES NOT EXIST ON A NORMAL SERVER. The route is registered
// only when the router is built with WithE2EFixtures, which the server
// passes only when CREWSHIP_E2E_FIXTURES=1 (E2EFixturesEnabled) — and never
// in production regardless of the flag. Without the option the path answers
// exactly as a route that was never written (TestE2EFixtures_RouteAbsent-
// WithoutTheGate pins that), it is absent from the reviewed route-role
// manifest, and cmd/gen-openapi excludes the /api/v1/e2e/ prefix from the
// public spec. Behind the gate it is still an ordinary authedMut route:
// session + workspace membership + MANAGER, scope workspace:admin.
//
// FIXTURE, NOT A RUNTIME TEST — the same caveat `admin seed-inbox` carries.
// The assignment row is synthetic: no run executed, so nothing here is
// evidence that a NEEDS_HUMAN run raises a card. That is
// TestCreateOutcomeInboxItem's job. What this proves, through the browser,
// is that acting on the card reaches the session that asked and leaves the
// receipt where §10.5 says it goes.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// E2EFixturesEnabled decides, from the environment, whether the server
// should register the fixture surface. Only "1" / "true" widen the gate —
// an accidental empty export keeps it closed, matching skipTokenProbe
// (onboarding.go) — and a production instance refuses regardless, the same
// posture MustNotDisableRateLimitInProd takes for its own flag.
//
// getenv is a parameter rather than os.Getenv so the decision is testable
// at its boundary without mutating process state.
func E2EFixturesEnabled(getenv func(string) string) bool {
	v := strings.TrimSpace(getenv("CREWSHIP_E2E_FIXTURES"))
	if v != "1" && v != "true" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(getenv("CREWSHIP_ENV"))) {
	case "prod", "production":
		return false
	}
	return true
}

// WithE2EFixtures registers the browser-test fixture routes. Pass it only
// on an instance that exists to be tested — see E2EFixturesEnabled.
func WithE2EFixtures() RouterOption {
	return func(r *Router) {
		r.e2eFixtures = true
	}
}

// registerE2EFixtureRoutes is called from registerRoutes only when the
// option is set. The handler needs the live AssignmentHandler because the
// card is written through its createOutcomeInboxItem — the real producer.
func (r *Router) registerE2EFixtureRoutes(oh orchestrationHandlers) {
	h := &E2EFixtureHandler{db: r.db, logger: r.logger, assign: oh.assign}
	r.logger.Warn("CREWSHIP_E2E_FIXTURES is enabled — browser-test fixture routes are registered under /api/v1/e2e/. DO NOT set this in production.")
	r.authedMut("POST", "/api/v1/e2e/fixtures/run-needs-human", roleManage, h.RunNeedsHuman)
}

// E2EFixtureHandler serves the fixture surface. See the file header.
type E2EFixtureHandler struct {
	db     *sql.DB
	logger *slog.Logger
	assign *AssignmentHandler
}

// e2eRunNeedsHumanRequest is the body of POST /api/v1/e2e/fixtures/run-needs-human.
type e2eRunNeedsHumanRequest struct {
	// IssueIdentifier names the issue the run belongs to (e.g. "ENG-7").
	// Required.
	IssueIdentifier string `json:"issue_identifier"`
	// AgentSlug names the agent whose session asked. Optional: defaults to
	// the issue's lead agent, else the first agent in the issue's crew.
	AgentSlug string `json:"agent_slug"`
	// Reason is what the agent was blocked on; it becomes the card's body
	// the way a real run's checkpoint `blockers:` line does. Optional.
	Reason string `json:"reason"`
}

// e2eRunNeedsHumanResponse names everything the fixture wrote so the test
// can navigate to the card and read the issue's event log afterwards.
type e2eRunNeedsHumanResponse struct {
	InboxItemID     string `json:"inbox_item_id"`
	AssignmentID    string `json:"assignment_id"`
	SessionID       string `json:"session_id"`
	MissionID       string `json:"mission_id"`
	IssueIdentifier string `json:"issue_identifier"`
	CrewID          string `json:"crew_id"`
	AgentID         string `json:"agent_id"`
	AgentSlug       string `json:"agent_slug"`
}

// RunNeedsHuman — POST /api/v1/e2e/fixtures/run-needs-human — plants a
// run_needs_human card bound to a real (issue, agent) session.
func (h *E2EFixtureHandler) RunNeedsHuman(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := WorkspaceIDFromContext(ctx)
	if workspaceID == "" || UserFromContext(ctx) == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}
	var req e2eRunNeedsHumanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.IssueIdentifier = strings.TrimSpace(req.IssueIdentifier)
	if req.IssueIdentifier == "" {
		replyError(w, http.StatusBadRequest, "issue_identifier is required")
		return
	}

	// The issue, in the caller's workspace.
	var missionID, title string
	var crewIDRow, leadAgentID sql.NullString
	err := h.db.QueryRowContext(ctx,
		`SELECT id, crew_id, lead_agent_id, title FROM missions WHERE workspace_id = ? AND identifier = ? ORDER BY created_at LIMIT 1`,
		workspaceID, req.IssueIdentifier).Scan(&missionID, &crewIDRow, &leadAgentID, &title)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "issue not found")
		return
	}
	if err != nil {
		h.logger.Error("e2e fixture: load issue", "error", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	// A card is raised for a crew's agent; an issue filed outside any crew
	// has nobody to have asked, and saying so beats a scan error.
	if !crewIDRow.Valid || crewIDRow.String == "" {
		replyError(w, http.StatusUnprocessableEntity, "issue has no crew; a run_needs_human card needs a crew agent")
		return
	}
	crewID := crewIDRow.String

	// The agent whose session asked. A held (PENDING_REVIEW) agent is
	// never chosen by default: the answer path refuses to wake one
	// (refuseHeldAgent), and a fixture that plants an unanswerable card
	// proves nothing.
	var agentID, agentSlug string
	switch {
	case strings.TrimSpace(req.AgentSlug) != "":
		err = h.db.QueryRowContext(ctx,
			`SELECT id, slug FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
			workspaceID, strings.TrimSpace(req.AgentSlug)).Scan(&agentID, &agentSlug)
	case leadAgentID.Valid && leadAgentID.String != "":
		err = h.db.QueryRowContext(ctx,
			`SELECT id, slug FROM agents WHERE workspace_id = ? AND id = ? AND deleted_at IS NULL`,
			workspaceID, leadAgentID.String).Scan(&agentID, &agentSlug)
	default:
		err = h.db.QueryRowContext(ctx,
			`SELECT id, slug FROM agents
			  WHERE workspace_id = ? AND crew_id = ? AND deleted_at IS NULL AND COALESCE(status,'') != 'PENDING_REVIEW'
			  ORDER BY created_at, id LIMIT 1`,
			workspaceID, crewID).Scan(&agentID, &agentSlug)
	}
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "no agent to bind the session to (give agent_slug, or add an agent to the issue's crew)")
		return
	}
	if err != nil {
		h.logger.Error("e2e fixture: load agent", "error", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	// What a real NEEDS_HUMAN run leaves behind, in the order the real path
	// writes it: the mission's pseudo-chat (assignments.chat_id FK), the
	// (issue, agent) session, the run bound to it, and the session settled
	// into the state RouteForOutcome maps NEEDS_HUMAN to.
	if err := ensureMissionChat(ctx, h.db, missionID, workspaceID, agentID, title); err != nil {
		h.logger.Error("e2e fixture: mission chat", "error", err)
		replyError(w, http.StatusInternalServerError, "could not prepare the issue")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Which bucket should the export go to — staging or prod?"
	}
	result := "---CHECKPOINT---\ndone: investigated the export target\nblockers: " + reason +
		"\nnext_step: wait for the answer\noutcome: " + orchestrator.OutcomeNeedsHuman + "\n---END CHECKPOINT---"
	now := time.Now().UTC().Format(time.RFC3339)
	assignmentID := generateCUID()
	var sessionID string
	err = func() error {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		sessionID, err = resolveOrCreateIssueAgentSessionTx(ctx, tx, workspaceID, missionID, agentID)
		if err != nil {
			return fmt.Errorf("session: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, depth,
			                         created_at, started_at, finished_at, mission_id, session_id, result_summary, outcome)
			VALUES (?, ?, ?, ?, ?, ?, 'COMPLETED', ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
			assignmentID, workspaceID, missionID, agentID, agentID,
			"e2e fixture: a run on "+req.IssueIdentifier+" that stopped for a human",
			missionID, now, now, now, missionID, sessionID, result, orchestrator.OutcomeNeedsHuman); err != nil {
			return fmt.Errorf("assignment: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_agent_sessions
			   SET state = ?, active_run_id = NULL, last_activity_at = ?, updated_at = ?
			 WHERE id = ?`,
			orchestrator.RouteForOutcome(orchestrator.OutcomeNeedsHuman).SessionState, now, now, sessionID); err != nil {
			return fmt.Errorf("settle session: %w", err)
		}
		return tx.Commit()
	}()
	if err != nil {
		h.logger.Error("e2e fixture: plant run", "error", err, "issue", req.IssueIdentifier)
		replyError(w, http.StatusInternalServerError, "could not plant the run")
		return
	}

	// The card, through the real producer. It threads by issue (B10), so a
	// second call on the same open issue refreshes the one card rather
	// than adding a sibling — the row's source_id then still names the
	// FIRST run, which is what the response reports.
	h.assign.createOutcomeInboxItem(ctx, assignmentID, workspaceID, agentSlug, result)
	var cardID, cardSource string
	if err := h.db.QueryRowContext(ctx, `
		SELECT id, source_id FROM inbox_items
		 WHERE workspace_id = ? AND kind = ? AND thread_key = ? AND state != 'resolved'
		 ORDER BY created_at ASC, id ASC LIMIT 1`,
		workspaceID, inbox.KindRunNeedsHuman, "issue:"+workspaceID+":"+missionID).Scan(&cardID, &cardSource); err != nil {
		h.logger.Error("e2e fixture: card not raised", "error", err, "assignment_id", assignmentID)
		replyError(w, http.StatusInternalServerError, "the run was planted but no card was raised")
		return
	}
	writeJSON(w, http.StatusCreated, e2eRunNeedsHumanResponse{
		InboxItemID: cardID, AssignmentID: cardSource, SessionID: sessionID, MissionID: missionID,
		IssueIdentifier: req.IssueIdentifier, CrewID: crewID, AgentID: agentID, AgentSlug: agentSlug,
	})
}
