package hooks

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// schemaSQL mirrors migration 52's hooks_config shape. Test opens a fresh
// in-memory DB and applies this directly rather than pulling in the whole
// migrate package — keeps the unit tests fast and decoupled from migration
// order.
const schemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT);
INSERT INTO workspaces (id) VALUES ('ws_test');
INSERT INTO crews (id, workspace_id) VALUES ('crew_a', 'ws_test');
INSERT INTO crews (id, workspace_id) VALUES ('crew_b', 'ws_test');

CREATE TABLE hooks_config (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    event TEXT NOT NULL,
    matcher TEXT NOT NULL DEFAULT '{}',
    handler_kind TEXT NOT NULL CHECK(handler_kind IN ('shell','http','subagent')),
    handler_config TEXT NOT NULL DEFAULT '{}',
    blocking INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_hooks_event ON hooks_config(event, enabled);
CREATE INDEX idx_hooks_ws ON hooks_config(workspace_id, enabled);
`

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// recordingEmitter is a journal.Emitter stub that captures every entry in
// a slice so tests can assert on what the dispatcher wrote.
type recordingEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (r *recordingEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.ID == "" {
		e.ID = "j_test"
	}
	r.entries = append(r.entries, e)
	return e.ID, nil
}

func (r *recordingEmitter) Flush(_ context.Context) error { return nil }

func (r *recordingEmitter) typesSeen() []journal.EntryType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]journal.EntryType, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.Type)
	}
	return out
}

// ---------------------------------------------------------------------
// Store: Register + Get roundtrip, shell-not-allowed, enable/disable
// ---------------------------------------------------------------------

func TestRegisterGetRoundtrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	h := Hook{
		WorkspaceID: "ws_test",
		CrewID:      "crew_a",
		Event:       EventPreToolCall,
		Matcher:     Matcher{Tools: []string{"Bash"}},
		HandlerKind: HandlerKindHTTP,
		HandlerConfig: map[string]any{
			"url": "https://example.com/webhook",
		},
		Blocking:  true,
		Enabled:   true,
		CreatedBy: "user_owner",
	}
	id, err := Register(ctx, db, h, false /* shell not relevant */)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id == "" {
		t.Fatal("expected id")
	}

	got, err := Get(ctx, db, "ws_test", id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("get returned nil")
	}
	if got.WorkspaceID != "ws_test" || got.CrewID != "crew_a" {
		t.Errorf("scope mismatch: %+v", got)
	}
	if got.Event != EventPreToolCall {
		t.Errorf("event: %q", got.Event)
	}
	if got.HandlerKind != HandlerKindHTTP {
		t.Errorf("kind: %q", got.HandlerKind)
	}
	if !got.Blocking || !got.Enabled {
		t.Errorf("flags: blocking=%v enabled=%v", got.Blocking, got.Enabled)
	}
	if len(got.Matcher.Tools) != 1 || got.Matcher.Tools[0] != "Bash" {
		t.Errorf("matcher lost: %+v", got.Matcher)
	}
	if got.HandlerConfig["url"] != "https://example.com/webhook" {
		t.Errorf("handler_config lost: %+v", got.HandlerConfig)
	}
}

func TestRegisterShellNotAllowed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	h := Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindShell,
		HandlerConfig: map[string]any{"command": "echo hi"},
		Enabled:       true,
	}
	_, err := Register(ctx, db, h, false)
	if !errors.Is(err, ErrShellHookNotAllowed) {
		t.Fatalf("expected ErrShellHookNotAllowed, got %v", err)
	}

	// Same hook with allowedShell=true succeeds.
	id, err := Register(ctx, db, h, true)
	if err != nil {
		t.Fatalf("register(allowed): %v", err)
	}
	if id == "" {
		t.Fatal("expected id")
	}
}

func TestEnableDisable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://x"},
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Disable(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	got, _ := Get(ctx, db, "ws_test", id)
	if got.Enabled {
		t.Fatal("expected disabled")
	}
	if err := Enable(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	got, _ = Get(ctx, db, "ws_test", id)
	if !got.Enabled {
		t.Fatal("expected enabled")
	}
}

// ---------------------------------------------------------------------
// ListByEvent: crew scoping + enabled filtering
// ---------------------------------------------------------------------

func TestListByEventCrewScope(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Workspace-wide hook (crew_id nil).
	wsID, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://ws"},
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Crew_a hook.
	aID, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://a"},
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Crew_b hook — should NOT appear when we list for crew_a.
	bID, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_b",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://b"},
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Disabled hook — filtered out.
	disID, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://dis"},
		Enabled:       false,
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	// List for crew_a: expect ws-wide + crew_a only.
	got, err := ListByEvent(ctx, db, "ws_test", "crew_a", EventPreToolCall)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := map[string]bool{}
	for _, h := range got {
		gotIDs[h.ID] = true
	}
	if !gotIDs[wsID] {
		t.Error("missing ws-wide hook")
	}
	if !gotIDs[aID] {
		t.Error("missing crew_a hook")
	}
	if gotIDs[bID] {
		t.Error("crew_b hook leaked")
	}
	if gotIDs[disID] {
		t.Error("disabled hook leaked")
	}

	// Empty crewID returns only ws-wide.
	got, err = ListByEvent(ctx, db, "ws_test", "", EventPreToolCall)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != wsID {
		t.Errorf("expected only ws-wide hook, got %d", len(got))
	}
}

// ---------------------------------------------------------------------
// Matcher: tool regex, agent id, severity
// ---------------------------------------------------------------------

func TestMatcherToolRegex(t *testing.T) {
	m := Matcher{Tools: []string{"^Bash$", "^Read$"}}
	if !Matches(m, EventContext{ToolName: "Bash"}) {
		t.Error("Bash should match")
	}
	if !Matches(m, EventContext{ToolName: "Read"}) {
		t.Error("Read should match")
	}
	if Matches(m, EventContext{ToolName: "Write"}) {
		t.Error("Write should not match")
	}
	if Matches(m, EventContext{ToolName: ""}) {
		t.Error("empty tool name should not match when Tools set")
	}

	// Empty matcher matches everything.
	if !Matches(Matcher{}, EventContext{ToolName: "anything"}) {
		t.Error("empty matcher should match all")
	}
}

func TestMatcherAgentAndSeverity(t *testing.T) {
	m := Matcher{AgentIDs: []string{"agent_a"}, Severities: []string{"warn", "error"}}
	if !Matches(m, EventContext{AgentID: "agent_a", Severity: "warn"}) {
		t.Error("should match")
	}
	if Matches(m, EventContext{AgentID: "agent_b", Severity: "warn"}) {
		t.Error("wrong agent should not match")
	}
	if Matches(m, EventContext{AgentID: "agent_a", Severity: "info"}) {
		t.Error("wrong severity should not match")
	}
}

// ---------------------------------------------------------------------
// Shell handler: echo stdout, non-zero exit, timeout
// ---------------------------------------------------------------------

func TestShellHandlerPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell handler requires sh")
	}
	h := Hook{
		HandlerKind: HandlerKindShell,
		HandlerConfig: map[string]any{
			"command": "echo hello-crewship",
		},
	}
	res, err := shellHandler(context.Background(), h, EventContext{
		Event:       EventPreToolCall,
		WorkspaceID: "ws_test",
	})
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if res.Outcome != OutcomePass {
		t.Errorf("outcome: %s", res.Outcome)
	}
	payload, _ := res.Payload.(map[string]any)
	stdout, _ := payload["stdout"].(string)
	if stdout == "" || stdout[:5] != "hello" {
		t.Errorf("stdout not captured: %q", stdout)
	}
}

func TestShellHandlerBlockOnExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	h := Hook{
		HandlerKind: HandlerKindShell,
		HandlerConfig: map[string]any{
			"command": "exit 7",
		},
	}
	res, err := shellHandler(context.Background(), h, EventContext{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if res.Outcome != OutcomeBlock {
		t.Errorf("outcome: %s", res.Outcome)
	}
	payload, _ := res.Payload.(map[string]any)
	if code, _ := payload["exit_code"].(int); code != 7 {
		t.Errorf("exit_code: %v", payload["exit_code"])
	}
}

func TestShellHandlerTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	h := Hook{
		HandlerKind: HandlerKindShell,
		HandlerConfig: map[string]any{
			"command":      "sleep 5",
			"timeout_secs": 1,
		},
	}
	start := time.Now()
	res, err := shellHandler(context.Background(), h, EventContext{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if res.Outcome != OutcomeBlock {
		t.Errorf("outcome: %s", res.Outcome)
	}
	payload, _ := res.Payload.(map[string]any)
	if b, _ := payload["timed_out"].(bool); !b {
		t.Errorf("expected timed_out=true, got %+v", payload)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout not enforced, took %s", time.Since(start))
	}
}

// ---------------------------------------------------------------------
// HTTP handler: 2xx pass, 5xx block, HMAC header
// ---------------------------------------------------------------------

func TestHTTPHandlerPass(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := r.Header.Get("X-Crewship-Signature"); got == "" {
			t.Error("expected signature header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	h := Hook{
		HandlerKind: HandlerKindHTTP,
		HandlerConfig: map[string]any{
			"url":    ts.URL,
			"secret": "shh",
		},
	}
	res, err := httpHandler(context.Background(), h, EventContext{
		Event:       EventPreToolCall,
		WorkspaceID: "ws_test",
	})
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	if !called {
		t.Fatal("webhook not hit")
	}
	if res.Outcome != OutcomePass {
		t.Errorf("outcome: %s", res.Outcome)
	}
}

func TestHTTPHandlerBlockOn5xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`service down`))
	}))
	defer ts.Close()

	h := Hook{
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
	}
	res, err := httpHandler(context.Background(), h, EventContext{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	if res.Outcome != OutcomeBlock {
		t.Errorf("outcome: %s", res.Outcome)
	}
}

// ---------------------------------------------------------------------
// Dispatcher: blocking short-circuit, non-blocking fire-and-forget
// ---------------------------------------------------------------------

func TestDispatcherBlockingShortCircuit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Server A returns 503 → block. Server B would return 200 → pass, but
	// we expect it never to be hit because A comes first.
	tsBlock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer tsBlock.Close()

	var bHits int32
	var mu sync.Mutex
	tsPass := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		bHits++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer tsPass.Close()

	aID, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": tsBlock.URL},
		Blocking:      true,
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": tsPass.URL},
		Blocking:      true,
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	rec := &recordingEmitter{}
	err = Dispatch(ctx, db, rec, EventPreToolCall, EventContext{
		WorkspaceID: "ws_test",
	})
	if err == nil {
		t.Fatal("expected BlockedError")
	}
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BlockedError, got %T: %v", err, err)
	}
	if be.HookID != aID {
		t.Errorf("blocked by wrong hook: %s", be.HookID)
	}

	// Second hook should NOT have been called.
	mu.Lock()
	defer mu.Unlock()
	if bHits != 0 {
		t.Errorf("short-circuit failed; second hook hit %d times", bHits)
	}

	// Journal got one fired + one blocked entry.
	types := rec.typesSeen()
	var hasFired, hasBlocked bool
	for _, ty := range types {
		if ty == journal.EntryHookFired {
			hasFired = true
		}
		if ty == journal.EntryHookBlocked {
			hasBlocked = true
		}
	}
	if !hasFired || !hasBlocked {
		t.Errorf("journal types: %v", types)
	}
}

func TestDispatcherNonBlockingDoesNotBlockCaller(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Slow webhook — would block caller for 2s if dispatch waited.
	tsSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	defer tsSlow.Close()

	_, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPostToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": tsSlow.URL},
		Blocking:      false,
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	rec := &recordingEmitter{}
	start := time.Now()
	if err := Dispatch(ctx, db, rec, EventPostToolCall, EventContext{
		WorkspaceID: "ws_test",
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("non-blocking hook blocked caller for %s", elapsed)
	}
}

func TestDispatcherMatcherFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	var hits int
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer ts.Close()

	// Hook only fires on Bash tool calls.
	_, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreToolCall,
		Matcher:       Matcher{Tools: []string{"^Bash$"}},
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true,
		Enabled:       true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	rec := &recordingEmitter{}
	// Read → should NOT fire.
	if err := Dispatch(ctx, db, rec, EventPreToolCall, EventContext{
		WorkspaceID: "ws_test",
		ToolName:    "Read",
	}); err != nil {
		t.Fatalf("dispatch Read: %v", err)
	}
	mu.Lock()
	if hits != 0 {
		t.Errorf("matcher failed: Read hit %d times", hits)
	}
	mu.Unlock()

	// Bash → should fire.
	if err := Dispatch(ctx, db, rec, EventPreToolCall, EventContext{
		WorkspaceID: "ws_test",
		ToolName:    "Bash",
	}); err != nil {
		t.Fatalf("dispatch Bash: %v", err)
	}
	mu.Lock()
	if hits != 1 {
		t.Errorf("matcher failed: Bash hit %d times (want 1)", hits)
	}
	mu.Unlock()
}

// ---------------------------------------------------------------------
// Subagent: unconfigured handler returns Error
// ---------------------------------------------------------------------

func TestSubagentHandlerNotConfigured(t *testing.T) {
	// Reset any previously-registered handler, then ensure nil path hits.
	SetSubagentHandler(nil)
	res, err := subagentHandlerDispatch(context.Background(), Hook{HandlerKind: HandlerKindSubagent}, EventContext{})
	if !errors.Is(err, ErrSubagentHandlerNotConfigured) {
		t.Fatalf("expected ErrSubagentHandlerNotConfigured, got %v", err)
	}
	if res.Outcome != OutcomeError {
		t.Errorf("outcome: %s", res.Outcome)
	}
}

type stubSubagent struct{ called bool }

func (s *stubSubagent) Run(_ context.Context, _ Hook, _ EventContext) (Result, error) {
	s.called = true
	return Result{Outcome: OutcomePass, Message: "ok"}, nil
}

func TestSubagentHandlerConfigured(t *testing.T) {
	stub := &stubSubagent{}
	SetSubagentHandler(stub)
	defer SetSubagentHandler(nil)

	res, err := subagentHandlerDispatch(context.Background(), Hook{HandlerKind: HandlerKindSubagent}, EventContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !stub.called {
		t.Error("stub not called")
	}
	if res.Outcome != OutcomePass {
		t.Errorf("outcome: %s", res.Outcome)
	}
}
