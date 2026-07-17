package api

// Issue #1186 (remainder): the crew binding on internal crwv1 tokens was
// enforced only where a handler consulted it. requireInternal 403s an
// explicit ?crew_id that disagrees with the binding, and
// assertBoundCrewWorkspaceDB covers the body-carried crew fields of its 13
// existing call sites — but internal routes that read crew_id OUTSIDE the
// shared helper still ran workspace-wide for a crew-bound token:
//
//   - optional ?crew_id read filters (crew-connections, issues list,
//     mission get/start): omitted crew_id → workspace-wide result;
//   - body-carried crew_id on handlers that never called the helper
//     (mission create, issue create, port-expose, report-confidence,
//     keeper phase-2 F4.x): sibling-crew attribution / policy borrowing.
//
// The fix routes the read filters through effectiveCrewFilter (bound crew
// constrains, sibling data becomes invisible) and the body writes through
// assertBoundCrewWorkspaceDB (own crew exact-match, omitted field filled
// in). These tests pin both halves per endpoint. Workspace-bound (wsv1)
// and master callers keep the old optional-filter semantics — pinned by
// the unbound cases below and the existing suites.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// crewBoundCtx1186 builds a request context as requireInternal would for a
// crwv1 token bound to (wsID, crewID).
func crewBoundCtx1186(wsID, crewID string) context.Context {
	ctx := context.WithValue(context.Background(), ctxInternalTokenWS, wsID)
	return context.WithValue(ctx, ctxInternalTokenCrew, crewID)
}

