package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

const webhookSchemaSQL = `
CREATE TABLE IF NOT EXISTS pipeline_webhooks (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL,
    name                     TEXT NOT NULL,
    target_pipeline_id       TEXT NOT NULL,
    target_pipeline_version  INTEGER,
    token                    TEXT NOT NULL UNIQUE,
    signing_secret           TEXT,
    inputs_template          TEXT NOT NULL DEFAULT '{}',
    enabled                  INTEGER NOT NULL DEFAULT 1,
    rate_limit_per_min       INTEGER NOT NULL DEFAULT 0,
    last_fired_at            TEXT,
    last_status              TEXT,
    last_run_id              TEXT,
    fire_count               INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT
);`

func openWebhookTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if _, err := db.ExecContext(context.Background(), webhookSchemaSQL); err != nil {
		t.Fatalf("webhook schema: %v", err)
	}
	return db
}

func TestExecutor_If_TrueExecutes(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"ran"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go",
			If: "{{ inputs.go }}"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
		Inputs: map[string]any{"go": "yes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StepOutputs["s1"] != "ran" {
		t.Errorf("expected step to run, got %q", res.StepOutputs["s1"])
	}
}

func TestExecutor_If_FalseSkips(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"should not appear"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go",
			If: "{{ inputs.go }}"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
		Inputs: map[string]any{"go": "false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StepOutputs["s1"] != "<skipped>" {
		t.Errorf("expected <skipped>, got %q", res.StepOutputs["s1"])
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected zero runner calls (step skipped), got %d", len(runner.calls))
	}
}

func TestExecutor_If_DependsOnPriorStep(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["classifier"] = []string{"true"}
	runner.outputsBySlug["worker"] = []string{"did work"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "classify", Type: StepAgentRun, AgentSlug: "classifier", Prompt: "is needed?"},
		{ID: "do", Type: StepAgentRun, AgentSlug: "worker", Prompt: "do it",
			If: "{{ steps.classify.output }}"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: %s", res.Status)
	}
	if res.StepOutputs["do"] != "did work" {
		t.Errorf("expected do to run because classify said true, got %q", res.StepOutputs["do"])
	}
}

func TestEvalIfCondition(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"false", false},
		{"FALSE", false},
		{"0", false},
		{"null", false},
		{"NIL", false},
		{"no", false},
		{"OFF", false},
		{"true", true},
		{"yes", true},
		{"1", true},
		{"any string", true},
		{"  TRUE  ", true},
	}
	for _, c := range cases {
		if got := evalIfCondition(c.in); got != c.want {
			t.Errorf("evalIfCondition(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestExecutor_CostCap_TripsAfterStep(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// Mock returns cost 0.001 per call (default)
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{
		Name:       "x",
		MaxCostUSD: 0.0015, // cap below 2 calls
		Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "1"},
			{ID: "s2", Type: StepAgentRun, AgentSlug: "a", Prompt: "2"},
			{ID: "s3", Type: StepAgentRun, AgentSlug: "a", Prompt: "3"},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED on cost cap, got %s", res.Status)
	}
	// Should have run 2 steps before tripping (cumulative > 0.0015)
	if got := len(runner.calls); got != 2 {
		t.Errorf("expected 2 runner calls before cap trips, got %d", got)
	}
	if res.FailedAtStep != "s2" {
		t.Errorf("expected FailedAtStep=s2, got %s", res.FailedAtStep)
	}
}

func TestExecutor_CostCap_NoCapWhenZero(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{
		Name:       "x",
		MaxCostUSD: 0, // explicitly disabled
		Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "1"},
			{ID: "s2", Type: StepAgentRun, AgentSlug: "a", Prompt: "2"},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED with no cap, got %s err=%s", res.Status, res.ErrorMessage)
	}
}

func TestExecutor_CostCap_PreservesPartialOutputs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["a"] = []string{"first done"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{
		Name:       "x",
		MaxCostUSD: 0.0005, // trips after first step
		Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "1"},
			{ID: "s2", Type: StepAgentRun, AgentSlug: "a", Prompt: "2"},
		},
	}
	res, _ := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if res.StepOutputs["s1"] != "first done" {
		t.Errorf("expected partial output preserved, got %v", res.StepOutputs)
	}
}

// Webhook store tests
func TestWebhookStore_Save_GeneratesToken(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewWebhookStore(db)

	out, err := store.Save(context.Background(), SaveWebhookInput{
		WorkspaceID:      "ws_test",
		Name:             "stripe",
		TargetPipelineID: "pipe_1",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(out.Token) < 32 {
		t.Errorf("expected long token, got %q", out.Token)
	}
	if out.Token[:3] != "wh_" {
		t.Errorf("expected wh_ prefix, got %s", out.Token)
	}
}

func TestWebhookStore_GetByToken_ResolvesEnabled(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewWebhookStore(db)
	ctx := context.Background()

	created, _ := store.Save(ctx, SaveWebhookInput{
		WorkspaceID: "ws_test", Name: "x",
		TargetPipelineID: "pipe_1", Enabled: true,
	})

	got, err := store.GetByToken(ctx, created.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID {
		t.Errorf("token resolution wrong id")
	}
}

func TestWebhookStore_GetByToken_UnknownTokenIs404(t *testing.T) {
	db := openWebhookTestDB(t)
	defer db.Close()
	store := NewWebhookStore(db)
	if _, err := store.GetByToken(context.Background(), "wh_nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestWebhookStore_HMAC_Validates(t *testing.T) {
	const secret = "shh"
	w := &Webhook{SigningSecret: secret}
	body := []byte(`{"hello":"world"}`)
	// Compute expected sig in-test rather than hard-coding magic
	// hex — keeps the test self-checking against the real HMAC algo.
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !w.ValidateSignature(body, expected) {
		t.Errorf("expected valid HMAC to pass")
	}
	if w.ValidateSignature(body, "deadbeef") {
		t.Errorf("expected wrong HMAC to fail")
	}
}

func TestWebhookStore_HMAC_NoSecretPasses(t *testing.T) {
	w := &Webhook{SigningSecret: ""}
	if !w.ValidateSignature([]byte("anything"), "") {
		t.Errorf("expected pass when no secret configured")
	}
}

func TestWebhookStore_RateLimit_FastPath(t *testing.T) {
	// limit=0 means unlimited; ensure no false rejection
	for i := 0; i < 100; i++ {
		if !AllowWebhookFire("token_a", 0) {
			t.Errorf("limit=0 should never reject, hit %d", i)
			return
		}
	}
}

func TestWebhookStore_RateLimit_TripsAt(t *testing.T) {
	// AllowWebhookFire is backed by a package-level rateLimiter whose
	// per-key window state lives for the lifetime of the test binary.
	// Re-running this test in the same process (`go test -count=N`)
	// would otherwise pre-charge the window from prior iterations and
	// trip the limit before i=5. A unique token per call sidesteps the
	// leak without needing to expose a reset hook on production code.
	tok := fmt.Sprintf("token_ratelimit_test_%d_%d", os.Getpid(), atomic.AddInt64(&webhookRateLimitTestSeq, 1))
	for i := 0; i < 5; i++ {
		if !AllowWebhookFire(tok, 5) {
			t.Errorf("rejection before limit reached at i=%d", i)
		}
	}
	if AllowWebhookFire(tok, 5) {
		t.Errorf("expected rejection at i=5 (over limit)")
	}
}

// webhookRateLimitTestSeq makes every -count=N iteration use a fresh
// token so the package-level rateLimiter's window state from prior
// iterations doesn't pre-charge this one's count.
var webhookRateLimitTestSeq int64
