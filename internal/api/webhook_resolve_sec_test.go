package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// ---------------------------------------------------------------------------
// FIX A — webhook secret lookup must thread crew_id so the server-side crew
// scoping (internal_credentials.go GetWebhookSecret) actually engages.
//
// FIX C — webhook run ids must be CUIDs (no UnixNano collision window) and a
// repeated Idempotency-Key must not dispatch a second run.
//
// Prefix: TestSecResolve* (FIX A) / TestSecWebhook2* (FIX C).
// ---------------------------------------------------------------------------

// TestSecResolveWebhookSecretSendsCrewID stands up a stub server in front of a
// real IPCResolver and asserts the GET carries ?crew_id=. Pre-fix the resolver
// built the URL with no query, so the server's crew scoping was dead — this is
// the RED guard.
func TestSecResolveWebhookSecretSendsCrewID(t *testing.T) {
	var gotQuery atomic.Value
	gotQuery.Store("")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery.Store(r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webhook_secret":"sek"}`))
	}))
	t.Cleanup(ts.Close)

	r := chatbridge.NewIPCResolver(ts.URL, "tok", newTestLogger())
	if _, err := r.GetWebhookSecret(context.Background(), "crew-77", "agent-9"); err != nil {
		t.Fatalf("GetWebhookSecret: %v", err)
	}

	raw, _ := gotQuery.Load().(string)
	vals, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	if got := vals.Get("crew_id"); got != "crew-77" {
		t.Errorf("crew_id query = %q, want %q (raw=%q) — server crew scoping never engages without it", got, "crew-77", raw)
	}
}

// TestSecResolveLookupSecretThreadsCrewID pins the WebhookHandler.lookupSecret
// contract: the crewID from the webhook URL must be forwarded to the resolver
// (not discarded into the `_` param as pre-fix).
func TestSecResolveLookupSecretThreadsCrewID(t *testing.T) {
	resolver := &fakeChatResolver{lookupReturnSecret: "shhh"}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	if _, err := h.lookupSecret(context.Background(), "crew-abc", "agent-7"); err != nil {
		t.Fatalf("lookupSecret: %v", err)
	}
	if resolver.lookupCalledWithCrewID != "crew-abc" {
		t.Errorf("resolver crewID = %q, want \"crew-abc\" (must thread, not discard)", resolver.lookupCalledWithCrewID)
	}
	if resolver.lookupCalledWithAgentID != "agent-7" {
		t.Errorf("resolver agentID = %q, want \"agent-7\"", resolver.lookupCalledWithAgentID)
	}
}

// secWebhook2Container counts EnsureCrewRuntime invocations so the dedup test
// can assert a duplicate delivery never reaches the dispatch side-effect.
type secWebhook2Container struct {
	ensureCount atomic.Int32
}

func (m *secWebhook2Container) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	m.ensureCount.Add(1)
	// Succeed so trigger proceeds synchronously to CreateRun (step 4). The
	// async dispatch goroutine is neutered in the tests by holding the
	// backup guard for the workspace (refuseIfBackupInProgress then refuses
	// before touching the nil orchestrator).
	return "container-sec", nil
}
func (m *secWebhook2Container) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (m *secWebhook2Container) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (m *secWebhook2Container) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (m *secWebhook2Container) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *secWebhook2Container) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (m *secWebhook2Container) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (m *secWebhook2Container) CrewContainerName(slug string) string { return "crew-" + slug }
func (m *secWebhook2Container) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*secWebhook2Container)(nil)

// runIDRecordingResolver captures every CreateRun runID so the test can assert
// the format is a CUID and that a deduped delivery never creates a second run.
type runIDRecordingResolver struct {
	fakeChatResolver
	createdRunIDs []string
}

func (r *runIDRecordingResolver) CreateRun(_ context.Context, runID, _, _, _, _ string, _ map[string]interface{}) error {
	r.createdRunIDs = append(r.createdRunIDs, runID)
	return nil
}

var cuidRe = regexp.MustCompile(`^c[0-9a-z]+$`)

// TestSecWebhook2RunIDIsCUIDNotUnixNano asserts the webhook run id is a CUID
// (e.g. "ckj...") and NOT the legacy "run-wh-<digits>" UnixNano form that
// could collide within a nanosecond tick.
func TestSecWebhook2RunIDIsCUIDNotUnixNano(t *testing.T) {
	body := webhook.WebhookPayload{Event: "deploy", Source: "gh"}
	resolver := &runIDRecordingResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: "agent-1", AgentSlug: "ag", CrewID: "crew-1", CrewSlug: "c", WorkspaceID: "ws-cuid",
	}
	container := &secWebhook2Container{}
	// A zero-value Orchestrator is non-accepting: the async dispatch
	// goroutine's RunAgent returns "not accepting new runs" instead of
	// nil-derefing, so the goroutine is harmless. CreateRun (the run-id
	// mint) still runs synchronously before the goroutine spawns.
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, &orchestrator.Orchestrator{}, nil, container, nil)

	_ = h.trigger(context.Background(), "crew-1", "agent-1", body)

	if len(resolver.createdRunIDs) != 1 {
		t.Fatalf("CreateRun called %d times, want 1", len(resolver.createdRunIDs))
	}
	runID := resolver.createdRunIDs[0]
	if matched, _ := regexp.MatchString(`^run-wh-\d+$`, runID); matched {
		t.Errorf("run id %q is the legacy run-wh-<digits> UnixNano form; want a CUID", runID)
	}
	if !cuidRe.MatchString(runID) {
		t.Errorf("run id %q is not a CUID", runID)
	}
}

// TestSecWebhook2DuplicateIdempotencyKeyNoDoubleDispatch drives trigger twice
// with the same Idempotency-Key (stashed via context, the way ServeHTTP does)
// and asserts the second delivery short-circuits before EnsureCrewRuntime /
// CreateRun. Pre-fix (no dedup) both deliveries would dispatch.
func TestSecWebhook2DuplicateIdempotencyKeyNoDoubleDispatch(t *testing.T) {
	body := webhook.WebhookPayload{Event: "deploy", Source: "gh"}
	resolver := &runIDRecordingResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: "agent-1", AgentSlug: "ag", CrewID: "crew-1", CrewSlug: "c", WorkspaceID: "ws-dedup",
	}
	container := &secWebhook2Container{}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, &orchestrator.Orchestrator{}, nil, container, nil)

	ctx := context.WithValue(context.Background(), webhookIdempotencyKeyCtxKey{}, "idem-key-1")

	_ = h.trigger(ctx, "crew-1", "agent-1", body)
	_ = h.trigger(ctx, "crew-1", "agent-1", body)

	if got := container.ensureCount.Load(); got != 1 {
		t.Errorf("EnsureCrewRuntime called %d times, want 1 (duplicate Idempotency-Key must short-circuit)", got)
	}
	if len(resolver.createdRunIDs) != 1 {
		t.Errorf("CreateRun called %d times, want 1 (no double dispatch on duplicate key)", len(resolver.createdRunIDs))
	}
}
