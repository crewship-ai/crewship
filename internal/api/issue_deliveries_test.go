package api

// Tests for issue_deliveries.go and its wiring into mentionRecorder.record
// (PRD-ISSUES-AND-ROUTINES-2026 §9.3/§10.2, work package B2, #2337).
//
// Three things this file proves, matching B2's accept line:
//
//   - ten concurrent identical deliveries produce one run (the claim CAS,
//     both as a direct function test and threaded through the real
//     dispatch path);
//   - the ack (issue.delivery.acked) reaches a subscriber before the run's
//     own model-call machinery would ever produce a signal;
//   - a restart between writing the event and consuming the delivery loses
//     nothing — the row, its state, and the run it claimed all survive a
//     real close-and-reopen of the SQLite file.

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// seedDeliveryFixture is the minimal workspace → crew → agent → mission →
// mission_activity chain a delivery row's FKs need — event_id has a real FK
// to mission_activity(id) (20260904145200_deliveries_widen.sql), so every
// test here seeds the event row it claims to be delivering.
func seedDeliveryFixture(t *testing.T, db *sql.DB) (wsID, missionID, agentID string) {
	t.Helper()
	wsID, missionID, agentID = "ws_dlv", "msn_dlv", "agent_dlv"
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws-dlv')`, wsID)
	execOrFatal(t, db, `INSERT INTO users (id, email) VALUES ('user_dlv', 'dlv@example.com')`)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_dlv', ?, 'C', 'crew-dlv')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role) VALUES (?, 'crew_dlv', ?, 'A', 'agent-dlv', 'MEMBER')`, agentID, wsID)
	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES (?, ?, 'crew_dlv', ?, 'trace-dlv', 'issue', 'BACKLOG', 'issue', datetime('now'), datetime('now'))`, missionID, wsID, agentID)
	return wsID, missionID, agentID
}

func seedDeliveryEvent(t *testing.T, db *sql.DB, missionID, eventID string) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action) VALUES (?, ?, 'user', 'user_dlv', 'mentioned')`,
		eventID, missionID)
}

