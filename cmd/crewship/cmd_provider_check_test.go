package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/llm"
)

// envFunc builds a getenv for buildCheckProvider. Nothing here touches the
// process environment: the resolution rules are the thing under test, and a
// test that had to set OPENAI_API_KEY globally could not run beside one that
// asserts it is unset.
func envFunc(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// --- unit: how the flags become a provider ---

func TestBuildCheckProvider(t *testing.T) {
	const srvURL = "http://127.0.0.1:9/v1"

	tests := []struct {
		name    string
		opts    checkOptions
		env     map[string]string
		want    checkTarget
		wantErr int // 0 = success, otherwise the expected exit code
	}{
		{
			name: "registry openai, key from env",
			opts: checkOptions{Provider: "openai", Model: "gpt-5.5"},
			env:  map[string]string{"OPENAI_API_KEY": "sk-test"},
			want: checkTarget{
				Provider: "openai", PricingKey: "openai", Codec: "openai-compat",
				Endpoint: "https://api.openai.com/v1/chat/completions", Model: "gpt-5.5",
				KeySource: "$OPENAI_API_KEY",
			},
		},
		{
			name: "an explicit key beats the environment, and is never echoed",
			opts: checkOptions{Provider: "openai", Model: "gpt-5.5", APIKey: "sk-flag"},
			env:  map[string]string{"OPENAI_API_KEY": "sk-env"},
			want: checkTarget{
				Provider: "openai", PricingKey: "openai", Codec: "openai-compat",
				Endpoint: "https://api.openai.com/v1/chat/completions", Model: "gpt-5.5",
				KeySource: "--api-key",
			},
		},
		{
			// The rule that makes a keyless local backend reachable through
			// the hosted provider's codec.
			name: "no key is fine once the endpoint is overridden",
			opts: checkOptions{Provider: "openai", Model: "qwen2.5:3b", BaseURL: srvURL},
			want: checkTarget{
				Provider: "openai", PricingKey: "openai", Codec: "openai-compat",
				Endpoint: srvURL, Model: "qwen2.5:3b", KeySource: "unset ($OPENAI_API_KEY)",
			},
		},
		{
			name:    "no key and the hosted endpoint is a refusal, not a 401",
			opts:    checkOptions{Provider: "openai", Model: "gpt-5.5"},
			wantErr: cli.ExitValidation,
		},
		{
			name: "registry anthropic",
			opts: checkOptions{Provider: "anthropic", Model: "claude-haiku-4-5"},
			env:  map[string]string{"ANTHROPIC_API_KEY": "sk-ant"},
			want: checkTarget{
				Provider: "anthropic", PricingKey: "anthropic", Codec: "anthropic-messages",
				Endpoint: "https://api.anthropic.com/v1/messages", Model: "claude-haiku-4-5",
				KeySource: "$ANTHROPIC_API_KEY",
			},
		},
		{
			name: "registry ollama needs no key at all",
			opts: checkOptions{Provider: "ollama", Model: "qwen2.5:3b"},
			want: checkTarget{
				Provider: "ollama", PricingKey: "ollama", Codec: "ollama-native",
				Endpoint: "http://localhost:11434", Model: "qwen2.5:3b", KeySource: "none",
			},
		},
		{
			name: "ollama takes its endpoint from BaseEnv",
			opts: checkOptions{Provider: "ollama", Model: "qwen2.5:3b"},
			env:  map[string]string{"KEEPER_OLLAMA_URL": "http://gpu-box:11434"},
			want: checkTarget{
				Provider: "ollama", PricingKey: "ollama", Codec: "ollama-native",
				Endpoint: "http://gpu-box:11434", Model: "qwen2.5:3b", KeySource: "none",
			},
		},
		{
			name: "--base-url beats BaseEnv",
			opts: checkOptions{Provider: "ollama", Model: "qwen2.5:3b", BaseURL: srvURL},
			env:  map[string]string{"KEEPER_OLLAMA_URL": "http://gpu-box:11434"},
			want: checkTarget{
				Provider: "ollama", PricingKey: "ollama", Codec: "ollama-native",
				Endpoint: srvURL, Model: "qwen2.5:3b", KeySource: "none",
			},
		},
		{
			// The preset's own Name survives: ollama-openai bills as ollama,
			// which is a free row, and taking the typed id instead would move
			// every local call onto a priced one.
			name: "ollama-openai preset prices as ollama",
			opts: checkOptions{Provider: "ollama-openai", Model: "qwen2.5:3b"},
			want: checkTarget{
				Provider: "ollama-openai", PricingKey: "ollama", Codec: "openai-compat",
				Endpoint: "http://localhost:11434/v1", Model: "qwen2.5:3b", KeySource: "none",
			},
		},
		{
			name: "vllm preset prices as local",
			opts: checkOptions{Provider: "vllm", Model: "Qwen/Qwen3-8B", BaseURL: srvURL},
			want: checkTarget{
				Provider: "vllm", PricingKey: "local", Codec: "openai-compat",
				Endpoint: srvURL, Model: "Qwen/Qwen3-8B", KeySource: "none",
			},
		},
		{
			// withDefaults would resolve an empty BaseURL to api.openai.com and
			// send the operator's traffic somewhere they never named.
			name:    "vllm without an endpoint is refused, not defaulted",
			opts:    checkOptions{Provider: "vllm", Model: "Qwen/Qwen3-8B"},
			wantErr: cli.ExitValidation,
		},
		{
			name: "deepseek preset has no key env of its own",
			opts: checkOptions{Provider: "deepseek", Model: "deepseek-chat"},
			env:  map[string]string{"OPENAI_API_KEY": "sk-not-this-one"},
			want: checkTarget{
				Provider: "deepseek", PricingKey: "deepseek", Codec: "openai-compat",
				Endpoint: "https://api.deepseek.com/v1", Model: "deepseek-chat", KeySource: "none",
			},
		},
		{
			name: "case and padding are normalized",
			opts: checkOptions{Provider: "  OLLAMA-OpenAI ", Model: "qwen2.5:3b"},
			want: checkTarget{
				Provider: "ollama-openai", PricingKey: "ollama", Codec: "openai-compat",
				Endpoint: "http://localhost:11434/v1", Model: "qwen2.5:3b", KeySource: "none",
			},
		},
		{
			name:    "unknown provider is a not-found, not a generic failure",
			opts:    checkOptions{Provider: "gemini", Model: "gemini-2.5-pro"},
			wantErr: cli.ExitNotFound,
		},
		{
			name:    "no provider",
			opts:    checkOptions{Model: "gpt-5.5"},
			wantErr: cli.ExitValidation,
		},
		{
			name:    "no model",
			opts:    checkOptions{Provider: "ollama"},
			wantErr: cli.ExitValidation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, got, err := buildCheckProvider(tc.opts, envFunc(tc.env))
			if tc.wantErr != 0 {
				if err == nil {
					t.Fatalf("expected error, got target %+v", got)
				}
				if code := cli.ExitCodeFor(err); code != tc.wantErr {
					t.Errorf("exit code = %d, want %d (err: %v)", code, tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildCheckProvider: %v", err)
			}
			if got != tc.want {
				t.Errorf("target = %+v\nwant     %+v", got, tc.want)
			}
			if p == nil {
				t.Fatal("provider is nil")
			}
			// Provider.Name() is what paymaster is keyed on downstream, so
			// the reported pricing key has to be the constructed one.
			if p.Name() != tc.want.PricingKey {
				t.Errorf("Provider.Name() = %q, want %q", p.Name(), tc.want.PricingKey)
			}
		})
	}
}

func TestResolveCheckKey(t *testing.T) {
	tests := []struct {
		name       string
		flagKey    string
		keyEnv     string
		env        map[string]string
		wantKey    string
		wantSource string
	}{
		{"flag wins", "sk-flag", "OPENAI_API_KEY", map[string]string{"OPENAI_API_KEY": "sk-env"}, "sk-flag", "--api-key"},
		{"env used", "", "OPENAI_API_KEY", map[string]string{"OPENAI_API_KEY": "sk-env"}, "sk-env", "$OPENAI_API_KEY"},
		{"env unset", "", "OPENAI_API_KEY", nil, "", "unset ($OPENAI_API_KEY)"},
		{"provider needs no key", "", "", map[string]string{"OPENAI_API_KEY": "sk-env"}, "", "none"},
		{"whitespace is not a key", "   ", "OPENAI_API_KEY", map[string]string{"OPENAI_API_KEY": "  "}, "", "unset ($OPENAI_API_KEY)"},
		{"key is trimmed", "  sk-flag  ", "OPENAI_API_KEY", nil, "sk-flag", "--api-key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, source := resolveCheckKey(tc.flagKey, tc.keyEnv, envFunc(tc.env))
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			// The label is printed; it must never carry the secret.
			if tc.wantKey != "" && strings.Contains(source, tc.wantKey) {
				t.Errorf("key source %q leaks the key", source)
			}
		})
	}
}

func TestCheckProviderNames(t *testing.T) {
	got := checkProviderNames()
	// Registry first, in declaration order — the order the console's picker
	// renders and the one RegisteredProviders promises.
	reg := llm.RegisteredProviders()
	if len(got) < len(reg) {
		t.Fatalf("names = %v, shorter than the registry %v", got, reg)
	}
	for i, id := range reg {
		if got[i] != id {
			t.Errorf("names[%d] = %q, want %q (registry order must survive)", i, got[i], id)
		}
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate %q in %v", n, got)
		}
		seen[n] = true
	}
	for _, want := range []string{"deepseek", "ollama-openai", "vllm"} {
		if !seen[want] {
			t.Errorf("preset %q is missing from %v", want, got)
		}
	}
	// "openai" is both a registry id and a preset key; it must appear once.
	if !seen["openai"] {
		t.Errorf("names = %v, missing openai", got)
	}
}

