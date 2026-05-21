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

// TestKeeperPhase2_NegativeLearning_AllowWritesLesson pins the
// integration handler-side: POST → evaluator ALLOW → lessons.md
// gains a kind=negative entry under the supplied agent_memory_dir.
func TestKeeperPhase2_NegativeLearning_AllowWritesLesson(t *testing.T) {
	db, pr := kp2DB(t)
	tmp := t.TempDir()

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

func mustJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}
