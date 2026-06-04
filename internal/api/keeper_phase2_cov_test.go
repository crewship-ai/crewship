package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/lookout"
	"github.com/crewship-ai/crewship/internal/policy"
)

// covKPReq builds an internal-auth-style POST request for an F4 endpoint
// with the given body and a ctx workspace_id set (mirroring what the
// internalWsCtx middleware would attach in production).
func covKPReq(t *testing.T, body any, ctxWS string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/x", mustJSON(t, body))
	if ctxWS != "" {
		r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, ctxWS))
	}
	return r
}

// TestCovKPInboxBlockingForPolicy exercises every autonomy-level branch
// of inboxBlockingForPolicy: strict/guided → blocking, trusted/full →
// non-blocking. This is the pure helper that was at 0% coverage.
func TestCovKPInboxBlockingForPolicy(t *testing.T) {
	cases := []struct {
		level policy.AutonomyLevel
		want  bool
	}{
		{policy.AutonomyStrict, true},
		{policy.AutonomyGuided, true},
		{policy.AutonomyTrusted, false},
		{policy.AutonomyFull, false},
		{policy.AutonomyLevel("unknown"), false},
	}
	for _, tc := range cases {
		got := inboxBlockingForPolicy(policy.Policy{AutonomyLevel: tc.level})
		if got != tc.want {
			t.Errorf("inboxBlockingForPolicy(%q) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

// TestCovKPBehaviorPriorityForDecision covers all four BehaviorDecision
// branches plus the default.
func TestCovKPBehaviorPriorityForDecision(t *testing.T) {
	cases := []struct {
		d    gatekeeper.BehaviorDecision
		want string
	}{
		{gatekeeper.BehaviorDeny, "high"},
		{gatekeeper.BehaviorEscalate, "medium"},
		{gatekeeper.BehaviorWarn, "low"},
		{gatekeeper.BehaviorAllow, "medium"},
		{gatekeeper.BehaviorDecision("???"), "medium"},
	}
	for _, tc := range cases {
		if got := behaviorPriorityForDecision(tc.d); got != tc.want {
			t.Errorf("behaviorPriorityForDecision(%q) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestCovKPShortPrefix covers every request-type branch and the default.
func TestCovKPShortPrefix(t *testing.T) {
	cases := []struct {
		rt   keeper.RequestType
		want string
	}{
		{keeper.RequestTypeSkillReview, "skr"},
		{keeper.RequestTypeBehavior, "bhv"},
		{keeper.RequestTypeMemoryHealth, "mhc"},
		{keeper.RequestTypeNegativeLearning, "neg"},
		{keeper.RequestType("other"), "kp2"},
	}
	for _, tc := range cases {
		if got := shortPrefix(tc.rt); got != tc.want {
			t.Errorf("shortPrefix(%q) = %q, want %q", tc.rt, got, tc.want)
		}
	}
}

// TestCovKPScopeKeeperRequest covers both the empty-workspace early
// return (ctx unchanged, no scope attached) and the populated path
// (scope attached).
func TestCovKPScopeKeeperRequest(t *testing.T) {
	base := context.Background()

	// Empty workspace → ctx returned unchanged, no scope.
	got := scopeKeeperRequest(base, "", "cr1", "a1")
	if _, ok := lookout.ScopeFromContext(got); ok {
		t.Errorf("empty workspace_id should not attach a scope")
	}

	// Populated → scope present with the expected fields.
	got = scopeKeeperRequest(base, "ws1", "cr1", "a1")
	sc, ok := lookout.ScopeFromContext(got)
	if !ok {
		t.Fatalf("expected a lookout scope to be attached")
	}
	if sc.WorkspaceID != "ws1" || sc.CrewID != "cr1" || sc.AgentID != "a1" {
		t.Errorf("scope = %+v, want ws1/cr1/a1", sc)
	}
}

// TestCovKPResolvePolicySafe covers the nil-resolver and empty-crew
// fallback branches (guided/warn defaults) plus the happy resolve path.
func TestCovKPResolvePolicySafe(t *testing.T) {
	db, pr := kp2DB(t)

	// nil resolver → guided/warn default.
	hNil := NewKeeperPhase2Handler(db, "tok", nil, nil, nil, nil, nil, kp2Logger())
	if p := hNil.resolvePolicySafe(context.Background(), "cr1"); p.AutonomyLevel != policy.AutonomyGuided {
		t.Errorf("nil resolver: autonomy = %q, want guided", p.AutonomyLevel)
	}

	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, nil, kp2Logger())

	// empty crew_id → guided/warn default (no resolve attempted).
	if p := h.resolvePolicySafe(context.Background(), ""); p.AutonomyLevel != policy.AutonomyGuided {
		t.Errorf("empty crew: autonomy = %q, want guided", p.AutonomyLevel)
	}

	// real crew row (seeded guided) → resolves successfully.
	if p := h.resolvePolicySafe(context.Background(), "cr1"); p.AutonomyLevel != policy.AutonomyGuided {
		t.Errorf("resolve cr1: autonomy = %q, want guided", p.AutonomyLevel)
	}
}

// TestCovKPHandlersInvalidJSON drives the readJSON-failure branch (400)
// of all four handlers — they must reach the evaluator-configured guard
// first (so an evaluator is wired) then reject the malformed body.
func TestCovKPHandlersInvalidJSON(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr,
		gatekeeper.NewSkillReviewEvaluator(gk, kp2Logger()),
		gatekeeper.NewBehaviorEvaluator(gk, kp2Logger()),
		gatekeeper.NewMemoryHealthEvaluator(gk, kp2Logger()),
		gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger()),
		kp2Logger())

	handlers := map[string]http.HandlerFunc{
		"skill-review":      h.HandleSkillReview,
		"behavior":          h.HandleBehavior,
		"memory-health":     h.HandleMemoryHealth,
		"negative-learning": h.HandleNegativeLearning,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/"+name,
				bytes.NewBufferString("not json"))
			fn(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s invalid JSON: status = %d, want 400", name, w.Code)
			}
		})
	}
}

// TestCovKPHandlersMissingRequiredFields drives the body-validation
// branch (400) of each handler with an empty (but well-formed) body.
func TestCovKPHandlersMissingRequiredFields(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr,
		gatekeeper.NewSkillReviewEvaluator(gk, kp2Logger()),
		gatekeeper.NewBehaviorEvaluator(gk, kp2Logger()),
		gatekeeper.NewMemoryHealthEvaluator(gk, kp2Logger()),
		gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger()),
		kp2Logger())

	cases := []struct {
		name string
		body any
		fn   http.HandlerFunc
	}{
		{"skill-review", skillReviewBody{}, h.HandleSkillReview},
		{"behavior", behaviorBody{}, h.HandleBehavior},
		{"memory-health", memoryHealthBody{}, h.HandleMemoryHealth},
		{"negative-learning", negativeLearningBody{}, h.HandleNegativeLearning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/"+tc.name, mustJSON(t, tc.body))
			tc.fn(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s missing fields: status = %d, want 400", tc.name, w.Code)
			}
		})
	}
}

