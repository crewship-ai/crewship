package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- zero-value safety ---

// TestAnthropicZeroValueBehavesLikeDefault pins the shape existing tests build
// directly: &Anthropic{apiKey: "..."} with a hand-assigned client. Every
// config-derived value has to resolve lazily to its default, or that literal
// silently produces a provider with an empty version header and no URL.
func TestAnthropicZeroValueBehavesLikeDefault(t *testing.T) {
	t.Parallel()
	zero := &Anthropic{apiKey: "test"}
	zero.client = &http.Client{}

	if got := zero.Name(); got != "anthropic" {
		t.Errorf("Name() = %q, want %q", got, "anthropic")
	}
	if got := zero.displayName(); got != "Anthropic" {
		t.Errorf("displayName() = %q, want %q", got, "Anthropic")
	}
	if got := zero.version(); got != "2023-06-01" {
		t.Errorf("version() = %q, want %q", got, "2023-06-01")
	}
	if got := zero.betas(); len(got) != 1 || got[0] != "prompt-caching-2024-07-31" {
		t.Errorf("betas() = %v, want [prompt-caching-2024-07-31]", got)
	}
	if got := zero.chatURL("claude-x", false); got != anthropicAPIURL {
		t.Errorf("chatURL = %q, want %q", got, anthropicAPIURL)
	}
	if got := zero.modelsURL(); got != anthropicModelsURL {
		t.Errorf("modelsURL = %q, want %q", got, anthropicModelsURL)
	}
	if err := zero.baseURLError(); err != nil {
		t.Errorf("baseURLError = %v, want nil", err)
	}

	req, err := zero.newHTTPRequest(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("newHTTPRequest: %v", err)
	}
	for k, want := range map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "prompt-caching-2024-07-31",
		"x-api-key":         "test",
	} {
		if got := req.Header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
}

// --- accessor defaulting matrix ---

func TestAnthropicAccessorDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		cfg           AnthropicConfig
		wantName      string
		wantDisplay   string
		wantVersion   string
		wantBetaHdr   string
		wantBetaUnset bool
	}{
		{
			name:        "all zero",
			cfg:         AnthropicConfig{},
			wantName:    "anthropic",
			wantDisplay: "Anthropic",
			wantVersion: "2023-06-01",
			wantBetaHdr: "prompt-caching-2024-07-31",
		},
		{
			name:        "explicit values pass through",
			cfg:         AnthropicConfig{Name: "mantle", DisplayName: "Mantle", Version: "2099-01-01", Beta: []string{"a", "b"}},
			wantName:    "mantle",
			wantDisplay: "Mantle",
			wantVersion: "2099-01-01",
			wantBetaHdr: "a,b",
		},
		{
			name:          "empty non-nil beta sends no header",
			cfg:           AnthropicConfig{Beta: []string{}},
			wantName:      "anthropic",
			wantDisplay:   "Anthropic",
			wantVersion:   "2023-06-01",
			wantBetaUnset: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := NewAnthropicWith(tc.cfg)
			if got := a.Name(); got != tc.wantName {
				t.Errorf("Name() = %q, want %q", got, tc.wantName)
			}
			if got := a.displayName(); got != tc.wantDisplay {
				t.Errorf("displayName() = %q, want %q", got, tc.wantDisplay)
			}
			if got := a.version(); got != tc.wantVersion {
				t.Errorf("version() = %q, want %q", got, tc.wantVersion)
			}
			req, err := a.newHTTPRequest(context.Background(), []byte(`{}`))
			if err != nil {
				t.Fatalf("newHTTPRequest: %v", err)
			}
			got := req.Header.Get("anthropic-beta")
			if tc.wantBetaUnset {
				if _, ok := req.Header["Anthropic-Beta"]; ok {
					t.Errorf("anthropic-beta = %q, want header absent", got)
				}
				return
			}
			if got != tc.wantBetaHdr {
				t.Errorf("anthropic-beta = %q, want %q", got, tc.wantBetaHdr)
			}
		})
	}
}

// --- URL normalization matrix ---

