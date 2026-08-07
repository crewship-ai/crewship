package api

// The fan-out bucket must contain only what the CAPPED DOORS wrote.
//
// The previous round narrowed a human's mention to "rows filed under the
// target and addressed back to it" (dispatchCaller.selfFiled). That bucket is
// character-for-character the shape the mission engine writes: a lead's own
// planning row, and every mission task a lead assigns to itself, are
// assigned_by = assigned_to = the lead, chat_id = the mission, parent NULL,
// in-flight. So on the busy-lead scenario the narrowing was written for, it
// still counted the lead's own mission rows and still refused the mention.
//
// The discriminator is `depth`: the mission engine writes 0 (pinned by
// orchestrator's TestMissionAssignmentRowsCarryDepthZero and by the migration
// that added the column — "0 is deliberately NOT a valid depth for a new
// row"), and every row insertCappedAssignment writes carries the depth
// enforceDelegationCaps derived, which is 1 or more.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ── 1. a lead running a mission is still mentionable ───────────────────────
//
// The scenario the narrowing was written to fix, with the rows the mission
// engine actually writes rather than the ones the previous round assumed.
// Fan-out is set to 1 so a single miscounted row is the whole difference
// between "dispatched" and "refused".

func TestMentions_LeadRunningItsOwnMissionRowsIsStillMentionable(t *testing.T) {
	f := setupMentionFixture(t)
	f.setLimit(t, SettingDelegationMaxFanout, 1)

	execOrFatal(t, f.db, `
		INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`,
		f.missionID, f.target, f.wsID)

	// Exactly what mission_tasks_planning.go writes for a lead's planning
	// turn, and what mission_tasks.go writes for a task the lead assigned to
	// itself: filed under the lead, addressed to the lead, in the mission's
	// pseudo-chat, no parent, depth 0.
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, depth, created_at)
		VALUES ('lead-planning', ?, ?, ?, ?, '[PLANNING] ENG-1', 'RUNNING', ?, 0, datetime('now'))`,
		f.wsID, f.missionID, f.target, f.target, f.missionID)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, depth, created_at)
		VALUES ('lead-selftask', ?, ?, ?, ?, 'task the lead took itself', 'PENDING', ?, 0, datetime('now'))`,
		f.wsID, f.missionID, f.target, f.target, f.missionID)

	f.comment(t, "quick question "+mentionToken("lead", f.target))

	var state, detail string
	if err := f.db.QueryRow(
		`SELECT dispatch_state, COALESCE(dispatch_detail,'') FROM mission_comment_mentions`).
		Scan(&state, &detail); err != nil {
		t.Fatalf("mention row missing: %v", err)
	}
	if state != mentionDispatchDispatched {
		t.Fatalf("dispatch_state = %q (%s), want %q — a person's mention was refused because the "+
			"agent it names is leading a mission; the fan-out bucket is still counting mission rows",
			state, detail, mentionDispatchDispatched)
	}
	var depth int
	if err := f.db.QueryRow(
		`SELECT depth FROM assignments WHERE id NOT IN ('lead-planning','lead-selftask')`).Scan(&depth); err != nil {
		t.Fatalf("read the mention's own row: %v", err)
	}
	if depth < 1 {
		t.Errorf("the mention wrote depth %d — a capped-door row must be distinguishable from a "+
			"mission row, and depth is the column that does it", depth)
	}
}

// ── 2. the bound itself survives ───────────────────────────────────────────
//
// The narrowing must not become "count nothing". One in-flight row that a
// MENTION created (depth 1, filed under and addressed to the target) is the
// budget, and the next mention is refused.