// TestCovKPMemoryHealthEscalateBlocking drives HandleMemoryHealth on the
// ESCALATE path. A DENY from the LLM combined with ContradictionCount>0
// forces ESCALATE inside the evaluator, which writes a blocking inbox
// item (crew is guided → blocking=true).
func TestCovKPMemoryHealthEscalateBlocking(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"DENY","reason":"stale memory","risk":6}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewMemoryHealthEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, ev, nil, kp2Logger())

	body := memoryHealthBody{
		WorkspaceID: "ws1", CrewID: "cr1", CrewName: "Ops", AgentName: "system",
		ContradictionCount: 3, // forces ESCALATE override in the evaluator
	}
	w := httptest.NewRecorder()
	r := covKPReq(t, body, "ws1")
	h.HandleMemoryHealth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	// keeper_requests row recorded with request_type='memory_health'.
	var rt string
	if err := db.QueryRow(
		`SELECT request_type FROM keeper_requests WHERE requesting_crew_id='cr1' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&rt); err != nil {
		t.Fatalf("read keeper_requests: %v", err)
	}
	if rt != "memory_health" {
		t.Errorf("request_type = %q, want memory_health", rt)
	}

	// Blocking inbox row written (guided crew + ESCALATE).
	var blocking int
	if err := db.QueryRow(
		`SELECT blocking FROM inbox_items WHERE sender_id='keeper_memory_health' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&blocking); err != nil {
		t.Fatalf("inbox row not found: %v", err)
	}
	if blocking != 1 {
		t.Errorf("inbox blocking = %d, want 1 for guided-crew ESCALATE", blocking)
	}
}

