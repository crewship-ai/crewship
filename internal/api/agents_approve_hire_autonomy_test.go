package api

// Red-first coverage for the three defects the #1768 widening of
// approve-hire introduced. All three come from the same root: the
// approve-hire door — MANAGER-tier, keyed on agent_id — was taught to
// decide kind=autonomy_gate approvals, which are ADMIN-tier and keyed on
// (target, target_id).
//
//	F1  a MISSION hold carries agent_id = the LEAD agent, so
//	    findHireApprovalRow's agent-id-only match could return it and
//	    approve-hire would silently approve a mission nobody reviewed.
//	F2  applyAutonomyGateDecisionTx's deny arm is a no-op on the target
//	    and never writes agents.expired_at, so the approve-hire preflight
//	    could not see a denied/timed-out hold and released the agent.
//	F3  approve-hire is registered roleCreate (MANAGER+) while every
//	    autonomy-gate hold is addressed to ADMIN because its decide route
//	    is roleManage — so a MANAGER could take an OWNER/ADMIN decision.
//
// The fix collapses all three: approve-hire decides EPHEMERAL hires only,
// and an autonomy-gate hold is released on the approvals door alone. Each
// test below therefore asserts two things — the door refuses, AND the hold
// is still releasable by the role the gate does consider sufficient.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/policy"
)

