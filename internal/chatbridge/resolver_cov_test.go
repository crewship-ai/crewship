package chatbridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// badURLResolver returns a resolver whose baseURL contains an invalid
// percent-escape, so http.NewRequestWithContext fails before any network
// I/O. This is the only way to drive the "create request" error branches
// without touching production code.
func badURLResolver() *IPCResolver {
	return NewIPCResolver("http://127.0.0.1:0/%zz", "tok", slog.Default())
}

// ---------- convertInstalledSkills ----------

func TestConvertInstalledSkills(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []installedSkillEntry
		want []string // expected slugs in order
	}{
		{"nil input", nil, nil},
		{"empty input", []installedSkillEntry{}, nil},
		{
			"drops empty slug",
			[]installedSkillEntry{{Slug: "", Content: "body"}},
			[]string{},
		},
		{
			"drops empty content",
			[]installedSkillEntry{{Slug: "deploy", Content: ""}},
			[]string{},
		},
		{
			"keeps valid, preserves order",
			[]installedSkillEntry{
				{Slug: "deploy", Vendor: "acme", Content: "# deploy"},
				{Slug: "", Content: "ignored"},
				{Slug: "review", Content: "# review"},
			},
			[]string{"deploy", "review"},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := convertInstalledSkills(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i, slug := range tc.want {
				if got[i].Slug != slug {
					t.Errorf("got[%d].Slug = %q, want %q", i, got[i].Slug, slug)
				}
			}
		})
	}
}

