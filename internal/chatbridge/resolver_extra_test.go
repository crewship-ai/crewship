package chatbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder captures every request the resolver makes to the test server.
type recorder struct {
	method  string
	path    string
	headers http.Header
	body    []byte
}

func (r *recorder) capture(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.method = req.Method
	// Use EscapedPath so tests can assert on URL-encoded segments (the raw
	// `RequestURI` would also work, but EscapedPath is path-only).
	r.path = req.URL.EscapedPath()
	r.headers = req.Header.Clone()
	r.body = body
}

// ---------- CreateRun ----------

func TestCreateRunSendsExpectedRequest(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ts.Close)

	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	meta := map[string]interface{}{"foo": "bar"}
	if err := r.CreateRun(context.Background(), "run-1", "agent-1", "chat-1", "ws-1", "USER", meta); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.method)
	}
	if rec.path != "/api/v1/internal/runs" {
		t.Errorf("path = %s", rec.path)
	}
	if rec.headers.Get("X-Internal-Token") != "tok" {
		t.Error("missing X-Internal-Token header")
	}
	if rec.headers.Get("Content-Type") != "application/json" {
		t.Error("missing JSON content-type")
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["id"] != "run-1" || payload["agent_id"] != "agent-1" || payload["chat_id"] != "chat-1" {
		t.Errorf("unexpected payload: %+v", payload)
	}
	if payload["metadata"] == nil {
		t.Error("metadata should be propagated")
	}
}

func TestCreateRunNilMetadataOmitted(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ts.Close)

	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if err := r.CreateRun(context.Background(), "r1", "a1", "c1", "w1", "USER", nil); err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if _, ok := payload["metadata"]; ok {
		t.Error("metadata key must be absent when nil")
	}
}

func TestCreateRunServerError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"db down"}`))
	}))
	t.Cleanup(ts.Close)

	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	err := r.CreateRun(context.Background(), "r1", "a1", "c1", "w1", "USER", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status code in error, got %v", err)
	}
}

func TestCreateRunNetworkError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default()) // closed port
	err := r.CreateRun(context.Background(), "r1", "a1", "c1", "w1", "USER", nil)
	if err == nil {
		t.Fatal("expected network error")
	}
}

// ---------- UpdateRun ----------

func TestUpdateRunIncludesAllFields(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	exit := 0
	errMsg := "boom"
	meta := map[string]interface{}{"duration_ms": float64(42)}
	if err := r.UpdateRun(context.Background(), "run-with/slash", "FAILED", &exit, &errMsg, meta); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", rec.method)
	}
	// runID with slash must be path-escaped.
	if !strings.HasSuffix(rec.path, "/runs/run-with%2Fslash") {
		t.Errorf("path not escaped: %s", rec.path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if payload["status"] != "FAILED" {
		t.Errorf("status: %v", payload["status"])
	}
	// JSON numbers decode as float64.
	if got, _ := payload["exit_code"].(float64); got != 0 {
		t.Errorf("exit_code: %v", payload["exit_code"])
	}
	if payload["error_message"] != "boom" {
		t.Errorf("error_message: %v", payload["error_message"])
	}
	if payload["metadata"] == nil {
		t.Error("metadata missing")
	}
}

func TestUpdateRunOmitsNilOptionals(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if err := r.UpdateRun(context.Background(), "r1", "RUNNING", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	for _, key := range []string{"exit_code", "error_message", "metadata"} {
		if _, ok := payload[key]; ok {
			t.Errorf("%s should be omitted when nil", key)
		}
	}
}

func TestUpdateRunServerError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if err := r.UpdateRun(context.Background(), "r1", "OK", nil, nil, nil); err == nil {
		t.Fatal("expected error")
	}
}

// ---------- IncrementMessageCount ----------

func TestIncrementMessageCount(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if err := r.IncrementMessageCount(context.Background(), "chat with space", 3); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch {
		t.Errorf("method = %s", rec.method)
	}
	if !strings.Contains(rec.path, "/chats/chat%20with%20space/message-count") {
		t.Errorf("path not escaped: %s", rec.path)
	}
	var payload map[string]int
	if err := json.Unmarshal(rec.body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if payload["delta"] != 3 {
		t.Errorf("delta: %d", payload["delta"])
	}
}

func TestIncrementMessageCountServerError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if err := r.IncrementMessageCount(context.Background(), "c1", 1); err == nil {
		t.Fatal("expected error")
	}
}

// ---------- UpdateChatTitle ----------

func TestUpdateChatTitle(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if err := r.UpdateChatTitle(context.Background(), "c1", "hello"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch {
		t.Errorf("method = %s", rec.method)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if payload["title"] != "hello" {
		t.Errorf("title: %q", payload["title"])
	}
}

func TestUpdateChatTitleServerError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if err := r.UpdateChatTitle(context.Background(), "c1", "h"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------- ResolveAgent ----------

func TestResolveAgentSuccess(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:    "a1",
		AgentSlug:  "alice",
		CrewID:     "c1",
		CLIAdapter: "CLAUDE_CODE",
	}
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.capture(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)

	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveAgent(context.Background(), "a1", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/api/v1/internal/agents/a1/resolve" {
		t.Errorf("path: %s", rec.path)
	}
	if info.AgentSlug != "alice" {
		t.Errorf("slug: %q", info.AgentSlug)
	}
}

func TestResolveAgentNotFound(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if _, err := r.ResolveAgent(context.Background(), "missing", ""); err == nil {
		t.Fatal("expected error")
	}
}

// ---------- GetWebhookSecret ----------

func TestGetWebhookSecretSuccess(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webhook_secret":"sek"}`))
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	got, err := r.GetWebhookSecret(context.Background(), "", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sek" {
		t.Errorf("secret: %q", got)
	}
}