// TestCovKPMemoryHealthEscalateNonBlocking is the inverse of the above:
// a trusted crew produces a NON-blocking inbox item on ESCALATE. This
// exercises the inboxBlockingForPolicy=false branch through the handler.
func TestCovKPMemoryHealthEscalateNonBlocking(t *testing.T) {
	db, _ := kp2DB(t)
	if _, err := db.Exec(`UPDATE crews SET autonomy_level='trusted' WHERE id='cr1'`); err != nil {
		t.Fatal(err)
	}
	pr := policy.NewResolver(db)

	p := &kp2Provider{content: `{"decision":"ESCALATE","reason":"operator review","risk":5}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewMemoryHealthEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, ev, nil, kp2Logger())

	body := memoryHealthBody{
		WorkspaceID: "ws1", CrewID: "cr1", CrewName: "Ops", AgentName: "system",
	}
	w := httptest.NewRecorder()
	r := covKPReq(t, body, "ws1")
	h.HandleMemoryHealth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	var blocking int
	if err := db.QueryRow(
		`SELECT blocking FROM inbox_items WHERE sender_id='keeper_memory_health' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&blocking); err != nil {
		t.Fatalf("inbox row not found: %v", err)
	}
	if blocking != 0 {
		t.Errorf("inbox blocking = %d, want 0 for trusted-crew ESCALATE", blocking)
	}
}

