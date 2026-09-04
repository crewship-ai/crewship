package api

// query_delegation_hooks_test.go proves pre_peer_conversation and
// post_peer_conversation — two of the ten hook events found alongside
// pre_tool_call (#2132) to be declared in hooks.AllEvents, accepted by the
// CLI/API, and reached by zero hooks.Dispatch call sites — now actually
// fire from QueryHandler.Create / finishQuery, the single entry point
// every `curl localhost:9119/query` peer question converges on.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
)

func newPeerQueryRig(t *testing.T) (h *QueryHandler, wsID string, body *bytes.Buffer) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		 VALUES ('crewX', ?, 'Eng', 'eng', 'free', 4096, 2.0)`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('from1', 'crewX', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('to1', 'crewX', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatX', 'from1', ?, 'CHAT', 'ACTIVE')`, wsID)

	h = NewQueryHandler(db, nil, nil, "token", newTestLogger())
	body = bytes.NewBufferString(`{"target_slug":"nela","question":"ping?","from_slug":"viktor","crew_id":"crewX","workspace_id":"` + wsID + `","chat_id":"chatX"}`)
	return
}

// TestQueryCreate_DispatchesPreAndPostPeerConversationHooks registers a
// webhook on each event (blocking only for the pre-event) and drives Create with a nil
// orchestrator (503, but Create still reaches finishQuery — see
// TestCovQCreateNilOrchEmitsJournal) so both hooks fire without needing a
// full agent run.
func TestQueryCreate_DispatchesPreAndPostPeerConversationHooks(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1

	h, wsID, body := newPeerQueryRig(t)

	seenCh := make(chan string, 2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCh <- r.URL.Path
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	for _, ev := range []hooks.Event{hooks.EventPrePeerConversation, hooks.EventPostPeerConversation} {
		if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
			WorkspaceID:   wsID,
			Event:         ev,
			HandlerKind:   hooks.HandlerKindHTTP,
			HandlerConfig: map[string]any{"url": ts.URL + "/" + string(ev)},
			Blocking:      ev == hooks.EventPrePeerConversation,
			Enabled:       true,
		}, false); err != nil {
			t.Fatalf("register %s hook: %v", ev, err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (nil orchestrator), got %d; body=%s", w.Code, w.Body.String())
	}
	seen := make([]string, 0, 2)
	for len(seen) < 2 {
		select {
		case path := <-seenCh:
			seen = append(seen, path)
		case <-time.After(time.Second):
			t.Fatalf("hook hits = %v, want exactly [pre_peer_conversation, post_peer_conversation]", seen)
		}
	}
	if seen[0] != "/pre_peer_conversation" || seen[1] != "/post_peer_conversation" {
		t.Errorf("hook order = %v, want [/pre_peer_conversation /post_peer_conversation]", seen)
	}
}

func TestFinishQuery_PersistsTerminalStateBeforePostHook(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")
	h, wsID, _ := newPeerQueryRig(t)
	execOrFatal(t, h.db, `INSERT INTO peer_conversations
		(id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		VALUES ('conv-order', ?, 'crewX', 'chatX', 'from1', 'to1', 'ping?', 'RUNNING', datetime('now'))`, wsID)

	entered := make(chan struct{})
	release := make(chan struct{})
	observedStatus := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		var status string
		if err := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM peer_conversations WHERE id = 'conv-order'`).Scan(&status); err != nil {
			status = "query-error: " + err.Error()
		}
		observedStatus <- status
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Simulate a row created before observation events were made
	// non-blocking. Dispatch must treat this legacy flag as configuration
	// history, not as permission for a post-event to gate finishQuery.
	execOrFatal(t, h.db, `INSERT INTO hooks_config
		(id, workspace_id, event, matcher, handler_kind, handler_config, blocking, enabled)
		VALUES ('legacy-post-peer', ?, 'post_peer_conversation', '{}', 'http', ?, 1, 1)`,
		wsID, `{"url":"`+ts.URL+`"}`)
	// The raw INSERT bypasses hooks.Register, so it also bypasses the
	// negative dispatch cache's invalidation (#2154). Every rig in this
	// package shares the same workspace id, so an earlier test that
	// dispatched post_peer_conversation with no hooks has cached "nothing
	// registered here" — and under -shuffle that test can run first, which
	// left this hook silently never firing.
	hooks.InvalidateCache(wsID)

	done := make(chan struct{})
	go func() {
		h.finishQuery(context.Background(), "conv-order", "", "chatX", "viktor", "nela",
			wsID, "crewX", "to1", "answer", "", time.Now().Add(-time.Millisecond), nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("post_peer_conversation blocked finishQuery before its database update")
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("post hook never started")
	}
	close(release)
	select {
	case status := <-observedStatus:
		if status != "COMPLETED" {
			t.Fatalf("post hook observed peer_conversations.status = %q, want COMPLETED", status)
		}
	case <-time.After(time.Second):
		t.Fatal("post hook did not report observed status")
	}
}

// TestQueryCreate_PrePeerConversationHookBlocksTheQuery proves a blocking
// pre_peer_conversation hook refuses the query before the
// peer_conversations row is even created.
func TestQueryCreate_PrePeerConversationHookBlocksTheQuery(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")

	h, wsID, body := newPeerQueryRig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503) // Block outcome
	}))
	defer ts.Close()

	if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
		WorkspaceID:   wsID,
		Event:         hooks.EventPrePeerConversation,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (blocked), got %d; body=%s", w.Code, w.Body.String())
	}
	var n int
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM peer_conversations WHERE to_agent_id = 'to1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected no peer_conversations row when pre_peer_conversation blocks, got %d", n)
	}
}

// TestQueryCreate_PrePeerConversationDispatchFailureIsA500NotA403 pins the
// other half of the gate contract. A *hooks.BlockedError is the caller
// being refused (403, with the handler's reason). A *hooks.DispatchError is
// the registry being unreadable or a handler being broken — our fault, not
// the caller's — so it must not come back as "you are not allowed", and the
// raw cause (which wraps a DB error) must not be echoed into the response
// body. Both still fail closed: an unevaluable gate is not a passed gate.
//
// Induced with a subagent hook and no SubagentHandler installed: the one
// handler kind that fails with a plain error and no network, so the
// DispatchError arm is deterministic.
func TestQueryCreate_PrePeerConversationDispatchFailureIsA500NotA403(t *testing.T) {
	h, wsID, body := newPeerQueryRig(t)

	if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
		WorkspaceID:   wsID,
		Event:         hooks.EventPrePeerConversation,
		HandlerKind:   hooks.HandlerKindSubagent,
		HandlerConfig: map[string]any{"agent_id": "nobody"},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a hook that could not be evaluated is not the caller being refused; body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "blocked by") {
		t.Errorf("body = %s — a dispatch failure is being reported as a policy block", w.Body.String())
	}

	// Still fails closed.
	var n int
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM peer_conversations WHERE to_agent_id = 'to1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("peer_conversations row created despite an unevaluable pre_peer_conversation gate (%d rows)", n)
	}
}

// TestQueryCreate_PrePeerConversationJoinedErrorReportsTheDispatchFailure
// covers the case that makes the ORDER of the two errors.As checks
// load-bearing rather than cosmetic.
//
// Dispatch runs blocking hooks in registration order, accumulating a
// DispatchError per hook that failed to run, and returns
// errors.Join(dispatchErrs..., blocked) as soon as a LATER hook blocks. That
// joined error satisfies errors.As for BOTH types. Asking about BlockedError
// first therefore answers 403 — a tidy policy refusal — while throwing away
// the fact that another hook never executed at all. The operator sees a
// policy they can find and not the broken handler they cannot.
//
// Two hooks, in registration order (ListByEvent sorts by created_at, id):
// a subagent hook with no handler installed, which fails, then an HTTP hook
// that blocks. Both blocking, so both run in the synchronous pass.
func TestQueryCreate_PrePeerConversationJoinedErrorReportsTheDispatchFailure(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")

	h, wsID, body := newPeerQueryRig(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503) // Block outcome
	}))
	defer ts.Close()

	// Registered first: fails to run (no SubagentHandler installed).
	if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
		ID:            "hk_aaa_broken",
		WorkspaceID:   wsID,
		Event:         hooks.EventPrePeerConversation,
		HandlerKind:   hooks.HandlerKindSubagent,
		HandlerConfig: map[string]any{"agent_id": "nobody"},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register broken hook: %v", err)
	}
	// Registered second: blocks.
	if _, err := hooks.Register(context.Background(), h.db, hooks.Hook{
		ID:            "hk_bbb_blocks",
		WorkspaceID:   wsID,
		Event:         hooks.EventPrePeerConversation,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register blocking hook: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a hook that could not run is present in the joined error and must win over the block; body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "blocked by") {
		t.Errorf("body = %s — reported as a policy refusal while a hook never ran", w.Body.String())
	}
}