// stageHeldAgent creates a persistent agent through the gated internal
// route under a `guided` crew, i.e. the #1768 hold: the row lands
// PENDING_REVIEW with an approvals_queue row and a blocking ADMIN
// waitpoint. Returns the agent id and the approval id.
func stageHeldAgent(t *testing.T, db *sql.DB, wsID, crewID string, res *policy.Resolver) (agentID, approvalID string) {
	t.Helper()
	h := gatedInternalHandler(t, db, res)
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"crew_id":"`+crewID+`","name":"Staged By Agent"}`, wsID, crewID))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("stage held agent: status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	agentID, _ = body["id"].(string)
	approvalID, _ = body["approval_id"].(string)
	if agentID == "" || approvalID == "" {
		t.Fatalf("stage held agent: missing id/approval_id in %v", body)
	}
	return agentID, approvalID
}

func heldAgentStatus(t *testing.T, db *sql.DB, agentID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&s); err != nil {
		t.Fatalf("read agent status: %v", err)
	}
	return s
}

// ── F1 ──────────────────────────────────────────────────────────────────────

// TestApproveHire_DoesNotDecideAMissionHold is the collision case.
//
// Agent B is staged under a guided crew (hold R1, kind=autonomy_gate,
// agent_id=B, target=agent). The operator then lowers the crew to strict and
// an agent plans a mission with B as its LEAD — missions_internal.go checks
// only that the lead belongs to the crew, not that it is live — so the
// mission is held too (R2, kind=autonomy_gate, agent_id=B, target=mission),
// and R2 is NEWER than R1.
//
// `crewship hire approve B` must not touch R2. Pre-fix findHireApprovalRow
// matched autonomy-gate rows by agent_id alone and ORDER BY created_at DESC
// handed back R2, so approve-hire approved a mission hold nobody had read and
// InternalMissionHandler.Start then dispatched the whole task list.
// Two leads, because the fix has two independent layers and each has to be
// proved on its own:
//
//	persistent lead — stopped by the preflight (approve-hire is ephemeral-only).
//	ephemeral lead  — a hire the operator staged with `crewship hire`, which
//	                  approve-hire legitimately DOES decide. It sails past the
//	                  preflight, so the only thing standing between it and the
//	                  mission hold is findHireApprovalRow selecting one kind.
func TestApproveHire_DoesNotDecideAMissionHold(t *testing.T) {
	// holdMissionOn plans a mission led by leadID under a strict crew and
	// returns (missionID, approvalID) for the resulting hold, which is
	// guaranteed newer than any approval that already exists for leadID.
	holdMissionOn := func(t *testing.T, db *sql.DB, wsID, crewID, leadID string,
		res *policy.Resolver, mh *InternalMissionHandler) (string, string) {
		t.Helper()
		rr := httptest.NewRecorder()
		mh.Create(rr, boundInternalReq(http.MethodPost, "/",
			`{"title":"Unreviewed","lead_agent_id":"`+leadID+`","crew_id":"`+crewID+
				`","workspace_id":"`+wsID+`"}`, wsID, crewID))
		if rr.Code != http.StatusAccepted {
			t.Fatalf("mission create: status = %d, want 202 (held at strict); body=%s", rr.Code, rr.Body.String())
		}
		body := decodeGateBody(t, rr.Body.Bytes())
		missionID, _ := body["id"].(string)
		approvalID, _ := body["approval_id"].(string)
		if missionID == "" || approvalID == "" {
			t.Fatalf("held mission must return its id and approval; got %v", body)
		}
		// Make "newest" unambiguous rather than trusting sub-millisecond
		// ordering: the collision only bites when the mission hold sorts
		// first, so the test must guarantee that, not hope for it.
		execOrFatal(t, db, `UPDATE approvals_queue SET created_at = '2000-01-01T00:00:00.000Z'
			WHERE workspace_id = ? AND id != ?`, wsID, approvalID)
		return missionID, approvalID
	}

	assertMissionStillClosed := func(t *testing.T, mh *InternalMissionHandler, wsID, crewID, missionID string) {
		t.Helper()
		req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
		req.SetPathValue("missionId", missionID)
		rr := httptest.NewRecorder()
		mh.Start(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("mission start = %d, want 403 — approve-hire opened the mission gate; body=%s",
				rr.Code, rr.Body.String())
		}
	}

	// The reported scenario: agent B is staged under a guided crew (hold R1,
	// target=agent), the operator lowers the crew to strict, and an agent
	// plans a mission with B as LEAD — missions_internal.go checks only that
	// the lead belongs to the crew, not that it is live — so the mission is
	// held too (R2, agent_id=B, target=mission) and R2 is newer.
	t.Run("persistent lead", func(t *testing.T) {
		db, wsID, crewID, userID, res := autonomyRig(t, "guided")
		agentB, r1 := stageHeldAgent(t, db, wsID, crewID, res)

		execOrFatal(t, db, `UPDATE crews SET autonomy_level = 'strict' WHERE id = ?`, crewID)
		res.Invalidate(crewID)
		mh := NewInternalMissionHandler(db, nil, nil, testLogger())
		mh.SetAutonomyGate(res, nil)
		missionID, r2 := holdMissionOn(t, db, wsID, crewID, agentB, res, mh)

		// Taken by the most privileged role there is, so a pass cannot be
		// mistaken for the RBAC gate doing the work.
		got := postApproveHire(t, newApproveHireHandler(t, db), userID, wsID, "OWNER", agentB)

		if s := approvalStatus(t, db, r2); s != "pending" {
			t.Errorf("MISSION hold R2 is %q after approve-hire on the LEAD AGENT — approve-hire decided a "+
				"mission nobody reviewed (approve-hire replied %d: %s)", s, got.Code, got.Body.String())
		}
		if s := approvalStatus(t, db, r1); s != "pending" {
			t.Errorf("agent hold R1 is %q; the agent door must not have decided it either", s)
		}
		assertMissionStillClosed(t, mh, wsID, crewID, missionID)
	})

	// The layer below the preflight. This hire is ephemeral, so approve-hire
	// SHOULD decide it — and does, 200, agent live. What it must not do on the
	// way is reach past its own kind and take the mission hold that happens to
	// carry the same agent_id.
	t.Run("ephemeral lead", func(t *testing.T) {
		db, wsID, crewID, userID, res := autonomyRig(t, "strict")
		seedPendingReviewAgent(t, db, wsID, crewID, "eph-lead")
		hireRow := enqueueHireApproval(t, db, wsID, crewID, "eph-lead", userID)

		mh := NewInternalMissionHandler(db, nil, nil, testLogger())
		mh.SetAutonomyGate(res, nil)
		missionID, r2 := holdMissionOn(t, db, wsID, crewID, "eph-lead", res, mh)

		rr := postApproveHire(t, newApproveHireHandler(t, db), userID, wsID, "OWNER", "eph-lead")
		if rr.Code != http.StatusOK {
			t.Fatalf("approve-hire on a real ephemeral hire = %d, want 200 — the fix must not brick "+
				"the flow it exists for; body=%s", rr.Code, rr.Body.String())
		}
		if s := heldAgentStatus(t, db, "eph-lead"); s != "IDLE" {
			t.Fatalf("approved ephemeral hire is %q, want IDLE", s)
		}
		if s := approvalStatus(t, db, hireRow); s != "approved" {
			t.Errorf("the agent's OWN ephemeral_hire row is %q, want approved — approve-hire decided "+
				"some other row instead of the one it was asked about", s)
		}
		if s := approvalStatus(t, db, r2); s != "pending" {
			t.Errorf("MISSION hold is %q — approving an ephemeral hire also approved the mission "+
				"that happens to name it as lead", s)
		}
		assertMissionStillClosed(t, mh, wsID, crewID, missionID)
	})
}

// ── F2 ──────────────────────────────────────────────────────────────────────

