package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/policy"
)

// The manual-run surface for the four Keeper Reviews evaluators (issue #1555).
//
// Until this route existed the evaluators could only be reached by the
// scheduler or by a sidecar holding an internal token, which meant the
// behaviour watchdog had never been exercised live: it needs a tool call to
// fire and there was no way to stage one on demand. These tests drive the
// operator path — admin auth, workspace from the session, subject assembled
// server-side — and assert the run lands in the same audit trail a scheduled
// sweep writes to.

// newReviewTestHandler builds the real admin handler over the real Phase 2
// handler, the real evaluators and a real (migrated) DB. Only the LLM is
// faked — everything from the route down is production code, because the
// thing worth pinning is that a manual run records the same row a scheduled
// one does.
func newReviewTestHandler(t *testing.T) (*AdminKeeperReviewHandler, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)

	seed := []string{
		`INSERT INTO users (id, email, full_name) VALUES ('admin-1', 'admin@example.com', 'Admin')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS One', 'ws-one')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws2', 'WS Two', 'ws-two')`,
		`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode)
		   VALUES ('cr1', 'ws1', 'Ops', 'ops', 'guided', 'warn')`,
		`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode)
		   VALUES ('cr2', 'ws2', 'Other', 'other', 'guided', 'warn')`,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'cr1', 'ws1', 'Worker', 'worker')`,
		`INSERT INTO skills (id, name, slug, display_name, description, lifecycle_state, last_used_at)
		   VALUES ('sk_fresh', 'fresh', 'fresh', 'Fresh', 'used yesterday', 'active', '2026-07-29T00:00:00Z')`,
		`INSERT INTO skills (id, name, slug, display_name, description, lifecycle_state, last_used_at)
		   VALUES ('sk_stale', 'stale', 'stale', 'Stale', 'nobody has run this in months', 'active', '2026-01-01T00:00:00Z')`,
		`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as1', 'a1', 'sk_fresh', 1)`,
		`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as2', 'a1', 'sk_stale', 1)`,
		// A real failure for the lesson extractor to learn from. Without one
		// there is nothing honest for a bare negative-learning run to reason
		// about — see TestAdminKeeperReview_NegativeLearningWithNoFailureSaysSo.
		`INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, ts, entry_type, actor_type, summary)
		   VALUES ('je1', 'ws1', 'cr1', 'a1', '2026-07-30T10:00:00Z', 'run.failed', 'agent',
		           'deploy step exited 127: command not found')`,
	}
	for _, q := range seed {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	log := newTestLogger()
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"nothing alarming","risk":2}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", log)
	kp2 := NewKeeperPhase2Handler(db, "tok", policy.NewResolver(db),
		gatekeeper.NewSkillReviewEvaluator(gk, log),
		gatekeeper.NewBehaviorEvaluator(gk, log),
		gatekeeper.NewMemoryHealthEvaluator(gk, log),
		gatekeeper.NewNegativeLearningEvaluator(gk, log),
		log)

	return NewAdminKeeperReviewHandler(db, kp2, log), db
}

// reviewReq builds an admin-authenticated run request for one slot, in the
// shape RequireWorkspace + authedMut leave it: workspace and role on the
// context, slot on the path.
func reviewReq(slot, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest("POST", "/api/v1/admin/keeper/review/"+slot+"/run", nil)
	} else {
		r = httptest.NewRequest("POST", "/api/v1/admin/keeper/review/"+slot+"/run", strings.NewReader(body))
	}
	r.SetPathValue("slot", slot)
	ctx := context.WithValue(r.Context(), ctxRole, "OWNER")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "admin-1"})
	ctx = context.WithValue(ctx, ctxWorkspaceID, "ws1")
	return r.WithContext(ctx)
}

func decodeReviewRun(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return out
}

// A wrong slot is the most likely operator mistake (there are four names and
// they are not guessable), so the refusal has to name the alternatives rather
// than just say no.
func TestAdminKeeperReview_UnknownSlotNamesTheValidOnes(t *testing.T) {
	h, _ := newReviewTestHandler(t)

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("skills", ""))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"skill-review", "behavior", "memory-health", "negative-learning"} {
		if !strings.Contains(body, want) {
			t.Errorf("400 body does not name the %q slot: %s", want, body)
		}
	}
}

