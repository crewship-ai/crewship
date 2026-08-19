package api

// The escalation state machine.
//
//	PENDING → RESOLVED    a human decided; `action` says approve/reject/redirect
//	        → EXPIRED     the deadline passed and no human decided
//	        → CANCELLED   a human withdrew the question before deciding
//
// Terminal is terminal: every transition out of RESOLVED, EXPIRED or CANCELLED
// is refused. See internal/database/migrations/20260813212851_escalation_deadline.sql
// for why ANSWERED and REJECTED were not adopted as separate statuses.
//
// The assertion this whole file exists for is
// TestEscalationTimeoutAndRowStateAgree: before this change the agent gave up
// after 300 s and proceeded without an answer while the row said PENDING
// forever. The two must now describe the same event because the same event
// produces both.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ─── rig ──────────────────────────────────────────────────────────────────

// escLifecycleRig seeds one workspace / crew / agent and returns a handler
// with a recording journal wired, so every case can assert on the entry a
// transition wrote rather than merely on the row it left behind.
func escLifecycleRig(t *testing.T) (*QueryHandler, *recordingEmitter, string, string, string, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "lc-crew", wsID, "Crew", "lc-crew")
	agentID := seedAgentRow(t, db, "lc-agent", wsID, crewID, "Agent", "lc-agent", "AGENT")
	h := NewQueryHandler(db, nil, nil, "", newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	return h, rec, userID, wsID, crewID, agentID
}