// TestApproveHire_CannotResurrectATerminalAutonomyHold pins that a hold an
// operator REFUSED — or one whose window lapsed — stays refused on every
// door.
//
// The autonomy-gate deny arm is deliberately a no-op on the target (the
// agent just stays inert), which means it never writes agents.expired_at.
// Pre-fix that left approve-hire's preflight blind: PENDING_REVIEW +
// expired_at NULL looked decidable, the terminal approvals row was dropped
// as a "previous-cycle verdict", and the unconditional UPDATE flipped a
// denied agent — whose system prompt another agent wrote — to IDLE.
func TestApproveHire_CannotResurrectATerminalAutonomyHold(t *testing.T) {
	cases := []struct {
		name string
		// terminate puts the hold into its terminal state.
		terminate func(t *testing.T, db *sql.DB, wsID, userID, approvalID string)
		want      string
	}{
		{
			name: "denied",
			terminate: func(t *testing.T, db *sql.DB, wsID, userID, approvalID string) {
				approveHold(t, db, wsID, userID, approvalID, "denied")
			},
			want: "denied",
		},
		{
			name: "timed out",
			terminate: func(t *testing.T, db *sql.DB, wsID, userID, approvalID string) {
				// What harbormaster's sweeper does to an unattended hold. It
				// ghosts the agent only for kind=ephemeral_hire, so an
				// autonomy-gate hold leaves agents.expired_at NULL.
				execOrFatal(t, db, `UPDATE approvals_queue SET status = 'timeout' WHERE id = ?`, approvalID)
			},
			want: "timeout",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, wsID, crewID, userID, res := autonomyRig(t, "guided")
			agentID, approvalID := stageHeldAgent(t, db, wsID, crewID, res)
			tc.terminate(t, db, wsID, userID, approvalID)

			h := newApproveHireHandler(t, db)
			rr := postApproveHire(t, h, userID, wsID, "OWNER", agentID)
			if rr.Code != http.StatusConflict {
				t.Errorf("approve-hire on a %s hold = %d, want 409; body=%s", tc.name, rr.Code, rr.Body.String())
			}
			if s := heldAgentStatus(t, db, agentID); s != "PENDING_REVIEW" {
				t.Fatalf("a %s hold released the agent to %q — an agent whose system prompt another "+
					"agent wrote went live after the operator refused it", tc.name, s)
			}
			if s := approvalStatus(t, db, approvalID); s != tc.want {
				t.Errorf("approvals row = %q, want %q — approve-hire must not rewrite a terminal verdict", s, tc.want)
			}
		})
	}
}

// TestAutonomyGate_ApproveDoesNotResurrectAGhostedAgent pins the fail-closed
// guard on the one door that DOES release a gate-held agent.
//
// Nothing in the product writes agents.expired_at on a persistent row today —
// the ephemeral deny arm and harbormaster's sweeper both scope theirs to
// ephemeral=1 — which is exactly why the guard needs a test rather than a
// reader's confidence. The release must leave a dead row dead; the approval
// still goes terminal, because refusing to record the operator's decision
// would leave a hold nobody can ever clear.
func TestAutonomyGate_ApproveDoesNotResurrectAGhostedAgent(t *testing.T) {
	db, wsID, crewID, userID, res := autonomyRig(t, "guided")
	agentID, approvalID := stageHeldAgent(t, db, wsID, crewID, res)

	execOrFatal(t, db, `UPDATE agents SET expired_at = '2026-01-01T00:00:00Z' WHERE id = ?`, agentID)
	approveHold(t, db, wsID, userID, approvalID, "approved")

	if s := heldAgentStatus(t, db, agentID); s != "PENDING_REVIEW" {
		t.Fatalf("approving released a ghosted agent to %q — the release arm must fail closed", s)
	}
}

// ── F3 ──────────────────────────────────────────────────────────────────────