func TestConvertInstalledSkillsPreservesVendorAndContent(t *testing.T) {
	t.Parallel()
	got := convertInstalledSkills([]installedSkillEntry{
		{Slug: "deploy", Vendor: "acme", Content: "# steps"},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Vendor != "acme" || got[0].Content != "# steps" {
		t.Errorf("bundle = %+v", got[0])
	}
}

// installed_skills flow end-to-end through resolve().
func TestResolveChatInstalledSkills(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:    "a1",
		CLIAdapter: "CLAUDE_CODE",
		InstalledSkills: []installedSkillEntry{
			{Slug: "deploy", Vendor: "acme", Content: "# deploy"},
			{Slug: "", Content: "dropped"},
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
	if len(info.InstalledSkills) != 1 || info.InstalledSkills[0].Slug != "deploy" {
		t.Errorf("InstalledSkills = %+v, want exactly the valid entry", info.InstalledSkills)
	}
}

// ---------- ResolveAgent workspace scope ----------

func TestResolveAgentSendsWorkspaceScope(t *testing.T) {
	t.Parallel()
	var gotQuery string
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.Query().Get("workspace_id")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatResolveResponse{AgentID: "a1", CLIAdapter: "CLAUDE_CODE"})
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	info, err := r.ResolveAgent(context.Background(), "agent/1", "ws 1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/internal/agents/agent%2F1/resolve" {
		t.Errorf("path = %q, agent id must be path-escaped", gotPath)
	}
	if gotQuery != "ws 1" {
		t.Errorf("workspace_id query = %q, want %q", gotQuery, "ws 1")
	}
	if info.AgentID != "a1" {
		t.Errorf("AgentID = %q", info.AgentID)
	}
}

// ---------- GetWebhookSecret crew scope ----------

func TestGetWebhookSecretSendsCrewScope(t *testing.T) {
	t.Parallel()
	var gotCrew string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCrew = r.URL.Query().Get("crew_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webhook_secret":"sek"}`))
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	got, err := r.GetWebhookSecret(context.Background(), "crew 1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sek" {
		t.Errorf("secret = %q", got)
	}
	if gotCrew != "crew 1" {
		t.Errorf("crew_id query = %q, want %q", gotCrew, "crew 1")
	}
}

func TestGetWebhookSecretNetworkError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default()) // closed port
	if _, err := r.GetWebhookSecret(context.Background(), "", "a1"); err == nil {
		t.Fatal("expected network error")
	}
}

// ---------- request-creation (bad base URL) error branches ----------

func TestRequestCreationErrorsOnBadBaseURL(t *testing.T) {
	t.Parallel()
	r := badURLResolver()
	ctx := context.Background()

	if err := r.CreateChat(ctx, CreateChatRequest{ChatID: "c1"}); err == nil {
		t.Error("CreateChat: expected request-creation error")
	} else if !strings.Contains(err.Error(), "create request") {
		t.Errorf("CreateChat error = %v, want 'create request' wrap", err)
	}
	if err := r.CreateRun(ctx, "r1", "a1", "c1", "w1", "USER", nil); err == nil {
		t.Error("CreateRun: expected request-creation error")
	}
	if err := r.UpdateRun(ctx, "r1", "OK", nil, nil, nil); err == nil {
		t.Error("UpdateRun: expected request-creation error")
	}
	if err := r.IncrementMessageCount(ctx, "c1", 1); err == nil {
		t.Error("IncrementMessageCount: expected request-creation error")
	}
	if err := r.UpdateChatTitle(ctx, "c1", "t"); err == nil {
		t.Error("UpdateChatTitle: expected request-creation error")
	} else if !strings.Contains(err.Error(), "update chat title: create request") {
		t.Errorf("UpdateChatTitle error = %v, want 'update chat title: create request' wrap", err)
	}
	if _, err := r.GetWebhookSecret(ctx, "", "a1"); err == nil {
		t.Error("GetWebhookSecret: expected request-creation error")
	}
	if _, err := r.ResolveChat(ctx, "c1"); err == nil {
		t.Error("ResolveChat: expected request-creation error")
	} else if !strings.Contains(err.Error(), "create request") {
		t.Errorf("ResolveChat error = %v, want 'create request' wrap", err)
	}
}

// ---------- marshal error branches ----------

func TestCreateRunMarshalError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default())
	// A channel is not JSON-serializable — json.Marshal must fail before
	// any network call (the closed-port URL would otherwise error differently).
	err := r.CreateRun(context.Background(), "r1", "a1", "c1", "w1", "USER",
		map[string]interface{}{"bad": make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "create run: marshal") {
		t.Errorf("error = %v, want 'create run: marshal' wrap", err)
	}
}

func TestUpdateRunMarshalError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default())
	err := r.UpdateRun(context.Background(), "r1", "OK", nil, nil,
		map[string]interface{}{"bad": make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "update run: marshal") {
		t.Errorf("error = %v, want 'update run: marshal' wrap", err)
	}
}

// ---------- network error branches (httpClient.Do failures) ----------

func TestUpdateRunNetworkError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default())
	if err := r.UpdateRun(context.Background(), "r1", "OK", nil, nil, nil); err == nil {
		t.Fatal("expected network error")
	} else if !strings.Contains(err.Error(), "update run") {
		t.Errorf("error = %v, want 'update run' wrap", err)
	}
}

func TestIncrementMessageCountNetworkError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default())
	if err := r.IncrementMessageCount(context.Background(), "c1", 1); err == nil {
		t.Fatal("expected network error")
	} else if !strings.Contains(err.Error(), "increment message count") {
		t.Errorf("error = %v, want 'increment message count' wrap", err)
	}
}

func TestUpdateChatTitleNetworkError(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default())
	if err := r.UpdateChatTitle(context.Background(), "c1", "t"); err == nil {
		t.Fatal("expected network error")
	} else if !strings.Contains(err.Error(), "update chat title") {
		t.Errorf("error = %v, want 'update chat title' wrap", err)
	}
}

// ---------- resolve(): response body read failure ----------

func TestResolveChatBodyReadError(t *testing.T) {
	t.Parallel()
	// Declare a Content-Length larger than what is written; the server
	// aborts the connection and the client's io.ReadAll fails mid-body.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
	}))
	t.Cleanup(ts.Close)
	r := NewIPCResolver(ts.URL, "tok", slog.Default())
	_, err := r.ResolveChat(context.Background(), "c1")
	if err == nil {
		t.Fatal("expected body read error")
	}
	if !strings.Contains(err.Error(), "read response body") {
		t.Errorf("error = %v, want 'read response body' wrap", err)
	}
}

// ---------- resolve(): feature-only containerEnv initialization ----------

// When cached_requirements declares containerEnv but devcontainer_config has
// none, the map must be initialized from the feature env (the nil-map branch).
func TestResolveChatFeatureEnvWithoutDevcontainerEnv(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:            "a1",
		CLIAdapter:         "CLAUDE_CODE",
		CachedRequirements: `{"containerEnv":{"GOPATH":"/go"}}`,
		// DevcontainerConfig intentionally empty.
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
	if info.ContainerEnv == nil || info.ContainerEnv["GOPATH"] != "/go" {
		t.Errorf("ContainerEnv = %v, want feature env to initialize the map", info.ContainerEnv)
	}
	if info.CachedRequirements == nil || info.CachedRequirements.ContainerEnv["GOPATH"] != "/go" {
		t.Errorf("CachedRequirements = %+v", info.CachedRequirements)
	}
}

// ---------- resolve(): ServiceEnvLookup wiring ----------

func TestResolveChatWiresServiceEnvLookup(t *testing.T) {
	t.Parallel()
	resp := chatResolveResponse{
		AgentID:    "a1",
		CLIAdapter: "CLAUDE_CODE",
		Credentials: []credentialResponse{
			{ID: "c1", EnvVar: "PG_PASSWORD", Value: "hunter2"},
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
	if info.ServiceEnvLookup == nil {
		t.Fatal("ServiceEnvLookup must be wired from credentials")
	}
	if got := info.ServiceEnvLookup("PG_PASSWORD"); got != "hunter2" {
		t.Errorf("ServiceEnvLookup(PG_PASSWORD) = %q, want hunter2", got)
	}
	if got := info.ServiceEnvLookup("NOPE"); got != "" {
		t.Errorf("ServiceEnvLookup(NOPE) = %q, want empty", got)
	}
}