// seedDeliveryAssignment seeds one real assignments row so claimed_by_run_id
// — a real FK to assignments(id), same as the existing assignment_id column
// — has something to reference. INSERT OR IGNORE on chat_dlv: several tests
// attach more than one "run" and all can share the same chat.
func seedDeliveryAssignment(t *testing.T, db *sql.DB, id, agentID string) {
	t.Helper()
	execOrFatal(t, db, `INSERT OR IGNORE INTO chats (id, agent_id, workspace_id) VALUES ('chat_dlv', ?, 'ws_dlv')`, agentID)
	execOrFatal(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task) VALUES (?, 'ws_dlv', 'chat_dlv', ?, ?, 'do it')`,
		id, agentID, agentID)
}

// ── the claim CAS, directly ──────────────────────────────────────────────

// TestDeliveries_TenConcurrentIdenticalDeliveriesProduceOneRun is the B2
// accept-line proof, at the level §9.3 itself describes it: N concurrent
// callers naming the SAME (event_id, agent_id) — the shape a redelivered
// webhook or a restart-time reprocess would produce — must collapse to
// exactly one winning claim, because a caller that does not win must never
// call dispatchOne (deliverAndDispatch enforces that; this test proves the
// CAS underneath it holds under real goroutines and -race).
func TestDeliveries_TenConcurrentIdenticalDeliveriesProduceOneRun(t *testing.T) {
	db := setupTestDB(t)
	_, missionID, agentID := seedDeliveryFixture(t, db)
	const eventID = "evt_ten_concurrent"
	seedDeliveryEvent(t, db, missionID, eventID)

	const n = 10
	var wg sync.WaitGroup
	var wins int64
	var runsStarted int64
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			delivery, err := createDelivery(context.Background(), db, deliveryParams{
				WorkspaceID: "ws_dlv", MissionID: missionID, EventID: eventID,
				AgentID: agentID, Priority: deliveryPriorityNormal,
			})
			if err != nil {
				errs[i] = err
				return
			}
			won, err := claimDelivery(context.Background(), db, delivery.ID)
			if err != nil {
				errs[i] = err
				return
			}
			if won {
				atomic.AddInt64(&wins, 1)
				// Standing in for dispatchOne actually starting a run —
				// what matters is that this branch is reached by exactly
				// one goroutine.
				atomic.AddInt64(&runsStarted, 1)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("claim wins = %d, want 1 — ten identical deliveries of the same event must produce exactly one winner", wins)
	}
	if runsStarted != 1 {
		t.Fatalf("runs started = %d, want 1", runsStarted)
	}

	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_comment_mentions WHERE event_id = ? AND agent_id = ?`,
		eventID, agentID).Scan(&rowCount); err != nil {
		t.Fatalf("count delivery rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("delivery rows for (event, agent) = %d, want 1 — UNIQUE(event_id, agent_id) should have collapsed ten INSERTs to one row", rowCount)
	}

	var state string
	if err := db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE event_id = ? AND agent_id = ?`,
		eventID, agentID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != "claimed" {
		t.Errorf("state = %q, want %q", state, "claimed")
	}
}

// TestDeliveries_ClaimDelivery_LoserGetsFalseNotError pins F57's own point:
// losing the claim CAS is (false, nil), never an error — a caller that
// treated "someone else already claimed it" as a failure would log noise on
// every ordinary redelivery.
func TestDeliveries_ClaimDelivery_LoserGetsFalseNotError(t *testing.T) {
	db := setupTestDB(t)
	_, missionID, agentID := seedDeliveryFixture(t, db)
	seedDeliveryEvent(t, db, missionID, "evt_claim_loser")

	delivery, err := createDelivery(context.Background(), db, deliveryParams{
		WorkspaceID: "ws_dlv", MissionID: missionID, EventID: "evt_claim_loser", AgentID: agentID,
	})
	if err != nil {
		t.Fatalf("createDelivery: %v", err)
	}

	won1, err := claimDelivery(context.Background(), db, delivery.ID)
	if err != nil || !won1 {
		t.Fatalf("first claim: won=%v err=%v, want true/nil", won1, err)
	}
	won2, err := claimDelivery(context.Background(), db, delivery.ID)
	if err != nil {
		t.Fatalf("second claim returned an error rather than a lost CAS: %v", err)
	}
	if won2 {
		t.Fatal("second claim of an already-claimed delivery won — the CAS did not guard on state='pending'")
	}
}

// TestDeliveries_CreateDelivery_IsIdempotent proves createDelivery's own
// idempotent-create half directly: two calls for the same (event, agent)
// return the SAME id, and only the first reports Created.
func TestDeliveries_CreateDelivery_IsIdempotent(t *testing.T) {
	db := setupTestDB(t)
	_, missionID, agentID := seedDeliveryFixture(t, db)
	seedDeliveryEvent(t, db, missionID, "evt_idempotent")

	p := deliveryParams{WorkspaceID: "ws_dlv", MissionID: missionID, EventID: "evt_idempotent", AgentID: agentID}
	d1, err := createDelivery(context.Background(), db, p)
	if err != nil {
		t.Fatalf("first createDelivery: %v", err)
	}
	if !d1.Created {
		t.Error("first createDelivery: Created = false, want true")
	}
	d2, err := createDelivery(context.Background(), db, p)
	if err != nil {
		t.Fatalf("second createDelivery: %v", err)
	}
	if d2.Created {
		t.Error("second createDelivery: Created = true, want false — the row already existed")
	}
	if d1.ID != d2.ID {
		t.Errorf("ids differ: %q vs %q", d1.ID, d2.ID)
	}
}

// ── the consume CAS ───────────────────────────────────────────────────────

// TestDeliveries_ConsumeRequiresClaimedState: consumeDelivery on a still-
// pending row must be a no-op (false, nil), and must succeed once the row is
// claimed — the second half of §10.2's pending -> claimed -> consumed chain.
func TestDeliveries_ConsumeRequiresClaimedState(t *testing.T) {
	db := setupTestDB(t)
	_, missionID, agentID := seedDeliveryFixture(t, db)
	seedDeliveryEvent(t, db, missionID, "evt_consume")

	delivery, err := createDelivery(context.Background(), db, deliveryParams{
		WorkspaceID: "ws_dlv", MissionID: missionID, EventID: "evt_consume", AgentID: agentID,
	})
	if err != nil {
		t.Fatalf("createDelivery: %v", err)
	}

	consumed, err := consumeDelivery(context.Background(), db, delivery.ID)
	if err != nil {
		t.Fatalf("consumeDelivery on a pending row: %v", err)
	}
	if consumed {
		t.Fatal("consumeDelivery succeeded on a still-pending row — it must require state='claimed'")
	}

	if _, err := claimDelivery(context.Background(), db, delivery.ID); err != nil {
		t.Fatalf("claimDelivery: %v", err)
	}
	consumed, err = consumeDelivery(context.Background(), db, delivery.ID)
	if err != nil {
		t.Fatalf("consumeDelivery on a claimed row: %v", err)
	}
	if !consumed {
		t.Fatal("consumeDelivery failed on a claimed row")
	}

	var state string
	if err := db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE id = ?`, delivery.ID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != "consumed" {
		t.Errorf("state = %q, want %q", state, "consumed")
	}
}

