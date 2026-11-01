package chatbridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Keep the real IPC ownership handshake and terminal serializer on the real
// Bridge/orchestrator path; only the remote API and container are fixtures.
type hostChatOutcomeResolver struct{ *ipcOutcomeResolver }

func (r *hostChatOutcomeResolver) CreateRun(ctx context.Context, id, agent, chat, workspace, trigger string, metadata map[string]interface{}) error {
	return r.ipc.CreateRun(ctx, id, agent, chat, workspace, trigger, metadata)
}
func (r *hostChatOutcomeResolver) takeHostChatWork(id string) bool { return r.ipc.takeHostChatWork(id) }
func (r *hostChatOutcomeResolver) hostChatPhase(ctx context.Context, id, phase string) error {
	return r.ipc.hostChatPhase(ctx, id, phase)
}

func TestHostChatConfirmedStopCarriesFencedCompletionProof(t *testing.T) {
	var mu sync.Mutex
	var admitted, requested bool
	var terminal *hostChatProof
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID           string         `json:"id"`
			Status       string         `json:"status"`
			HostChatWork bool           `json:"host_chat_work"`
			Proof        *hostChatProof `json:"host_chat_proof"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			admitted = request.HostChatWork
			_ = json.NewEncoder(w).Encode(map[string]string{"id": request.ID, "host_chat_work": "v1"})
			return
		}
		if r.Method != http.MethodPatch {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(400)
			return
		}
		if request.Status == "RUNNING" {
			if request.Proof != nil && request.Proof.Phase == "request" {
				requested = true
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": path.Base(r.URL.Path), "status": "RUNNING"})
			return
		}
		terminal = request.Proof
		// The API cannot acknowledge a host-owned CANCELLED projection from an
		// observer cancellation or a timestamp; it needs actual provider exit proof.
		if terminal == nil || terminal.Phase != "finish" || terminal.ObservedExit == nil || *terminal.ObservedExit != 143 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": path.Base(r.URL.Path), "status": "CANCELLED"})
	}))
	defer server.Close()
	info := baseInfo()
	info.HumanAuthority = &chataudience.Receipt{UserID: "user-1", ChatID: "host-stop-proof", WorkspaceID: info.WorkspaceID, AgentID: info.AgentID}
	resolver := &hostChatOutcomeResolver{&ipcOutcomeResolver{capResolver: &capResolver{info: info}, ipc: NewIPCResolver(server.URL, "synthetic-host-token", slog.Default())}}
	bridge := testBridgeWithContainer(t, resolver, &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0})
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	err := bridge.HandleChatMessage(ctx, "user-1", "host-stop-proof", "hi", func(e ws.ChatEvent) {
		if e.Type == "text" {
			cancel(ws.ErrExplicitStopRequested)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if !admitted || !requested {
		t.Fatalf("host admission/creation gate not exercised: admitted=%v requested=%v err=%v", admitted, requested, err)
	}
	if terminal == nil || terminal.Phase != "finish" {
		t.Fatalf("direct outcome update lost fenced host completion proof: %+v err=%v", terminal, err)
	}
	if terminal.ObservedExit == nil || *terminal.ObservedExit != 143 {
		t.Fatalf("confirmed provider stop lost actual exit proof: %+v err=%v", terminal, err)
	}
	if !errors.Is(err, ws.ErrRunCancelled) {
		t.Fatalf("confirmed host stop not acknowledged: %v", err)
	}
}
