package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/paymaster"
)

// --- withDefaults ---

// TestOpenAICompatConfig_WithDefaults pins the defaulting rules, which are the
// whole reason an OpenAICompatConfig{} is a working hosted-OpenAI client: every
// caller that never thought about a knob must land on the behaviour this
// provider had before it was configurable.
func TestOpenAICompatConfig_WithDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   OpenAICompatConfig
		want OpenAICompatConfig
	}{
		{
			name: "zero value is hosted openai",
			in:   OpenAICompatConfig{},
			want: OpenAICompatConfig{
				Name: "openai", DisplayName: "OpenAI", BaseURL: openaiAPIURL,
				AuthHeader: "Authorization", AuthPrefix: "Bearer ",
				MaxTokensField: "max_tokens", Timeout: 120 * time.Second,
			},
		},
		{
			name: "explicit values pass through",
			in: OpenAICompatConfig{
				Name: "deepseek", DisplayName: "DeepSeek", BaseURL: "https://api.deepseek.com/v1",
				AuthHeader: "X-Key", AuthPrefix: "Token ",
				MaxTokensField: "max_completion_tokens", Timeout: 5 * time.Second,
			},
			want: OpenAICompatConfig{
				Name: "deepseek", DisplayName: "DeepSeek", BaseURL: "https://api.deepseek.com/v1",
				AuthHeader: "X-Key", AuthPrefix: "Token ",
				MaxTokensField: "max_completion_tokens", Timeout: 5 * time.Second,
			},
		},
		{
			name: "dash sentinel means no prefix",
			in:   OpenAICompatConfig{AuthHeader: "api-key", AuthPrefix: noAuthPrefix},
			want: OpenAICompatConfig{
				Name: "openai", DisplayName: "OpenAI", BaseURL: openaiAPIURL,
				AuthHeader: "api-key", AuthPrefix: "",
				MaxTokensField: "max_tokens", Timeout: 120 * time.Second,
			},
		},
		{
			name: "negative timeout is treated as unset",
			in:   OpenAICompatConfig{Timeout: -1},
			want: OpenAICompatConfig{
				Name: "openai", DisplayName: "OpenAI", BaseURL: openaiAPIURL,
				AuthHeader: "Authorization", AuthPrefix: "Bearer ",
				MaxTokensField: "max_tokens", Timeout: 120 * time.Second,
			},
		},
		{
			name: "flags and bodies survive untouched",
			in: OpenAICompatConfig{
				NoAuth: true, IncludeUsage: true, DefaultMaxTokens: 512,
			},
			want: OpenAICompatConfig{
				Name: "openai", DisplayName: "OpenAI", BaseURL: openaiAPIURL,
				AuthHeader: "Authorization", AuthPrefix: "Bearer ",
				MaxTokensField: "max_tokens", Timeout: 120 * time.Second,
				NoAuth: true, IncludeUsage: true, DefaultMaxTokens: 512,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.withDefaults(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("withDefaults() = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestOpenAICompatConfig_WithDefaultsIsPure guards against the defaulting layer
// mutating the caller's config — a preset is handed out by value and a caller
// that finished one must not find its BaseURL rewritten behind its back.
func TestOpenAICompatConfig_WithDefaultsIsPure(t *testing.T) {
	t.Parallel()
	in := OpenAICompatConfig{}
	_ = in.withDefaults()
	if in.Name != "" || in.BaseURL != "" || in.Timeout != 0 {
		t.Errorf("withDefaults mutated its receiver: %+v", in)
	}
}

// --- identity strings ---

// TestOpenAICompat_DefaultIdentityStrings proves the configurable wording still
// renders byte-identically for the hosted default. These strings are what
// operators grep for, and several are pinned by other tests in this package.
func TestOpenAICompat_DefaultIdentityStrings(t *testing.T) {
	t.Parallel()
	if got := NewOpenAI("k").Name(); got != "openai" {
		t.Errorf("Name() = %q, want openai", got)
	}
	if got := NewOpenAICompat(OpenAICompatConfig{}).displayName(); got != "OpenAI" {
		t.Errorf("displayName() = %q, want OpenAI", got)
	}

	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "invalid OpenAI API key"},
		{http.StatusTooManyRequests, "OpenAI rate limit exceeded"},
		{http.StatusInternalServerError, "OpenAI API returned 500: boom"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("boom"))
			}))
			t.Cleanup(srv.Close)
			// ListModels goes straight through checkStatus with no retry
			// loop, so the message is the unwrapped one.
			o := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
			_, err := o.ListModels(context.Background())
			if err == nil {
				t.Fatalf("want error for status %d", tc.status)
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// TestOpenAICompat_CustomIdentityStrings is the other half: a configured
// backend must name ITSELF in every operator-facing string, otherwise a
// DeepSeek outage reads as an OpenAI one.
func TestOpenAICompat_CustomIdentityStrings(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	o := NewOpenAICompat(OpenAICompatConfig{
		Name: "deepseek", DisplayName: "DeepSeek",
		BaseURL: srv.URL + "/v1", APIKey: "k",
	})
	if got := o.Name(); got != "deepseek" {
		t.Errorf("Name() = %q, want deepseek", got)
	}
	_, err := o.ListModels(context.Background())
	if err == nil || err.Error() != "invalid DeepSeek API key" {
		t.Fatalf("error = %v, want %q", err, "invalid DeepSeek API key")
	}

	// The lowercase name is the transport-wrap prefix.
	dead := NewOpenAICompat(OpenAICompatConfig{
		Name: "deepseek", DisplayName: "DeepSeek",
		BaseURL: "http://example.invalid/v1", APIKey: "k",
	})
	dead.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("down")
	})
	if _, err := dead.ListModels(context.Background()); err == nil ||
		!strings.HasPrefix(err.Error(), "deepseek http: ") {
		t.Errorf("error = %v, want a \"deepseek http: \" prefix", err)
	}
}

// TestOpenAICompat_ListModelsProviderField checks the ModelInfo carries the
// configured provider id. It is the paymaster pricing key downstream, so a
// hardcoded "openai" here would bill every DeepSeek call at OpenAI rates.
func TestOpenAICompat_ListModelsProviderField(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
	}))
	t.Cleanup(srv.Close)

	o := NewOpenAICompat(OpenAICompatConfig{Name: "deepseek", DisplayName: "DeepSeek", BaseURL: srv.URL + "/v1", APIKey: "k"})
	models, err := o.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].Provider != "deepseek" || models[0].ID != "deepseek-chat" {
		t.Errorf("models = %+v", models)
	}
}