func TestGetWebhookSecretMissingMapsToErr(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webhook_secret":""}`))
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	_, err := r.GetWebhookSecret(context.Background(), "", "a1")
	if !errors.Is(err, ErrNoWebhookSecret) {
		t.Errorf("err = %v, want ErrNoWebhookSecret", err)
	}
}

func TestGetWebhookSecretBadStatus(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if _, err := r.GetWebhookSecret(context.Background(), "", "a1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetWebhookSecretBadJSON(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if _, err := r.GetWebhookSecret(context.Background(), "", "a1"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------- ResolveChat malformed JSON / network failure ----------

func TestResolveChatBadJSON(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	if _, err := r.ResolveChat(context.Background(), "c1"); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestResolveChatNetworkFailure(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default()) // closed
	if _, err := r.ResolveChat(context.Background(), "c1"); err == nil {
		t.Fatal("expected network error")
	}
}

// ---------- Resolve enrichment paths ----------

// network_mode and allowed_domains defaults populate when empty.
func TestResolveChatDefaultsNetworkMode(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:    "a1",
		CLIAdapter: "CLAUDE_CODE",
		// NetworkMode and AllowedDomains intentionally zero
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveChat(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.NetworkMode != "free" {
		t.Errorf("default NetworkMode = %q, want free", info.NetworkMode)
	}
	if info.AllowedDomains == nil || len(info.AllowedDomains) != 0 {
		t.Errorf("AllowedDomains = %v, want empty slice", info.AllowedDomains)
	}
}

// ContainerEnv + RootPostStart parsed from devcontainer_config (string list form).
func TestResolveChatExtractsRuntimeFieldsFromDevcontainer(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:            "a1",
		CLIAdapter:         "CLAUDE_CODE",
		DevcontainerConfig: `{"containerEnv":{"FOO":"bar"},"postStartCommand":["echo","hi"]}`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveChat(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.ContainerEnv["FOO"] != "bar" {
		t.Errorf("ContainerEnv[FOO] = %q", info.ContainerEnv["FOO"])
	}
	if len(info.RootPostStart) != 2 {
		t.Errorf("RootPostStart = %v, want length 2", info.RootPostStart)
	}
}

// Unparseable devcontainer_config logs a warning and yields nil ContainerEnv.
func TestResolveChatBadDevcontainerNonFatal(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:            "a1",
		CLIAdapter:         "CLAUDE_CODE",
		DevcontainerConfig: `{not json`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveChat(context.Background(), "c1")
	if err != nil {
		t.Fatalf("should not propagate decode error, got: %v", err)
	}
	if info.ContainerEnv != nil {
		t.Errorf("ContainerEnv should be nil on decode failure, got %v", info.ContainerEnv)
	}
}

// MCP servers with stdio transport must use env_var_name as header.
func TestResolveChatStdioMCPUsesEnvVarName(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:    "a1",
		CLIAdapter: "CLAUDE_CODE",
		MCPServers: []mcpServerResponse{
			{
				ID:         "s1",
				Name:       "stdio-server",
				Transport:  "stdio",
				CredToken:  "secret",
				CredType:   "API_KEY",
				CredHeader: "Authorization",
				EnvVarName: "MY_TOKEN",
			},
			{
				ID:         "s2",
				Name:       "http-server",
				Transport:  "http",
				CredToken:  "secret2",
				CredType:   "API_KEY",
				CredHeader: "X-Auth",
			},
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveChat(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.MCPServers) != 2 {
		t.Fatalf("MCPServers = %d", len(info.MCPServers))
	}
	if info.MCPServers[0].Credential == nil || info.MCPServers[0].Credential.Header != "MY_TOKEN" {
		t.Errorf("stdio server should use env_var_name as header, got %+v", info.MCPServers[0].Credential)
	}
	if info.MCPServers[1].Credential == nil || info.MCPServers[1].Credential.Header != "X-Auth" {
		t.Errorf("http server should keep cred_header, got %+v", info.MCPServers[1].Credential)
	}
}

// Parses crew members + nested integrations.
func TestResolveChatParsesCrewMembersAndIntegrations(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:    "a1",
		CLIAdapter: "CLAUDE_CODE",
		CrewMembers: []crewMemberResponse{
			{
				ID:   "m1",
				Slug: "viktor",
				Integrations: []memberIntegrationResponse{
					{Name: "github", ServerName: "gh", Tools: []string{"create_pr"}},
				},
			},
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveChat(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.CrewMembers) != 1 || info.CrewMembers[0].Slug != "viktor" {
		t.Errorf("CrewMembers = %+v", info.CrewMembers)
	}
	if len(info.CrewMembers[0].Integrations) != 1 ||
		info.CrewMembers[0].Integrations[0].Name != "github" {
		t.Errorf("CrewMembers integrations = %+v", info.CrewMembers[0].Integrations)
	}
}

// CachedRequirements ContainerEnv merges with devcontainer ContainerEnv,
// devcontainer wins on conflict.
func TestResolveChatCachedRequirementsMerge(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:            "a1",
		CLIAdapter:         "CLAUDE_CODE",
		DevcontainerConfig: `{"containerEnv":{"FOO":"root"}}`,
		CachedRequirements: `{"containerEnv":{"FOO":"feature","BAR":"baz"}}`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveChat(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.CachedRequirements == nil {
		t.Fatal("CachedRequirements should be populated")
	}
	if info.ContainerEnv["FOO"] != "root" {
		t.Errorf("FOO = %q, want root (devcontainer wins)", info.ContainerEnv["FOO"])
	}
	if info.ContainerEnv["BAR"] != "baz" {
		t.Errorf("BAR = %q, want baz", info.ContainerEnv["BAR"])
	}
}

func TestResolveChatBadCachedRequirementsNonFatal(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:            "a1",
		CLIAdapter:         "CLAUDE_CODE",
		CachedRequirements: `{not json`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveChat(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.CachedRequirements != nil {
		t.Errorf("CachedRequirements should be nil after parse failure")
	}
}

// CreateChat fails when the request body cannot be built (closed port).
func TestCreateChatNetworkError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default())
	err := r.CreateChat(context.Background(), CreateChatRequest{ChatID: "c1", AgentID: "a1", WorkspaceID: "w1"})
	if err == nil {
		t.Fatal("expected network error")
	}
}