// TestAnthropicURLNormalization runs every shape an operator plausibly pastes
// through endpoint.Normalize. Normalization may only REPAIR a base: a value
// that cannot be parsed falls back to the raw string rather than becoming a
// construction-time failure.
func TestAnthropicURLNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		base       string
		wantChat   string
		wantModels string
		wantErr    bool
	}{
		{
			name:       "unset falls back to the constants",
			base:       "",
			wantChat:   anthropicAPIURL,
			wantModels: anthropicModelsURL,
		},
		{
			name:       "bare root",
			base:       "https://api.anthropic.com",
			wantChat:   "https://api.anthropic.com/v1/messages",
			wantModels: "https://api.anthropic.com/v1/models",
		},
		{
			name:       "version suffix",
			base:       "https://api.anthropic.com/v1",
			wantChat:   "https://api.anthropic.com/v1/messages",
			wantModels: "https://api.anthropic.com/v1/models",
		},
		{
			name:       "full messages path",
			base:       "https://api.anthropic.com/v1/messages",
			wantChat:   "https://api.anthropic.com/v1/messages",
			wantModels: "https://api.anthropic.com/v1/models",
		},
		{
			name:       "trailing slash",
			base:       "https://api.anthropic.com/v1/",
			wantChat:   "https://api.anthropic.com/v1/messages",
			wantModels: "https://api.anthropic.com/v1/models",
		},
		{
			name:       "bare host port gets http",
			base:       "192.168.1.40:8080",
			wantChat:   "http://192.168.1.40:8080/v1/messages",
			wantModels: "http://192.168.1.40:8080/v1/models",
		},
		{
			name:       "mount prefix survives",
			base:       "https://gw.example.com/claude/v1/messages",
			wantChat:   "https://gw.example.com/claude/v1/messages",
			wantModels: "https://gw.example.com/claude/v1/models",
		},
		{
			name:       "unparseable base falls back to raw",
			base:       strings.Repeat("x", 4096),
			wantChat:   strings.Repeat("x", 4096),
			wantModels: strings.Repeat("x", 4096) + "/models",
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := NewAnthropicWith(AnthropicConfig{APIKey: "k", BaseURL: tc.base})
			if got := a.chatURL("claude-x", false); got != tc.wantChat {
				t.Errorf("chatURL = %q, want %q", got, tc.wantChat)
			}
			if got := a.modelsURL(); got != tc.wantModels {
				t.Errorf("modelsURL = %q, want %q", got, tc.wantModels)
			}
			err := a.baseURLError()
			if tc.wantErr != (err != nil) {
				t.Errorf("baseURLError = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestAnthropicPolicyRejectedBaseStillWorks proves a base this package rejects
// on POLICY (embedded credentials) is not surfaced as an error: it may be an
// odd deployment that used to work, so the raw value is attempted instead.
func TestAnthropicPolicyRejectedBaseStillWorks(t *testing.T) {
	t.Parallel()
	a := NewAnthropicWith(AnthropicConfig{APIKey: "k", BaseURL: "https://u:p@proxy.example.com/v1/messages"})
	if err := a.baseURLError(); err != nil {
		t.Errorf("baseURLError = %v, want nil for a policy rejection", err)
	}
	if got := a.chatURL("claude-x", false); got != "https://u:p@proxy.example.com/v1/messages" {
		t.Errorf("chatURL = %q, want the raw base", got)
	}
	if _, err := a.newHTTPRequest(context.Background(), []byte(`{}`)); err != nil {
		t.Errorf("newHTTPRequest: %v, want the raw base to be attempted", err)
	}
}

// --- header matrix over the wire ---

func TestAnthropicRequestHeadersOverTheWire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      AnthropicConfig
		wantHdrs map[string]string
		absent   []string
	}{
		{
			name: "defaults",
			cfg:  AnthropicConfig{APIKey: "sk-test"},
			wantHdrs: map[string]string{
				"x-api-key":         "sk-test",
				"anthropic-version": "2023-06-01",
				"anthropic-beta":    "prompt-caching-2024-07-31",
				"Content-Type":      "application/json",
			},
		},
		{
			name: "custom version and betas",
			cfg:  AnthropicConfig{APIKey: "sk-test", Version: "2030-02-02", Beta: []string{"one", "two"}},
			wantHdrs: map[string]string{
				"anthropic-version": "2030-02-02",
				"anthropic-beta":    "one,two",
			},
		},
		{
			name:     "empty beta slice omits the header",
			cfg:      AnthropicConfig{APIKey: "sk-test", Beta: []string{}},
			wantHdrs: map[string]string{"x-api-key": "sk-test"},
			absent:   []string{"anthropic-beta"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got http.Header
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
			}))
			t.Cleanup(srv.Close)

			cfg := tc.cfg
			cfg.BaseURL = srv.URL
			a := NewAnthropicWith(cfg)
			if _, err := a.Complete(context.Background(), Request{
				Model:    "claude-x",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			for k, want := range tc.wantHdrs {
				if v := got.Get(k); v != want {
					t.Errorf("header %s = %q, want %q", k, v, want)
				}
			}
			for _, k := range tc.absent {
				if v := got.Get(k); v != "" {
					t.Errorf("header %s = %q, want absent", k, v)
				}
			}
		})
	}
}

// TestAnthropicSignReplacesAPIKey covers the Bedrock auth seam: when Sign is
// set it owns authentication entirely and x-api-key is not sent.
func TestAnthropicSignReplacesAPIKey(t *testing.T) {
	t.Parallel()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"end_turn"}`))
	}))
	t.Cleanup(srv.Close)

	var signedBody []byte
	a := NewAnthropicWith(AnthropicConfig{
		APIKey:  "sk-should-not-be-sent",
		BaseURL: srv.URL,
		Sign: func(r *http.Request, body []byte) error {
			signedBody = body
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=fake")
			return nil
		},
	})
	if _, err := a.Complete(context.Background(), Request{
		Model:    "claude-x",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if v := got.Get("x-api-key"); v != "" {
		t.Errorf("x-api-key = %q, want absent when Sign is set", v)
	}
	if v := got.Get("Authorization"); v != "AWS4-HMAC-SHA256 Credential=fake" {
		t.Errorf("Authorization = %q, want the signed value", v)
	}
	if !strings.Contains(string(signedBody), `"model":"claude-x"`) {
		t.Errorf("Sign received body %q, want the payload about to be sent", signedBody)
	}
}

// TestAnthropicSignErrorFailsRequestBuild proves a signer failure surfaces as a
// request-build error, which the retry loop must not retry.
func TestAnthropicSignErrorFailsRequestBuild(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("no credentials")
	a := NewAnthropicWith(AnthropicConfig{
		Sign: func(*http.Request, []byte) error { return sentinel },
	})
	_, err := a.newHTTPRequest(context.Background(), []byte(`{}`))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap the signer error", err)
	}
	if !strings.Contains(err.Error(), "create request") {
		t.Errorf("err = %q, want a create-request wrap", err)
	}
}

// --- Bedrock seam: default path must be provably unchanged ---

func TestAnthropicBedrockSeamDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		cfg           AnthropicConfig
		stream        bool
		wantInBody    []string
		wantNotInBody []string
		wantURLSuffix string
	}{
		{
			name:          "defaults keep the direct API shape",
			cfg:           AnthropicConfig{},
			wantInBody:    []string{`"model":"claude-x"`},
			wantNotInBody: []string{"anthropic_version"},
			wantURLSuffix: "/v1/messages",
		},
		{
			name:          "defaults streaming keeps the messages path",
			cfg:           AnthropicConfig{},
			stream:        true,
			wantInBody:    []string{`"model":"claude-x"`, `"stream":true`},
			wantNotInBody: []string{"anthropic_version"},
			wantURLSuffix: "/v1/messages",
		},
		{
			name:          "VersionInBody moves the version and drops the model",
			cfg:           AnthropicConfig{VersionInBody: true},
			wantInBody:    []string{`"anthropic_version":"2023-06-01"`},
			wantNotInBody: []string{`"model"`},
			wantURLSuffix: "/v1/messages",
		},
		{
			name:          "VersionInBody honours a custom version",
			cfg:           AnthropicConfig{VersionInBody: true, Version: "bedrock-2023-05-31"},
			wantInBody:    []string{`"anthropic_version":"bedrock-2023-05-31"`},
			wantNotInBody: []string{`"model"`},
			wantURLSuffix: "/v1/messages",
		},
		{
			name:          "ModelInPath invokes",
			cfg:           AnthropicConfig{ModelInPath: true},
			wantInBody:    []string{`"model":"claude-x"`},
			wantURLSuffix: "/model/claude-x/invoke",
		},
		{
			name:          "ModelInPath streaming invokes with response stream",
			cfg:           AnthropicConfig{ModelInPath: true},
			stream:        true,
			wantURLSuffix: "/model/claude-x/invoke-with-response-stream",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := NewAnthropicWith(tc.cfg)
			body, err := a.buildRequestBody(Request{
				Model:    "claude-x",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}, tc.stream)
			if err != nil {
				t.Fatalf("buildRequestBody: %v", err)
			}
			for _, want := range tc.wantInBody {
				if !strings.Contains(string(body), want) {
					t.Errorf("body %s missing %s", body, want)
				}
			}
			for _, notWant := range tc.wantNotInBody {
				if strings.Contains(string(body), notWant) {
					t.Errorf("body %s must not contain %s", body, notWant)
				}
			}
			if got := a.chatURL("claude-x", tc.stream); !strings.HasSuffix(got, tc.wantURLSuffix) {
				t.Errorf("chatURL = %q, want suffix %q", got, tc.wantURLSuffix)
			}
		})
	}
}

// TestAnthropicModelInPathEscapes proves a model id carrying a slash — Bedrock's
// inference-profile ids do — is escaped into a single path segment rather than
// silently inventing a route.
func TestAnthropicModelInPathEscapes(t *testing.T) {
	t.Parallel()
	a := NewAnthropicWith(AnthropicConfig{BaseURL: "https://bedrock.example.com", ModelInPath: true})
	got := a.chatURL("us.anthropic/claude-x:1", false)
	want := "https://bedrock.example.com/model/us.anthropic%2Fclaude-x:1/invoke"
	if got != want {
		t.Errorf("chatURL = %q, want %q", got, want)
	}
}

// TestAnthropicDefaultBodyUnchanged is the golden check that the whole config
// refactor did not move a byte of the default request body.
func TestAnthropicDefaultBodyUnchanged(t *testing.T) {
	t.Parallel()
	temp := 0.3
	req := Request{
		Model:       "claude-x",
		System:      "be brief",
		MaxTokens:   256,
		Temperature: &temp,
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		Tools:       []ToolDef{{Name: "t", Description: "d", InputSchema: map[string]any{"type": "object"}}},
	}
	body, err := NewAnthropic("k").buildRequestBody(req, true)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	const want = `{"max_tokens":256,"messages":[{"role":"user","content":"hi"}],"model":"claude-x",` +
		`"stream":true,"system":[{"cache_control":{"type":"ephemeral"},"text":"be brief","type":"text"}],` +
		`"temperature":0.3,"tools":[{"cache_control":{"type":"ephemeral"},"description":"d","input_schema":{"type":"object"},"name":"t"}]}`
	if string(body) != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
}

// --- error wording ---

func TestAnthropicErrorWording(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "401", status: http.StatusUnauthorized, body: "nope", want: "invalid Anthropic API key"},
		{name: "429", status: http.StatusTooManyRequests, body: "slow", want: "Anthropic rate limit exceeded"},
		{name: "500", status: http.StatusInternalServerError, body: "boom", want: "Anthropic API returned 500: boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			// A retryable status would sleep through three attempts; a
			// per-call context keeps the table fast without changing wording.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			a := NewAnthropicWith(AnthropicConfig{APIKey: "k", BaseURL: srv.URL})
			_, err := a.ListModels(ctx)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestAnthropicCustomNamesRenderInErrors proves the wrap prefixes are built from
// the config rather than hard-coded, without disturbing the defaults above.
func TestAnthropicCustomNamesRenderInErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	a := NewAnthropicWith(AnthropicConfig{
		APIKey: "k", BaseURL: srv.URL, Name: "mantle", DisplayName: "Mantle",
	})
	_, err := a.ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid Mantle API key") {
		t.Fatalf("err = %v, want it to contain %q", err, "invalid Mantle API key")
	}
	if a.Name() != "mantle" {
		t.Errorf("Name() = %q, want mantle", a.Name())
	}
}

// TestAnthropicListModelsUsesConfiguredBase proves discovery follows the
// configured endpoint instead of the hard-coded api.anthropic.com constant.
func TestAnthropicListModelsUsesConfiguredBase(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x","display_name":"Claude X"}]}`))
	}))
	t.Cleanup(srv.Close)

	a := NewAnthropicWith(AnthropicConfig{APIKey: "k", BaseURL: srv.URL})
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if len(models) != 1 || models[0].ID != "claude-x" || models[0].Provider != "anthropic" {
		t.Errorf("models = %+v", models)
	}
}