// TestOpenAICompat_TruncationMessage keeps the silent-truncation guard's
// wording tied to the configured name, and proves the handler still saw the
// partial content that did arrive.
func TestOpenAICompat_TruncationMessage(t *testing.T) {
	t.Parallel()
	srv := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"half"}}]}` + "\n\n"))
		flush()
		// No finish_reason, no [DONE] — the handler just returns.
	})
	t.Cleanup(srv.Close)

	o := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
	var got strings.Builder
	var sawDone bool
	_, err := o.Stream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(e StreamEvent) error {
		switch e.Type {
		case "text":
			got.WriteString(e.Content)
		case "done":
			sawDone = true
		}
		return nil
	})
	if err == nil {
		t.Fatal("truncated stream must error")
	}
	if !strings.HasPrefix(err.Error(), "openai stream ended without a finish_reason") {
		t.Errorf("error = %q, want the openai truncation prefix", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error %v must wrap io.ErrUnexpectedEOF", err)
	}
	if got.String() != "half" {
		t.Errorf("handler text = %q, want the partial content", got.String())
	}
	if !sawDone {
		t.Error("done event must still fire with the partial response")
	}
}

// --- auth ---

// captureHeaders runs one ListModels against a server that records the request
// headers, and returns them.
func captureHeaders(t *testing.T, cfg OpenAICompatConfig) http.Header {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL + "/v1"
	if _, err := NewOpenAICompat(cfg).ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	return got
}

// TestOpenAICompat_AuthMatrix covers the auth shapes this codec has to speak.
// The empty-key row is the bug fix: a bare "Bearer " used to go out, which a
// local vLLM/llama.cpp server rejects outright.
func TestOpenAICompat_AuthMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		cfg        OpenAICompatConfig
		wantHeader string // "" => assert absent
		wantValue  string
		wantExtra  map[string]string
	}{
		{
			name:       "key set sends bearer",
			cfg:        OpenAICompatConfig{APIKey: "k"},
			wantHeader: "Authorization", wantValue: "Bearer k",
		},
		{
			name:       "empty key sends no auth header",
			cfg:        OpenAICompatConfig{},
			wantHeader: "Authorization",
		},
		{
			name:       "NoAuth suppresses a present key",
			cfg:        OpenAICompatConfig{APIKey: "k", NoAuth: true},
			wantHeader: "Authorization",
		},
		{
			name:       "azure style raw key",
			cfg:        OpenAICompatConfig{APIKey: "k", AuthHeader: "api-key", AuthPrefix: noAuthPrefix},
			wantHeader: "Api-Key", wantValue: "k",
		},
		{
			name: "extra headers applied and auth not clobbered",
			cfg: OpenAICompatConfig{
				APIKey:  "k",
				Headers: map[string]string{"X-Title": "crewship", "Authorization": "nope"},
			},
			wantHeader: "Authorization", wantValue: "Bearer k",
			wantExtra: map[string]string{"X-Title": "crewship"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := captureHeaders(t, tc.cfg)
			got := h.Get(tc.wantHeader)
			if got != tc.wantValue {
				t.Errorf("%s = %q, want %q", tc.wantHeader, got, tc.wantValue)
			}
			for k, v := range tc.wantExtra {
				if h.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, h.Get(k), v)
				}
			}
		})
	}
}

// --- request body ---

func decodeBody(t *testing.T, o *OpenAI, req Request, stream bool) map[string]any {
	t.Helper()
	raw, err := o.buildRequestBody(req, stream)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return got
}

// TestOpenAICompat_BodyMatrix covers every knob that reaches the request body.
// The stream_options row is load-bearing: without it a streamed call reports
// zero tokens and paymaster prices the whole conversation at $0.
func TestOpenAICompat_BodyMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cfg     OpenAICompatConfig
		req     Request
		stream  bool
		want    map[string]any // key => expected value
		absent  []string
		wantUse bool // stream_options present
	}{
		{
			name:    "streamed with IncludeUsage asks for usage",
			cfg:     OpenAICompatConfig{IncludeUsage: true},
			req:     Request{Model: "m"},
			stream:  true,
			wantUse: true,
		},
		{
			name:   "non-streamed never asks for usage",
			cfg:    OpenAICompatConfig{IncludeUsage: true},
			req:    Request{Model: "m"},
			stream: false,
			absent: []string{"stream_options", "stream"},
		},
		{
			name:   "IncludeUsage off omits it on a stream",
			cfg:    OpenAICompatConfig{},
			req:    Request{Model: "m"},
			stream: true,
			want:   map[string]any{"stream": true},
			absent: []string{"stream_options"},
		},
		{
			name:   "request max tokens uses the configured field",
			cfg:    OpenAICompatConfig{MaxTokensField: "max_completion_tokens"},
			req:    Request{Model: "m", MaxTokens: 77},
			want:   map[string]any{"max_completion_tokens": float64(77)},
			absent: []string{"max_tokens"},
		},
		{
			name: "DefaultMaxTokens fills in when the request omits one",
			cfg:  OpenAICompatConfig{DefaultMaxTokens: 512},
			req:  Request{Model: "m"},
			want: map[string]any{"max_tokens": float64(512)},
		},
		{
			name: "request max tokens beats the default",
			cfg:  OpenAICompatConfig{DefaultMaxTokens: 512},
			req:  Request{Model: "m", MaxTokens: 8},
			want: map[string]any{"max_tokens": float64(8)},
		},
		{
			name:   "neither omits the key entirely",
			cfg:    OpenAICompatConfig{},
			req:    Request{Model: "m"},
			absent: []string{"max_tokens"},
		},
		{
			name: "ExtraBody is merged and wins on collision",
			cfg: OpenAICompatConfig{
				ExtraBody: map[string]any{"provider": "routed", "model": "override"},
			},
			req:  Request{Model: "m"},
			want: map[string]any{"provider": "routed", "model": "override"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decodeBody(t, NewOpenAICompat(tc.cfg), tc.req, tc.stream)
			for k, want := range tc.want {
				if !reflect.DeepEqual(got[k], want) {
					t.Errorf("body[%q] = %#v, want %#v", k, got[k], want)
				}
			}
			for _, k := range tc.absent {
				if _, ok := got[k]; ok {
					t.Errorf("body must not carry %q, got %#v", k, got[k])
				}
			}
			if tc.wantUse {
				opts, ok := got["stream_options"].(map[string]any)
				if !ok || opts["include_usage"] != true {
					t.Errorf("stream_options = %#v, want {include_usage:true}", got["stream_options"])
				}
			}
		})
	}
}

// TestOpenAICompat_DefaultBodyUnchanged is the regression guard for the
// existing hosted path: the only key this refactor adds to a NewOpenAI request
// is stream_options, and only when streaming.
func TestOpenAICompat_DefaultBodyUnchanged(t *testing.T) {
	t.Parallel()
	o := NewOpenAI("k")
	got := decodeBody(t, o, Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, false)
	wantKeys := map[string]bool{"model": true, "messages": true}
	for k := range got {
		if !wantKeys[k] {
			t.Errorf("non-streamed body grew an unexpected key %q = %#v", k, got[k])
		}
	}
}

// --- stop reasons ---

// TestOpenAICompat_StopReason covers the vocabulary this codec accepts.
// function_call/tool_use/max_tokens are the additions: a compat backend
// emitting any of them used to accumulate its tool calls correctly and then
// report end_turn, which reads to an agent loop as "nothing was asked for".
func TestOpenAICompat_StopReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		overlay map[string]StopReason
		in      string
		want    StopReason
	}{
		{name: "tool_calls", in: "tool_calls", want: StopToolUse},
		{name: "function_call", in: "function_call", want: StopToolUse},
		{name: "tool_use", in: "tool_use", want: StopToolUse},
		{name: "length", in: "length", want: StopMaxToks},
		{name: "max_tokens", in: "max_tokens", want: StopMaxToks},
		{name: "stop", in: "stop", want: StopEndTurn},
		{name: "empty", in: "", want: StopEndTurn},
		{name: "unknown defaults to end_turn", in: "eos", want: StopEndTurn},
		{
			name:    "overlay wins over the built-in map",
			overlay: map[string]StopReason{"stop": StopMaxToks},
			in:      "stop", want: StopMaxToks,
		},
		{
			name:    "overlay adds a backend-specific reason",
			overlay: map[string]StopReason{"eos_token": StopEndTurn, "call": StopToolUse},
			in:      "call", want: StopToolUse,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := NewOpenAICompat(OpenAICompatConfig{StopReasons: tc.overlay})
			if got := o.stopReason(tc.in); got != tc.want {
				t.Errorf("stopReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestOpenAICompat_StopReasonReachesComplete proves the overlay is applied on
// the non-streaming path too — toResponse resolves through the built-in map
// alone, so Complete has to re-map or a configured vocabulary silently only
// worked when streaming.
func TestOpenAICompat_StopReasonReachesComplete(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"function_call"}]}`))
	}))
	t.Cleanup(srv.Close)

	o := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL + "/v1", APIKey: "k"})
	resp, err := o.Complete(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q, want tool_use", resp.StopReason)
	}
}