// --- unit: the call itself, against a local stub ---

// openAIStub serves one Chat Completions response. Everything the check
// reports — tokens, stop reason, reply — comes off this wire, so the stub is
// the fixture for the whole report.
func openAIStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunProviderCheck(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		pricingKey     string
		model          string
		wantOut        int
		wantCached     int
		wantStop       string
		wantReply      string
		wantSource     rateSource
		wantCostNotNil bool
	}{
		{
			name: "hand-written rate, no caching",
			body: `{"choices":[{"message":{"role":"assistant","content":"Pong"},"finish_reason":"stop"}],
			        "usage":{"prompt_tokens":1000,"completion_tokens":500}}`,
			pricingKey: "openai", model: "gpt-5.5",
			wantOut: 500, wantStop: "end_turn", wantReply: "Pong",
			wantSource: rateFromTable, wantCostNotNil: true,
		},
		{
			name: "cached reads are carried through to the report",
			body: `{"choices":[{"message":{"role":"assistant","content":"Pong"},"finish_reason":"stop"}],
			        "usage":{"prompt_tokens":1500,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":1200}}}`,
			pricingKey: "openai", model: "gpt-5.5",
			wantOut: 50, wantCached: 1200, wantStop: "end_turn", wantReply: "Pong",
			wantSource: rateFromTable, wantCostNotNil: true,
		},
		{
			name: "a free provider reports a free call",
			body: `{"choices":[{"message":{"role":"assistant","content":"Pong!"},"finish_reason":"stop"}],
			        "usage":{"prompt_tokens":37,"completion_tokens":4}}`,
			pricingKey: "ollama", model: "qwen2.5:0.5b",
			wantOut: 4, wantStop: "end_turn", wantReply: "Pong!",
			wantSource: rateFromFree,
		},
		{
			// The finding the command exists to surface: a backend that
			// answers but reports nothing bills at $0 forever.
			name:       "no usage block at all",
			body:       `{"choices":[{"message":{"role":"assistant","content":"Pong"},"finish_reason":"stop"}]}`,
			pricingKey: "openai", model: "gpt-5.5",
			wantStop: "end_turn", wantReply: "Pong", wantSource: rateFromTable,
		},
		{
			name: "truncation is reported, not hidden",
			body: `{"choices":[{"message":{"role":"assistant","content":"Pong and then some"},"finish_reason":"length"}],
			        "usage":{"prompt_tokens":10,"completion_tokens":64}}`,
			pricingKey: "openai", model: "gpt-5.5",
			wantOut: 64, wantStop: "max_tokens", wantReply: "Pong and then some",
			wantSource: rateFromTable, wantCostNotNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := openAIStub(t, tc.body)
			p := llm.NewOpenAICompat(llm.OpenAICompatConfig{
				Name: tc.pricingKey, BaseURL: srv.URL + "/v1", NoAuth: true,
			})
			target := checkTarget{
				Provider: tc.pricingKey, PricingKey: tc.pricingKey,
				Codec: string(llm.CodecOpenAICompat), Endpoint: srv.URL + "/v1",
				Model: tc.model, KeySource: "none",
			}

			res, err := runProviderCheck(context.Background(), p, target, "")
			if err != nil {
				t.Fatalf("runProviderCheck: %v", err)
			}
			if res.checkTarget != target {
				t.Errorf("target = %+v, want %+v", res.checkTarget, target)
			}
			if res.OutputToks != tc.wantOut {
				t.Errorf("OutputToks = %d, want %d", res.OutputToks, tc.wantOut)
			}
			if res.CachedInputToks != tc.wantCached {
				t.Errorf("CachedInputToks = %d, want %d", res.CachedInputToks, tc.wantCached)
			}
			if res.StopReason != tc.wantStop {
				t.Errorf("StopReason = %q, want %q", res.StopReason, tc.wantStop)
			}
			if res.Reply != tc.wantReply {
				t.Errorf("Reply = %q, want %q", res.Reply, tc.wantReply)
			}
			if res.RateSource != tc.wantSource {
				t.Errorf("RateSource = %q, want %q", res.RateSource, tc.wantSource)
			}
			if res.LatencyMS < 0 {
				t.Errorf("LatencyMS = %d", res.LatencyMS)
			}
			if tc.wantCostNotNil && res.CostUSD <= 0 {
				t.Errorf("CostUSD = %v, want > 0 for a priced model that reported tokens", res.CostUSD)
			}
			if !tc.wantCostNotNil && res.CostUSD != 0 {
				t.Errorf("CostUSD = %v, want 0", res.CostUSD)
			}
		})
	}
}

