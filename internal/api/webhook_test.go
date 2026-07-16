package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// ---------------------------------------------------------------------------
// webhook.go — NewWebhookHandler, ServeHTTP, lookupSecret, trigger.
//
// The full trigger flow runs a background goroutine that needs the
// orchestrator + container provider + log writer stack. We cover the
// pre-goroutine surface (constructor wiring, the crew-scoped DB secret
// lookup, the ResolveAgent error branch that bails before the goroutine
// spawns) and the ServeHTTP delegation contract.
// ---------------------------------------------------------------------------

// fakeChatResolver records calls to ResolveAgent so tests can assert the
// trigger handler routes through the resolver before reaching the
// orchestrator. Other methods exist to satisfy the chatbridge.ChatResolver
// interface; they return zero values. Webhook secrets are NOT resolved here:
// lookupSecret reads the agents table directly (#999), so tests seed rows
// via seedWebhookSecretAgent instead of stubbing a resolver method.
type fakeChatResolver struct {
	resolveCalledWithAgentID     string
	resolveCalledWithWorkspaceID string
	resolveReturnInfo            *chatbridge.ChatInfo
	resolveReturnErr             error
}

func (f *fakeChatResolver) CreateChat(_ context.Context, _ chatbridge.CreateChatRequest) error {
	return nil
}
func (f *fakeChatResolver) ResolveChat(_ context.Context, _ string) (*chatbridge.ChatInfo, error) {
	return nil, nil
}
func (f *fakeChatResolver) ResolveAgent(_ context.Context, agentID, workspaceID string) (*chatbridge.ChatInfo, error) {
	f.resolveCalledWithAgentID = agentID
	f.resolveCalledWithWorkspaceID = workspaceID
	return f.resolveReturnInfo, f.resolveReturnErr
}
func (f *fakeChatResolver) CreateRun(_ context.Context, _, _, _, _, _ string, _ map[string]interface{}) error {
	return nil
}
func (f *fakeChatResolver) UpdateRun(_ context.Context, _, _ string, _ *int, _ *string, _ map[string]interface{}) error {
	return nil
}
func (f *fakeChatResolver) IncrementMessageCount(_ context.Context, _ string, _ int) error {
	return nil
}
func (f *fakeChatResolver) UpdateChatTitle(_ context.Context, _, _ string) error { return nil }

func (f *fakeChatResolver) RecordCost(_ context.Context, _ chatbridge.RunCostUsage) error {
	return nil
}

// Compile-time interface satisfaction — catches a chatbridge.ChatResolver
// method-set drift before the test binary builds.
var _ chatbridge.ChatResolver = (*fakeChatResolver)(nil)

