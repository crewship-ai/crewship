package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// ---------------------------------------------------------------------------
// webhook.go — NewWebhookHandler, ServeHTTP, lookupSecret, trigger.
//
// The full trigger flow runs a background goroutine that needs the
// orchestrator + container provider + log writer stack. We cover the
// pre-goroutine surface (constructor wiring, secret lookup delegation,
// the ResolveAgent error branch that bails before the goroutine spawns)
// and the ServeHTTP delegation contract.
// ---------------------------------------------------------------------------

// fakeChatResolver records calls to GetWebhookSecret / ResolveAgent so
// tests can assert the trigger handler routes through the resolver before
// reaching the orchestrator. Other methods exist to satisfy the
// chatbridge.ChatResolver interface; they return zero values.
type fakeChatResolver struct {
	lookupCalledWithAgentID      string
	lookupCalledWithCrewID       string
	lookupReturnSecret           string
	lookupReturnErr              error
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
func (f *fakeChatResolver) GetWebhookSecret(_ context.Context, crewID, agentID string) (string, error) {
	f.lookupCalledWithAgentID = agentID
	f.lookupCalledWithCrewID = crewID
	return f.lookupReturnSecret, f.lookupReturnErr
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

// Compile-time interface satisfaction — catches a chatbridge.ChatResolver
// method-set drift before the test binary builds.
var _ chatbridge.ChatResolver = (*fakeChatResolver)(nil)

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
		t.Error("resolver not stored — lookupSecret + trigger would dispatch to the wrong source")
	}
	if h.handler == nil {
		t.Fatal("inner webhook.Handler not constructed; ServeHTTP would nil-deref")
	}
}

// ---- lookupSecret ----

func TestLookupSecret_DelegatesToResolverWithAgentID(t *testing.T) {
	// Source: lookupSecret discards the first arg (the URL path identifier)
	// and forwards only the agentID to the resolver. Pin that contract so
	// a refactor that accidentally passes the path arg can't sneak in.
	resolver := &fakeChatResolver{lookupReturnSecret: "shhh"}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	got, err := h.lookupSecret(context.Background(), "ignored-path-id", "agent-7")
	if err != nil {
		t.Fatalf("lookupSecret: %v", err)
	}
	if got != "shhh" {
		t.Errorf("secret = %q, want \"shhh\"", got)
	}
	if resolver.lookupCalledWithAgentID != "agent-7" {
		t.Errorf("resolver called with %q, want \"agent-7\" (the second arg, not the first)", resolver.lookupCalledWithAgentID)
	}
}

func TestLookupSecret_PropagatesResolverError(t *testing.T) {
	// A resolver error must bubble — the webhook handler returns 401 from
	// upstream when the secret can't be looked up; a swallowed error would
	// silently accept unauthenticated webhooks.
	want := errors.New("agent not found")
	resolver := &fakeChatResolver{lookupReturnErr: want}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	_, err := h.lookupSecret(context.Background(), "x", "agent-x")
	if err != want {
		t.Errorf("err = %v, want %v (errors should propagate unchanged)", err, want)
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