// The cost must be the one paymaster would write to cost_ledger, not a
// recomputation — that is the only reason to print it at all.
func TestRunProviderCheck_CostMatchesPaymaster(t *testing.T) {
	srv := openAIStub(t, `{"choices":[{"message":{"content":"Pong"},"finish_reason":"stop"}],
	                       "usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	p := llm.NewOpenAICompat(llm.OpenAICompatConfig{Name: "openai", BaseURL: srv.URL + "/v1", NoAuth: true})
	target := checkTarget{Provider: "openai", PricingKey: "openai", Model: "gpt-5.5"}

	res, err := runProviderCheck(context.Background(), p, target, "")
	if err != nil {
		t.Fatalf("runProviderCheck: %v", err)
	}
	want := paymasterEstimateForCheck("openai", "gpt-5.5",
		res.InputToks, res.OutputToks, res.CachedInputToks, res.CacheCreationToks)
	if res.CostUSD != want {
		t.Errorf("CostUSD = %v, want %v", res.CostUSD, want)
	}
}

func TestRunProviderCheck_UpstreamErrorIsPreserved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model 'nope' not found","type":"not_found_error"}}`))
	}))
	defer srv.Close()

	p := llm.NewOpenAICompat(llm.OpenAICompatConfig{Name: "openai", BaseURL: srv.URL + "/v1", NoAuth: true})
	target := checkTarget{Provider: "openai", PricingKey: "openai", Model: "nope"}

	_, err := runProviderCheck(context.Background(), p, target, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	// The provider's own words are the diagnosis; rewording them throws away
	// the only specific part of the message.
	if !strings.Contains(err.Error(), "model 'nope' not found") {
		t.Errorf("error %q does not carry the upstream text", err)
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitServer {
		t.Errorf("exit code = %d, want %d (an answer that says no)", code, cli.ExitServer)
	}
}

func TestRunProviderCheck_UnreachableEndpointIsAConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	p := llm.NewOpenAICompat(llm.OpenAICompatConfig{Name: "openai", BaseURL: url + "/v1", NoAuth: true})
	_, err := runProviderCheck(context.Background(), p,
		checkTarget{Provider: "openai", PricingKey: "openai", Model: "gpt-5.5"}, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitConnection {
		t.Errorf("exit code = %d, want %d (nothing answered at that address)", code, cli.ExitConnection)
	}
}

// An expired deadline must not be reported as an API rejection: a script that
// retries a timeout but not a 400 depends on the difference. The deadline is
// already in the past rather than "short", so the client fails on the first
// attempt and the test neither sleeps nor races the retry backoff.
func TestRunProviderCheck_TimeoutIsAConnectionError(t *testing.T) {
	srv := openAIStub(t, `{"choices":[{"message":{"content":"Pong"},"finish_reason":"stop"}]}`)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	p := llm.NewOpenAICompat(llm.OpenAICompatConfig{Name: "openai", BaseURL: srv.URL + "/v1", NoAuth: true})
	_, err := runProviderCheck(ctx, p,
		checkTarget{Provider: "openai", PricingKey: "openai", Model: "gpt-5.5"}, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitConnection {
		t.Errorf("exit code = %d, want %d", code, cli.ExitConnection)
	}
}

// The prompt actually reaches the wire, and an empty one becomes the default
// rather than an empty user turn (which several backends reject outright).
func TestRunProviderCheck_PromptReachesTheWire(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{"explicit", "say hi", "say hi"},
		{"empty falls back to the default", "", defaultCheckPrompt},
		{"whitespace falls back too", "   ", defaultCheckPrompt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
				Messages  []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode request: %v", err)
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer srv.Close()

			p := llm.NewOpenAICompat(llm.OpenAICompatConfig{Name: "openai", BaseURL: srv.URL + "/v1", NoAuth: true})
			if _, err := runProviderCheck(context.Background(), p,
				checkTarget{Provider: "openai", PricingKey: "openai", Model: "gpt-5.5"}, tc.prompt); err != nil {
				t.Fatalf("runProviderCheck: %v", err)
			}
			if len(got.Messages) != 1 || got.Messages[0].Content != tc.want {
				t.Errorf("messages = %+v, want one user turn %q", got.Messages, tc.want)
			}
			if got.Messages[0].Role != llm.RoleUser {
				t.Errorf("role = %q, want %q", got.Messages[0].Role, llm.RoleUser)
			}
			if got.Model != "gpt-5.5" {
				t.Errorf("model = %q, want gpt-5.5", got.Model)
			}
			if got.MaxTokens != checkMaxTokens {
				t.Errorf("max_tokens = %d, want %d", got.MaxTokens, checkMaxTokens)
			}
		})
	}
}

func TestCheckExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"api rejection", errors.New("OpenAI API returned 401: invalid api key"), cli.ExitServer},
		{"transport failure", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, cli.ExitConnection},
		{"wrapped transport failure", fmt.Errorf("openai http: %w", &url.Error{Op: "Post", Err: errors.New("refused")}), cli.ExitConnection},
		// llm/httpretry.go returns a BARE ctx.Err() when the deadline expires
		// during retry backoff, so a refused connection that the retry loop was
		// still sleeping on arrives with no *url.Error to unwrap. Reproduced
		// live: `provider check --provider deepseek --base-url
		// http://127.0.0.1:9/v1 --timeout 3s` reported exit 7. The deadline
		// belongs to this command (--timeout), so nothing answered inside it —
		// that is 8, and a script that retries 8 but not 7 must see 8.
		{"deadline expired during retry backoff", context.DeadlineExceeded, cli.ExitConnection},
		{"wrapped deadline", fmt.Errorf("deepseek: %w", context.DeadlineExceeded), cli.ExitConnection},
		{"cancelled", context.Canceled, cli.ExitConnection},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkExitCode(tc.err); got != tc.want {
				t.Errorf("checkExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestPrintProviderCheck_DoesNotPanic(t *testing.T) {
	for _, res := range []providerCheckResult{
		{
			checkTarget: checkTarget{Provider: "openai", PricingKey: "openai", Codec: "openai-compat",
				Endpoint: "https://api.openai.com/v1/chat/completions", Model: "gpt-5.5", KeySource: "$OPENAI_API_KEY"},
			LatencyMS: 812, StopReason: "end_turn", InputToks: 1000, OutputToks: 500,
			CostUSD: 0.016, Rates: rateCard{InputPerM: 4}, RateSource: rateFromTable, Reply: "Pong",
		},
		{
			// The zero-usage path, plus a preset whose pricing key differs.
			checkTarget: checkTarget{Provider: "ollama-openai", PricingKey: "ollama", Codec: "openai-compat",
				Endpoint: "http://localhost:11434/v1", Model: "qwen2.5:3b", KeySource: "none"},
			RateSource: rateFromFree,
		},
	} {
		printProviderCheck(res)
	}
}

// --- acceptance: the built binary against a stub, with no config ---

func TestAcceptance_ProviderCheck_JSON(t *testing.T) {
	bin := buildCrewshipBinary(t)

	srv := openAIStub(t, `{"choices":[{"message":{"role":"assistant","content":"Pong"},"finish_reason":"stop"}],
	                       "usage":{"prompt_tokens":1000,"completion_tokens":500}}`)

	// No token, no workspace, no --server: a config path that does not exist
	// is the proof the command never reaches for an API client.
	cmd := exec.Command(bin, "provider", "check",
		"--provider", "openai", "--base-url", srv.URL+"/v1",
		"--model", "gpt-5.5", "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+filepath.Join(t.TempDir(), "absent.yaml"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	var got providerCheckResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.5" {
		t.Errorf("identity = %q/%q", got.Provider, got.Model)
	}
	if got.Endpoint != srv.URL+"/v1" {
		t.Errorf("endpoint = %q, want %q", got.Endpoint, srv.URL+"/v1")
	}
	if got.OutputToks != 500 {
		t.Errorf("output_tokens = %d, want 500", got.OutputToks)
	}
	if got.Reply != "Pong" {
		t.Errorf("reply = %q", got.Reply)
	}
	if got.CostUSD <= 0 {
		t.Errorf("cost_usd = %v, want > 0", got.CostUSD)
	}
	if got.RateSource != rateFromTable {
		t.Errorf("rate_source = %q, want %q", got.RateSource, rateFromTable)
	}
}

func TestAcceptance_ProviderCheck_Human(t *testing.T) {
	bin := buildCrewshipBinary(t)

	srv := openAIStub(t, `{"choices":[{"message":{"content":"Pong"},"finish_reason":"stop"}],
	                       "usage":{"prompt_tokens":37,"completion_tokens":4}}`)

	cmd := exec.Command(bin, "provider", "check",
		"--provider", "ollama-openai", "--base-url", srv.URL+"/v1",
		"--model", "qwen2.5:0.5b", "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+filepath.Join(t.TempDir(), "absent.yaml"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"ollama-openai", "openai-compat", "pricing key", "latency", "tokens", "$0.000000", "Pong"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestAcceptance_ProviderCheck_ExitCodes(t *testing.T) {
	bin := buildCrewshipBinary(t)

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"unknown provider", []string{"provider", "check", "--provider", "gemini", "--model", "x"}, cli.ExitNotFound},
		{"no model", []string{"provider", "check", "--provider", "ollama"}, cli.ExitValidation},
		{"vllm with no endpoint", []string{"provider", "check", "--provider", "vllm", "--model", "x"}, cli.ExitValidation},
		{"nothing listening", []string{"provider", "check", "--provider", "openai",
			"--base-url", "http://127.0.0.1:1/v1", "--model", "gpt-5.5", "--timeout", "5s"}, cli.ExitConnection},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+filepath.Join(t.TempDir(), "absent.yaml"))
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected failure, got success:\n%s", out)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("err = %v (%T), want *exec.ExitError", err, err)
			}
			if ee.ExitCode() != tc.want {
				t.Errorf("exit = %d, want %d\noutput: %s", ee.ExitCode(), tc.want, out)
			}
		})
	}
}

