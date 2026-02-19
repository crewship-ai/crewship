package chatbridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPCResolverResolveSession(t *testing.T) {
	mockResp := chatResolveResponse{
		AgentID:      "agent-uuid-1",
		AgentSlug:    "claude-dev",
		CrewID:       "crew-uuid-1",
		CrewSlug:     "engineering",
		ContainerID:  "",
		CLIAdapter:   "CLAUDE_CODE",
		SystemPrompt: "You are a helpful assistant.",
		ToolProfile:  "CODING",
		Credentials: []credentialResponse{
			{ID: "cred-1", EnvVar: "ANTHROPIC_API_KEY", Value: "sk-ant-test", Priority: 0},
		},
		TimeoutSecs: 1800,
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/chats/chat-123/resolve" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Internal-Token") != "crewshipd" {
			t.Fatal("missing internal token header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResp)
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, "crewshipd", slog.Default())

	info, err := resolver.ResolveChat(context.Background(), "chat-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.AgentID != "agent-uuid-1" {
		t.Errorf("expected agent_id 'agent-uuid-1', got %q", info.AgentID)
	}
	if info.AgentSlug != "claude-dev" {
		t.Errorf("expected agent_slug 'claude-dev', got %q", info.AgentSlug)
	}
	if info.CrewSlug != "engineering" {
		t.Errorf("expected team_slug 'engineering', got %q", info.CrewSlug)
	}
	if info.CLIAdapter != "CLAUDE_CODE" {
		t.Errorf("expected cli_adapter 'CLAUDE_CODE', got %q", info.CLIAdapter)
	}
	if len(info.Credentials) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(info.Credentials))
	}
	if info.Credentials[0].EnvVarName != "ANTHROPIC_API_KEY" {
		t.Errorf("expected env_var 'ANTHROPIC_API_KEY', got %q", info.Credentials[0].EnvVarName)
	}
	if info.Credentials[0].PlainValue != "sk-ant-test" {
		t.Errorf("expected value 'sk-ant-test', got %q", info.Credentials[0].PlainValue)
	}
	if info.TimeoutSecs != 1800 {
		t.Errorf("expected timeout 1800, got %d", info.TimeoutSecs)
	}
}

func TestIPCResolverResolveChat_MemoryEnabled(t *testing.T) {
	mockResp := chatResolveResponse{
		AgentID:       "agent-mem-1",
		AgentSlug:     "jarmila",
		CrewID:        "crew-1",
		CrewSlug:      "ops",
		CLIAdapter:    "CLAUDE_CODE",
		SystemPrompt:  "You are Jarmila.",
		ToolProfile:   "CODING",
		TimeoutSecs:   1800,
		MemoryEnabled: true,
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResp)
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, "crewshipd", slog.Default())
	info, err := resolver.ResolveChat(context.Background(), "chat-mem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.MemoryEnabled {
		t.Error("expected MemoryEnabled=true")
	}
	if info.AgentSlug != "jarmila" {
		t.Errorf("expected agent_slug 'jarmila', got %q", info.AgentSlug)
	}
}

func TestIPCResolverResolveChat_MemoryDisabled(t *testing.T) {
	mockResp := chatResolveResponse{
		AgentID:       "agent-nomem",
		AgentSlug:     "basic",
		CLIAdapter:    "CODEX_CLI",
		MemoryEnabled: false,
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResp)
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, "crewshipd", slog.Default())
	info, err := resolver.ResolveChat(context.Background(), "chat-nomem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.MemoryEnabled {
		t.Error("expected MemoryEnabled=false")
	}
}

func TestIPCResolverResolveSessionNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Chat not found"})
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, "crewshipd", slog.Default())

	_, err := resolver.ResolveChat(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent chat")
	}
}

func TestIPCResolverCreateSession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/chats" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("X-Internal-Token") != "crewshipd" {
			t.Fatal("missing internal token header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("missing content-type header")
		}

		var body CreateChatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.ChatID != "chat-001" {
			t.Errorf("expected chat_id 'chat-001', got %q", body.ChatID)
		}
		if body.AgentID != "agent-1" {
			t.Errorf("expected agent_id 'agent-1', got %q", body.AgentID)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "chat-001", "status": "created"})
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, "crewshipd", slog.Default())

	err := resolver.CreateChat(context.Background(), CreateChatRequest{
		ChatID: "chat-001",
		AgentID:   "agent-1",
		WorkspaceID:     "org-1",
		UserID:    "user-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIPCResolverCreateSessionError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "DB error"})
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, "crewshipd", slog.Default())

	err := resolver.CreateChat(context.Background(), CreateChatRequest{
		ChatID: "chat-001",
		AgentID:   "agent-1",
		WorkspaceID:     "org-1",
	})
	if err == nil {
		t.Fatal("expected error for server error")
	}
}
