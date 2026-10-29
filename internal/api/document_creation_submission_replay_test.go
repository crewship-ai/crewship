package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
	"golang.org/x/net/websocket"
)

type replayRuntime struct {
	provider.ContainerProvider
	starts atomic.Int32
}

func (r *replayRuntime) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	r.starts.Add(1)
	return "owned-submission-runtime", nil
}

type replayResolver struct {
	*chatbridge.IPCResolver
	f     *artifactPrincipalFixture
	calls atomic.Int32
}

func (r *replayResolver) ResolveHumanChat(ctx context.Context, user, chat string) (*chatbridge.ChatInfo, error) {
	receipt, err := chataudience.CaptureTrusted(ctx, r.f.rig.db, chat, user, chataudience.ReceiptFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &chatbridge.ChatInfo{HumanAuthority: receipt, AgentID: r.f.a.AgentID, AgentSlug: "principal-agent", CrewID: r.f.a.CrewID, CrewSlug: "principal-crew", WorkspaceID: r.f.a.WorkspaceID, Visibility: "private"}, nil
}
func (r *replayResolver) CreateRun(context.Context, string, string, string, string, string, map[string]any) error {
	r.calls.Add(1)
	return errors.New("owned stop before model execution")
}

// Production WS authentication/Hub/Bridge/SQLite/JSONL and the real CLI reader.
// Runtime provisioning and model execution are replaced at their boundaries;
// this test makes no claim of model output or a saved Document.
func TestDocumentCreationSubmissionReplayThroughWebSocketAndBinary(t *testing.T) {
	f, _ := documentCreationAdmissionFixture(t, true)
	mustExec(t, f.rig.db, `UPDATE workspaces SET slug='submission-replay' WHERE id=?`, f.a.WorkspaceID)
	dir := t.TempDir()
	store := conversation.NewStore(filepath.Join(dir, "transcript"), newTestLogger(), conversation.WithDB(f.rig.db))
	t.Cleanup(store.Close)
	socket := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chats/"+f.a.ChatID+"/messages" {
			http.NotFound(w, r)
			return
		}
		messages, err := store.Read(r.Context(), f.a.ChatID, 0, 0)
		if err != nil {
			http.Error(w, "read failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": messages})
	}))
	const secret = "owned-submission-replay-jwt-secret"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := sessions.NewDBStore(f.rig.db)
	hub := ws.NewHub(newTestLogger(), nil, validator, sessionStore)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.rig.db))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	router, err := NewRouter(f.rig.db, secret, newTestLogger(), WithSocketPath(socket), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", hub.HandleUpgrade)
	mux.Handle("/", router)
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	resolver := &replayResolver{IPCResolver: chatbridge.NewIPCResolver(host.URL, "owned-internal-token", newTestLogger()), f: f}
	runtime := &replayRuntime{}
	bridge := chatbridge.New(orchestrator.New(runtime, nil, newTestLogger()), runtime, store, nil, resolver, chatbridge.BridgeConfig{DocumentCreationEnabled: true}, newTestLogger())
	hub.SetChatHandler(bridge)
	session, err := sessionStore.Create(t.Context(), f.a.HumanUserID, "fixture", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := validator.IssueAccessToken(f.a.HumanUserID, session.ID, "Fixture", "fixture@example.test")
	if err != nil {
		t.Fatal(err)
	}
	connect := func() *websocket.Conn {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ws-token", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Workspace-ID", f.a.WorkspaceID)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		var ticket struct {
			Token string `json:"token"`
		}
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &ticket) != nil || ticket.Token == "" {
			t.Fatalf("WS ticket:%d %s", rr.Code, rr.Body.String())
		}
		conn, err := websocket.Dial(strings.Replace(host.URL, "http://", "ws://", 1)+"/ws", "", host.URL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if err := websocket.JSON.Send(conn, map[string]any{"type": "auth", "token": ticket.Token}); err != nil {
			t.Fatal(err)
		}
		if err := websocket.JSON.Send(conn, map[string]any{"type": "ping"}); err != nil {
			t.Fatal(err)
		}
		var authFrame map[string]any
		if err := websocket.JSON.Receive(conn, &authFrame); err != nil {
			t.Fatal(err)
		}
		if authFrame["type"] != "pong" {
			t.Fatalf("auth/ping failed:%v", authFrame)
		}
		return conn
	}
	packet := map[string]any{"type": "send_message", "payload": map[string]any{"session_id": f.a.ChatID, "content": "Write exactly one submission replay document", "metadata": map[string]any{"document_creation_intent": map[string]any{"submission_id": "owned-original-key", "drafting": true}}}}
	original := connect()
	if err := websocket.JSON.Send(original, packet); err != nil {
		t.Fatal(err)
	}
	// Consume and discard all original acknowledgement frames. Closing the
	// originating connection after this durable boundary models a lost reply.
	for {
		var frame ws.ServerMessage
		if err := websocket.JSON.Receive(original, &frame); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(frame.Payload)
		var event ws.ChatEvent
		_ = json.Unmarshal(raw, &event)
		if event.Type == "done" {
			break
		}
	}
	_ = original.Close()
	if resolver.calls.Load() != 1 {
		t.Fatalf("initial run attempts:%d", resolver.calls.Load())
	}
	starts := runtime.starts.Load()
	retry := connect()
	if err := websocket.JSON.Send(retry, packet); err != nil {
		t.Fatal(err)
	}
	for {
		var frame ws.ServerMessage
		if err := websocket.JSON.Receive(retry, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "run_begin" {
			t.Fatal("replay started a new transport run")
		}
		raw, _ := json.Marshal(frame.Payload)
		var event ws.ChatEvent
		_ = json.Unmarshal(raw, &event)
		if event.Type == "user_message" || event.Type == "done" {
			t.Fatalf("replay emitted duplicate/terminal event:%s", raw)
		}
		if frame.Type == "error" {
			if !strings.Contains(string(raw), "already accepted") {
				t.Fatalf("replay refusal:%s", raw)
			}
			break
		}
	}
	// Ordered ping is a barrier after the refusal. A synthetic done buffered
	// behind the error must be observed before pong and fail the test.
	if err := websocket.JSON.Send(retry, map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	for {
		var frame ws.ServerMessage
		if err := websocket.JSON.Receive(retry, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "pong" {
			break
		}
		raw, _ := json.Marshal(frame.Payload)
		var event ws.ChatEvent
		_ = json.Unmarshal(raw, &event)
		if event.Type == "done" || event.Type == "user_message" {
			t.Fatalf("late duplicate event:%s", raw)
		}
	}
	if resolver.calls.Load() != 1 || runtime.starts.Load() != starts {
		t.Fatal("replay touched runtime/run admission")
	}
	binary := filepath.Join(dir, "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = "../.."
	build.Env = nestedGoBuildEnv(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("actual binary build:%s %v", out, err)
	}
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\nworkspace: submission-replay\ntoken: %s\n", host.URL, bearer)), 0600); err != nil {
		t.Fatal(err)
	}
	read := func() []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, "chat", f.a.ChatID, "--format", "json")
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI transcript:%s %v", out, err)
		}
		return out
	}
	output := read()
	if strings.Count(string(output), "Write exactly one submission replay document") != 1 {
		t.Fatalf("reload not exactly one durable turn:%s", output)
	}
	// A still-connected author whose live membership loses trust cannot use
	// the retained submission to recover its original message identity.
	mustExec(t, f.rig.db, `UPDATE workspace_members SET access_mode='restricted',capabilities='[]',access_revision=access_revision+1 WHERE workspace_id=? AND user_id=?`, f.a.WorkspaceID, f.a.HumanUserID)
	allowed, authErr := ws.NewDBChannelAuthorizer(f.rig.db).CanSubscribe(t.Context(), f.a.HumanUserID, "session:"+f.a.ChatID)
	if authErr != nil || allowed {
		t.Fatalf("membership revocation was not effective: allowed=%v err=%v", allowed, authErr)
	}
	closedTransport := func(err error) bool {
		return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
	}
	_ = retry.SetDeadline(time.Now().Add(10 * time.Second))
	if err := websocket.JSON.Send(retry, packet); err != nil && !closedTransport(err) {
		t.Fatal(err)
	}
	var denied ws.ServerMessage
	var receiveErr error
	for {
		receiveErr = websocket.JSON.Receive(retry, &denied)
		if receiveErr != nil {
			if !closedTransport(receiveErr) {
				t.Fatal(receiveErr)
			}
			break
		}
		// A slow actual CLI build can queue the Hub's empty keepalive.
		// No chat/result/terminal frame is skipped by this loop.
		if denied.Type == "ping" && denied.Payload == nil && denied.Channel == "" && denied.Seq == 0 {
			continue
		}
		break
	}
	// The live authorization sweep may close the revoked socket before
	// either write or read. A confirmed closed socket exposes no retained
	// result bytes; timeouts and unrelated errors are not accepted.
	if receiveErr == nil {
		deniedRaw, _ := json.Marshal(denied)
		if denied.Type != "error" || strings.Contains(string(deniedRaw), "already accepted") || strings.Contains(string(deniedRaw), "Original message") {
			t.Fatalf("revoked replay:%s", deniedRaw)
		}
	}
	if resolver.calls.Load() != 1 || runtime.starts.Load() != starts {
		t.Fatal("revoked replay touched runtime")
	}
	messages, err := store.Read(t.Context(), f.a.ChatID, 0, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("revoked replay changed transcript:%d %v", len(messages), err)
	}
	if err := sessionStore.Revoke(t.Context(), session.ID, "owned regression"); err != nil {
		t.Fatal(err)
	}
	revoked := exec.CommandContext(t.Context(), binary, "chat", f.a.ChatID, "--format", "json")
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CREWSHIP_") {
			revoked.Env = append(revoked.Env, v)
		}
	}
	revoked.Env = append(revoked.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1")
	out, err := revoked.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "401") || strings.Contains(string(out), "Write exactly one submission replay document") {
		t.Fatalf("revoked CLI exposed transcript:%s %v", out, err)
	}
}