// seedEscalationWithDeadline inserts one escalation in a chosen status with
// BOTH clocks set to the same instant — the agent's wait window and the human's
// answerability window (see escalation_lifecycle.go). A nil deadline is the
// legacy shape (pre-migration rows, which must never expire).
//
// Collapsing the two here is a deliberate choice for the cases below, which are
// about what happens once a question is dead by every measure. The cases where
// the two clocks DISAGREE — which is the normal production shape, 300 s against
// 7 days — live in escalation_two_clocks_test.go, and that separation is why
// this helper may keep its one argument.
func seedEscalationWithDeadline(t *testing.T, h *QueryHandler, id, wsID, crewID, agentID, status string, deadline *time.Time) {
	t.Helper()
	var dl interface{}
	if deadline != nil {
		dl = deadline.UTC().Format(time.RFC3339)
	}
	execOrFatal(t, h.db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status,
		 deadline_at, answer_deadline_at, created_at)
		VALUES (?, ?, ?, 'lc-chat', ?, 'need a decision', 'TEXT', ?, ?, ?, ?)`,
		id, wsID, crewID, agentID, status, dl, dl, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
}

func escStatus(t *testing.T, h *QueryHandler, id string) string {
	t.Helper()
	var status string
	if err := h.db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("load status for %s: %v", id, err)
	}
	return status
}

// escCancel drives POST /api/v1/escalations/{id}/cancel as a MANAGER.
func escCancel(h *QueryHandler, userID, wsID, escID, reason string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/escalations/"+escID+"/cancel",
			jsonBody(map[string]string{"reason": reason})),
		userID, wsID, "MANAGER")
	req.SetPathValue("escalationId", escID)
	rr := httptest.NewRecorder()
	h.CancelEscalation(rr, req)
	return rr
}

// escalationStateEntries returns every peer.escalation entry whose payload
// claims the given lifecycle state. Asserting on `state` rather than on a
// count is what makes these tests fail when an emit is deleted AND when it is
// weakened into describing the wrong transition.
func escalationStateEntries(rec *recordingEmitter, escID, state string) []journal.Entry {
	var out []journal.Entry
	for _, e := range rec.entries {
		if e.Type != journal.EntryPeerEscalation {
			continue
		}
		if s, _ := e.Payload["state"].(string); s != state {
			continue
		}
		if ref, _ := e.Refs["escalation_id"].(string); ref != escID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ─── every legal transition, and every illegal one refused ────────────────

func TestEscalationTransitions(t *testing.T) {
	past := time.Now().UTC().Add(-time.Minute)

	// `apply` performs one transition attempt against a row seeded in
	// `from`, and returns the HTTP status the caller saw (0 for the sweep,
	// which is not an HTTP call).
	cases := []struct {
		name       string
		from       string
		transition string // resolve | cancel | sweep
		wantCode   int
		wantStatus string
	}{
		// Legal.
		{"pending resolves", "PENDING", "resolve", http.StatusOK, "RESOLVED"},
		{"pending cancels", "PENDING", "cancel", http.StatusOK, "CANCELLED"},
		{"pending past its deadline expires", "PENDING", "sweep", 0, "EXPIRED"},

		// Illegal — terminal states are terminal. Each of these was
		// previously either impossible to reach or silently accepted.
		{"resolved cannot be cancelled", "RESOLVED", "cancel", http.StatusConflict, "RESOLVED"},
		{"resolved cannot be resolved twice", "RESOLVED", "resolve", http.StatusConflict, "RESOLVED"},
		{"resolved is never swept", "RESOLVED", "sweep", 0, "RESOLVED"},
		{"expired cannot be resolved", "EXPIRED", "resolve", http.StatusConflict, "EXPIRED"},
		{"expired cannot be cancelled", "EXPIRED", "cancel", http.StatusConflict, "EXPIRED"},
		{"expired is never swept again", "EXPIRED", "sweep", 0, "EXPIRED"},
		{"cancelled cannot be resolved", "CANCELLED", "resolve", http.StatusConflict, "CANCELLED"},
		{"cancelled cannot be cancelled twice", "CANCELLED", "cancel", http.StatusConflict, "CANCELLED"},
		{"cancelled is never swept", "CANCELLED", "sweep", 0, "CANCELLED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, userID, wsID, crewID, agentID := escLifecycleRig(t)
			const escID = "lc-esc"
			seedEscalationWithDeadline(t, h, escID, wsID, crewID, agentID, tc.from, &past)

			switch tc.transition {
			case "resolve":
				rr := covEscResolve(h, userID, wsID, escID, map[string]string{
					"resolution": "decided", "action": "approve",
				})
				if rr.Code != tc.wantCode {
					t.Errorf("resolve status = %d, want %d; body=%s", rr.Code, tc.wantCode, rr.Body.String())
				}
			case "cancel":
				rr := escCancel(h, userID, wsID, escID, "no longer needed")
				if rr.Code != tc.wantCode {
					t.Errorf("cancel status = %d, want %d; body=%s", rr.Code, tc.wantCode, rr.Body.String())
				}
			case "sweep":
				n, err := h.sweepExpiredEscalations(context.Background(), wsID)
				if err != nil {
					t.Fatalf("sweep: %v", err)
				}
				wantSwept := 0
				if tc.wantStatus == "EXPIRED" && tc.from == "PENDING" {
					wantSwept = 1
				}
				if n != wantSwept {
					t.Errorf("sweep expired %d rows, want %d", n, wantSwept)
				}
			}

			if got := escStatus(t, h, escID); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
		})
	}
}

// A row with no deadline is a row raised before deadlines existed. Sweeping it
// would retro-expire a question a human may still intend to answer, so it
// stays PENDING until somebody decides or withdraws it.
func TestEscalationWithoutDeadlineNeverExpires(t *testing.T) {
	h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
	seedEscalationWithDeadline(t, h, "lc-legacy", wsID, crewID, agentID, "PENDING", nil)

	n, err := h.sweepExpiredEscalations(context.Background(), wsID)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("sweep expired %d rows, want 0 — a NULL deadline means no deadline", n)
	}
	if got := escStatus(t, h, "lc-legacy"); got != "PENDING" {
		t.Errorf("status = %q, want PENDING", got)
	}
}

// A deadline in the FUTURE is not a deadline that has passed.
func TestEscalationBeforeDeadlineIsNotExpired(t *testing.T) {
	h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
	future := time.Now().UTC().Add(time.Hour)
	seedEscalationWithDeadline(t, h, "lc-future", wsID, crewID, agentID, "PENDING", &future)

	n, err := h.sweepExpiredEscalations(context.Background(), wsID)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("sweep expired %d rows, want 0 — the deadline is an hour away", n)
	}
}

// ─── exactly once, whether swept or computed ──────────────────────────────

// An escalation past its deadline reaches EXPIRED exactly once, no matter how
// many observers notice. The expiry is CAS-guarded, so the second sweep, the
// read-path sweep behind pending-count and a waiter arriving late all lose the
// race and none of them writes a second journal entry.
func TestEscalationExpiresExactlyOnce(t *testing.T) {
	h, rec, userID, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedEscalationWithDeadline(t, h, "lc-once", wsID, crewID, agentID, "PENDING", &past)

	first, err := h.sweepExpiredEscalations(context.Background(), wsID)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if first != 1 {
		t.Fatalf("first sweep expired %d rows, want 1", first)
	}
	second, err := h.sweepExpiredEscalations(context.Background(), wsID)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second != 0 {
		t.Errorf("second sweep expired %d rows, want 0 — the transition is not idempotent", second)
	}

	// The read paths compute expiry too. Neither may write a second entry.
	countReq := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), userID, wsID, "OWNER")
	h.PendingEscalationCount(httptest.NewRecorder(), countReq)

	entries := escalationStateEntries(rec, "lc-once", "expired")
	if len(entries) != 1 {
		t.Fatalf("journal has %d expiry entries for one escalation, want exactly 1", len(entries))
	}

	e := entries[0]
	if e.WorkspaceID != wsID {
		t.Errorf("workspace_id = %q, want %q", e.WorkspaceID, wsID)
	}
	if e.CrewID != crewID {
		t.Errorf("crew_id = %q, want %q — the scope must survive onto the entry", e.CrewID, crewID)
	}
	if e.AgentID != agentID {
		t.Errorf("agent_id = %q, want the agent that asked (%q)", e.AgentID, agentID)
	}
	if e.ActorType != journal.ActorSystem {
		t.Errorf("actor_type = %q, want system — nobody decided, the clock did", e.ActorType)
	}
	if e.Severity != journal.SeverityWarn {
		t.Errorf("severity = %q, want warn — an agent proceeded without the answer it asked for", e.Severity)
	}
	if ref, _ := e.Refs["chat_id"].(string); ref != "lc-chat" {
		t.Errorf("refs.chat_id = %q, want lc-chat — the expiry must be findable from the conversation", ref)
	}
}

// The unanswered-question product decision, asserted rather than described:
// the row records that the agent CONTINUED, and says so explicitly.
func TestEscalationExpiryRecordsTheAgentContinued(t *testing.T) {
	h, rec, _, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedEscalationWithDeadline(t, h, "lc-policy", wsID, crewID, agentID, "PENDING", &past)

	if _, err := h.sweepExpiredEscalations(context.Background(), wsID); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	entries := escalationStateEntries(rec, "lc-policy", "expired")
	if len(entries) != 1 {
		t.Fatalf("want 1 expiry entry, got %d", len(entries))
	}
	outcome, _ := entries[0].Payload["agent_outcome"].(string)
	if outcome != escalationOutcomeContinuedWithWarning {
		t.Errorf("payload.agent_outcome = %q, want %q — what the agent did with an unanswered question "+
			"is a product decision and must be legible in the trail, not inferred",
			outcome, escalationOutcomeContinuedWithWarning)
	}

	var resolvedBy, resolution string
	if err := h.db.QueryRow(
		`SELECT COALESCE(resolved_by,''), COALESCE(resolution,'') FROM escalations WHERE id = 'lc-policy'`).
		Scan(&resolvedBy, &resolution); err != nil {
		t.Fatalf("load row: %v", err)
	}
	if resolvedBy != "system" {
		t.Errorf("resolved_by = %q, want system", resolvedBy)
	}
	if resolution == "" {
		t.Error("an expired escalation stored no resolution text — an operator reading the row later " +
			"cannot tell an unanswered question from an answered one")
	}
}

// ─── the assertion the package exists for ─────────────────────────────────

// The agent's give-up and the row's state must agree. Both cases below drive
// the SAME endpoint the sidecar long-polls, so agreement is proven on the real
// path rather than on a reconstruction of it.
func TestEscalationTimeoutAndRowStateAgree(t *testing.T) {
	t.Run("deadline already passed: the waiter is told EXPIRED and the row says EXPIRED", func(t *testing.T) {
		h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
		past := time.Now().UTC().Add(-time.Minute)
		seedEscalationWithDeadline(t, h, "lc-wait-past", wsID, crewID, agentID, "PENDING", &past)

		code, body := escWaitCall(t, h, "lc-wait-past", 2*time.Second)
		if code != http.StatusOK {
			t.Fatalf("wait status = %d, want 200; body=%s", code, body)
		}
		var got map[string]interface{}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decode wait body %q: %v", body, err)
		}
		if got["status"] != "EXPIRED" {
			t.Errorf("wait told the agent status=%v, want EXPIRED", got["status"])
		}
		if w, _ := got["warning"].(string); w == "" {
			t.Error("the agent was given no warning — proceeding without an answer must be explicit, " +
				"which is the whole defect this closes")
		}
		if s := escStatus(t, h, "lc-wait-past"); s != "EXPIRED" {
			t.Errorf("row status = %q, want EXPIRED — the agent was told one thing and the database says another", s)
		}
	})

	t.Run("deadline passes while waiting: the wait ends at the deadline and the row agrees", func(t *testing.T) {
		h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
		soon := time.Now().UTC().Add(300 * time.Millisecond)
		seedEscalationWithDeadline(t, h, "lc-wait-soon", wsID, crewID, agentID, "PENDING", &soon)

		start := time.Now()
		// 30 s of client budget against a 300 ms server deadline: the margin is
		// what proves WHICH clock ended the wait, so it is deliberately huge.
		code, body := escWaitCall(t, h, "lc-wait-soon", 30*time.Second)
		elapsed := time.Since(start)

		if code != http.StatusOK {
			t.Fatalf("wait status = %d, want 200; body=%s", code, body)
		}
		var got map[string]interface{}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decode wait body %q: %v", body, err)
		}
		if got["status"] != "EXPIRED" {
			t.Errorf("wait told the agent status=%v, want EXPIRED", got["status"])
		}
		// The server's deadline is what ends the wait, not the client's
		// context: 5 s was available and the call returned near 300 ms.
		if elapsed > 10*time.Second {
			t.Errorf("wait took %s — it ran to the client's timeout instead of the server's deadline", elapsed)
		}
		if s := escStatus(t, h, "lc-wait-soon"); s != "EXPIRED" {
			t.Errorf("row status = %q, want EXPIRED", s)
		}
	})

	t.Run("a human answering before the deadline still wins", func(t *testing.T) {
		h, _, userID, wsID, crewID, agentID := escLifecycleRig(t)
		// A deadline far enough out that a loaded machine cannot make the
		// clock win a race the human wins: the answer lands at ~150 ms and the
		// deadline is 10 s away. Tightening this would test the scheduler.
		soon := time.Now().UTC().Add(10 * time.Second)
		seedEscalationWithDeadline(t, h, "lc-wait-race", wsID, crewID, agentID, "PENDING", &soon)

		go func() {
			time.Sleep(150 * time.Millisecond)
			covEscResolve(h, userID, wsID, "lc-wait-race", map[string]string{
				"resolution": "yes, go ahead", "action": "approve",
			})
		}()

		code, body := escWaitCall(t, h, "lc-wait-race", 30*time.Second)
		if code != http.StatusOK {
			t.Fatalf("wait status = %d, want 200; body=%s", code, body)
		}
		var got map[string]interface{}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decode wait body %q: %v", body, err)
		}
		if got["status"] != "RESOLVED" {
			t.Fatalf("wait told the agent status=%v, want RESOLVED — the deadline must not steal a real answer", got["status"])
		}
		if got["resolution"] != "yes, go ahead" {
			t.Errorf("resolution = %v, want the human's answer", got["resolution"])
		}
		if s := escStatus(t, h, "lc-wait-race"); s != "RESOLVED" {
			t.Errorf("row status = %q, want RESOLVED", s)
		}
	})
}

// escWaitCall drives WaitForEscalationResponse as the master-token caller the
// internal router produces, bounded by a client timeout the SERVER's deadline
// is expected to beat.
func escWaitCall(t *testing.T, h *QueryHandler, escID string, clientTimeout time.Duration) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/v1/internal/escalations/"+escID+"/wait", nil).WithContext(ctx)
	req.SetPathValue("escalationId", escID)
	rr := httptest.NewRecorder()
	h.WaitForEscalationResponse(rr, req)
	return rr.Code, rr.Body.String()
}

// ─── cancel: actor, scope, journal, and the cross-tenant refusal ──────────

func TestCancelEscalationRecordsActorAndScope(t *testing.T) {
	h, rec, userID, wsID, crewID, agentID := escLifecycleRig(t)
	future := time.Now().UTC().Add(time.Hour)
	seedEscalationWithDeadline(t, h, "lc-cancel", wsID, crewID, agentID, "PENDING", &future)

	rr := escCancel(h, userID, wsID, "lc-cancel", "the deploy was rolled back")
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	entries := escalationStateEntries(rec, "lc-cancel", "cancelled")
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 cancellation entry, got %d", len(entries))
	}
	e := entries[0]
	if e.ActorType != journal.ActorUser {
		t.Errorf("actor_type = %q, want user — a cancellation is a human act", e.ActorType)
	}
	if e.ActorID != userID {
		t.Errorf("actor_id = %q, want the cancelling user %q", e.ActorID, userID)
	}
	if e.WorkspaceID != wsID || e.CrewID != crewID {
		t.Errorf("scope = %q/%q, want %q/%q", e.WorkspaceID, e.CrewID, wsID, crewID)
	}
	if reason, _ := e.Payload["reason"].(string); reason != "the deploy was rolled back" {
		t.Errorf("payload.reason = %q, want the operator's stated reason", reason)
	}
}

// Cross-tenant refusal, following the neighbouring convention in
// escalation_waiter_authz_test.go: a foreign id is a 404, never a 403, so the
// endpoint cannot be used to confirm that an id exists in someone else's
// tenant.
func TestCancelEscalationCrossTenantIsNotFound(t *testing.T) {
	h, rec, userID, wsID, crewID, agentID := escLifecycleRig(t)
	future := time.Now().UTC().Add(time.Hour)
	seedEscalationWithDeadline(t, h, "lc-foreign", wsID, crewID, agentID, "PENDING", &future)

	rr := escCancel(h, userID, "ws-somebody-else", "lc-foreign", "not mine to cancel")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for another tenant's escalation; body=%s", rr.Code, rr.Body.String())
	}
	if s := escStatus(t, h, "lc-foreign"); s != "PENDING" {
		t.Errorf("status = %q — a cross-tenant caller mutated a row it cannot see", s)
	}
	if got := escalationStateEntries(rec, "lc-foreign", "cancelled"); len(got) != 0 {
		t.Errorf("a refused cross-tenant cancel emitted %d entries", len(got))
	}
}

// The sweeper is workspace-scoped when a workspace is named, so one tenant's
// operator (or the CLI's `escalation sweep-expired`) cannot expire another
// tenant's questions.
func TestSweepExpiredEscalationsIsWorkspaceScoped(t *testing.T) {
	h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedEscalationWithDeadline(t, h, "lc-mine", wsID, crewID, agentID, "PENDING", &past)

	execOrFatal(t, h.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'ws-other')`)
	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-other', 'ws-other', 'O', 'o')`)
	execOrFatal(t, h.db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag-other', 'crew-other', 'ws-other', 'O', 'o')`)
	seedEscalationWithDeadline(t, h, "lc-theirs", "ws-other", "crew-other", "ag-other", "PENDING", &past)

	n, err := h.sweepExpiredEscalations(context.Background(), wsID)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("scoped sweep expired %d rows, want only this workspace's 1", n)
	}
	if s := escStatus(t, h, "lc-theirs"); s != "PENDING" {
		t.Errorf("another tenant's escalation is now %q — the sweep was not scoped", s)
	}

	// The unscoped sweep is the background one; it covers every workspace.
	all, err := h.sweepExpiredEscalations(context.Background(), "")
	if err != nil {
		t.Fatalf("unscoped sweep: %v", err)
	}
	if all != 1 {
		t.Errorf("unscoped sweep expired %d rows, want the remaining 1", all)
	}
}