// TestCovKPMemoryHealthAllowNoInbox covers HandleMemoryHealth on the
// ALLOW path: a keeper_requests row lands but no inbox item is written.
func TestCovKPMemoryHealthAllowNoInbox(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"healthy","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewMemoryHealthEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, ev, nil, kp2Logger())

	body := memoryHealthBody{
		WorkspaceID: "ws1", CrewID: "cr1", CrewName: "Ops", AgentName: "system",
	}
	w := httptest.NewRecorder()
	r := covKPReq(t, body, "ws1")
	h.HandleMemoryHealth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE sender_id='keeper_memory_health'`).Scan(&n)
	if n != 0 {
		t.Errorf("ALLOW path inbox rows = %d, want 0", n)
	}
}

// TestCovKPMemoryHealthWorkspaceMismatch confirms the
// assertBodyWorkspaceMatchesCtx guard fires for memory-health too.
func TestCovKPMemoryHealthWorkspaceMismatch(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewMemoryHealthEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, ev, nil, kp2Logger())

	body := memoryHealthBody{WorkspaceID: "ws_other", CrewID: "cr1"}
	w := httptest.NewRecorder()
	r := covKPReq(t, body, "ws1") // ctx ws1 != body ws_other
	h.HandleMemoryHealth(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for workspace mismatch", w.Code)
	}
}

// TestCovKPBehaviorWarnNonBlockingInbox drives HandleBehavior on a
// WARN/escalate-to-inbox path under a guided crew (behavior_mode default
// warn). PolicyDecision auto_log_inbox / inbox_approve writes a
// NON-blocking inbox item; only block_inbox is blocking.
func TestCovKPBehaviorWarnInbox(t *testing.T) {
	db, _ := kp2DB(t)
	// guided crew with warn behavior is the default seed; ESCALATE from
	// the LLM under warn mode routes to a non-blocking inbox.
	pr := policy.NewResolver(db)

	p := &kp2Provider{content: `{"decision":"ESCALATE","reason":"unusual tool sequence","risk":6}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, ev, nil, nil, kp2Logger())

	body := behaviorBody{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
		AgentName: "Worker", CrewName: "Ops",
		ToolName: "shell_exec", ToolArgsSnippet: `{"cmd":"ls"}`,
	}
	w := httptest.NewRecorder()
	r := covKPReq(t, body, "ws1")
	h.HandleBehavior(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	// A behavior keeper_requests row is recorded regardless of inbox path.
	var rt string
	if err := db.QueryRow(
		`SELECT request_type FROM keeper_requests WHERE requesting_agent_id='a1' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&rt); err != nil {
		t.Fatalf("read keeper_requests: %v", err)
	}
	if rt != "behavior" {
		t.Errorf("request_type = %q, want behavior", rt)
	}
}

// TestCovKPNegativeLearningEvaluatorError drives the evaluator-error
// branch of HandleNegativeLearning (400 with "evaluator error:" body).
// An unknown trigger makes the negative-learning evaluator reject the
// request before any LLM call.
func TestCovKPNegativeLearningEvaluatorError(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, ev, kp2Logger())

	body := negativeLearningBody{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
		AgentName: "Worker", CrewName: "Ops",
		Trigger:        "totally_unknown_trigger_value",
		FailureSnippet: "x",
	}
	w := httptest.NewRecorder()
	r := covKPReq(t, body, "ws1")
	h.HandleNegativeLearning(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400 evaluator error", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "evaluator error") {
		t.Errorf("body = %s, want 'evaluator error'", w.Body.String())
	}
}

// TestCovKPNegativeLearningDenyEscalatesInbox drives the DENY path: a
// DENY decision surfaces to the operator inbox (and writes no lesson).
func TestCovKPNegativeLearningDenyInbox(t *testing.T) {
	db, pr := kp2DB(t)
	tmp := t.TempDir()
	p := &kp2Provider{content: `{"decision":"DENY","reason":"transient noise","risk":2}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, ev, kp2Logger())

	body := negativeLearningBody{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
		AgentName: "Worker", CrewName: "Ops",
		AgentMemoryDir: tmp,
		Trigger:        "run_failed",
		FailureSnippet: "deploy.sh blew up",
	}
	w := httptest.NewRecorder()
	r := covKPReq(t, body, "ws1")
	h.HandleNegativeLearning(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE sender_id='keeper_negative_learning'`,
	).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if n == 0 {
		t.Errorf("DENY path should surface an inbox row; got 0")
	}
}

// TestCovKPLoadSelfLearningEnabled covers loadSelfLearningEnabled
// directly: nil/empty short-circuits, missing row → false, present row.
func TestCovKPLoadSelfLearningEnabled(t *testing.T) {
	db, _ := kp2DB(t)
	ctx := context.Background()

	// nil db / empty ids → false, no error.
	if v, err := loadSelfLearningEnabled(ctx, nil, "ws1", "a1"); err != nil || v {
		t.Errorf("nil db: got (%v,%v), want (false,nil)", v, err)
	}
	if v, err := loadSelfLearningEnabled(ctx, db, "", "a1"); err != nil || v {
		t.Errorf("empty ws: got (%v,%v), want (false,nil)", v, err)
	}
	if v, err := loadSelfLearningEnabled(ctx, db, "ws1", ""); err != nil || v {
		t.Errorf("empty agent: got (%v,%v), want (false,nil)", v, err)
	}

	// missing row (wrong workspace) → false, no error.
	if v, err := loadSelfLearningEnabled(ctx, db, "ws_nope", "a1"); err != nil || v {
		t.Errorf("missing row: got (%v,%v), want (false,nil)", v, err)
	}

	// seeded agent a1 has self_learning_enabled=0 → false.
	if v, err := loadSelfLearningEnabled(ctx, db, "ws1", "a1"); err != nil || v {
		t.Errorf("seeded off: got (%v,%v), want (false,nil)", v, err)
	}

	// flip on → true.
	if _, err := db.Exec(`UPDATE agents SET self_learning_enabled=1 WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	if v, err := loadSelfLearningEnabled(ctx, db, "ws1", "a1"); err != nil || !v {
		t.Errorf("flipped on: got (%v,%v), want (true,nil)", v, err)
	}
}
