package sidecar

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// LLMUsage carries the token-level numbers we need to write a paymaster
// ledger row. All four channels are tracked separately because cached input
// is priced at ~10% of fresh input on every major provider, and conflating
// the two produces materially wrong cost figures (we got bitten by this in
// the v1 ceník — Opus 4.7 was 3× over because cached input was billed at
// fresh-input rates). Zero on any field means "not reported by upstream".
type LLMUsage struct {
	Provider            string
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CachedInputTokens   int64
	CacheCreationTokens int64
}

// QuotaInfo summarises the rate-limit headers the upstream returned. We
// surface only the most-restrictive remaining-pct so the paymaster's
// EnforceQuota has a single number to threshold against, plus the window
// label so the UI can tell the operator which axis pinched (requests vs
// tokens, input vs output). Zero RemainingPct + empty Window means the
// upstream returned no rate-limit signal — typical for Google, for OAuth
// CONNECT tunnels (TLS hides everything), and for transport errors that
// were resolved before headers landed.
type QuotaInfo struct {
	// RemainingPct is the smaller of (requests_remaining/limit,
	// tokens_remaining/limit) — i.e. whichever axis is closer to exhausted.
	// Range 0.0–1.0 inclusive; 0.0 only set when at least one axis was
	// genuinely zero in the headers (don't conflate with "no data").
	RemainingPct float64

	// Window names which axis RemainingPct refers to. Mirrors the paymaster
	// QuotaWindow enum strings 1:1.
	Window string

	// HadStatus429 is set when the upstream returned 429 — authoritative
	// "you're out", regardless of headers (some providers don't return
	// remaining=0 before they 429 you).
	HadStatus429 bool
}