// Every slot runs end-to-end from the admin path with no body at all — the
// server assembles the subject — and each records its own keeper_requests row.
// This is the audit trail a scheduled sweep writes to, which is the point: a
// manual run is not a second-class event.
func TestAdminKeeperReview_EachSlotRunsAndRecordsItsRequestType(t *testing.T) {
	for _, tc := range []struct{ slot, wantType string }{
		{"skill-review", "skill_review"},
		{"behavior", "behavior"},
		{"memory-health", "memory_health"},
		{"negative-learning", "negative_learning"},
	} {
		t.Run(tc.slot, func(t *testing.T) {
			h, db := newReviewTestHandler(t)

			rr := httptest.NewRecorder()
			h.Run(rr, reviewReq(tc.slot, ""))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			out := decodeReviewRun(t, rr)
			reqID, _ := out["request_id"].(string)
			if reqID == "" {
				t.Fatalf("no request_id in response: %s", rr.Body.String())
			}
			if out["decision"] != "ALLOW" {
				t.Errorf("decision = %v, want ALLOW", out["decision"])
			}

			var gotType string
			if err := db.QueryRow(`SELECT request_type FROM keeper_requests WHERE id = ?`, reqID).Scan(&gotType); err != nil {
				t.Fatalf("read back %s: %v", reqID, err)
			}
			if gotType != tc.wantType {
				t.Errorf("request_type = %q, want %q", gotType, tc.wantType)
			}
		})
	}
}

// "Check my agents' skills now" with nothing typed picks the stalest skill one
// of this workspace's agents actually has — a real subject, not a placeholder,
// so the decision that lands in the audit trail is about something real.
func TestAdminKeeperReview_SkillReviewDefaultsToTheStalestAssignedSkill(t *testing.T) {
	h, db := newReviewTestHandler(t)

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("skill-review", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	reqID, _ := decodeReviewRun(t, rr)["request_id"].(string)

	var intent string
	if err := db.QueryRow(`SELECT intent FROM keeper_requests WHERE id = ?`, reqID).Scan(&intent); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(intent, "stale") {
		t.Errorf("intent = %q, want the stalest skill (stale), not the fresh one", intent)
	}
}

// The recurring trap on this codebase is an admin route that forgets it is
// workspace-scoped. Without a workspace there is no tenant to bill, no policy
// to resolve and no crew to pick — so it must refuse, not guess.
func TestAdminKeeperReview_RefusesWithoutWorkspaceContext(t *testing.T) {
	h, _ := newReviewTestHandler(t)

	r := httptest.NewRequest("POST", "/api/v1/admin/keeper/review/behavior/run", nil)
	r.SetPathValue("slot", "behavior")
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, "OWNER"))

	rr := httptest.NewRecorder()
	h.Run(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(strings.ToLower(rr.Body.String()), "workspace") {
		t.Errorf("400 should say what is missing; got %s", rr.Body.String())
	}
}

// Running an evaluator is a mutation that spends money and writes to the audit
// trail, so it takes the same OWNER/ADMIN floor the rest of the Keeper admin
// surface does — enforced in the handler as well as at registration.
func TestAdminKeeperReview_RequiresManageRole(t *testing.T) {
	h, _ := newReviewTestHandler(t)

	r := reviewReq("behavior", "")
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Run(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// A crew id from another tenant is the cross-tenant probe this route would
// otherwise be wide open to: the internal endpoints are protected by a
// workspace-bound token, and an admin session has no such binding.
func TestAdminKeeperReview_RefusesACrewFromAnotherWorkspace(t *testing.T) {
	h, _ := newReviewTestHandler(t)

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("behavior", `{"crew_id":"cr2","tool_name":"bash"}`))
	if rr.Code != http.StatusForbidden && rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400/403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cr2") && !strings.Contains(strings.ToLower(rr.Body.String()), "workspace") {
		t.Errorf("refusal should name the offending crew or the workspace; got %s", rr.Body.String())
	}
}

