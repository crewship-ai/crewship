package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

// Cross-tenant status-code shape for GET /api/v1/chats/{chatId}/stream.
//
// CodeRabbit's finding on PR #1822: the only negative coverage was an invented
// chat id, which exercises the "no such row" arm of the authorizer and nothing
// else. An implementation that answers 404 for a nonexistent chat but 403 for
// an existing chat in ANOTHER tenant passes that test while leaking chat
// existence across the tenant boundary — a caller can enumerate ids and read
// the status code as an oracle.
//
// These tests use the REAL production authorizer (ws.DBChannelAuthorizer)
// against a real database with two workspaces, so the two denial paths are
// genuinely distinct at the data layer and only the response can collapse
// them. Both must be indistinguishable to the caller.

// twoTenantFixture builds: workspace A with user A and a chat, workspace B
// with user B. Neither user is a member of the other's workspace.
type twoTenantFixture struct {
	db      *sql.DB
	userA   string
	userB   string
	chatInA string
}

func newTwoTenantFixture(t *testing.T) twoTenantFixture {
	t.Helper()
	db := setupTestDB(t)

	f := twoTenantFixture{
		db:      db,
		userA:   "u-tenant-a",
		userB:   "u-tenant-b",
		chatInA: "cchat0000000000tenanta",
	}
	wsA, wsB := "ws-tenant-a", "ws-tenant-b"
	agentA := "agent-tenant-a"

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}

	exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, f.userA, "a@tenant-a.test", "Tenant A")
	exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, f.userB, "b@tenant-b.test", "Tenant B")
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, wsA, "Tenant A", "tenant-a")
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, wsB, "Tenant B", "tenant-b")
	// Each user is a member of their OWN workspace only. That is the whole
	// point of the fixture: user B is authenticated and legitimate, and simply
	// has no business seeing workspace A's chat.
	exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`, "wm-a", wsA, f.userA)
	exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`, "wm-b", wsB, f.userB)
	exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`, agentA, wsA, "Agent A", "agent-a")
	exec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`, f.chatInA, agentA, wsA)

	return f
}

// streamStatusAs issues the request as userID and returns the status code.
// The handler is wired to the REAL authorizer so the tenancy decision is the
// production one, not a stub.
func (f twoTenantFixture) streamStatusAs(t *testing.T, userID, chatID string) int {
	t.Helper()
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.db))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	h := NewRunStreamHandler(hub, newTestLogger())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/chats/{chatId}/stream", func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), ctxUser, &AuthUser{ID: userID}))
		h.Stream(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// idle=1 so the sanity case (an authorized caller) cannot hang the test
	// waiting for a run that will never start.
	resp, err := http.Get(srv.URL + "/api/v1/chats/" + chatID + "/stream?idle=1")
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	// Drain the body so the handler returns (and detaches its observer) before
	// the test server is closed by the deferred Close above.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// The core property: an existing chat in another tenant and a chat that does
// not exist must be INDISTINGUISHABLE. A 403 on the first would confirm the id
// is real in somebody else's workspace.
func TestRunStream_CrossTenantChatIsIndistinguishableFromMissing(t *testing.T) {
	f := newTwoTenantFixture(t)

	existsElsewhere := f.streamStatusAs(t, f.userB, f.chatInA)
	neverExisted := f.streamStatusAs(t, f.userB, "cchat0000000000notreal")

	if existsElsewhere != http.StatusNotFound {
		t.Errorf("status for an EXISTING chat in another tenant = %d, want 404; %d leaks that the chat exists",
			existsElsewhere, existsElsewhere)
	}
	if neverExisted != http.StatusNotFound {
		t.Errorf("status for a nonexistent chat = %d, want 404", neverExisted)
	}
	if existsElsewhere != neverExisted {
		t.Fatalf("cross-tenant chat answered %d but a nonexistent one answered %d — "+
			"the difference is an existence oracle a caller can enumerate against",
			existsElsewhere, neverExisted)
	}
}

// The denial must be real, not an artefact of the fixture: the chat's own
// owner has to be served, or the test above would pass on a handler that 404s
// unconditionally.
func TestRunStream_OwnerOfTheChatIsServed(t *testing.T) {
	f := newTwoTenantFixture(t)

	if got := f.streamStatusAs(t, f.userA, f.chatInA); got != http.StatusOK {
		t.Fatalf("status for the chat's own workspace member = %d, want 200 — "+
			"the cross-tenant test proves nothing if nobody can read this chat", got)
	}
}

// Losing membership is the real-world trigger, and it must flip the answer to
// the same 404 an outsider gets — no separate "you used to be allowed" status.
func TestRunStream_RemovedMemberGetsTheSame404(t *testing.T) {
	f := newTwoTenantFixture(t)

	if got := f.streamStatusAs(t, f.userA, f.chatInA); got != http.StatusOK {
		t.Fatalf("precondition: owner status = %d, want 200", got)
	}
	if _, err := f.db.Exec(`DELETE FROM workspace_members WHERE user_id = ?`, f.userA); err != nil {
		t.Fatal(err)
	}
	if got := f.streamStatusAs(t, f.userA, f.chatInA); got != http.StatusNotFound {
		t.Fatalf("status after removal from the workspace = %d, want 404", got)
	}
}
