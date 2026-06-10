package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/policy"

	_ "modernc.org/sqlite"
)

func kp2Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

type kp2Provider struct{ content string }

func (p *kp2Provider) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: p.content}, nil
}
func (p *kp2Provider) Stream(ctx context.Context, req llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, _ := p.Complete(ctx, req)
	_ = h(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}
func (p *kp2Provider) Name() string { return "kp2" }

func kp2DB(t *testing.T) (*sql.DB, *policy.Resolver) {
	t.Helper()
	d, err := database.Open("file:" + filepath.Join(t.TempDir(), "kp2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := database.Migrate(context.Background(), d.DB, kp2Logger()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode) VALUES ('cr1', 'ws1', 'Ops', 'ops', 'guided', 'warn')`); err != nil {
		t.Fatal(err)
	}
	// Seed an agent so handlers that record keeper_requests with a
	// requesting_agent_id ("a1" in the body fixtures) don't trip the FK
	// after the recordKeeperRequest persistence-failure fix landed. The
	// previous code swallowed insert errors; tests now must seed
	// referenced rows.
	if _, err := d.DB.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'cr1', 'ws1', 'Worker', 'worker')`); err != nil {
		t.Fatal(err)
	}
	return d.DB, policy.NewResolver(d.DB)
}

// TestKeeperPhase2_SkillReview_AllowPersists pins:
//  1. The /skill-review endpoint reaches the evaluator (ALLOW path).
//  2. A keeper_requests row with request_type='skill_review' lands.
//  3. No inbox row (ALLOW shouldn't escalate).
func TestKeeperPhase2_SkillReview_AllowPersists(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"active","risk":2}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewSkillReviewEvaluator(gk, kp2Logger())

	h := NewKeeperPhase2Handler(db, "tok", pr, ev, nil, nil, nil, kp2Logger())

	body := skillReviewBody{
		WorkspaceID: "ws1", CrewID: "cr1",
		SkillID: "sk_x", SkillName: "x", LifecycleState: "active",
		Assignments: 1, AssignedAgents: []string{"agent-y"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/skill-review", mustJSON(t, body))
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, body.WorkspaceID))
	h.HandleSkillReview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Decision          string `json:"decision"`
		VerifyAfterDecide bool   `json:"verify_after_decide"`
		ProposedLifecycle string `json:"proposed_lifecycle"`
		RequestID         string `json:"request_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if resp.Decision != string(keeper.DecisionAllow) || !resp.VerifyAfterDecide {
		t.Errorf("got decision=%q verify=%v, want ALLOW/true", resp.Decision, resp.VerifyAfterDecide)
	}

	// keeper_requests row exists with request_type='skill_review'.
	var got string
	if err := db.QueryRow(`SELECT request_type FROM keeper_requests WHERE id = ?`, resp.RequestID).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "skill_review" {
		t.Errorf("request_type=%q, want skill_review", got)
	}

	// No inbox row (ALLOW).
	var inbox int
	_ = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = ?`, resp.RequestID).Scan(&inbox)
	if inbox != 0 {
		t.Errorf("inbox rows = %d, want 0 for ALLOW", inbox)
	}
}

// TestKeeperPhase2_Behavior_BlockMode_EscalatesAndInboxes pins:
//  1. block mode + DENY → ShouldBlock=true + blocking inbox item.
//  2. keeper_requests row with request_type='behavior'.
func TestKeeperPhase2_Behavior_BlockMode_EscalatesAndInboxes(t *testing.T) {
	db, _ := kp2DB(t)
	// Flip crew to block mode.
	if _, err := db.Exec(`UPDATE crews SET behavior_mode='block' WHERE id='cr1'`); err != nil {
		t.Fatal(err)
	}
	pr := policy.NewResolver(db)

	p := &kp2Provider{content: `{"decision":"DENY","reason":"destructive","risk":9}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, kp2Logger())

	h := NewKeeperPhase2Handler(db, "tok", pr, nil, ev, nil, nil, kp2Logger())

	body := behaviorBody{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentID: "a1", AgentName: "Worker", CrewName: "Ops",
		ToolName: "shell_exec", ToolArgsSnippet: `{"cmd":"rm -rf /"}`,
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/behavior", mustJSON(t, body))
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, body.WorkspaceID))
	h.HandleBehavior(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Decision    string `json:"decision"`
		ShouldBlock bool   `json:"should_block"`
		RequestID   string `json:"request_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Decision != "DENY" || !resp.ShouldBlock {
		t.Errorf("decision=%q should_block=%v, want DENY/true", resp.Decision, resp.ShouldBlock)
	}

	var inboxBlocking int
	if err := db.QueryRow(
		`SELECT blocking FROM inbox_items WHERE source_id = ?`, resp.RequestID,
	).Scan(&inboxBlocking); err != nil {
		t.Fatalf("inbox row not found: %v", err)
	}
	if inboxBlocking != 1 {
		t.Errorf("inbox.blocking = %d, want 1 for block-mode DENY", inboxBlocking)
	}
}

func TestKeeperPhase2_NotConfigured_Returns503(t *testing.T) {
	db, pr := kp2DB(t)
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, nil, kp2Logger())

	cases := []struct {
		path string
		body any
		fn   http.HandlerFunc
	}{
		{path: "/skill-review", body: skillReviewBody{}, fn: h.HandleSkillReview},
		{path: "/behavior", body: behaviorBody{}, fn: h.HandleBehavior},
		{path: "/memory-health", body: memoryHealthBody{}, fn: h.HandleMemoryHealth},
		{path: "/negative-learning", body: negativeLearningBody{}, fn: h.HandleNegativeLearning},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper"+tc.path, mustJSON(t, tc.body))
			tc.fn(w, r)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("path %s status = %d, want 503", tc.path, w.Code)
			}
		})
	}
}

// TestKeeperPhase2_NegativeLearning_AllowWritesLesson_WhenSelfLearningON
// pins the F4.1 UX gate added in PR-G: ALLOW from the evaluator writes
// the lesson immediately ONLY when the agent has self_learning=1.
func TestKeeperPhase2_NegativeLearning_AllowWritesLesson_WhenSelfLearningON(t *testing.T) {
	db, pr := kp2DB(t)
	tmp := t.TempDir()

	// kp2DB seeds agent a1 with self_learning_enabled=0 (default).
	// Flip it ON for this test.
	if _, err := db.Exec(`UPDATE agents SET self_learning_enabled = 1 WHERE id = 'a1'`); err != nil {
		t.Fatalf("flip self_learning ON: %v", err)
	}

	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"check env vars","risk":3}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())

	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, ev, kp2Logger())

	body := negativeLearningBody{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentID: "a1", AgentName: "Loser", CrewName: "Ops",
		AgentMemoryDir: tmp,
		Trigger:        "run_failed",
		FailureSnippet: "deploy.sh: missing DATABASE_URL",
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/negative-learning", mustJSON(t, body))
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, body.WorkspaceID))
	h.HandleNegativeLearning(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	// lessons.md should now exist under tmp.
	data, err := os.ReadFile(filepath.Join(tmp, "lessons.md"))
	if err != nil {
		t.Fatalf("lessons.md not written: %v", err)
	}
	if !bytes.Contains(data, []byte("kind: negative")) {
		t.Errorf("lessons.md missing 'kind: negative' line\n---\n%s\n---", string(data))
	}
}

// TestKeeperPhase2_NegativeLearning_AllowQueuesInbox_WhenSelfLearningOFF
// pins the inverse of the previous test: same ALLOW signal from the
// evaluator, but self_learning=0 (the safe default) means we DON'T
// touch the agent's lessons.md. Instead a blocking inbox row is queued
// so the operator can approve the proposed lesson before it lands.
//
// This is the gate that gives the per-agent self-learning UI toggle
// (PR-G AgentLearningToggle) an actual functional consequence — the
// auditor's #1 anti-pattern would be a UI toggle with no downstream
// effect, so this test exists to prevent that regression specifically.
func TestKeeperPhase2_NegativeLearning_AllowQueuesInbox_WhenSelfLearningOFF(t *testing.T) {
	db, pr := kp2DB(t)
	tmp := t.TempDir()

	// kp2DB seeds agent a1 with self_learning_enabled=0 — exactly the
	// state we want to assert against. Confirm rather than assume so a
	// future change to the seed doesn't silently break the contract.
	var got int
	if err := db.QueryRow(`SELECT self_learning_enabled FROM agents WHERE id = 'a1'`).Scan(&got); err != nil {
		t.Fatalf("read self_learning: %v", err)
	}
	if got != 0 {
		t.Fatalf("seed assumption broken: agent a1 self_learning=%d, want 0", got)
	}

	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"missing env vars","risk":3}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())

	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, ev, kp2Logger())

	body := negativeLearningBody{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentID: "a1", AgentName: "Loser", CrewName: "Ops",
		AgentMemoryDir: tmp,
		Trigger:        "run_failed",
		FailureSnippet: "deploy.sh: missing DATABASE_URL",
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/negative-learning", mustJSON(t, body))
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, body.WorkspaceID))
	h.HandleNegativeLearning(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	// lessons.md MUST NOT exist — the ALLOW was gated by self_learning=0.
	if _, err := os.Stat(filepath.Join(tmp, "lessons.md")); err == nil {
		raw, _ := os.ReadFile(filepath.Join(tmp, "lessons.md"))
		t.Fatalf("lessons.md was written despite self_learning=0; content=%s", string(raw))
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error: %v", err)
	}

	// A blocking inbox item must exist instead, carrying the lesson
	// proposal as payload so the inbox-approve handler can land the
	// lesson once the operator confirms.
	var (
		title    string
		blocking int
		payload  string
	)
	err := db.QueryRow(`
		SELECT title, blocking, payload_json
		FROM inbox_items
		WHERE workspace_id = 'ws1' AND sender_id = 'keeper_negative_learning'
		ORDER BY created_at DESC LIMIT 1`,
	).Scan(&title, &blocking, &payload)
	if err != nil {
		t.Fatalf("inbox row not found: %v", err)
	}
	if blocking != 1 {
		t.Errorf("inbox row not blocking; got blocking=%d", blocking)
	}
	if !bytes.Contains([]byte(payload), []byte(`"self_learning_gate":"off"`)) {
		t.Errorf("inbox payload missing self_learning_gate=off marker: %s", payload)
	}
	if !bytes.Contains([]byte(payload), []byte(`"lesson_kind"`)) {
		t.Errorf("inbox payload missing lesson_kind: %s", payload)
	}
}

func mustJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

// TestKeeperPhase2_NegativeLearning_RejectsBodyWorkspaceMismatch pins
// the audit-round-3 defense: when the body's workspace_id doesn't
// match the request context's workspace_id, the handler rejects with
// 400 BEFORE evaluating anything. The asymmetric-bypass vector
// (caller passes workspace A in query, claims workspace B in body)
// is the one this fix closes; the symmetric case (caller picks one
// workspace consistently) is closed by PR-F24 token-to-workspace
// binding — see TestKeeperPhase2_SymmetricCrossTenant_ClosedByTokenBinding.
//
// If this test fails the cross-tenant gate is half-open again —
// don't loosen it; it remains the in-handler defense layer under
// the middleware token binding.
func TestKeeperPhase2_NegativeLearning_RejectsBodyWorkspaceMismatch(t *testing.T) {
	db, pr := kp2DB(t)
	tmp := t.TempDir()

	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, ev, kp2Logger())

	body := negativeLearningBody{
		WorkspaceID:    "ws_attacker",
		CrewID:         "cr1",
		AgentID:        "a1",
		AgentName:      "Worker",
		CrewName:       "Ops",
		AgentMemoryDir: tmp,
		Trigger:        "run_failed",
		FailureSnippet: "x",
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/negative-learning", mustJSON(t, body))
	// Simulate internalWsCtx middleware having put a DIFFERENT
	// workspace_id in ctx (e.g. caller passed ?workspace_id=ws1 in
	// query but body.workspace_id="ws_attacker").
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, "ws1"))

	h.HandleNegativeLearning(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400 (workspace mismatch should be rejected)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("workspace_id in body must match")) {
		t.Errorf("error body should explain the mismatch; got %s", w.Body.String())
	}
	// And: no keeper_requests row should have landed (handler aborted before persist).
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM keeper_requests`).Scan(&n)
	if n != 0 {
		t.Errorf("keeper_requests should be empty after rejected request; got %d rows", n)
	}
}