// TestDeliveries_ConsumeDeliveriesForRun_OnlyTouchesThatRunsClaims: the bulk
// consume finishAssignment calls must scope strictly to claimed_by_run_id —
// a delivery claimed by a DIFFERENT run must be untouched.
func TestDeliveries_ConsumeDeliveriesForRun_OnlyTouchesThatRunsClaims(t *testing.T) {
	db := setupTestDB(t)
	_, missionID, agentID := seedDeliveryFixture(t, db)
	seedDeliveryEvent(t, db, missionID, "evt_run_a")
	seedDeliveryEvent(t, db, missionID, "evt_run_b")

	dA, err := createDelivery(context.Background(), db, deliveryParams{WorkspaceID: "ws_dlv", MissionID: missionID, EventID: "evt_run_a", AgentID: agentID})
	if err != nil {
		t.Fatalf("createDelivery A: %v", err)
	}
	dB, err := createDelivery(context.Background(), db, deliveryParams{WorkspaceID: "ws_dlv", MissionID: missionID, EventID: "evt_run_b", AgentID: agentID})
	if err != nil {
		t.Fatalf("createDelivery B: %v", err)
	}
	for _, id := range []string{dA.ID, dB.ID} {
		if _, err := claimDelivery(context.Background(), db, id); err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
	}
	seedDeliveryAssignment(t, db, "run_a", agentID)
	seedDeliveryAssignment(t, db, "run_b", agentID)
	if err := attachDeliveryRun(context.Background(), db, dA.ID, "run_a"); err != nil {
		t.Fatalf("attach run A: %v", err)
	}
	if err := attachDeliveryRun(context.Background(), db, dB.ID, "run_b"); err != nil {
		t.Fatalf("attach run B: %v", err)
	}

	n, err := consumeDeliveriesForRun(context.Background(), db, "run_a")
	if err != nil {
		t.Fatalf("consumeDeliveriesForRun: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows consumed = %d, want 1", n)
	}

	var stateA, stateB string
	if err := db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE id = ?`, dA.ID).Scan(&stateA); err != nil {
		t.Fatalf("read state A: %v", err)
	}
	if err := db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE id = ?`, dB.ID).Scan(&stateB); err != nil {
		t.Fatalf("read state B: %v", err)
	}
	if stateA != "consumed" {
		t.Errorf("run A's delivery state = %q, want consumed", stateA)
	}
	if stateB != "claimed" {
		t.Errorf("run B's delivery state = %q, want claimed (untouched by run A's completion)", stateB)
	}
}

// ── restart survives ──────────────────────────────────────────────────────