// --- clients ---

// TestOpenAICompat_StreamClient guards the streaming client against the
// http.Client.Timeout regression: that timeout covers body read, so applying
// the 120s non-streaming deadline to an SSE stream silently truncates any
// generation that outruns it.
func TestOpenAICompat_StreamClient(t *testing.T) {
	t.Parallel()
	o := NewOpenAI("k")
	if o.stream == nil {
		t.Fatal("stream client must be initialized")
	}
	if o.stream.Timeout != 0 {
		t.Errorf("stream client Timeout = %s, want 0 — a deadline here kills long generations", o.stream.Timeout)
	}
	if o.client.Timeout != 120*time.Second {
		t.Errorf("non-streaming client Timeout = %s, want 120s", o.client.Timeout)
	}
	tr, ok := o.stream.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("stream transport = %T, want *http.Transport", o.stream.Transport)
	}
	if o.stream.Transport == http.DefaultTransport {
		t.Error("stream transport must be a CLONE — mutating the shared DefaultTransport leaks into every other client in the process")
	}
	if tr.Proxy == nil {
		t.Error("stream transport dropped Proxy — a bare http.Transport breaks deployments behind HTTP_PROXY")
	}
	if tr.ResponseHeaderTimeout != 60*time.Second {
		t.Errorf("ResponseHeaderTimeout = %s, want 60s", tr.ResponseHeaderTimeout)
	}
}

