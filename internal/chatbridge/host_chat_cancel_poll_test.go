package chatbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// blockingAgentContainer streams the start of a reply and then keeps the agent
// process alive until the run's own stop (tmux kill-session) reaches it, the
// way a long-running CLI turn behaves.
type blockingAgentContainer struct {
	scriptedContainer
	once    sync.Once
	pipeW   *io.PipeWriter
	killed  chan struct{}
	expired bool
}

func newBlockingAgentContainer() *blockingAgentContainer {
	return &blockingAgentContainer{killed: make(chan struct{})}
}

func (c *blockingAgentContainer) kill() {
	c.once.Do(func() {
		c.mu.Lock()
		c.stopObserved = true
		w := c.pipeW
		c.mu.Unlock()
		close(c.killed)
		if w != nil {
			_ = w.Close()
		}
	})
}

func (c *blockingAgentContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) > 0 {
		r, w := io.Pipe()
		c.mu.Lock()
		c.pipeW = w
		c.mu.Unlock()
		go func() {
			_, _ = io.WriteString(w, `{"type":"system","subtype":"init","model":"claude-test"}`+"\n"+
				`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"working"}}}`+"\n")
			select {
			case <-c.killed:
			case <-time.After(8 * time.Second):
				// Safety valve so a regression fails instead of hanging the
				// suite: the process "finishes" on its own, unstopped.
				c.mu.Lock()
				c.expired = true
				c.mu.Unlock()
				_ = w.Close()
			}
		}()
		return &provider.ExecResult{ExecID: "agent-exec", Reader: r}, nil
	}
	if strings.Contains(strings.Join(cfg.Cmd, " "), "tmux kill-session") {
		c.kill()
		return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader("ABSENT"))}, nil
	}
	return c.scriptedContainer.Exec(ctx, cfg)
}

func (c *blockingAgentContainer) ExecInspect(_ context.Context, id string) (bool, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id != "agent-exec" {
		return false, 0, nil
	}
	if c.stopObserved {
		return false, 143, nil
	}
	return !c.expired, 0, nil
}