// ─── the deadline is set where the question is asked ──────────────────────

func TestCreateEscalationSetsAndPublishesTheDeadline(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	execOrFatal(t, h.db, `INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`,
		chatID, leadID, wsID)

	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{
		"from_slug": "lead", "reason": "need a decision",
		"crew_id": crewID, "workspace_id": wsID, "chat_id": chatID,
	}))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	var body struct {
		EscalationID   string `json:"escalation_id"`
		DeadlineAt     string `json:"deadline_at"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The sidecar bounds its long poll on these two fields. Without them it
	// falls back to a hardcoded 300 s that only coincidentally matches the
	// server, which is the disagreement this change removes.
	if body.DeadlineAt == "" {
		t.Error("create response carried no deadline_at — the agent cannot bound its wait on the server's clock")
	}
	if body.TimeoutSeconds <= 0 {
		t.Errorf("timeout_seconds = %d, want the server's TTL", body.TimeoutSeconds)
	}
	if _, err := time.Parse(time.RFC3339, body.DeadlineAt); err != nil {
		t.Errorf("deadline_at %q is not RFC3339: %v", body.DeadlineAt, err)
	}

	var stored string
	if err := h.db.QueryRow(`SELECT COALESCE(deadline_at,'') FROM escalations WHERE id = ?`, body.EscalationID).
		Scan(&stored); err != nil {
		t.Fatalf("load deadline: %v", err)
	}
	if stored != body.DeadlineAt {
		t.Errorf("stored deadline %q != published deadline %q", stored, body.DeadlineAt)
	}
}

// ─── the read paths agree with the state machine ──────────────────────────

// pending-count is the number on the dashboard tile. An expired question is
// not pending, and before this change it was counted forever.
func TestPendingCountExcludesExpired(t *testing.T) {
	h, _, userID, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Minute)
	future := time.Now().UTC().Add(time.Hour)
	seedEscalationWithDeadline(t, h, "lc-c-stale", wsID, crewID, agentID, "PENDING", &past)
	seedEscalationWithDeadline(t, h, "lc-c-live", wsID, crewID, agentID, "PENDING", &future)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PendingEscalationCount(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["count"] != 1 {
		t.Errorf("count = %d, want 1 — the past-deadline question is no longer pending", body["count"])
	}
	if s := escStatus(t, h, "lc-c-stale"); s != "EXPIRED" {
		t.Errorf("the read path reported a count without moving the row: status = %q", s)
	}
}

// `crewship escalation list --status` and docs/cli/escalation.mdx have always
// claimed a server-side `?status=` filter. The server ignored it, so with a
// four-state vocabulary the claim became actively misleading.
func TestListEscalationsFiltersByStatus(t *testing.T) {
	h, _, userID, wsID, crewID, agentID := escLifecycleRig(t)
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Minute)
	seedEscalationWithDeadline(t, h, "lc-l-pending", wsID, crewID, agentID, "PENDING", &future)
	seedEscalationWithDeadline(t, h, "lc-l-expired", wsID, crewID, agentID, "PENDING", &past)

	list := func(status string) []map[string]interface{} {
		t.Helper()
		url := "/api/v1/crews/" + crewID + "/escalations"
		if status != "" {
			url += "?status=" + status
		}
		req := withWorkspaceUser(httptest.NewRequest("GET", url, nil), userID, wsID, "OWNER")
		req.SetPathValue("crewId", crewID)
		rr := httptest.NewRecorder()
		h.ListEscalations(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("list status = %d; body=%s", rr.Code, rr.Body.String())
		}
		var items []map[string]interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return items
	}

	if got := len(list("")); got != 2 {
		t.Errorf("unfiltered list returned %d rows, want 2", got)
	}
	pending := list("PENDING")
	if len(pending) != 1 || pending[0]["id"] != "lc-l-pending" {
		t.Errorf("?status=PENDING returned %v, want only lc-l-pending", pending)
	}
	expired := list("EXPIRED")
	if len(expired) != 1 || expired[0]["id"] != "lc-l-expired" {
		t.Errorf("?status=EXPIRED returned %v, want only lc-l-expired", expired)
	}
	if got := list("NONSENSE"); len(got) != 0 {
		t.Errorf("?status=NONSENSE returned %d rows, want 0", len(got))
	}
}

// An expired CREDENTIAL escalation must not report "[credential submitted]".
// The mask exists to keep secret material out of the list, and only a RESOLVED
// row has any: on an EXPIRED row the mask was reporting a submission that never
// happened, which is exactly the kind of confident-but-false row that made the
// old PENDING-forever behaviour hard to reason about.
func TestListEscalationsDoesNotClaimAnExpiredCredentialWasSubmitted(t *testing.T) {
	h, _, userID, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Minute)
	execOrFatal(t, h.db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status,
		 deadline_at, answer_deadline_at, created_at)
		VALUES ('lc-cred', ?, ?, 'lc-chat', ?, 'need STRIPE_API_KEY', 'CREDENTIAL', 'PENDING', ?, ?, datetime('now'))`,
		wsID, crewID, agentID, past.Format(time.RFC3339), past.Format(time.RFC3339))

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/escalations", nil), userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	rr := httptest.NewRecorder()
	h.ListEscalations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d rows, want 1", len(items))
	}
	if items[0]["status"] != "EXPIRED" {
		t.Fatalf("status = %v, want EXPIRED — the read path should have settled it", items[0]["status"])
	}
	res, _ := items[0]["resolution"].(string)
	if res == "[credential submitted]" {
		t.Error("an expired credential request reports a submitted credential — nobody submitted anything")
	}
	if res == "" {
		t.Error("the expired row carries no resolution text explaining why it is terminal")
	}
}