// TestOpenAICompat_StreamClientFollowsTheFence checks the SSRF seam covers
// streaming as well. A provider fenced for Complete and open for Stream is one
// refactor away from dialling an arbitrary address, and the caller that asked
// for a guarded client would have no way to know.
func TestOpenAICompat_StreamClientFollowsTheFence(t *testing.T) {
	t.Parallel()
	marker := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("fenced")
	})
	guarded := &http.Client{Transport: marker, Timeout: 9 * time.Second}

	o := NewOpenAIWithClient("k", "http://x/v1", guarded)
	if o.client != guarded {
		t.Error("supplied client must be used verbatim for the non-streaming path")
	}
	if o.stream == nil || o.stream.Transport == nil {
		t.Fatal("stream client must exist")
	}
	if reflect.ValueOf(o.stream.Transport).Pointer() != reflect.ValueOf(http.RoundTripper(marker)).Pointer() {
		t.Error("stream client must inherit the guarded transport")
	}
	if o.stream.Timeout != 0 {
		t.Errorf("stream client inherited a %s request timeout", o.stream.Timeout)
	}

	// An explicit StreamClient is used verbatim.
	custom := &http.Client{}
	o2 := NewOpenAICompat(OpenAICompatConfig{StreamClient: custom})
	if o2.stream != custom {
		t.Error("an explicit StreamClient must be used verbatim")
	}
}