// seedWebhookSecretAgent seeds a crew + agent row carrying webhook_secret so
// lookupSecret's crew-scoped DB read can be exercised against real rows
// (#999 — the secret never leaves this process over IPC). secret == "" seeds
// a NULL webhook_secret (the not-configured shape).
func seedWebhookSecretAgent(t *testing.T, db *sql.DB, wsID, crewID, agentID, secret string) {
	t.Helper()
	seedCrewRow(t, db, crewID, wsID, "C-"+crewID, "c-"+crewID)
	var sec any = secret
	if secret == "" {
		sec = nil
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled, webhook_secret)
		VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0, ?)`,
		agentID, wsID, crewID, "N-"+agentID, "s-"+agentID, sec); err != nil {
		t.Fatalf("seed webhook agent %s: %v", agentID, err)
	}
}

// ---- NewWebhookHandler ----

func TestNewWebhookHandler_WiresAllDependenciesAndBuildsInnerHandler(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	resolver := &fakeChatResolver{}

	// Other deps (orch, hub, container, logWriter) can be nil for this
	// wiring check — none are touched during construction.
	h := NewWebhookHandler(db, logger, resolver, nil, nil, nil, nil)
	if h == nil {
		t.Fatal("NewWebhookHandler returned nil")
	}
	if h.db != db {
		t.Error("db not stored")
	}
	if h.logger != logger {
		t.Error("logger not stored")
	}
	if h.resolver != resolver {
		t.Error("resolver not stored — trigger would dispatch to the wrong source")
	}
	if h.handler == nil {
		t.Fatal("inner webhook.Handler not constructed; ServeHTTP would nil-deref")
	}
}

// ---- lookupSecret ----

func TestLookupSecret_ReadsCrewScopedRowFromDB(t *testing.T) {
	// lookupSecret reads webhook_secret straight from the local agents table
	// (#999 — the secret never travels over the internal IPC hop), and the
	// (crew, agent) pair named in the webhook URL scopes the row: an agent id
	// alone must not resolve another crew's secret.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedWebhookSecretAgent(t, db, wsID, "crew-1", "agent-7", "shhh")
	h := NewWebhookHandler(db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)

	got, err := h.lookupSecret(context.Background(), "crew-1", "agent-7")
	if err != nil {
		t.Fatalf("lookupSecret: %v", err)
	}
	if got != "shhh" {
		t.Errorf("secret = %q, want \"shhh\"", got)
	}

	if secret, err := h.lookupSecret(context.Background(), "crew-wrong", "agent-7"); err == nil || secret != "" {
		t.Errorf("wrong-crew lookup = (%q, %v), want empty secret + error (crew scoping must engage)", secret, err)
	}
}

// TestLookupSecret_DecryptsEncryptedSecretAtRest pins the #1072 read path: a
// webhook_secret stored AES-256-GCM encrypted at rest must be DECRYPTED by
// lookupSecret so HMAC verification uses the real secret. Reverting the
// DecryptIfEncrypted call would return the envelope and fail this.
func TestLookupSecret_DecryptsEncryptedSecretAtRest(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	enc, err := encryption.Encrypt("whsec_real_value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	seedWebhookSecretAgent(t, db, wsID, "crew-1", "agent-enc", enc)
	h := NewWebhookHandler(db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)

	got, err := h.lookupSecret(context.Background(), "crew-1", "agent-enc")
	if err != nil {
		t.Fatalf("lookupSecret: %v", err)
	}
	if got != "whsec_real_value" {
		t.Errorf("lookupSecret returned %q, want the DECRYPTED plaintext (envelope not decrypted?)", got)
	}
}

func TestLookupSecret_ErrorsBubbleForMissingOrUnconfiguredAgent(t *testing.T) {
	// A lookup error must bubble — the webhook handler maps it to 404
	// upstream; a swallowed error would silently accept unauthenticated
	// webhooks.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedWebhookSecretAgent(t, db, wsID, "crew-1", "agent-nosecret", "") // NULL webhook_secret
	h := NewWebhookHandler(db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)

	if _, err := h.lookupSecret(context.Background(), "crew-1", "agent-ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown agent err = %v, want \"not found\"", err)
	}
	if _, err := h.lookupSecret(context.Background(), "crew-1", "agent-nosecret"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("NULL-secret agent err = %v, want \"not configured\"", err)
	}

	// nil db (test wiring) must refuse rather than pretend a secret matched.
	hn := NewWebhookHandler(nil, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)
	if _, err := hn.lookupSecret(context.Background(), "crew-1", "agent-7"); err == nil {
		t.Error("nil-db lookup succeeded; want error")
	}
}

// ---- ServeHTTP ----

func TestServeHTTP_DelegatesToInnerHandler(t *testing.T) {
	// ServeHTTP forwards to webhook.Handler.ServeHTTP. We can't easily
	// observe the inner Handler's behavior here without a full payload
	// + signature setup, but a plain GET to the handler should produce
	// some 4xx response (the inner handler rejects non-POST or missing
	// signature), proving the delegation reached real code rather than
	// nil-deref'ing.
	resolver := &fakeChatResolver{}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/agent-1", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Any non-2xx response confirms the delegation reached the inner
	// handler (which rejects GET / missing signature / bad path). A
	// 0 / 200 status would suggest ServeHTTP no-op'd.
	if rr.Code >= 200 && rr.Code < 300 {
		t.Errorf("status = %d for unsigned GET; expected the inner webhook.Handler to reject (4xx)", rr.Code)
	}
}

// ---- trigger error paths ----

func TestTrigger_ResolveAgentFailure_BubblesWrappedError(t *testing.T) {
	// trigger's first call inside the synchronous body is ResolveAgent.
	// On failure it returns "resolve agent: %w" — the goroutine never
	// spawns, so we can assert on the error without orchestrator wiring.
	want := errors.New("db gone")
	resolver := &fakeChatResolver{resolveReturnErr: want}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	err := h.trigger(context.Background(), "crew-1", "agent-1", webhook.WebhookPayload{
		Event: "test.event", Source: "test", Data: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error on ResolveAgent failure")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "resolve agent") {
		t.Errorf("err = %v, want \"resolve agent\" prefix (so operators can find the failure source)", err)
	}
	// Verify the resolver was called with the agentID, not the crewID.
	if resolver.resolveCalledWithAgentID != "agent-1" {
		t.Errorf("ResolveAgent called with %q, want \"agent-1\"", resolver.resolveCalledWithAgentID)
	}
}