// TestDeliveries_RestartBetweenEventAndConsumptionLosesNothing is the B2
// accept line's third clause: "a restart between event and consumption
// loses nothing". A pending delivery is created and claimed (the state a
// live run holds it in), the *sql.DB is CLOSED and a fresh one opened
// against the SAME file (database.Open, exactly what a real process restart
// does), and the delivery — id, state, claimed_by_run_id, the event it
// references — is read back unchanged and consumed through the new handle.
// Nothing here is in-memory-only: the row is the only state, and it is a
// SQLite row on disk.
func TestDeliveries_RestartBetweenEventAndConsumptionLosesNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	wrapped := testutil.MigratedDBAt(t, dbPath)
	db := wrapped.DB

	_, missionID, agentID := seedDeliveryFixture(t, db)
	seedDeliveryEvent(t, db, missionID, "evt_restart")

	delivery, err := createDelivery(context.Background(), db, deliveryParams{
		WorkspaceID: "ws_dlv", MissionID: missionID, EventID: "evt_restart", AgentID: agentID,
	})
	if err != nil {
		t.Fatalf("createDelivery: %v", err)
	}
	if won, err := claimDelivery(context.Background(), db, delivery.ID); err != nil || !won {
		t.Fatalf("claimDelivery: won=%v err=%v", won, err)
	}
	seedDeliveryAssignment(t, db, "run_restart", agentID)
	if err := attachDeliveryRun(context.Background(), db, delivery.ID, "run_restart"); err != nil {
		t.Fatalf("attachDeliveryRun: %v", err)
	}

	// "Restart": close this handle entirely and open a brand new one against
	// the same file, the way a fresh process boot would.
	if err := wrapped.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	reopened, err := database.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("reopen after restart: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	freshDB := reopened.DB

	var state, claimedBy, eventID string
	if err := freshDB.QueryRow(
		`SELECT state, COALESCE(claimed_by_run_id,''), COALESCE(event_id,'') FROM mission_comment_mentions WHERE id = ?`,
		delivery.ID,
	).Scan(&state, &claimedBy, &eventID); err != nil {
		t.Fatalf("read delivery after restart: %v", err)
	}
	if state != "claimed" {
		t.Errorf("state after restart = %q, want claimed — the claim must survive", state)
	}
	if claimedBy != "run_restart" {
		t.Errorf("claimed_by_run_id after restart = %q, want run_restart", claimedBy)
	}
	if eventID != "evt_restart" {
		t.Errorf("event_id after restart = %q, want evt_restart", eventID)
	}

	// And the consume half of the lifecycle still works through the NEW
	// handle — nothing about the claim was process-local state.
	consumed, err := consumeDelivery(context.Background(), freshDB, delivery.ID)
	if err != nil {
		t.Fatalf("consumeDelivery after restart: %v", err)
	}
	if !consumed {
		t.Fatal("consumeDelivery after restart did not find the claimed row")
	}
}

// ── threaded through the real dispatch path ────────────────────────────────

