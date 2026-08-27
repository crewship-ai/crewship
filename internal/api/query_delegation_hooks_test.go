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
	"testing"

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
// blocking webhook on each event and drives Create with a nil
// orchestrator (503, but Create still reaches finishQuery — see
// TestCovQCreateNilOrchEmitsJournal) so both hooks fire without needing a
// full agent run.
func TestQueryCreate_DispatchesPreAndPostPeerConversationHooks(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1

	h, wsID, body := newPeerQueryRig(t)

	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
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
			Blocking:      true,
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
	if len(seen) != 2 {
		t.Fatalf("hook hits = %v, want exactly [pre_peer_conversation, post_peer_conversation]", seen)
	}
	if seen[0] != "/pre_peer_conversation" || seen[1] != "/post_peer_conversation" {
		t.Errorf("hook order = %v, want [/pre_peer_conversation /post_peer_conversation]", seen)
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
