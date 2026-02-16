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
	mockResp := sessionResolveResponse{
		AgentID:      "agent-uuid-1",
		AgentSlug:    "claude-dev",
		TeamID:       "team-uuid-1",
		TeamSlug:     "engineering",
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
		if r.URL.Path != "/api/v1/internal/sessions/session-123/resolve" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Internal-Token") != "crewshipd" {
			t.Fatal("missing internal token header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResp)
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, slog.Default())

	info, err := resolver.ResolveSession(context.Background(), "session-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.AgentID != "agent-uuid-1" {
		t.Errorf("expected agent_id 'agent-uuid-1', got %q", info.AgentID)
	}
	if info.AgentSlug != "claude-dev" {
		t.Errorf("expected agent_slug 'claude-dev', got %q", info.AgentSlug)
	}
	if info.TeamSlug != "engineering" {
		t.Errorf("expected team_slug 'engineering', got %q", info.TeamSlug)
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

func TestIPCResolverResolveSessionNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Session not found"})
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, slog.Default())

	_, err := resolver.ResolveSession(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestIPCResolverCreateSession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/sessions" {
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

		var body CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.SessionID != "sess-001" {
			t.Errorf("expected session_id 'sess-001', got %q", body.SessionID)
		}
		if body.AgentID != "agent-1" {
			t.Errorf("expected agent_id 'agent-1', got %q", body.AgentID)
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "sess-001", "status": "created"})
	}))
	defer ts.Close()

	resolver := NewIPCResolver(ts.URL, slog.Default())

	err := resolver.CreateSession(context.Background(), CreateSessionRequest{
		SessionID: "sess-001",
		AgentID:   "agent-1",
		OrgID:     "org-1",
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

	resolver := NewIPCResolver(ts.URL, slog.Default())

	err := resolver.CreateSession(context.Background(), CreateSessionRequest{
		SessionID: "sess-001",
		AgentID:   "agent-1",
		OrgID:     "org-1",
	})
	if err == nil {
		t.Fatal("expected error for server error")
	}
}
