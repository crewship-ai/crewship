package server

// Tests for the B12 instrumentation collectors (metrics_domain_b12.go,
// PRD-ISSUES-AND-ROUTINES-2026 §17/§19.3, F39). Each test drives the
// behaviour through the real write path — real INSERTs into the tables
// B1/B2/B4/B5/B6 already ship, with explicit created_at timestamps where a
// latency needs a known gap — and then reads the collector's rendered
// Prometheus text, the same shape TestCollectLLMCostMetrics_FoldsOverflowIntoOther
// uses for an existing collector.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

// b12Fixture seeds the minimal workspace → crew → agent → mission → chat
// chain every B12 test below needs, mirroring
// internal/api/issue_deliveries_test.go's seedDeliveryFixture for the same
// tables, and returns a bare *Server wired to it — these collectors only
// touch s.db and s.logger, the same shape
// TestCollectLLMCostMetrics_FoldsOverflowIntoOther already uses.
func b12Fixture(t *testing.T) *Server {
	t.Helper()
	db := openTestDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_b12','WS','ws-b12')`)
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('user_b12','b12@example.com')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_b12','ws_b12','C','crew-b12')`)
	mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role) VALUES ('agent_b12','crew_b12','ws_b12','A','agent-b12','MEMBER')`)
	// A second agent so a test that needs two DISTINCT sessions on the same
	// mission (issue_agent_sessions is UNIQUE(mission_id, agent_id)) has
	// somewhere to put the second one without inventing a second mission.
	mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role) VALUES ('agent_b12_2','crew_b12','ws_b12','B','agent-b12-2','MEMBER')`)
	mustExec(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES ('mission_b12','ws_b12','crew_b12','agent_b12','trace-b12','issue','BACKLOG','issue',datetime('now'),datetime('now'))`)
	mustExec(t, db, `INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat_b12','agent_b12','ws_b12')`)
	return &Server{db: db, logger: logging.New("error", "json", nil)}
}

func seedMentionedEvent(t *testing.T, s *Server, id, createdAt string) {
	t.Helper()
	mustExec(t, s.db, `INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, created_at)
		VALUES (?, 'mission_b12', 'user', 'user_b12', 'mentioned', ?)`, id, createdAt)
}

func seedDelivery(t *testing.T, s *Server, id, eventID, state, createdAt string, claimedByRunID *string) {
	t.Helper()
	var runVal, eventVal any
	if claimedByRunID != nil {
		runVal = *claimedByRunID
	}
	if eventID != "" {
		eventVal = eventID
	}
	mustExec(t, s.db, `INSERT INTO mission_comment_mentions
		(id, workspace_id, mission_id, agent_id, event_id, state, claimed_by_run_id, created_at)
		VALUES (?, 'ws_b12', 'mission_b12', 'agent_b12', ?, ?, ?, ?)`,
		id, eventVal, state, runVal, createdAt)
}

// seedSession inserts one issue_agent_sessions row — assignments.session_id
// is a real FK to this table (20260904095703_assignments_session_id.sql),
// so any sessionID a test passes to seedAssignment must exist here first.
// agentID defaults to "agent_b12" when empty; pass "agent_b12_2" for a
// second session on the same mission (UNIQUE(mission_id, agent_id)).
func seedSession(t *testing.T, s *Server, id, agentID string) {
	t.Helper()
	if agentID == "" {
		agentID = "agent_b12"
	}
	mustExec(t, s.db, `INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id) VALUES (?, 'ws_b12', 'mission_b12', ?)`, id, agentID)
}

func seedAssignment(t *testing.T, s *Server, id, status, createdAt, sessionID string, outcome *string, contextPackTokens *int, contextPackCompaction *string) {
	t.Helper()
	var sessionVal, outcomeVal, tokensVal, compactionVal any
	if sessionID != "" {
		sessionVal = sessionID
	}
	if outcome != nil {
		outcomeVal = *outcome
	}
	if contextPackTokens != nil {
		tokensVal = *contextPackTokens
	}
	if contextPackCompaction != nil {
		compactionVal = *contextPackCompaction
	}
	mustExec(t, s.db, `INSERT INTO assignments
		(id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at, session_id, outcome, context_pack_tokens, context_pack_compaction)
		VALUES (?, 'ws_b12', 'chat_b12', 'agent_b12', 'agent_b12', 'do it', ?, ?, ?, ?, ?, ?)`,
		id, status, createdAt, sessionVal, outcomeVal, tokensVal, compactionVal)
}

