package sidecar

import (
	"net/http"
	"testing"
)

// TestParseLLMUsageAnthropic exercises the canonical Anthropic message-stop
// body shape, including the cache-creation / cache-read split that our
// pricing path treats independently. Numbers are taken from a real (mocked)
// Anthropic response so a future format change to `usage` jumps out as a
// failing assertion rather than a silently broken ledger.
func TestParseLLMUsageAnthropic(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4-6",
		"usage": {
			"input_tokens": 50,
			"output_tokens": 200,
			"cache_creation_input_tokens": 1000,
			"cache_read_input_tokens": 5000
		}
	}`
	got := parseLLMUsage("anthropic", body)
	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("model: got %q want claude-sonnet-4-6", got.Model)
	}
	if got.InputTokens != 50 || got.OutputTokens != 200 {
		t.Errorf("token counts: got in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.CachedInputTokens != 5000 || got.CacheCreationTokens != 1000 {
		t.Errorf("cache split: got cached=%d created=%d", got.CachedInputTokens, got.CacheCreationTokens)
	}
}

// TestParseLLMUsageOpenAI confirms the prompt/completion split and the
// `prompt_tokens_details.cached_tokens` subtraction — the math that keeps
// us from billing cached input at the fresh-input rate.
func TestParseLLMUsageOpenAI(t *testing.T) {
	body := `{
		"model": "gpt-5.5",
		"usage": {
			"prompt_tokens": 1500,
			"completion_tokens": 400,
			"prompt_tokens_details": {"cached_tokens": 1200}
		}
	}`
	got := parseLLMUsage("openai", body)
	if got.Model != "gpt-5.5" {
		t.Errorf("model: got %q", got.Model)
	}
	// fresh = prompt - cached = 1500 - 1200 = 300
	if got.InputTokens != 300 {
		t.Errorf("fresh input: got %d want 300", got.InputTokens)
	}
	if got.CachedInputTokens != 1200 {
		t.Errorf("cached: got %d want 1200", got.CachedInputTokens)
	}
	if got.OutputTokens != 400 {
		t.Errorf("output: got %d want 400", got.OutputTokens)
	}
}

// TestParseLLMUsageGoogle exercises the Gemini usageMetadata shape, which
// uses different field names from both Anthropic and OpenAI.
func TestParseLLMUsageGoogle(t *testing.T) {
	body := `{
		"modelVersion": "gemini-2.5-pro",
		"usageMetadata": {
			"promptTokenCount": 800,
			"candidatesTokenCount": 250,
			"cachedContentTokenCount": 500
		}
	}`
	got := parseLLMUsage("google", body)
	if got.Model != "gemini-2.5-pro" {
		t.Errorf("model: got %q", got.Model)
	}
	// fresh = prompt - cached = 800 - 500 = 300
	if got.InputTokens != 300 {
		t.Errorf("fresh input: got %d want 300", got.InputTokens)
	}
	if got.CachedInputTokens != 500 {
		t.Errorf("cached: got %d want 500", got.CachedInputTokens)
	}
}

// TestParseLLMUsageGarbage confirms that a malformed body never crashes
// the proxy — empty struct returned, parsing continues. This is load-
// bearing: the parse path is on the hot proxy goroutine and an unhandled
// JSON error would leak as a 502 to the agent.
func TestParseLLMUsageGarbage(t *testing.T) {
	got := parseLLMUsage("anthropic", "this is not json")
	if got.Provider != "anthropic" {
		t.Errorf("provider should be preserved: got %q", got.Provider)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("malformed body should yield zero counts: %+v", got)
	}
}

// TestParseQuotaInfoAnthropic asserts the most-restrictive-window selection
// logic — when both tokens-per-minute (95% remaining) and input-tokens-per-
// minute (5% remaining) headers are present, we pick the latter so the
// operator's signal reflects the actual bottleneck.
func TestParseQuotaInfoAnthropic(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-limit", "1000")
	h.Set("anthropic-ratelimit-requests-remaining", "950")
	h.Set("anthropic-ratelimit-tokens-limit", "100000")
	h.Set("anthropic-ratelimit-tokens-remaining", "95000")
	h.Set("anthropic-ratelimit-input-tokens-limit", "100000")
	h.Set("anthropic-ratelimit-input-tokens-remaining", "5000") // 5% — closest

	q := parseQuotaInfo(h, http.StatusOK)
	if q.HadStatus429 {
		t.Error("200 status should not flag 429")
	}
	if q.Window != "input_tokens_per_min" {
		t.Errorf("window: got %q want input_tokens_per_min (closest pinch)", q.Window)
	}
	// 5000 / 100000 = 0.05
	if q.RemainingPct < 0.04 || q.RemainingPct > 0.06 {
		t.Errorf("remaining pct: got %.4f want ~0.05", q.RemainingPct)
	}
}

// TestParseQuotaInfoOpenAI mirrors the Anthropic case for OpenAI's
// x-ratelimit-* headers — different header names, identical semantics.
func TestParseQuotaInfoOpenAI(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "500")
	h.Set("x-ratelimit-remaining-requests", "450")
	h.Set("x-ratelimit-limit-tokens", "200000")
	h.Set("x-ratelimit-remaining-tokens", "30000") // 15% — closer

	q := parseQuotaInfo(h, http.StatusOK)
	if q.Window != "tokens_per_min" {
		t.Errorf("window: got %q want tokens_per_min", q.Window)
	}
	if q.RemainingPct < 0.14 || q.RemainingPct > 0.16 {
		t.Errorf("remaining pct: got %.4f want ~0.15", q.RemainingPct)
	}
}

// TestParseQuotaInfoStatus429 confirms a 429 response surfaces as
// HadStatus429=true regardless of header completeness — the upstream's
// "you're out" is authoritative even when the headers got dropped.
func TestParseQuotaInfoStatus429(t *testing.T) {
	q := parseQuotaInfo(http.Header{}, http.StatusTooManyRequests)
	if !q.HadStatus429 {
		t.Error("429 status should flag HadStatus429")
	}
}

// TestParseQuotaInfoNoHeaders ensures the empty-headers path returns a
// fully-zero QuotaInfo so EnforceQuota can short-circuit on "nothing to
// say" and not emit spurious warnings on calls where the upstream
// (Google, OAuth tunnels) never returns rate-limit headers.
func TestParseQuotaInfoNoHeaders(t *testing.T) {
	q := parseQuotaInfo(http.Header{}, http.StatusOK)
	if q.RemainingPct != 0 || q.Window != "" || q.HadStatus429 {
		t.Errorf("expected zero QuotaInfo, got %+v", q)
	}
}

// TestIsJSONResponse covers the trivial cases plus the real-world pitfalls:
// uppercase `Content-Type`, charset suffix, leading/trailing whitespace.
// SSE responses must NOT be classified as JSON or the tee-buffer path
// would needlessly block streaming UX.
func TestIsJSONResponse(t *testing.T) {
	cases := map[string]bool{
		"":                                 false,
		"application/json":                 true,
		"Application/JSON":                 true,
		"application/json; charset=utf-8":  true,
		"  application/json  ":             true,
		"text/event-stream":                false,
		"text/event-stream; charset=utf-8": false,
		"text/plain":                       false,
	}
	for ct, want := range cases {
		if got := isJSONResponse(ct); got != want {
			t.Errorf("isJSONResponse(%q) = %v, want %v", ct, got, want)
		}
	}
}