// TestKeeperPhase2_NegativeLearning_RejectsEmptyCtxWorkspace pins the
// round-8 hardening on assertBodyWorkspaceMatchesCtx: a request with
// no ctx workspace_id (because internalWsCtx middleware didn't run,
// or a future middleware-chain change drops it) must be REJECTED,
// not silently let through. Earlier behaviour returned true on
// empty ctxWS — that defeated the cross-tenant guard whenever the
// middleware assumption failed. CodeRabbit round-8 catch.
func TestKeeperPhase2_NegativeLearning_RejectsEmptyCtxWorkspace(t *testing.T) {
	db, pr := kp2DB(t)
	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, nil, nil, ev, kp2Logger())

	body := negativeLearningBody{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1",
		AgentName: "Worker", CrewName: "Ops",
		Trigger: "run_failed", FailureSnippet: "x",
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/negative-learning", mustJSON(t, body))
	// NB: NOT setting ctxWorkspaceID — simulates a misrouted handler
	// or a missing-middleware scenario. The guard should refuse to
	// process the request rather than fall back to body.WorkspaceID.

	h.HandleNegativeLearning(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400 when ctx workspace missing", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("missing workspace_id")) {
		t.Errorf("error must explain missing ctx workspace; got %s", w.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM keeper_requests`).Scan(&n)
	if n != 0 {
		t.Errorf("keeper_requests should be empty after rejected request; got %d rows", n)
	}
}

// TestKeeperPhase2_SymmetricCrossTenant_ClosedByTokenBinding flips the
// long-standing known-gap note (PR-F24): the symmetric cross-tenant
// case — a caller that picks ONE foreign workspace consistently
// across query and body — used to sail through because the
// X-Internal-Token was a single global secret and internalWsCtx
// trusted ?workspace_id. Sidecars now hold a workspace-bound derived
// token, and requireInternal refuses any request whose query
// workspace disagrees with the token's binding. This test drives the
// real middleware chain the router builds
// (requireInternal → internalWsCtx → handler) end to end.
func TestKeeperPhase2_SymmetricCrossTenant_ClosedByTokenBinding(t *testing.T) {
	db, pr := kp2DB(t)
	tmp := t.TempDir()

	p := &kp2Provider{content: `{"decision":"ALLOW","reason":"ok","risk":1}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewNegativeLearningEvaluator(gk, kp2Logger())

	const master = "kp2-master-secret"
	kp2 := NewKeeperPhase2Handler(db, master, pr, nil, nil, nil, ev, kp2Logger())
	ih := NewInternalHandler(db, master, kp2Logger())
	chain := ih.requireInternal(internalWsCtx(http.HandlerFunc(kp2.HandleNegativeLearning)))

	makeReq := func(token string) (*httptest.ResponseRecorder, *http.Request) {
		body := negativeLearningBody{
			WorkspaceID:    "ws1", // consistent across query AND body — the symmetric shape
			CrewID:         "cr1",
			AgentID:        "a1",
			AgentName:      "Worker",
			CrewName:       "Ops",
			AgentMemoryDir: tmp,
			Trigger:        "run_failed",
			FailureSnippet: "x",
		}
		r := httptest.NewRequest(http.MethodPost,
			"/api/v1/internal/keeper/negative-learning?workspace_id=ws1", mustJSON(t, body))
		r.RemoteAddr = "127.0.0.1:1234" // pass requireInternal's network gate
		r.Header.Set("X-Internal-Token", token)
		return httptest.NewRecorder(), r
	}

	// Attacker: sidecar token bound to ws_attacker, request aimed
	// consistently at ws1. Pre-PR-F24 this was the open bypass; it
	// must now die in the middleware with 403, before the handler
	// (and any keeper_requests persistence) runs.
	w, r := makeReq(internaltoken.DeriveWorkspaceToken(master, "ws_attacker"))
	chain.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant status = %d body=%s; want 403 (symmetric bypass must be closed)",
			w.Code, w.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM keeper_requests`).Scan(&n)
	if n != 0 {
		t.Fatalf("keeper_requests rows = %d after rejected cross-tenant request, want 0", n)
	}

	// Legitimate sidecar: token bound to ws1 sends the identical
	// request and must still reach the evaluator.
	w, r = makeReq(internaltoken.DeriveWorkspaceToken(master, "ws1"))
	chain.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("same-workspace status = %d body=%s; want 200", w.Code, w.Body.String())
	}
}