// --- client construction ---

func TestNewAnthropicWithClient(t *testing.T) {
	t.Parallel()
	t.Run("nil falls back to the default client", func(t *testing.T) {
		t.Parallel()
		a := NewAnthropicWithClient("k", nil)
		if a.client == nil {
			t.Fatal("client must not be nil")
		}
		if a.client.Timeout != 120*time.Second {
			t.Errorf("Timeout = %s, want 120s", a.client.Timeout)
		}
	})
	t.Run("custom client is used verbatim", func(t *testing.T) {
		t.Parallel()
		custom := &http.Client{Timeout: 7 * time.Second}
		a := NewAnthropicWithClient("k", custom)
		if a.client != custom {
			t.Error("supplied client must be used as-is — it is the SSRF fence")
		}
	})
	t.Run("Timeout overrides the default", func(t *testing.T) {
		t.Parallel()
		a := NewAnthropicWith(AnthropicConfig{Timeout: 5 * time.Second})
		if a.client.Timeout != 5*time.Second {
			t.Errorf("Timeout = %s, want 5s", a.client.Timeout)
		}
	})
}

// TestAnthropicTransportIsAClone is the regression guard for the bare
// &http.Transport{DisableCompression: true} this codec used to build: a
// zero-value transport has a nil Proxy, so a deployment behind HTTP_PROXY /
// HTTPS_PROXY lost Anthropic connectivity while every other provider — all of
// which clone DefaultTransport — kept working.
func TestAnthropicTransportIsAClone(t *testing.T) {
	t.Parallel()
	tr, ok := NewAnthropic("k").client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", NewAnthropic("k").client.Transport)
	}
	if !tr.DisableCompression {
		t.Error("DisableCompression must stay on — SSE lines are small, gzip only adds latency")
	}
	if tr.Proxy == nil {
		t.Error("Proxy is nil: this is a bare &http.Transport{}, not a clone of DefaultTransport")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout is zero: DefaultTransport's dial defaults were dropped")
	}
	if tr == http.DefaultTransport {
		t.Error("must not mutate the shared DefaultTransport")
	}
}

// --- end-to-end through the configured endpoint ---

// TestAnthropicCompleteThroughConfiguredBase is what the skipped
// TestAnthropic_DoWithRetry_GivesUpAfterMaxAttempts was waiting on: a
// configurable base means the retry loop can now be driven against a test
// server without rewriting transports.
func TestAnthropicCompleteThroughConfiguredBase(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "claude-x" {
			t.Errorf("model = %v, want claude-x", body["model"])
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	a := NewAnthropicWith(AnthropicConfig{APIKey: "k", BaseURL: srv.URL})
	resp, err := a.Complete(context.Background(), Request{
		Model:    "claude-x",
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "pong" || resp.InputToks != 3 || resp.OutputToks != 1 {
		t.Errorf("resp = %+v", resp)
	}
}