// ── delivery: comment-persisted → ack-visible latency ────────────────────

func TestCollectDeliveryAckLatencyMetrics_RealWritePath(t *testing.T) {
	s := b12Fixture(t)
	seedMentionedEvent(t, s, "evt_1", "2026-01-01T00:00:00Z")
	seedDelivery(t, s, "dlv_1", "evt_1", "consumed", "2026-01-01T00:00:01Z", nil) // 1s latency
	seedMentionedEvent(t, s, "evt_2", "2026-01-01T00:00:10Z")
	seedDelivery(t, s, "dlv_2", "evt_2", "consumed", "2026-01-01T00:00:12Z", nil) // 2s latency

	var b strings.Builder
	s.collectDeliveryAckLatencyMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_delivery_ack_latency_seconds{hostname="h",quantile="0.5"} 1`) {
		t.Errorf("missing p50=1; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_delivery_ack_latency_seconds{hostname="h",quantile="0.95"} 2`) {
		t.Errorf("missing p95=2; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_delivery_ack_latency_sample_count{hostname="h"} 2`) {
		t.Errorf("missing sample_count=2; output:\n%s", out)
	}
}

// TestCollectDeliveryAckLatencyMetrics_NoDataIsAbsentNotZero pins F39's
// honesty rule: an empty store must not claim a 0-second ack latency —
// the quantile series must be entirely absent, only the (real, legitimately
// zero) sample count is emitted.
func TestCollectDeliveryAckLatencyMetrics_NoDataIsAbsentNotZero(t *testing.T) {
	s := b12Fixture(t)

	var b strings.Builder
	s.collectDeliveryAckLatencyMetrics(context.Background(), &b, "h")
	out := b.String()

	if strings.Contains(out, `crewshipd_delivery_ack_latency_seconds{`) {
		t.Errorf("quantile series must be absent with zero samples; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_delivery_ack_latency_sample_count{hostname="h"} 0`) {
		t.Errorf("sample_count must be a real, explicit 0; output:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE crewshipd_delivery_ack_latency_seconds gauge") {
		t.Error("family header must still be declared so absent() alerts have a stable series name")
	}
}

// ── delivery: delivery → run-claim latency ────────────────────────────────

func TestCollectDeliveryClaimLatencyMetrics_RealWritePath(t *testing.T) {
	s := b12Fixture(t)
	seedAssignment(t, s, "run_1", "RUNNING", "2026-01-01T00:00:05Z", "", nil, nil, nil)
	seedAssignment(t, s, "run_2", "RUNNING", "2026-01-01T00:00:19Z", "", nil, nil, nil)
	run1, run2 := "run_1", "run_2"
	seedDelivery(t, s, "dlv_1", "", "claimed", "2026-01-01T00:00:00Z", &run1) // 5s
	seedDelivery(t, s, "dlv_2", "", "claimed", "2026-01-01T00:00:10Z", &run2) // 9s

	var b strings.Builder
	s.collectDeliveryClaimLatencyMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_delivery_claim_latency_seconds{hostname="h",quantile="0.5"} 5`) {
		t.Errorf("missing p50=5; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_delivery_claim_latency_seconds{hostname="h",quantile="0.95"} 9`) {
		t.Errorf("missing p95=9; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_delivery_claim_latency_sample_count{hostname="h"} 2`) {
		t.Errorf("missing sample_count=2; output:\n%s", out)
	}
}

// ── delivery: lost deliveries ──────────────────────────────────────────────

func TestCollectLostDeliveriesMetric_PendingOlderThanThreshold(t *testing.T) {
	s := b12Fixture(t)
	// Old enough to count as lost.
	seedDelivery(t, s, "dlv_old", "", "pending", "2020-01-01T00:00:00Z", nil)
	// Recent: must not count.
	mustExec(t, s.db, `INSERT INTO mission_comment_mentions
		(id, workspace_id, mission_id, agent_id, state, created_at)
		VALUES ('dlv_new','ws_b12','mission_b12','agent_b12','pending', strftime('%Y-%m-%dT%H:%M:%SZ','now'))`)
	// Consumed and old: must not count, only 'pending' rows are "lost".
	seedDelivery(t, s, "dlv_done", "", "consumed", "2020-01-01T00:00:00Z", nil)

	var b strings.Builder
	s.collectLostDeliveriesMetric(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_deliveries_lost{hostname="h"} 1`) {
		t.Errorf("expected exactly 1 lost delivery; output:\n%s", out)
	}
}

// ── duplication: concurrent active runs per session ───────────────────────

func TestCollectDuplicateRunMetrics_CountsConcurrentActiveRunsPerSession(t *testing.T) {
	s := b12Fixture(t)
	seedSession(t, s, "sess_dup", "agent_b12")
	seedSession(t, s, "sess_solo", "agent_b12_2")
	// idx_assignments_one_active_per_session (B3, invariant I2) makes the
	// very row pair this test seeds impossible through a live INSERT — that
	// is the whole point of the index, and exactly why this metric is a
	// canary rather than a routine count. Drop it for this test only, so
	// the collector's counting logic can be proven against the violation it
	// exists to detect if the index itself ever regressed or a legacy
	// pre-B3 database still had unindexed duplicates.
	mustExec(t, s.db, `DROP INDEX idx_assignments_one_active_per_session`)
	// Two non-terminal runs sharing one session: the I2 violation this
	// metric exists to canary.
	seedAssignment(t, s, "dup_1", "RUNNING", "2026-01-01T00:00:00Z", "sess_dup", nil, nil, nil)
	seedAssignment(t, s, "dup_2", "PENDING", "2026-01-01T00:00:01Z", "sess_dup", nil, nil, nil)
	// A different session with only one active run: not a duplicate.
	seedAssignment(t, s, "solo_1", "RUNNING", "2026-01-01T00:00:00Z", "sess_solo", nil, nil, nil)

	var b strings.Builder
	s.collectDuplicateRunMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_duplicate_active_runs{hostname="h"} 1`) {
		t.Errorf("expected exactly 1 session with duplicate active runs; output:\n%s", out)
	}
}

func TestCollectDuplicateRunMetrics_ZeroWhenNoneOverlap(t *testing.T) {
	s := b12Fixture(t)
	seedSession(t, s, "sess_solo", "agent_b12")
	seedAssignment(t, s, "solo_1", "RUNNING", "2026-01-01T00:00:00Z", "sess_solo", nil, nil, nil)

	var b strings.Builder
	s.collectDuplicateRunMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_duplicate_active_runs{hostname="h"} 0`) {
		t.Errorf("expected 0 (a real, computed zero); output:\n%s", out)
	}
}

// ── continuation: context pack size and compaction ─────────────────────────

func TestCollectContextPackMetrics_TokenPercentilesAndCompactionCounts(t *testing.T) {
	s := b12Fixture(t)
	seedSession(t, s, "sess_a", "agent_b12")
	fit, summarized, truncated := "fit", "summarized", "truncated"
	tok100, tok200, tok300 := 100, 200, 300
	seedAssignment(t, s, "cp_1", "COMPLETED", "2026-01-01T00:00:00Z", "sess_a", nil, &tok100, &fit)
	seedAssignment(t, s, "cp_2", "COMPLETED", "2026-01-01T00:00:01Z", "sess_a", nil, &tok200, &summarized)
	seedAssignment(t, s, "cp_3", "COMPLETED", "2026-01-01T00:00:02Z", "sess_a", nil, &tok300, &truncated)
	// No session, no pack: must not pollute the percentile or the counts.
	seedAssignment(t, s, "cp_none", "COMPLETED", "2026-01-01T00:00:03Z", "", nil, nil, nil)

	var b strings.Builder
	s.collectContextPackMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_context_pack_tokens{hostname="h",quantile="0.5"} 200`) {
		t.Errorf("missing token p50=200; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_context_pack_tokens{hostname="h",quantile="0.95"} 300`) {
		t.Errorf("missing token p95=300; output:\n%s", out)
	}
	for compaction, want := range map[string]int{"fit": 1, "summarized": 1, "truncated": 1, "other": 0} {
		if !strings.Contains(out, fmt.Sprintf(`crewshipd_context_pack_compaction{compaction=%q,hostname="h"} %d`, compaction, want)) {
			t.Errorf("missing compaction=%s count=%d; output:\n%s", compaction, want, out)
		}
	}
}

// ── continuation: checkpoint compliance ─────────────────────────────────────

func TestCollectCheckpointComplianceMetrics_CountsParsedCheckpointsAgainstFinishedRuns(t *testing.T) {
	s := b12Fixture(t)
	mustExec(t, s.db, `INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id) VALUES ('sess_cp','ws_b12','mission_b12','agent_b12')`)
	seedAssignment(t, s, "run_done_1", "COMPLETED", "2026-01-01T00:00:00Z", "sess_cp", nil, nil, nil)
	seedAssignment(t, s, "run_done_2", "FAILED", "2026-01-01T00:00:01Z", "sess_cp", nil, nil, nil)
	seedAssignment(t, s, "run_running", "RUNNING", "2026-01-01T00:00:02Z", "sess_cp", nil, nil, nil) // not finished, excluded
	// run_done_1 wrote a well-formed checkpoint.
	mustExec(t, s.db, `INSERT INTO agent_session_checkpoints (id, workspace_id, session_id, run_id, checkpoint_json)
		VALUES ('ck_1','ws_b12','sess_cp','run_done_1','{"parsed":true}')`)
	// run_done_2's checkpoint failed to parse — must not count as compliant.
	mustExec(t, s.db, `INSERT INTO agent_session_checkpoints (id, workspace_id, session_id, run_id, checkpoint_json)
		VALUES ('ck_2','ws_b12','sess_cp','run_done_2','{"parsed":false}')`)

	var b strings.Builder
	s.collectCheckpointComplianceMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_session_runs_finished_total{hostname="h"} 2`) {
		t.Errorf("expected 2 finished session runs; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_session_runs_checkpointed_total{hostname="h"} 1`) {
		t.Errorf("expected 1 compliant (Parsed=true) checkpoint; output:\n%s", out)
	}
}

// ── comprehension: outcome routing ──────────────────────────────────────────

func TestCollectOutcomeRoutingMetrics_ClosedSetAndViolationCanary(t *testing.T) {
	s := b12Fixture(t)
	noChange, needsHuman := "NO_CHANGE", "NEEDS_HUMAN"
	seedAssignment(t, s, "oc_1", "COMPLETED", "2026-01-01T00:00:00Z", "", &noChange, nil, nil)
	seedAssignment(t, s, "oc_2", "COMPLETED", "2026-01-01T00:00:01Z", "", &needsHuman, nil, nil)

	// The honest inbox item for oc_2 (NEEDS_HUMAN): not a violation.
	mustExec(t, s.db, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('ibx_ok','ws_b12','run_needs_human','oc_2','Needs a human')`)
	// A violation: oc_1 resolved NO_CHANGE but somehow also raised a
	// run_needs_human item — §9.6's routing table says this must never
	// happen; the metric's job is to notice if it ever does.
	mustExec(t, s.db, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('ibx_bad','ws_b12','run_needs_human','oc_1','Should not exist')`)

	var b strings.Builder
	s.collectOutcomeRoutingMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_assignment_outcomes{hostname="h",outcome="no_change"} 1`) {
		t.Errorf("missing outcome=no_change count; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_assignment_outcomes{hostname="h",outcome="needs_human"} 1`) {
		t.Errorf("missing outcome=needs_human count; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_outcome_routing_violations{hostname="h"} 1`) {
		t.Errorf("expected exactly 1 routing violation; output:\n%s", out)
	}
}