// TestAutonomyHeldAgent_ManagerCannotRelease_OwnerCan is the authority half.
//
// writeAutonomyHold addresses every hold's inbox item to ADMIN precisely
// because POST /approvals/{id}/decide is roleManage (OWNER/ADMIN). approve-hire
// is roleCreate (MANAGER+). Widening approve-hire to decide autonomy-gate rows
// therefore handed MANAGER a decision the gate reserves for OWNER/ADMIN.
//
// The second half is the one that keeps the fix honest: refusing the MANAGER
// is only correct if the hold is still releasable by the role the gate DOES
// consider sufficient. A gate that cannot be opened is its own outage.
func TestAutonomyHeldAgent_ManagerCannotRelease_OwnerCan(t *testing.T) {
	db, wsID, crewID, userID, res := autonomyRig(t, "guided")
	agentID, approvalID := stageHeldAgent(t, db, wsID, crewID, res)

	h := newApproveHireHandler(t, db)
	rr := postApproveHire(t, h, userID, wsID, "MANAGER", agentID)
	if rr.Code == http.StatusOK {
		t.Errorf("approve-hire as MANAGER = 200 — a MANAGER took an OWNER/ADMIN decision; body=%s", rr.Body.String())
	}

	// The refusal has to be a signpost, not a dead end: `crewship hire
	// approve` is the command an operator naturally reaches for, and
	// cli.CheckError renders only the `error` field — so the door that says
	// no has to say where yes lives, there.
	refusal := decodeGateBody(t, rr.Body.Bytes())
	if got, _ := refusal["approval_id"].(string); got != approvalID {
		t.Errorf("409 body approval_id = %q, want %q — the operator is told no with no way to yes", got, approvalID)
	}
	if msg, _ := refusal["error"].(string); !strings.Contains(msg, "approvals approve "+approvalID) {
		t.Errorf("409 `error` = %q; it must carry the approvals command, because that is the only "+
			"field the CLI prints", msg)
	}
	if s := heldAgentStatus(t, db, agentID); s != "PENDING_REVIEW" {
		t.Fatalf("MANAGER released the held agent (status = %q); the gate reserves this for OWNER/ADMIN", s)
	}
	if s := approvalStatus(t, db, approvalID); s != "pending" {
		t.Fatalf("MANAGER decided the approvals row (status = %q)", s)
	}

	// … and the release path the gate DOES sanction still opens it.
	approveHold(t, db, wsID, userID, approvalID, "approved")
	if s := heldAgentStatus(t, db, agentID); s != "IDLE" {
		t.Fatalf("OWNER approve on the approvals door left the agent %q — the hold has no release path at all", s)
	}
}

// TestApproveHireRouteTierMatchesItsDecisions is the source-level invariant
// behind F3, in the shape of TestInboxTargetRoleMatchesDecider: the tier a
// route is registered at has to cover every decision its handler can take.
//
// approve-hire stays roleCreate — a MANAGER may hire, so a MANAGER may approve
// their own hire, which is the PR-D F5 contract. That is only sound while the
// handler decides ephemeral hires and nothing else.
//
// findHireApprovalRow is the single feeder of harbormaster.DecideTx in that
// handler, so the set of kinds its query matches IS the set of decisions the
// route can take. This reads that query and requires it to name exactly one
// kind. `kind IN (...)` is the literal shape of the #1768 widening, and it is
// also the shape that reintroduces F1: agent_id is not unique across kinds, so
// any second kind can hand back a hold whose target is not this agent.
func TestApproveHireRouteTierMatchesItsDecisions(t *testing.T) {
	repo := repoRoot(t)

	route := routeLineFor(readFile(t, filepath.Join(repo, "internal/api/router_crews.go")),
		"/api/v1/agents/{agentId}/approve-hire")
	if route == "" {
		t.Fatal("approve-hire route not found in router_crews.go — did it move?")
	}
	if !strings.Contains(route, "roleCreate") {
		t.Fatalf("approve-hire is no longer roleCreate:\n  %s\n"+
			"If the tier changed deliberately, update this test and cmd/crewship/cmd_hire.go's help text.",
			strings.TrimSpace(route))
	}

	src, err := os.ReadFile(filepath.Join(repo, "internal/api/agents_approve_hire.go"))
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	_, after, found := strings.Cut(string(src), "func findHireApprovalRow(")
	if !found {
		t.Fatal("findHireApprovalRow not found in agents_approve_hire.go — did it move?")
	}
	body, _, _ := strings.Cut(after, "\n}")
	if !strings.Contains(body, "kind = ?") || !strings.Contains(body, "KindEphemeralHire") {
		t.Fatalf("findHireApprovalRow no longer selects a single kind=ephemeral_hire row:\n%s", body)
	}
	if strings.Contains(body, "kind IN") || strings.Contains(body, "KindAutonomyGate") {
		t.Fatalf("findHireApprovalRow matches more than kind=ephemeral_hire:\n%s\n\n"+
			"Autonomy-gate holds are addressed to ADMIN because POST /approvals/{id}/decide is roleManage, "+
			"so deciding one from this roleCreate route lets a MANAGER take an OWNER/ADMIN decision (#1791 F3); "+
			"and agent_id is not unique across kinds, so a MISSION hold carrying its lead agent's id can be "+
			"returned here and silently approved (#1791 F1).", body)
	}
}