// TestEffectiveCrewFilter pins the helper contract: a crew-bound caller's
// binding is authoritative (constrains even when the query is omitted);
// unbound callers keep the query-driven optional filter.
func TestEffectiveCrewFilter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		boundCrew string // "" = wsv1/master caller, no crew binding
		query     string
		want      string
	}{
		{"crew_bound_omitted_query_constrains", "crew_a", "", "crew_a"},
		{"crew_bound_matching_query", "crew_a", "?crew_id=crew_a", "crew_a"},
		// requireInternal 403s a disagreeing ?crew_id before any handler
		// runs; if the chain were ever reordered, the binding must still win.
		{"crew_bound_context_wins_over_query", "crew_a", "?crew_id=crew_b", "crew_a"},
		{"unbound_query_passthrough", "", "?crew_id=crew_b", "crew_b"},
		{"unbound_omitted_stays_workspace_wide", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x"+tc.query, nil)
			if tc.boundCrew != "" {
				req = req.WithContext(crewBoundCtx1186("ws_a", tc.boundCrew))
			}
			if got := effectiveCrewFilter(req); got != tc.want {
				t.Errorf("effectiveCrewFilter = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCrewBoundToken_CrewConnections_OmittedCrew_ConstrainedToOwnCrew pins
// the ListCrewConnections decision: a crew-bound token with NO ?crew_id
// must see only connections involving its own crew, never the
// workspace-wide topology.
func TestCrewBoundToken_CrewConnections_OmittedCrew_ConstrainedToOwnCrew(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewA := seedCrewRow(t, db, "c1186-conn-a", wsID, "Alpha", "alpha-1186c")
	crewB := seedCrewRow(t, db, "c1186-conn-b", wsID, "Bravo", "bravo-1186c")
	crewC := seedCrewRow(t, db, "c1186-conn-c", wsID, "Charlie", "charlie-1186c")
	for _, c := range []struct{ id, from, to string }{
		{"conn-ab", crewA, crewB},
		{"conn-bc", crewB, crewC}, // does NOT involve crew A — must stay invisible
	} {
		if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
			VALUES (?, ?, ?, ?, 'bidirectional', 'active')`, c.id, wsID, c.from, c.to); err != nil {
			t.Fatalf("seed connection %s: %v", c.id, err)
		}
	}
	h := NewInternalHandler(db, "tok", testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crew-connections?workspace_id="+wsID, nil).
		WithContext(crewBoundCtx1186(wsID, crewA))
	rr := httptest.NewRecorder()
	h.ListCrewConnections(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var conns []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &conns); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rr.Body.String())
	}
	if len(conns) != 1 || conns[0].ID != "conn-ab" {
		t.Errorf("crew-bound listing = %+v, want exactly [conn-ab] (omitted crew_id must constrain to the bound crew, #1186)", conns)
	}
}

// TestCrewBoundToken_IssuesList_OmittedCrew_ConstrainedToOwnCrew pins the
// internal issues listing: crew-bound token, no ?crew_id → only the bound
// crew's issues, not the workspace backlog.
func TestCrewBoundToken_IssuesList_OmittedCrew_ConstrainedToOwnCrew(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	mh := NewInternalMissionHandler(h.db, nil, nil, testLogger())
	crewB, _ := seedMissionCrewB(t, mh, wsID, leadA)
	var leadB string
	if err := h.db.QueryRow(`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD'`, crewB).Scan(&leadB); err != nil {
		t.Fatalf("find crew B lead: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range []struct{ id, crew, lead, ident string }{
		{"iss-1186-own", crewA, leadA, "OWN-1"},
		{"iss-1186-sib", crewB, leadB, "SIB-1"},
	} {
		if _, err := h.db.Exec(`
			INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, number, identifier, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'iss', 'BACKLOG', 'issue', 1, ?, ?, ?)`,
			m.id, wsID, m.crew, m.lead, "tr-"+m.id, m.ident, now, now); err != nil {
			t.Fatalf("seed issue %s: %v", m.id, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/issues?workspace_id="+wsID, nil).
		WithContext(crewBoundCtx1186(wsID, crewA))
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var issues []issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &issues); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rr.Body.String())
	}
	if len(issues) != 1 || issues[0].ID != "iss-1186-own" {
		got := make([]string, 0, len(issues))
		for _, i := range issues {
			got = append(got, i.ID)
		}
		t.Errorf("crew-bound issue listing = %v, want exactly [iss-1186-own] (omitted crew_id must constrain to the bound crew, #1186)", got)
	}
}

// TestCrewBoundToken_MissionGetStart_OmittedCrew_SiblingInvisible pins the
// mission read/start scoping: a crew-bound token that OMITS ?crew_id must
// not be able to read or start a sibling crew's mission (404, status
// unchanged) — pre-fix the omitted filter widened the lookup to the whole
// workspace.
func TestCrewBoundToken_MissionGetStart_OmittedCrew_SiblingInvisible(t *testing.T) {
	ih, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	_, missionB := seedMissionCrewB(t, h, wsID, leadA)

	t.Run("get_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?workspace_id="+wsID, nil).
			WithContext(crewBoundCtx1186(wsID, crewA))
		req.SetPathValue("missionId", missionB)
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (sibling mission must be invisible without ?crew_id, #1186); body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("start_404_status_unchanged", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/?workspace_id="+wsID, nil).
			WithContext(crewBoundCtx1186(wsID, crewA))
		req.SetPathValue("missionId", missionB)
		rr := httptest.NewRecorder()
		h.Start(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
		}
		var status string
		if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionB).Scan(&status); err != nil {
			t.Fatalf("read mission status: %v", err)
		}
		if status != "PLANNING" {
			t.Errorf("mission status = %q, want PLANNING (crew-bound token must not start a sibling crew's mission)", status)
		}
	})
}

// TestCrewBoundToken_MissionCreate_SiblingCrew403 pins the mission-create
// write: the body-carried crew_id of a crew-bound caller must exact-match
// the binding — naming a sibling crew (even with that crew's real lead
// agent) is refused before any row lands.
func TestCrewBoundToken_MissionCreate_SiblingCrew403(t *testing.T) {
	ih, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	crewB, _ := seedMissionCrewB(t, h, wsID, leadA)
	var leadB string
	if err := h.db.QueryRow(`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD'`, crewB).Scan(&leadB); err != nil {
		t.Fatalf("find crew B lead: %v", err)
	}

	body := `{"title":"planted","lead_agent_id":"` + leadB + `","crew_id":"` + crewB + `","workspace_id":"` + wsID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/missions", strings.NewReader(body)).
		WithContext(crewBoundCtx1186(wsID, crewA))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (crew-bound token must not create a sibling crew's mission, #1186); body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE title = 'planted'`).Scan(&n); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	if n != 0 {
		t.Errorf("mission row landed despite 403")
	}
}

// TestCrewBoundToken_IssueCreate_SiblingCrew403 pins the issue-create
// write. Note the author-agent boundary check only runs when an
// author_agent_id is supplied — omitting it was the bypass, so the test
// omits it too.
func TestCrewBoundToken_IssueCreate_SiblingCrew403(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	mh := NewInternalMissionHandler(h.db, nil, nil, testLogger())
	crewB, _ := seedMissionCrewB(t, mh, wsID, leadA)

	body := `{"workspace_id":"` + wsID + `","crew_id":"` + crewB + `","title":"planted issue"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", strings.NewReader(body)).
		WithContext(crewBoundCtx1186(wsID, crewA))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (crew-bound token must not create a sibling crew's issue, #1186); body=%s", rr.Code, rr.Body.String())
	}
}

// TestCrewBoundToken_ReportConfidence_SiblingCrew403 pins the
// report-confidence write: a crew-bound token naming a sibling crew is
// refused before the task lookup runs.
func TestCrewBoundToken_ReportConfidence_SiblingCrew403(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewA := seedCrewRow(t, db, "c1186-conf-a", wsID, "Alpha", "alpha-1186f")
	crewB := seedCrewRow(t, db, "c1186-conf-b", wsID, "Bravo", "bravo-1186f")
	agentB := seedAgentRow(t, db, "a1186-conf-b", wsID, crewB, "Bee", "bee-1186f", "AGENT")
	h := NewQueryHandler(db, nil, nil, "tok", testLogger())

	body := `{"agent_id":"` + agentB + `","crew_id":"` + crewB + `","confidence":0.4}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/report-confidence", strings.NewReader(body)).
		WithContext(crewBoundCtx1186(wsID, crewA))
	rr := httptest.NewRecorder()
	h.ReportConfidence(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (crew-bound token must not report confidence for a sibling crew, #1186); body=%s", rr.Code, rr.Body.String())
	}
}

// TestCrewBoundToken_PortExpose_SiblingCrew403 pins the port-expose write:
// a crew-bound token must not expose a sibling crew's container, even when
// the named agent genuinely belongs to that sibling crew (the pre-fix
// boundary check proved agent∈crew, not crew==caller's).
func TestCrewBoundToken_PortExpose_SiblingCrew403(t *testing.T) {
	h, db := covPXHandler(t, AllowAllPolicy{}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	crewA := "crew-1186-px-a"
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id) VALUES (?, 'ws1')`, crewA); err != nil {
		t.Fatalf("seed crew A: %v", err)
	}

	// Body names crew1/agent1 (a real, consistent sibling pair from the
	// rig); the caller's token is bound to crewA.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/port-expose", mustJSON(t, covPXBody())).
		WithContext(crewBoundCtx1186("ws1", crewA))
	rr := httptest.NewRecorder()
	h.RequestExpose(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (crew-bound token must not expose a sibling crew's port, #1186); body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM port_exposures`).Scan(&n); err != nil {
		t.Fatalf("count exposures: %v", err)
	}
	if n != 0 {
		t.Errorf("port_exposures row landed despite 403")
	}
}

// TestCrewBoundToken_KeeperBehavior_SiblingCrew403 pins the F4.2 behavior
// endpoint: the body-carried crew drives policy resolution, so a
// crew-bound caller must not have its tool call judged under a sibling
// crew's (potentially laxer) policy.
func TestCrewBoundToken_KeeperBehavior_SiblingCrew403(t *testing.T) {
	db, pr := kp2DB(t)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode)
		VALUES ('cr1186', 'ws1', 'Own', 'own-1186', 'strict', 'block')`); err != nil {
		t.Fatalf("seed own crew: %v", err)
	}
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, ev, nil, nil, kp2Logger())

	// Caller bound to cr1186; body names sibling cr1 (guided/warn).
	body := behaviorBody{WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", ToolName: "bash"}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/behavior", mustJSON(t, body))
	ctx := context.WithValue(crewBoundCtx1186("ws1", "cr1186"), ctxWorkspaceID, "ws1")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.HandleBehavior(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (crew-bound token must not borrow a sibling crew's policy, #1186); body=%s", rr.Code, rr.Body.String())
	}
}

// TestCrewBoundToken_KeeperSkillReview_OmittedCrew_AttributedToBoundCrew
// pins the injection half on F4.1: an omitted crew_id from a crew-bound
// caller must attribute the keeper_requests audit row to the token's own
// crew instead of landing crew-less.
func TestCrewBoundToken_KeeperSkillReview_OmittedCrew_AttributedToBoundCrew(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"active","risk":2}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, ev, nil, nil, nil, kp2Logger())

	body := skillReviewBody{
		WorkspaceID: "ws1", // CrewID deliberately omitted
		SkillID:     "sk_1186", SkillName: "x", LifecycleState: "active",
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/skill-review", mustJSON(t, body))
	ctx := context.WithValue(crewBoundCtx1186("ws1", "cr1"), ctxWorkspaceID, "ws1")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.HandleSkillReview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var gotCrew string
	if err := db.QueryRow(`SELECT COALESCE(requesting_crew_id,'') FROM keeper_requests WHERE request_type = 'skill_review'`).Scan(&gotCrew); err != nil {
		t.Fatalf("read keeper_requests row: %v", err)
	}
	if gotCrew != "cr1" {
		t.Errorf("keeper_requests.requesting_crew_id = %q, want cr1 (omitted crew_id must be attributed to the token's bound crew, #1186)", gotCrew)
	}
}

// TestCrewBoundToken_CrewMessage_SpoofedSender403 pins the crew-message
// write: from_crew_id is the sender attribution, so a crew-bound token
// naming a sibling crew as sender is refused even when that sibling has a
// valid connection to the target. to_crew_id (the recipient) stays
// legitimately foreign.
func TestCrewBoundToken_CrewMessage_SpoofedSender403(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewA := seedCrewRow(t, db, "c1186-msg-a", wsID, "Alpha", "alpha-1186m")
	crewB := seedCrewRow(t, db, "c1186-msg-b", wsID, "Bravo", "bravo-1186m")
	crewC := seedCrewRow(t, db, "c1186-msg-c", wsID, "Charlie", "charlie-1186m")
	if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('conn-msg-bc', ?, ?, ?, 'bidirectional', 'active')`, wsID, crewB, crewC); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	h := NewCrewMessagingHandler(db, t.TempDir(), testLogger())

	// Caller bound to crew A claims to send AS crew B (which can reach C).
	body := `{"from_crew_id":"` + crewB + `","to_crew_id":"` + crewC + `","workspace_id":"` + wsID + `","content":"spoofed"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/crew-messages", strings.NewReader(body)).
		WithContext(crewBoundCtx1186(wsID, crewA))
	rr := httptest.NewRecorder()
	h.SendMessage(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (crew-bound token must not send as a sibling crew, #1186); body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_messages`).Scan(&n); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if n != 0 {
		t.Errorf("crew_messages row landed despite 403")
	}
}

// TestUnboundToken_OptionalCrewFilters_StayWorkspaceWide pins the
// compatibility half for the read filters: a workspace-bound (wsv1)
// caller omitting ?crew_id keeps the workspace-wide listing.
func TestUnboundToken_OptionalCrewFilters_StayWorkspaceWide(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewA := seedCrewRow(t, db, "c1186-ws-a", wsID, "Alpha", "alpha-1186w")
	crewB := seedCrewRow(t, db, "c1186-ws-b", wsID, "Bravo", "bravo-1186w")
	if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('conn-ws-ab', ?, ?, ?, 'bidirectional', 'active')`, wsID, crewA, crewB); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	h := NewInternalHandler(db, "tok", testLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crew-connections?workspace_id="+wsID, nil).
		WithContext(context.WithValue(context.Background(), ctxInternalTokenWS, wsID))
	rr := httptest.NewRecorder()
	h.ListCrewConnections(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var conns []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &conns); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(conns) != 1 {
		t.Errorf("wsv1 caller listing = %+v, want the workspace-wide result (optional filter semantics unchanged)", conns)
	}
}
