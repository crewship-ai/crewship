package api

import (
	"context"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/work"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Exercise the real IPC start used by a normal CLI/WebSocket chat turn. No
// work_items/work_attempts authorization row is inserted by this fixture.
func TestNormalChatIPCRegistersCurrentMemoryAttempt(t *testing.T) {
	h, ws, agent := covIRunFixture(t)
	wireTestJournalForHandler(t, h.db, h)
	var user string
	if err := h.db.QueryRowContext(t.Context(), `SELECT user_id FROM workspace_members WHERE workspace_id=? LIMIT 1`, ws).Scan(&user); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('memory-cli-chat',?,?,?,'private')`, ws, agent, user)
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "memory-cli-chat", user, nil)
	if err != nil || receipt == nil {
		t.Fatalf("human admission: %+v %v", receipt, err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/internal/runs", h.requireInternal(http.HandlerFunc(h.CreateRun)))
	server := httptest.NewServer(mux)
	defer server.Close()
	resolver := chatbridge.NewIPCResolver(server.URL, "tok", newTestLogger())
	ctx := runauthority.WithTriggerMessage(chataudience.WithReceipt(context.Background(), receipt), "memory-cli-message")
	ctx = chatbridge.WithHostChatWorkAdmission(ctx)
	const run = "msg_memory_cli_current"
	if err := resolver.CreateRun(ctx, run, agent, "memory-cli-chat", ws, "USER", nil); err != nil {
		t.Fatal(err)
	}
	memoryHandler := NewMemoryMutationHandler(h.db, storageDir(t), storageDir(t), newTestLogger(), "tok")
	generation, err := memoryHandler.attemptGeneration(t.Context(), run)
	if err != nil {
		t.Fatalf("normal CLI turn has no memory-authorized attempt: %v", err)
	}
	if generation <= 0 {
		t.Fatalf("invalid current generation %d", generation)
	}
	if err := memoryHandler.authorizeRun(ws, agent, run, generation)(t.Context()); err != nil {
		t.Fatalf("normal current turn refused: %v", err)
	}
	if err := memoryHandler.authorizeRun("foreign-workspace", agent, run, generation)(t.Context()); err == nil {
		t.Fatal("foreign workspace acquired memory authority")
	}
	if err := memoryHandler.authorizeRun(ws, "foreign-agent", run, generation)(t.Context()); err == nil {
		t.Fatal("foreign agent acquired memory authority")
	}
	if err := memoryHandler.authorizeRun(ws, agent, run, generation+1)(t.Context()); err == nil {
		t.Fatal("wrong generation acquired memory authority")
	}
}

func TestNormalChatLifecycleProofFencedAndRevocationAtCreation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke bool
		status string
		exit   int
	}{
		{"normal", false, "COMPLETED", 0}, {"confirmed-stop", false, "CANCELLED", 143}, {"revoked", true, "COMPLETED", 0},
	} {
		revoke := tc.revoke
		t.Run(tc.name, func(t *testing.T) {
			h, ws, agent := covIRunFixture(t)
			wireTestJournalForHandler(t, h.db, h)
			var user string
			if err := h.db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id=? LIMIT 1`, ws).Scan(&user); err != nil {
				t.Fatal(err)
			}
			execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('proof-chat',?,?,?,'private')`, ws, agent, user)
			receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "proof-chat", user, nil)
			if err != nil || receipt == nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.Handle("POST /api/v1/internal/runs", h.requireInternal(http.HandlerFunc(h.CreateRun)))
			mux.Handle("PATCH /api/v1/internal/runs/{runId}", h.requireInternal(http.HandlerFunc(h.UpdateRun)))
			server := httptest.NewServer(mux)
			defer server.Close()
			resolver := chatbridge.NewIPCResolver(server.URL, "tok", newTestLogger())
			ctx := chatbridge.WithHostChatWorkAdmission(runauthority.WithTriggerMessage(chataudience.WithReceipt(t.Context(), receipt), "proof-message"))
			const run = "msg_proof_current"
			if err = resolver.CreateRun(ctx, run, agent, "proof-chat", ws, "USER", nil); err != nil {
				t.Fatal(err)
			}
			phase := func(token, status, name string, exit *int) int {
				request := httptest.NewRequest("PATCH", "/api/v1/internal/runs/"+run, jsonBody(map[string]any{"status": status, "completed_at": time.Now().UTC().Format(time.RFC3339Nano), "host_chat_proof": map[string]any{"phase": name, "observed_exit": exit}}))
				request.RemoteAddr = "127.0.0.1:12345"
				request.Header.Set("X-Internal-Token", token)
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				return response.Code
			}
			if got := phase(internaltoken.DeriveWorkspaceToken("tok", ws), "RUNNING", "request", nil); got != 403 {
				t.Fatalf("derived token lifecycle status=%d", got)
			}
			if revoke {
				execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user)
			}
			expected := 200
			if revoke {
				expected = 403
			}
			if got := phase("tok", "RUNNING", "request", nil); got != expected {
				t.Fatalf("creation gate status=%d expected=%d", got, expected)
			}
			binding, _, err := work.NewStore(h.db).HostChatAttempt(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			if revoke {
				if binding.Phase != "starting" {
					t.Fatalf("revoked human created phase %s", binding.Phase)
				}
				return
			}
			if binding.Phase != "requested" {
				t.Fatalf("creation not durably fenced: %+v", binding)
			}
			if got := phase("tok", "RUNNING", "request", nil); got != 409 {
				t.Fatalf("duplicate creation status=%d", got)
			}
			if got := phase("tok", "RUNNING", "started", nil); got != 200 {
				t.Fatalf("actual creation status=%d", got)
			}
			zero := tc.exit
			if got := phase("tok", tc.status, "finish", &zero); got != 200 {
				t.Fatalf("actual exit status=%d", got)
			}
			projection, owned, err := work.NewStore(h.db).RunProjection(t.Context(), run)
			if err != nil || !owned || !projection.Ready || projection.Status != tc.status {
				t.Fatalf("actual host terminal projection: %+v %v", projection, err)
			}
			memoryHandler := NewMemoryMutationHandler(h.db, storageDir(t), storageDir(t), newTestLogger(), "tok")
			if err = memoryHandler.authorizeRun(ws, agent, run, binding.Generation)(t.Context()); err == nil {
				t.Fatal("ended run retained memory authority")
			}
		})
	}
}