// anthropicStub serves one canned /v1/messages response — the real wire shape,
// not an OpenAI body with the fields renamed.
func anthropicStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The Anthropic codec is the one this command has never been run against for
// real — nobody on this project has held an ANTHROPIC_API_KEY while the command
// existed, and every other RunProviderCheck case drives an OpenAI-shaped body.
// That leaves the second of the two shipped codecs proven only by construction:
// TestBuildCheckProvider asserts it resolves to the right endpoint and codec
// name, and nothing asserts a response ever parses.
//
// The wire shapes differ in exactly the places a bug hides. Anthropic returns
// content BLOCKS rather than a message string, names its stop reason
// "end_turn" rather than "stop", and — the one that matters for billing —
// reports cache_read_input_tokens SEPARATELY from input_tokens, where OpenAI
// reports cached_tokens INSIDE prompt_tokens. A codec that mixed those up
// double-counts, which is the bug already fixed on the OpenAI side; this pins
// that Anthropic's (correct, untouched) accounting stays that way.
func TestRunProviderCheck_AnthropicWireShape(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantIn     int
		wantOut    int
		wantCached int
		wantCreate int
		wantStop   string
		wantReply  string
	}{
		{
			name: "plain reply",
			body: `{"content":[{"type":"text","text":"Pong"}],"stop_reason":"end_turn",
			        "usage":{"input_tokens":1000,"output_tokens":500}}`,
			wantIn: 1000, wantOut: 500, wantStop: "end_turn", wantReply: "Pong",
		},
		{
			// input_tokens must be carried through UNCHANGED. Subtracting the
			// cache read here — the fix the OpenAI codec needed — would
			// under-report input by 1200 on this body.
			name: "cache read is separate from input, not inside it",
			body: `{"content":[{"type":"text","text":"Pong"}],"stop_reason":"end_turn",
			        "usage":{"input_tokens":300,"output_tokens":50,
			                 "cache_read_input_tokens":1200,"cache_creation_input_tokens":80}}`,
			wantIn: 300, wantOut: 50, wantCached: 1200, wantCreate: 80,
			wantStop: "end_turn", wantReply: "Pong",
		},
		{
			name: "truncation is reported, not hidden",
			body: `{"content":[{"type":"text","text":"Po"}],"stop_reason":"max_tokens",
			        "usage":{"input_tokens":10,"output_tokens":64}}`,
			wantIn: 10, wantOut: 64, wantStop: "max_tokens", wantReply: "Po",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := anthropicStub(t, tc.body)
			p := llm.NewAnthropicWith(llm.AnthropicConfig{
				BaseURL: srv.URL + "/v1/messages", APIKey: "test-key",
			})
			target := checkTarget{
				Provider: "anthropic", PricingKey: "anthropic",
				Codec: string(llm.CodecAnthropicMessages), Endpoint: srv.URL + "/v1/messages",
				Model: "claude-haiku-4-5", KeySource: "--api-key",
			}

			res, err := runProviderCheck(context.Background(), p, target, "")
			if err != nil {
				t.Fatalf("runProviderCheck: %v", err)
			}
			if res.InputToks != tc.wantIn {
				t.Errorf("InputToks = %d, want %d — the anthropic codec must carry input_tokens through unchanged", res.InputToks, tc.wantIn)
			}
			if res.OutputToks != tc.wantOut {
				t.Errorf("OutputToks = %d, want %d", res.OutputToks, tc.wantOut)
			}
			if res.CachedInputToks != tc.wantCached {
				t.Errorf("CachedInputToks = %d, want %d", res.CachedInputToks, tc.wantCached)
			}
			if res.CacheCreationToks != tc.wantCreate {
				t.Errorf("CacheCreationToks = %d, want %d", res.CacheCreationToks, tc.wantCreate)
			}
			if res.StopReason != tc.wantStop {
				t.Errorf("StopReason = %q, want %q", res.StopReason, tc.wantStop)
			}
			if res.Reply != tc.wantReply {
				t.Errorf("Reply = %q, want %q — content blocks must be flattened to text", res.Reply, tc.wantReply)
			}
			if res.CostUSD <= 0 {
				t.Errorf("CostUSD = %v, want > 0 — anthropic/claude-haiku-4-5 is a priced model", res.CostUSD)
			}
		})
	}
}

