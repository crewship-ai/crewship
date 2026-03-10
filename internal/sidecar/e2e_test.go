package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// TestE2ESidecarIntegration is a comprehensive integration test that verifies
// the entire sidecar proxy pipeline: credential injection, domain blocking,
// hop-by-hop stripping, scrubber, and priority-aware round-robin.
func TestE2ESidecarIntegration(t *testing.T) {
	// ---- Setup: fake upstream capturing injected credentials ----
	var lastAnthKey, lastAuthHeader, lastProxyAuth, lastGoogleKey string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		lastAnthKey = r.Header.Get("x-api-key")
		lastAuthHeader = r.Header.Get("Authorization")
		lastProxyAuth = r.Header.Get("Proxy-Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"key":        lastAnthKey,
			"auth":       lastAuthHeader,
			"proxy_auth": lastProxyAuth,
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		lastAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"auth": lastAuthHeader})
	})
	mux.HandleFunc("/v1/models/gemini", func(w http.ResponseWriter, r *http.Request) {
		lastGoogleKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"key": lastGoogleKey})
	})

	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstreamAddr := upstreamLn.Addr().String()
	upstreamSrv := &http.Server{Handler: mux}
	go upstreamSrv.Serve(upstreamLn)
	defer upstreamSrv.Close()

	// ---- Setup: sidecar with test credentials ----
	realAnthKey := "sk-ant-INJECTED-" + strings.Repeat("X", 20)
	backupAnthKey := "sk-ant-BACKUP-" + strings.Repeat("W", 20)
	// OpenAI keys: sk-proj-* or sk-{20+ alphanum}
	realOAIKey := "sk-proj-" + strings.Repeat("Y", 30)
	realGoogleKey := "AIzaSy" + strings.Repeat("Z", 33)

	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		Credentials: []Credential{
			{ID: "anth-1", Provider: ProviderAnthropic, Token: realAnthKey, Priority: 1},
			{ID: "anth-2", Provider: ProviderAnthropic, Token: backupAnthKey, Priority: 2},
			{ID: "oai-1", Provider: ProviderOpenAI, Token: realOAIKey, Priority: 1},
			{ID: "google-1", Provider: ProviderGoogle, Token: realGoogleKey, Priority: 1},
		},
		NetworkPolicy: &NetworkPolicyConfig{Mode: "restricted"},
	})

	// Override sidecar's internal transport to redirect LLM domains to our fake upstream
	srv.proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			for _, domain := range []string{"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com"} {
				if strings.HasPrefix(addr, domain) {
					return net.Dial(network, upstreamAddr)
				}
			}
			return net.Dial(network, addr)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Start(ctx)
	<-srv.Ready()
	time.Sleep(50 * time.Millisecond)

	sidecarAddr := srv.httpServer.Addr
	sidecarURL, _ := url.Parse("http://" + sidecarAddr)

	proxyClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(sidecarURL),
		},
	}

	// ---- TEST 1: Health Check ----
	t.Run("health_check", func(t *testing.T) {
		resp, err := http.Get("http://" + sidecarAddr + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var h map[string]interface{}
		json.Unmarshal(body, &h)

		if h["status"] != "ok" {
			t.Errorf("status = %v, want ok", h["status"])
		}
		if strings.Contains(string(body), "sk-ant-") {
			t.Error("credential leaked in health response")
		}
	})

	// ---- TEST 2: Anthropic Credential Injection ----
	t.Run("anthropic_injection", func(t *testing.T) {
		lastAnthKey = ""
		lastProxyAuth = ""
		req, _ := http.NewRequest("POST", "http://api.anthropic.com/v1/messages",
			strings.NewReader(`{"model":"claude-3"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", "sk-ant-dummy-crewship-sidecar")
		req.Header.Set("Proxy-Authorization", "Basic attacker-creds")

		resp, err := proxyClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]string
		json.NewDecoder(resp.Body).Decode(&result)

		if result["key"] != realAnthKey {
			t.Errorf("upstream got key %q, want real key", result["key"])
		}
		if result["key"] == "sk-ant-dummy-crewship-sidecar" {
			t.Error("dummy key was forwarded instead of real key")
		}
		if result["proxy_auth"] != "" {
			t.Errorf("Proxy-Authorization not stripped: %q", result["proxy_auth"])
		}
	})

	// ---- TEST 3: OpenAI Credential Injection ----
	t.Run("openai_injection", func(t *testing.T) {
		lastAuthHeader = ""
		req, _ := http.NewRequest("POST", "http://api.openai.com/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-dummy-crewship-sidecar")

		resp, err := proxyClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]string
		json.NewDecoder(resp.Body).Decode(&result)

		if result["auth"] != "Bearer "+realOAIKey {
			t.Errorf("upstream got auth %q, want Bearer <real key>", result["auth"])
		}
	})

	// ---- TEST 4: Google Credential Injection ----
	t.Run("google_injection", func(t *testing.T) {
		lastGoogleKey = ""
		req, _ := http.NewRequest("POST", "http://generativelanguage.googleapis.com/v1/models/gemini",
			strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")

		resp, err := proxyClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]string
		json.NewDecoder(resp.Body).Decode(&result)

		if result["key"] != realGoogleKey {
			t.Errorf("upstream got key %q, want real Google key", result["key"])
		}
	})

	// ---- TEST 5: Domain Blocking ----
	t.Run("domain_blocking", func(t *testing.T) {
		blocked := []string{"evil.com", "google.com", "github.com", "attacker.io"}
		for _, domain := range blocked {
			t.Run(domain, func(t *testing.T) {
				req, _ := http.NewRequest("GET", "http://"+domain+"/steal", nil)
				resp, err := proxyClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("got %d, want 403", resp.StatusCode)
				}
			})
		}
	})

	// ---- TEST 6: Scrubber catches all credential patterns ----
	t.Run("scrubber", func(t *testing.T) {
		s := scrubber.New()
		tests := []struct {
			name   string
			input  string
			leaked string
		}{
			{"anthropic", "Found: " + realAnthKey, "sk-ant-"},
			{"openai", "Token: " + realOAIKey, "sk-proj-"},
			{"google", "Key=" + realGoogleKey, "AIzaSy"},
			{"ssh_key", "-----BEGIN OPENSSH PRIVATE KEY-----\ndata\n-----END OPENSSH PRIVATE KEY-----", "BEGIN OPENSSH"},
			{"password", `{"password": "supersecret123"}`, "supersecret123"},
			{"env_secret", "SECRET_KEY=mybigsecret123456", "mybigsecret123456"},
			{"aws", "AKIA" + strings.Repeat("A", 16), "AKIA"},
			{"github", "ghp_" + strings.Repeat("a", 36), "ghp_"},
			{"slack", "xoxb-123456-" + strings.Repeat("a", 20), "xoxb-"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := s.Scrub(tt.input)
				if strings.Contains(got, tt.leaked) {
					t.Errorf("%q still in output: %s", tt.leaked, got)
				}
			})
		}
		t.Run("clean_unchanged", func(t *testing.T) {
			clean := "Normal output at line 42"
			if s.Scrub(clean) != clean {
				t.Error("clean text was modified")
			}
		})
	})

	// ---- TEST 7: Priority-Aware Round-Robin ----
	t.Run("priority_round_robin", func(t *testing.T) {
		cs := srv.CredStore()
		// Reset round-robin index by reloading
		cs.Load([]Credential{
			{ID: "anth-1", Provider: ProviderAnthropic, Token: realAnthKey, Priority: 1},
			{ID: "anth-2", Provider: ProviderAnthropic, Token: backupAnthKey, Priority: 2},
		})
		c1 := cs.Select(ProviderAnthropic)
		if c1 == nil || c1.ID != "anth-1" {
			t.Errorf("first select: got %v, want anth-1 (priority 1)", c1)
		}
		c2 := cs.Select(ProviderAnthropic)
		if c2 == nil || c2.ID != "anth-1" {
			t.Errorf("second select: got %v, want anth-1 (only one in top tier)", c2)
		}
	})
}