// parseLLMUsage decodes the response body for a known LLM provider into
// LLMUsage. Only non-streaming JSON responses are supported — streaming
// (text/event-stream) returns zero usage because the stream's `usage` block
// isn't visible until the body is closed, which happens *after* we'd have
// already passed the body through to the client. Streaming usage tracking
// would need a tee-reader + goroutine; deferred until we have a clear
// product reason to spend the latency budget.
//
// Returns an empty LLMUsage on any parse failure — we never fail the proxy
// over a usage parse error because the agent's call already succeeded
// upstream, and surfacing a 502 to the client over a billing miss would be
// a strictly worse outcome than a missing ledger row.
func parseLLMUsage(provider, body string) LLMUsage {
	out := LLMUsage{Provider: provider}
	if body == "" {
		return out
	}

	switch provider {
	case "anthropic":
		// Anthropic message body shape:
		//   { "model": "...", "usage": {
		//        "input_tokens": int,
		//        "output_tokens": int,
		//        "cache_creation_input_tokens": int (optional),
		//        "cache_read_input_tokens": int (optional)
		//   } }
		// Field names mirror what's in the platform docs as of 2026-04. We
		// don't validate the rest of the body — partial JSON / extra fields
		// are fine.
		var msg struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			return out
		}
		out.Model = msg.Model
		out.InputTokens = msg.Usage.InputTokens
		out.OutputTokens = msg.Usage.OutputTokens
		out.CachedInputTokens = msg.Usage.CacheReadInputTokens
		out.CacheCreationTokens = msg.Usage.CacheCreationInputTokens

	case "openai":
		// OpenAI completion body shape (chat.completions and responses):
		//   { "model": "...", "usage": {
		//        "prompt_tokens": int,
		//        "completion_tokens": int,
		//        "prompt_tokens_details": { "cached_tokens": int }
		//   } }
		// The cached_tokens nested key landed on the public API in late 2024;
		// older models still omit it, which is fine — we get zero and the
		// paymaster math handles that.
		var msg struct {
			Model string `json:"model"`
			Usage struct {
				PromptTokens        int64 `json:"prompt_tokens"`
				CompletionTokens    int64 `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			return out
		}
		out.Model = msg.Model
		// OpenAI's `prompt_tokens` includes cached tokens; subtract so the
		// fresh-input number we feed to paymaster.Estimate is what gets
		// billed at the input rate (cached gets the cached rate).
		fresh := msg.Usage.PromptTokens - msg.Usage.PromptTokensDetails.CachedTokens
		if fresh < 0 {
			fresh = 0
		}
		out.InputTokens = fresh
		out.OutputTokens = msg.Usage.CompletionTokens
		out.CachedInputTokens = msg.Usage.PromptTokensDetails.CachedTokens
		// OpenAI does not report cache-write tokens separately — they bill at
		// the input rate, which our pricing.go reflects by setting
		// CacheWritePerM equal to InputPerM for OpenAI rows.

	case "google":
		// Gemini body shape:
		//   { "modelVersion": "...", "usageMetadata": {
		//        "promptTokenCount": int,
		//        "candidatesTokenCount": int,
		//        "cachedContentTokenCount": int (optional)
		//   } }
		var msg struct {
			ModelVersion  string `json:"modelVersion"`
			UsageMetadata struct {
				PromptTokenCount        int64 `json:"promptTokenCount"`
				CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
				CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			return out
		}
		out.Model = msg.ModelVersion
		fresh := msg.UsageMetadata.PromptTokenCount - msg.UsageMetadata.CachedContentTokenCount
		if fresh < 0 {
			fresh = 0
		}
		out.InputTokens = fresh
		out.OutputTokens = msg.UsageMetadata.CandidatesTokenCount
		out.CachedInputTokens = msg.UsageMetadata.CachedContentTokenCount
	}

	return out
}

// parseQuotaInfo extracts the most-restrictive remaining-quota signal from
// upstream rate-limit headers. The picked window is the one with the
// smallest remaining/limit ratio so EnforceQuota always sees the closest
// pinch — we'd rather warn on tokens-per-minute when that's the bottleneck
// and tell the operator "you're at 95% of TPM" than keep saying "you're
// fine on RPM" while every call retries.
//
// Both Anthropic and OpenAI return their headers lowercased on the wire,
// but Go's http.Header normalises to canonical mixed-case
// (Anthropic-Ratelimit-Tokens-Remaining), so we read via .Get which handles
// the canonicalisation automatically.
func parseQuotaInfo(h http.Header, statusCode int) QuotaInfo {
	q := QuotaInfo{HadStatus429: statusCode == http.StatusTooManyRequests}

	// Anthropic. The most useful single number is whichever of
	// {requests, tokens, input-tokens, output-tokens} is closest to zero.
	// Headers are documented at platform.claude.com/docs/en/api/rate-limits.
	pickAnthropic := func() (float64, string, bool) {
		bestPct := 1.01 // sentinel — any real reading will beat this
		bestWindow := ""
		any := false
		check := func(remHdr, limHdr, window string) {
			rem := readFloat(h, remHdr)
			lim := readFloat(h, limHdr)
			if lim <= 0 {
				return
			}
			any = true
			pct := rem / lim
			if pct < bestPct {
				bestPct = pct
				bestWindow = window
			}
		}
		check("anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-requests-limit", "requests_per_min")
		check("anthropic-ratelimit-tokens-remaining", "anthropic-ratelimit-tokens-limit", "tokens_per_min")
		check("anthropic-ratelimit-input-tokens-remaining", "anthropic-ratelimit-input-tokens-limit", "input_tokens_per_min")
		check("anthropic-ratelimit-output-tokens-remaining", "anthropic-ratelimit-output-tokens-limit", "output_tokens_per_min")
		if !any {
			return 0, "", false
		}
		if bestPct > 1.0 {
			bestPct = 1.0
		}
		return bestPct, bestWindow, true
	}

	if pct, window, ok := pickAnthropic(); ok {
		q.RemainingPct = pct
		q.Window = window
		return q
	}

	// OpenAI. Same idea but with x-ratelimit-* names. OpenAI doesn't split
	// input vs output, just "requests" and "tokens".
	pickOpenAI := func() (float64, string, bool) {
		bestPct := 1.01
		bestWindow := ""
		any := false
		check := func(remHdr, limHdr, window string) {
			rem := readFloat(h, remHdr)
			lim := readFloat(h, limHdr)
			if lim <= 0 {
				return
			}
			any = true
			pct := rem / lim
			if pct < bestPct {
				bestPct = pct
				bestWindow = window
			}
		}
		check("x-ratelimit-remaining-requests", "x-ratelimit-limit-requests", "requests_per_min")
		check("x-ratelimit-remaining-tokens", "x-ratelimit-limit-tokens", "tokens_per_min")
		if !any {
			return 0, "", false
		}
		if bestPct > 1.0 {
			bestPct = 1.0
		}
		return bestPct, bestWindow, true
	}

	if pct, window, ok := pickOpenAI(); ok {
		q.RemainingPct = pct
		q.Window = window
	}
	return q
}

// readFloat parses a numeric header value, returning 0 on any error or
// missing header. Header values that can't be parsed produce 0 rather than
// an error — the rate-limit pipe is best-effort and we prefer "no signal"
// over "the entire proxy hot path is now in an error branch".
func readFloat(h http.Header, name string) float64 {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

// isJSONResponse decides whether the upstream response is parseable as
// non-streaming JSON. We check the prefix only because some providers add
// charset (`application/json; charset=utf-8`) and we don't want to fail a
// match on that.
func isJSONResponse(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if ct == "" {
		return false
	}
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "application/json"
}
