package api

// Local browser harness: isolated migrated SQLite + real authentication/router/WS.
// Enabled only by an explicit output path; no live instance data or agent execution.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/groupchatnotify"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestWorkspaceConversationManualBrowser(t *testing.T) {
	output := os.Getenv("CHAT_BROWSER_HARNESS")
	if output == "" {
		t.Skip("set CHAT_BROWSER_HARNESS to enable the isolated browser fixture")
	}
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	seedCrewRow(t, db, "browser-crew", workspace, "Browser crew", "browser-crew")
	seedAgentRow(t, db, "browser-agent", workspace, "browser-crew", "Ava", "ava", "AGENT")
	seedAgentRow(t, db, "browser-agent-theo", workspace, "browser-crew", "Theo", "theo", "AGENT")
	seedChatForDelete(t, &AgentHandler{db: db}, workspace, "browser-agent", "browser-ava-session", owner)
	if _, err := db.Exec(`UPDATE chats SET title='Ava browser history' WHERE id='browser-ava-session'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversation_messages SET content='Existing Ava session history' WHERE id='browser-ava-session-m1'`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "bob"} {
		if _, err := db.Exec(`INSERT INTO users(id,email,full_name)VALUES(?,?,?)`, u, u+"@example.test", u); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role)VALUES(?,?,?,'MEMBER')`, u, workspace, u); err != nil {
			t.Fatal(err)
		}
	}
	secret := "browser-test-only-secret-32chars!!"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	ss := sessions.NewDBStore(db)
	hub := ws.NewHub(newTestLogger(), nil, validator, ss)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(db))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); hub.Run(ctx) }()
	defer func() { cancel(); <-done }()
	// Legacy history reads go through the real Unix IPC proxy and JSONL store;
	// no execution endpoints or orchestration engine are installed here.
	legacyHistory := conversation.NewStore(t.TempDir(), newTestLogger())
	t.Cleanup(legacyHistory.Close)
	if err := legacyHistory.Append(ctx, "browser-ava-session", conversation.Message{ID: "browser-ava-session-m1", AgentID: "browser-agent", Role: conversation.RoleUser, Content: "Existing Ava session history"}); err != nil {
		t.Fatal(err)
	}
	ipc := http.NewServeMux()
	ipc.HandleFunc("GET /chats/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		messages, err := legacyHistory.Read(r.Context(), r.PathValue("id"), 0, 0)
		if err != nil {
			t.Error(err)
			http.Error(w, "history unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"chat_id": r.PathValue("id"), "messages": messages})
	})
	socketPath := newUnixIPCServer(t, ipc)
	router, err := NewRouter(db, secret, newTestLogger(), WithHub(hub), WithSocketPath(socketPath))
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := groupchatnotify.New(db, hub, newTestLogger())
	ndone := make(chan struct{})
	go func() { defer close(ndone); dispatcher.Run(ctx) }()
	defer func() { cancel(); <-ndone }()
	root, err := filepath.Abs("../../out")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	mux.Handle("/", StaticFileHandler(os.DirFS(root)))
	server := httptest.NewServer(mux)
	defer server.Close()
	tokens := map[string]string{}
	for _, u := range []string{owner, "alice", "bob"} {
		s, err := ss.Create(ctx, u, "browser", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		token, err := validator.IssueAccessToken(u, s.ID, u, u+"@example.test")
		if err != nil {
			t.Fatal(err)
		}
		tokens[u] = token
	}
	data, _ := json.Marshal(map[string]any{"fixture": "workspace-conversation-manual", "url": server.URL, "workspace_id": workspace, "tokens": tokens, "agent_session_id": "browser-ava-session", "agent_slug": "ava"})
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(output)
	defer os.Remove(output + ".stop")
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(output + ".stop"); err == nil {
				return
			}
		}
	}
}