// A key supplied to a keyless preset must actually be sent. The vllm and
// ollama-openai presets carry NoAuth, and OpenAI.applyHeaders returns early on
// it, so assigning APIKey without clearing NoAuth silently drops the
// credential — the operator sees a 401 next to "api key --api-key" and goes
// looking at the key's value instead of at the missing header.
func TestBuildFromPreset_SuppliedKeyClearsNoAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Pong"},"finish_reason":"stop"}],
		                        "usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	p, target, err := buildCheckProvider(checkOptions{
		Provider: "vllm", BaseURL: srv.URL + "/v1", Model: "m", APIKey: "sk-test-key",
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("buildCheckProvider: %v", err)
	}
	if target.KeySource != "--api-key" {
		t.Fatalf("KeySource = %q, want --api-key", target.KeySource)
	}
	if _, err := runProviderCheck(context.Background(), p, target, ""); err != nil {
		t.Fatalf("runProviderCheck: %v", err)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Errorf("Authorization = %q, want %q — a preset's NoAuth must not swallow an explicit key", gotAuth, "Bearer sk-test-key")
	}
}

// The endpoint is echoed into human output, JSON, and therefore bug reports.
// A base URL carrying userinfo must not take the credential with it.
func TestBuildCheckProvider_RedactsUserinfoInReportedEndpoint(t *testing.T) {
	for _, provider := range []string{"openai", "vllm"} {
		t.Run(provider, func(t *testing.T) {
			_, target, err := buildCheckProvider(checkOptions{
				Provider: provider,
				BaseURL:  "https://user:supersecret@example.invalid/v1",
				Model:    "m",
			}, func(string) string { return "" })
			if err != nil {
				t.Fatalf("buildCheckProvider: %v", err)
			}
			if strings.Contains(target.Endpoint, "supersecret") {
				t.Errorf("Endpoint = %q leaks the credential", target.Endpoint)
			}
			if !strings.Contains(target.Endpoint, "example.invalid") {
				t.Errorf("Endpoint = %q lost the host — redaction must not destroy the diagnosis", target.Endpoint)
			}
		})
	}
}