// The background sweeper is the net for rows whose waiter died — a crashed
// sidecar leaves nobody to notice the deadline. It must stop when its context
// does, and it must not need an HTTP request to work.
func TestEscalationExpirySweeperRunsAndStops(t *testing.T) {
	h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedEscalationWithDeadline(t, h, "lc-bg", wsID, crewID, agentID, "PENDING", &past)

	ctx, cancel := context.WithCancel(context.Background())
	h.StartEscalationExpirySweeper(ctx, 20*time.Millisecond)
	t.Cleanup(cancel)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if escStatus(t, h, "lc-bg") == "EXPIRED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background sweeper never expired a past-deadline escalation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
}

// A guard against the sweep quietly going vacuous: the SQL predicate must
// actually be able to see rows, whatever the timestamp format on the column.
func TestSweepMatchesTheStoredTimestampFormat(t *testing.T) {
	h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
	for i, dl := range []string{
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		time.Now().UTC().Add(-time.Minute).Format("2006-01-02 15:04:05"), // datetime('now') shape
	} {
		id := fmt.Sprintf("lc-fmt-%d", i)
		// answer_deadline_at is the column the sweep predicate compares — the
		// human's clock, not the agent's. Both shapes must be readable there.
		execOrFatal(t, h.db, `INSERT INTO escalations
			(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, answer_deadline_at, created_at)
			VALUES (?, ?, ?, 'lc-chat', ?, 'r', 'TEXT', 'PENDING', ?, datetime('now'))`,
			id, wsID, crewID, agentID, dl)
	}
	n, err := h.sweepExpiredEscalations(context.Background(), wsID)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Errorf("sweep expired %d rows, want 2 — a deadline the predicate cannot compare is a deadline that never fires", n)
	}
}