func TestMentions_FanoutStillCountsRowsTheMentionDoorWrote(t *testing.T) {
	f := setupMentionFixture(t)
	f.setLimit(t, SettingDelegationMaxFanout, 1)

	execOrFatal(t, f.db, `
		INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`,
		f.missionID, f.target, f.wsID)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, depth, created_at)
		VALUES ('earlier-mention', ?, ?, ?, ?, 'an earlier mention', 'RUNNING', ?, 1, datetime('now'))`,
		f.wsID, f.missionID, f.target, f.target, f.missionID)

	f.comment(t, "and another "+mentionToken("lead", f.target))

	var state, detail string
	if err := f.db.QueryRow(
		`SELECT dispatch_state, COALESCE(dispatch_detail,'') FROM mission_comment_mentions`).
		Scan(&state, &detail); err != nil {
		t.Fatalf("mention row missing: %v", err)
	}
	if state == mentionDispatchDispatched {
		t.Fatalf("dispatch_state = %q — the fan-out cap stopped counting the rows mentions create", state)
	}
	if !strings.Contains(detail, SettingDelegationMaxFanout) {
		t.Errorf("refusal %q does not name the setting an operator would change", detail)
	}
}

// ── 3. the pre-check and the insert-time re-prove cannot drift ─────────────
//
// delegation_limits.go's header calls this out by name: enforceDelegationCaps
// produces the readable refusal, fanoutGuard re-proves the same headroom
// inside the INSERT, and "the predicate must stay identical". They used to be
// two hand-copied SQL strings. This asserts they select the same rows for all
// three bucket shapes over one seeded table — including the depth-0 mission
// rows that are the whole point of the narrowing.

func TestFanoutPreCheckAndInsertGuardSelectTheSameRows(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewD', ?, 'C', 'cd')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('leadD', 'crewD', ?, 'Lead', 'leadd')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('workD', 'crewD', ?, 'Work', 'workd')`, wsID)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatD', 'leadD', ?, 'CHAT', 'ACTIVE')`, wsID)

	ins := func(id, by, to, status string, depth int, parent any) {
		t.Helper()
		execOrFatal(t, db, `
			INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, parent_assignment_id, created_at)
			VALUES (?, ?, 'chatD', ?, ?, 't', ?, ?, ?, datetime('now'))`,
			id, wsID, by, to, status, depth, parent)
	}
	// A mission planning row and a self-assigned mission task: depth 0.
	ins("m-plan", "leadD", "leadD", "RUNNING", 0, nil)
	ins("m-task", "leadD", "workD", "PENDING", 0, nil)
	// A mention of the lead, and an /assign the lead made: depth 1.
	ins("mention-1", "leadD", "leadD", "RUNNING", 1, nil)
	ins("assign-1", "leadD", "workD", "RUNNING", 1, nil)
	// A child of assign-1.
	ins("child-1", "workD", "leadD", "PENDING", 2, "assign-1")
	// Terminal rows never count in either bucket.
	ins("done-1", "leadD", "leadD", "COMPLETED", 1, nil)

	cases := []struct {
		name   string
		scope  delegationScope
		caller dispatchCaller
		want   int
	}{
		{"children of a parent", delegationScope{ParentID: "assign-1", Depth: 2}, agentCaller("workD"), 1},
		{"human mention, self-filed", delegationScope{Depth: 1}, dispatchCaller{FanoutSubjectID: "leadD"}, 1},
		{"agent root dispatch", delegationScope{Depth: 1}, agentCaller("leadD"), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := countDelegationSiblings(context.Background(), db, tc.scope, tc.caller, "chatD")
			if err != nil {
				t.Fatalf("countDelegationSiblings: %v", err)
			}
			if got != tc.want {
				t.Fatalf("pre-check count = %d, want %d — the bucket is counting mission rows "+
					"(depth 0) or has stopped counting capped-door rows", got, tc.want)
			}
			// The guard admits iff the count is strictly under the limit, so
			// probing it at `got` and at `got+1` reads the same number back
			// out of the INSERT-time predicate.
			guardSQL, guardArgs := fanoutGuard(tc.scope, tc.caller, "chatD", got)
			if admits(t, db, guardSQL, guardArgs) {
				t.Errorf("fanoutGuard admits at maxFanout=%d while the pre-check counts %d — "+
					"the two have drifted and the cap can be raced past", got, got)
			}
			guardSQL, guardArgs = fanoutGuard(tc.scope, tc.caller, "chatD", got+1)
			if !admits(t, db, guardSQL, guardArgs) {
				t.Errorf("fanoutGuard refuses at maxFanout=%d while the pre-check counts %d — "+
					"the two have drifted and legitimate dispatches are refused at the INSERT", got+1, got)
			}
		})
	}
}

// admits evaluates a fanoutGuard predicate standalone — the same boolean
// insertCappedAssignment puts in its INSERT ... WHERE.
func admits(t *testing.T, db *sql.DB, guard string, args []any) bool {
	t.Helper()
	var ok int
	if err := db.QueryRow(`SELECT CASE WHEN `+guard+` THEN 1 ELSE 0 END`, args...).Scan(&ok); err != nil {
		t.Fatalf("evaluate fanout guard: %v", err)
	}
	return ok == 1
}

// ── 4. the hold sentinel is one string, spelled in two packages ────────────
//
// internal/orchestrator cannot import chatbridge (chatbridge imports
// orchestrator), so the engine's admission check carries its own copy of the
// PENDING_REVIEW sentinel. This package imports both, which makes it the only
// place the two can be pinned equal. A drift here means the mission engine's
// pre-check silently never fires and every held dispatch falls back to the
// door — which still refuses, but writes a row per tick again.

func TestOrchestratorHoldSentinelMatchesChatbridge(t *testing.T) {
	if orchestrator.AgentStatusPendingReview != chatbridge.AgentStatusPendingReview {
		t.Fatalf("orchestrator.AgentStatusPendingReview = %q, chatbridge = %q — the mission "+
			"engine's admission check and the door's refusal no longer agree on what a hold is",
			orchestrator.AgentStatusPendingReview, chatbridge.AgentStatusPendingReview)
	}
}

// ── 5. the door's refusal is a DEFERRAL, and the caps' refusal is not ──────
//
// The mission engine classifies a dispatch error into fail / retry / wait by
// looking for DispatchDeferred. *agentHeldError must carry it (a hold is
// permanent until a human acts) and *delegationRefusal must not (a fan-out cap
// clears on its own as siblings finish, so retrying is exactly right).

func TestHeldAgentErrorIsADeferralAndACapRefusalIsNot(t *testing.T) {
	held := refuseHeldAgent("agt", chatbridge.AgentStatusPendingReview)
	if held == nil {
		t.Fatal("refuseHeldAgent returned nil for PENDING_REVIEW")
	}
	if _, ok := held.(interface{ DispatchDeferred() }); !ok {
		t.Errorf("%T does not carry DispatchDeferred — the mission engine will read a hold as a "+
			"terminal failure again", held)
	}
	var cap error = &delegationRefusal{msg: "at the cap"}
	if _, ok := cap.(interface{ DispatchDeferred() }); ok {
		t.Errorf("%T carries DispatchDeferred — a fan-out cap would stop being retried", cap)
	}
}