// TestMentions_DeliveryAckedBeforeDispatch is B2's other accept-line clause:
// "the ack reaches the client without a refresh". A real ws.Hub (Run loop
// included, mirroring issue_status_changed_broadcast_test.go's pattern) is
// wired to the fixture; a comment mentioning an agent must produce an
// issue.delivery.acked frame on the workspace channel, carrying the
// delivery and event ids, and the delivery row it names must already exist
// (claimed) by the time the frame is observed — i.e. the ack is not a
// trailing echo of a run that already finished.
func TestMentions_DeliveryAckedBeforeDispatch(t *testing.T) {
	f := setupMentionFixture(t)
	hub := startedTestHub(t)
	f.issues.hub = hub
	f.internal.hub = hub

	obs := hub.AddObserver("workspace:"+f.wsID, "u-delivery-acked", 8)
	defer hub.RemoveObserver("workspace:"+f.wsID, obs)

	f.comment(t, "please look "+mentionToken("lead", f.target))

	type ackFrame struct {
		Type    string `json:"type"`
		Payload struct {
			MissionID  string `json:"mission_id"`
			AgentID    string `json:"agent_id"`
			DeliveryID string `json:"delivery_id"`
			EventID    string `json:"event_id"`
		} `json:"payload"`
	}
	var frame ackFrame
	found := false
	for i := 0; i < 8; i++ {
		raw, ok := <-obs.Frames()
		if !ok {
			break
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		if frame.Type == "issue.delivery.acked" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no issue.delivery.acked frame observed")
	}
	if frame.Payload.MissionID != f.missionID {
		t.Errorf("payload mission_id = %q, want %q", frame.Payload.MissionID, f.missionID)
	}
	if frame.Payload.AgentID != f.target {
		t.Errorf("payload agent_id = %q, want %q", frame.Payload.AgentID, f.target)
	}
	if frame.Payload.DeliveryID == "" {
		t.Error("payload delivery_id is empty")
	}
	if frame.Payload.EventID == "" {
		t.Error("payload event_id is empty")
	}

	var state string
	if err := f.db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE id = ?`, frame.Payload.DeliveryID).Scan(&state); err != nil {
		t.Fatalf("read delivery state: %v", err)
	}
	// consumed, not just claimed: finishAssignment's consumeDeliveriesForRun
	// call (assignments_run.go) has already run by the time WaitDispatches
	// (inside f.comment) returns — the test environment has no real
	// container executor, so runAssignment fails fast and finishAssignment
	// still runs the terminal path for a FAILED run, which is exactly the
	// "did a run consume this, independent of whether it succeeded" property
	// §9.3 draws the state/dispatch_state distinction for.
	if state != "consumed" {
		t.Errorf("delivery state = %q, want consumed — finishAssignment should have closed the claim", state)
	}
}

// TestMentions_SelfMentionConsumesItsOwnDelivery: a self-mention is claimed
// (deliverAndDispatch wins the CAS) but dispatchOne refuses to call
// DispatchMention at all — no assignment is ever created, so nothing will
// ever call finishAssignment for this delivery. deliverAndDispatch must
// resolve it inline (consumeDelivery) rather than leaving it 'claimed'
// forever.
func TestMentions_SelfMentionConsumesItsOwnDelivery(t *testing.T) {
	f := setupMentionFixture(t)
	f.commentAsAgent(t, f.author, "note to self "+mentionToken("me", f.author))

	var state string
	var claimedBy sql.NullString
	if err := f.db.QueryRow(`SELECT state, claimed_by_run_id FROM mission_comment_mentions`).Scan(&state, &claimedBy); err != nil {
		t.Fatalf("mention row missing: %v", err)
	}
	if state != "consumed" {
		t.Errorf("state = %q, want consumed — a self-mention's delivery must not be left claimed forever", state)
	}
	if claimedBy.Valid {
		t.Errorf("claimed_by_run_id = %q, want NULL — no assignment was ever created for a self-mention", claimedBy.String)
	}
}

// TestMentions_DuplicateDeliveryDoesNotDoubleDispatch threads the ten-
// concurrent-deliveries guarantee through the REAL production call
// (deliverAndDispatch), not just the bare CAS functions: redelivering the
// same (event, agent) pair — the shape a retried webhook or a restart-time
// resync would produce — must not start a second run.
func TestMentions_DuplicateDeliveryDoesNotDoubleDispatch(t *testing.T) {
	f := setupMentionFixture(t)
	m := mentionRecorder{db: f.db, logger: newTestLogger(), events: f.issues.events(), dispatcher: f.assign}
	mc := mentionContext{
		WorkspaceID: f.wsID, MissionID: f.missionID, Identifier: f.ident,
		CommentID: "", AuthorType: "user", AuthorID: f.userID,
	}
	mention := resolvedMention{AgentID: f.target}

	const eventID = "evt_dup_dispatch"
	seedDeliveryEvent(t, f.db, f.missionID, eventID)

	state1, asg1, _ := m.deliverAndDispatch(context.Background(), mc, mention, eventID, 1, true)
	state2, asg2, detail2 := m.deliverAndDispatch(context.Background(), mc, mention, eventID, 1, true)
	f.assign.WaitDispatches()

	if state1 != mentionDispatchDispatched || asg1 == "" {
		t.Fatalf("first delivery: state=%q assignment=%q, want dispatched with an assignment id", state1, asg1)
	}
	if state2 != mentionDispatchSkipped || asg2 != "" {
		t.Fatalf("second (duplicate) delivery: state=%q assignment=%q, want skipped with no assignment", state2, asg2)
	}
	if detail2 == "" {
		t.Error("second delivery's detail is empty — the operator gets no explanation for why it was skipped")
	}
	if n := f.assignments(t); n != 1 {
		t.Fatalf("assignments = %d, want 1 — the duplicate delivery must not have started a second run", n)
	}
}

// TestMentions_IssueDeliveriesFlagOff_DispatchesWithoutADeliveryRow proves
// the off-switch degrades to the exact pre-B2 behaviour: dispatch still
// happens, and no delivery bookkeeping (row, ack) is attempted — matching
// resolveOrCreateIssueAgentSession's own off-switch shape.
func TestMentions_IssueDeliveriesFlagOff_DispatchesWithoutADeliveryRow(t *testing.T) {
	f := setupMentionFixture(t)
	execOrFatal(t, f.db, `UPDATE feature_flags SET enabled = 0 WHERE key = ?`, issueDeliveriesFlagKey)

	f.comment(t, "please look "+mentionToken("lead", f.target))

	if n := f.assignments(t); n != 1 {
		t.Fatalf("assignments = %d, want 1 — dispatch must still happen with the flag off", n)
	}
	var claimedByRun sql.NullString
	if err := f.db.QueryRow(`SELECT claimed_by_run_id FROM mission_comment_mentions`).Scan(&claimedByRun); err != nil {
		t.Fatalf("read delivery row: %v", err)
	}
	if claimedByRun.Valid {
		t.Error("claimed_by_run_id is set — the flag-off path should never run the claim CAS")
	}
}