// TestOpenAICompat_ClientTimeout checks Timeout is honoured when no client is
// supplied and ignored when one is.
func TestOpenAICompat_ClientTimeout(t *testing.T) {
	t.Parallel()
	if got := NewOpenAICompat(OpenAICompatConfig{Timeout: 7 * time.Second}).client.Timeout; got != 7*time.Second {
		t.Errorf("client timeout = %s, want 7s", got)
	}
	supplied := &http.Client{Timeout: 3 * time.Second}
	if got := NewOpenAICompat(OpenAICompatConfig{Timeout: 7 * time.Second, Client: supplied}).client; got != supplied {
		t.Error("a supplied client must be used verbatim, Timeout ignored")
	}
}

// --- presets ---

func TestOpenAIPreset(t *testing.T) {
	t.Parallel()
	cases := []struct {
		lookup          string
		ok              bool
		wantName        string
		wantDisplay     string
		wantBase        string
		wantNoAuth      bool
		wantIncludeUsed bool
	}{
		{lookup: "openai", ok: true, wantName: "openai", wantDisplay: "OpenAI", wantBase: openaiAPIURL, wantIncludeUsed: true},
		{lookup: "deepseek", ok: true, wantName: "deepseek", wantDisplay: "DeepSeek", wantBase: "https://api.deepseek.com/v1", wantIncludeUsed: true},
		{lookup: "ollama-openai", ok: true, wantName: "ollama", wantDisplay: "Ollama", wantBase: "http://localhost:11434/v1", wantNoAuth: true, wantIncludeUsed: true},
		{lookup: "vllm", ok: true, wantName: "local", wantDisplay: "vLLM", wantBase: "", wantNoAuth: true, wantIncludeUsed: true},
		{lookup: " OpenAI ", ok: true, wantName: "openai", wantDisplay: "OpenAI", wantBase: openaiAPIURL, wantIncludeUsed: true},
		{lookup: "groq", ok: false},
		{lookup: "openrouter", ok: false},
		{lookup: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.lookup, func(t *testing.T) {
			t.Parallel()
			cfg, ok := OpenAIPreset(tc.lookup)
			if ok != tc.ok {
				t.Fatalf("OpenAIPreset(%q) ok = %v, want %v", tc.lookup, ok, tc.ok)
			}
			if !tc.ok {
				if !reflect.DeepEqual(cfg, OpenAICompatConfig{}) {
					t.Errorf("unknown preset must return a zero config, got %+v", cfg)
				}
				return
			}
			if cfg.Name != tc.wantName || cfg.DisplayName != tc.wantDisplay || cfg.BaseURL != tc.wantBase {
				t.Errorf("preset = %+v, want name=%q display=%q base=%q", cfg, tc.wantName, tc.wantDisplay, tc.wantBase)
			}
			if cfg.NoAuth != tc.wantNoAuth {
				t.Errorf("NoAuth = %v, want %v", cfg.NoAuth, tc.wantNoAuth)
			}
			if cfg.IncludeUsage != tc.wantIncludeUsed {
				t.Errorf("IncludeUsage = %v, want %v", cfg.IncludeUsage, tc.wantIncludeUsed)
			}
		})
	}
}