// #4799: a cancel recorded through POST /work-items/{id}/cancel is only a
// durable request. The host that owns the chat execution must observe it and
// stop the process; nothing else owns host-chat attempts.
func TestHostChatWorkItemCancelStopsTheRunningExecution(t *testing.T) {
	var mu sync.Mutex
	var started, polled bool
	var terminal *hostChatProof
	var terminalStatus string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID           string         `json:"id"`
			Status       string         `json:"status"`
			HostChatWork bool           `json:"host_chat_work"`
			Proof        *hostChatProof `json:"host_chat_proof"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": request.ID, "host_chat_work": "v1"})
			return
		}
		if request.Status == "RUNNING" {
			reply := map[string]any{"id": path.Base(r.URL.Path), "status": "RUNNING"}
			if request.Proof != nil {
				switch request.Proof.Phase {
				case "started":
					started = true
				case "poll":
					polled = true
					// The work-item cancel was recorded once the process started.
					reply["cancel_requested"] = started
				}
			}
			_ = json.NewEncoder(w).Encode(reply)
			return
		}
		terminal, terminalStatus = request.Proof, request.Status
		if terminal == nil || terminal.Phase != "finish" || terminal.ObservedExit == nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": path.Base(r.URL.Path), "status": request.Status})
	}))
	defer server.Close()

	info := baseInfo()
	info.HumanAuthority = &chataudience.Receipt{UserID: "user-1", ChatID: "host-item-cancel", WorkspaceID: info.WorkspaceID, AgentID: info.AgentID}
	resolver := &hostChatOutcomeResolver{&ipcOutcomeResolver{capResolver: &capResolver{info: info}, ipc: NewIPCResolver(server.URL, "synthetic-host-token", slog.Default())}}
	container := newBlockingAgentContainer()
	bridge := testBridgeWithContainer(t, resolver, container)

	// The WebSocket context is never cancelled here: only the work-item
	// endpoint asked for the stop.
	err := bridge.HandleChatMessage(t.Context(), "user-1", "host-item-cancel", "hi", func(ws.ChatEvent) {})

	select {
	case <-container.killed:
	default:
		t.Fatalf("work-item cancel never stopped the running process (err=%v)", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !polled {
		t.Fatal("the host never asked whether its execution was cancelled")
	}
	if terminalStatus != "CANCELLED" || terminal == nil || terminal.ObservedExit == nil || *terminal.ObservedExit != 143 {
		t.Fatalf("cancelled turn did not settle as CANCELLED with the observed stop: status=%q proof=%+v err=%v", terminalStatus, terminal, err)
	}
	if !errors.Is(err, ws.ErrRunCancelled) {
		t.Fatalf("confirmed host stop not acknowledged: %v", err)
	}
}

// ipcSettlingResolver sends every terminal write through the real IPC
// client, so a cancelled settlement context fails the way it does in
// production instead of being ignored by the in-memory recorder.
type ipcSettlingResolver struct{ *hostChatOutcomeResolver }

func (r *ipcSettlingResolver) UpdateRun(ctx context.Context, id, status string, exit *int, message *string, metadata map[string]interface{}) error {
	return r.ipc.UpdateRun(ctx, id, status, exit, message, metadata)
}
func (r *ipcSettlingResolver) UpdateRunWithCompletion(ctx context.Context, id, status string, exit *int, message *string, metadata map[string]interface{}, completed string) error {
	return r.ipc.UpdateRunWithCompletion(ctx, id, status, exit, message, metadata, completed)
}

// slowSuccessContainer finishes the turn on its own after a delay, so a
// natural completion can race an in-flight cancel poll.
type slowSuccessContainer struct {
	scriptedContainer
	delay time.Duration
	mu2   sync.Mutex
	done  bool
}

func (c *slowSuccessContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) > 0 {
		r, w := io.Pipe()
		go func() {
			time.Sleep(c.delay)
			c.mu2.Lock()
			c.done = true
			c.mu2.Unlock()
			_, _ = io.WriteString(w, claudeSuccessOutput(0))
			_ = w.Close()
		}()
		return &provider.ExecResult{ExecID: "agent-exec", Reader: r}, nil
	}
	return c.scriptedContainer.Exec(ctx, cfg)
}

func (c *slowSuccessContainer) ExecInspect(_ context.Context, id string) (bool, int, error) {
	if id != "agent-exec" {
		return false, 0, nil
	}
	c.mu2.Lock()
	defer c.mu2.Unlock()
	return !c.done, 0, nil
}

// #4838 review: a cancel poll still in flight when the agent completes on its
// own must not cancel the settlement of that completed turn. The completed
// reply has to be persisted and the turn settled as COMPLETED.
func TestHostChatLateCancelPollDoesNotLoseACompletedTurn(t *testing.T) {
	var mu sync.Mutex
	var terminalStatus string
	var polled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     string         `json:"id"`
			Status string         `json:"status"`
			Proof  *hostChatProof `json:"host_chat_proof"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": request.ID, "host_chat_work": "v1"})
			return
		}
		if request.Status == "RUNNING" {
			reply := map[string]any{"id": path.Base(r.URL.Path), "status": "RUNNING"}
			if request.Proof != nil && request.Proof.Phase == "poll" {
				// The cancel was recorded while this poll was in flight; it
				// answers only after the agent has already completed.
				time.Sleep(1500 * time.Millisecond)
				mu.Lock()
				polled = true
				mu.Unlock()
				reply["cancel_requested"] = true
			}
			_ = json.NewEncoder(w).Encode(reply)
			return
		}
		mu.Lock()
		terminalStatus = request.Status
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": path.Base(r.URL.Path), "status": request.Status})
	}))
	defer server.Close()

	info := baseInfo()
	info.HumanAuthority = &chataudience.Receipt{UserID: "user-1", ChatID: "host-late-poll", WorkspaceID: info.WorkspaceID, AgentID: info.AgentID}
	resolver := &ipcSettlingResolver{&hostChatOutcomeResolver{&ipcOutcomeResolver{capResolver: &capResolver{info: info}, ipc: NewIPCResolver(server.URL, "synthetic-host-token", slog.Default())}}}
	// The first poll starts at ~2s and answers at ~3.5s; the agent completes at ~2.5s.
	bridge := testBridgeWithContainer(t, resolver, &slowSuccessContainer{delay: hostChatCancelPollInterval + 500*time.Millisecond})

	err := bridge.HandleChatMessage(t.Context(), "user-1", "host-late-poll", "hi", func(ws.ChatEvent) {})
	if err != nil {
		t.Fatalf("a naturally completed turn failed: %v", err)
	}
	mu.Lock()
	gotPoll, gotStatus := polled, terminalStatus
	mu.Unlock()
	if !gotPoll {
		t.Fatal("fixture did not race an in-flight poll against completion")
	}
	if gotStatus != "COMPLETED" {
		t.Fatalf("completed turn settled as %q, want COMPLETED", gotStatus)
	}
	msgs, rerr := bridge.convStore.Read(context.Background(), "host-late-poll", 0, 0)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var replied bool
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "Hello world") {
			replied = true
		}
	}
	if !replied {
		t.Fatalf("the completed reply was lost from history: %+v", msgs)
	}
}