// The workspace comes from the session, never from the body. A body that names
// a different one is a mistake worth surfacing rather than silently rewriting —
// the operator thinks they are running against ws2.
func TestAdminKeeperReview_RefusesABodyWorkspaceThatIsNotTheSessionsOne(t *testing.T) {
	h, _ := newReviewTestHandler(t)

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("behavior", `{"workspace_id":"ws2","tool_name":"bash"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// A tool call the operator names is evaluated as given — this is the staged
// tool call the watchdog has never had a way to receive.
func TestAdminKeeperReview_BehaviorEvaluatesTheNamedToolCall(t *testing.T) {
	h, db := newReviewTestHandler(t)

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("behavior", `{"crew_id":"cr1","agent_id":"a1","tool_name":"bash","tool_args_snippet":"rm -rf /"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	reqID, _ := decodeReviewRun(t, rr)["request_id"].(string)

	var intent, agentID string
	if err := db.QueryRow(
		`SELECT intent, COALESCE(requesting_agent_id, '') FROM keeper_requests WHERE id = ?`, reqID).
		Scan(&intent, &agentID); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(intent, "bash") {
		t.Errorf("intent = %q, want the evaluated tool name in it", intent)
	}
	if agentID != "a1" {
		t.Errorf("requesting_agent_id = %q, want a1", agentID)
	}
}

// The lesson extractor is the one evaluator with nothing to fall back on: its
// trigger set is closed and its ALLOW path writes into an agent's memory. So a
// workspace that has not failed gets told that, rather than a fabricated
// failure that would put fiction in the audit trail (and possibly in a lesson).
func TestAdminKeeperReview_NegativeLearningWithNoFailureSaysSo(t *testing.T) {
	h, db := newReviewTestHandler(t)
	if _, err := db.Exec(`DELETE FROM journal_entries`); err != nil {
		t.Fatalf("clear journal: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("negative-learning", ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	// And it says what to pass instead, naming the triggers the evaluator
	// accepts — the set is closed and not guessable.
	if !strings.Contains(rr.Body.String(), "run_failed") {
		t.Errorf("400 should name the accepted triggers; got %s", rr.Body.String())
	}
}

// Learning from the last real failure means learning from THAT failure — the
// text, and the agent it happened to.
func TestAdminKeeperReview_NegativeLearningUsesTheLastRealFailure(t *testing.T) {
	h, db := newReviewTestHandler(t)

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("negative-learning", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	reqID, _ := decodeReviewRun(t, rr)["request_id"].(string)

	var prompt, agentID string
	if err := db.QueryRow(
		`SELECT COALESCE(ollama_prompt, ''), COALESCE(requesting_agent_id, '')
		   FROM keeper_requests WHERE id = ?`, reqID).Scan(&prompt, &agentID); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(prompt, "exited 127") {
		t.Errorf("prompt does not carry the recorded failure: %q", prompt)
	}
	if agentID != "a1" {
		t.Errorf("requesting_agent_id = %q, want the agent that failed (a1)", agentID)
	}
}

// An evaluator that is not configured (no API key, partial rollout) must say so
// rather than 404 — the operator's next question is "why", and "not configured"
// answers it.
func TestAdminKeeperReview_UnconfiguredEvaluatorSaysSo(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS One', 'ws-one')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	log := newTestLogger()
	kp2 := NewKeeperPhase2Handler(db, "tok", policy.NewResolver(db), nil, nil, nil, nil, log)
	h := NewAdminKeeperReviewHandler(db, kp2, log)

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("memory-health", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

// Every press of this button spends one evaluator call on a paid model, and
// until #1575 nothing bounded how often it could be pressed. The neighbouring
// aux probe has been metered since it shipped — it delegates to the judge
// handler's instance-wide bucket precisely so a status surface cannot be
// refreshed into an invoice — while this route, which spends strictly MORE per
// press (a full evaluation against real subject material, not a reachability
// dial), inherited only the general authed-mutation limiter. roleManage bounds
// who, not how often.
//
// The burst is deliberately one full pass over the four evaluators, because
// "run everything now" is the operator flow #1555 shipped for. What must not be
// possible is a fifth run in the same instant — a held-down button, a retry
// loop, or a routine wired to `crewship keeper review run`.
func TestAdminKeeperReview_RunsAreRateLimited(t *testing.T) {
	h, _ := newReviewTestHandler(t)

	for i, slot := range keeperReviewSlots {
		rr := httptest.NewRecorder()
		h.Run(rr, reviewReq(slot, ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("run %d (%s) got %d, want 200 — one full pass over the evaluators must fit in the burst; body=%s",
				i+1, slot, rr.Code, rr.Body.String())
		}
	}

	rr := httptest.NewRecorder()
	h.Run(rr, reviewReq("behavior", ""))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("run %d got %d, want 429 — the button can outrun any cadence",
			len(keeperReviewSlots)+1, rr.Code)
	}

	// A bare 429 tells an operator their tool is broken. The refusal has to say
	// what tripped and when the next attempt will be taken, the way the judge
	// probe's does.
	body := rr.Body.String()
	if !strings.Contains(body, "rate limited") {
		t.Errorf("the refusal does not explain itself: %s", body)
	}
	if !strings.Contains(body, "Try again in") {
		t.Errorf("the refusal does not say when to retry: %s", body)
	}
	if got := rr.Header().Get("Retry-After"); got == "" {
		t.Errorf("429 carries no Retry-After header, so a script cannot back off intelligently")
	}
}

// A token buys a model call, so a request that never reaches a model must not
// spend one. Otherwise the cheapest way to lock an operator out of their own
// evaluators is a loop of typos.
func TestAdminKeeperReview_RefusedRunsDoNotSpendTheBudget(t *testing.T) {
	h, _ := newReviewTestHandler(t)

	for i := 0; i < 20; i++ {
		rr := httptest.NewRecorder()
		h.Run(rr, reviewReq("skills", "")) // not a slot: 400, no model called
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("rejected run %d got %d, want 400", i+1, rr.Code)
		}
	}

	for i, slot := range keeperReviewSlots {
		rr := httptest.NewRecorder()
		h.Run(rr, reviewReq(slot, ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("run %d (%s) got %d after 20 rejected requests, want 200 — "+
				"requests that never reached a model spent the run budget; body=%s",
				i+1, slot, rr.Code, rr.Body.String())
		}
	}
}