func TestOpenAIPresetNames(t *testing.T) {
	t.Parallel()
	want := []string{"deepseek", "ollama-openai", "openai", "vllm"}
	if got := OpenAIPresetNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("OpenAIPresetNames() = %v, want %v", got, want)
	}
}

// TestPresetsResolveToAPricedProvider is a permanent block on the $0-billing
// trap. Provider.Name() is the paymaster pricing key, and lookupPrice returns a
// ZERO rate for a provider it has never heard of — so shipping a preset whose
// Name has no price row means every call through it is billed at nothing, with
// no error anywhere. A new preset therefore has to arrive together with a
// verified rate row (an exact model row, a "<name>/*" wildcard, or a
// providerFallback entry).
//
// ollama and local are the deliberate exceptions: they are free at the call
// site by design, priced through the "ollama/*" and "local/*" wildcards.
func TestPresetsResolveToAPricedProvider(t *testing.T) {
	t.Parallel()
	freeByDesign := map[string]bool{"ollama": true, "local": true}
	for _, name := range OpenAIPresetNames() {
		cfg, ok := OpenAIPreset(name)
		if !ok {
			t.Fatalf("OpenAIPresetNames listed %q but OpenAIPreset does not know it", name)
		}
		rate := paymaster.RateCard(cfg.Name, "a-model-that-does-not-exist")
		if freeByDesign[cfg.Name] {
			if rate.InputPerM != 0 || rate.OutputPerM != 0 {
				t.Errorf("preset %q (%s) is meant to be free, got %+v", name, cfg.Name, rate)
			}
			continue
		}
		if rate.InputPerM <= 0 || rate.OutputPerM <= 0 {
			t.Errorf("preset %q resolves to provider %q, which paymaster prices at $0 — "+
				"add a verified rate row before shipping it", name, cfg.Name)
		}
	}
}
