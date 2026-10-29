package sidecar

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Frozen legacy oracle from 9cf047dff5949a43e765402dcb30f3a03bda6a04.
// Only local symbols were renamed; keep independent of production helpers.
func g08FrozenParse(codec, body string) LLMUsage {
	var out LLMUsage
	if body == "" {
		return out
	}

	switch codec {
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
		// TWO usage shapes, because the codec serves two OpenAI APIs and they
		// do not agree on field names.
		//
		//   chat.completions:  usage.prompt_tokens / completion_tokens
		//                      usage.prompt_tokens_details.cached_tokens
		//   responses:         usage.input_tokens / output_tokens
		//                      usage.input_tokens_details.cached_tokens
		//
		// The sidecar proxies /openai/v1/responses (it has its own golden
		// fixture), so the second family is not hypothetical. Parsing only the
		// first meant every Responses call reported zero tokens and the cost
		// event was dropped — the same silent-zero failure this whole codepath
		// was just fixed for, surviving in the one API the comment above used
		// to claim was already covered.
		//
		// Cached-token details landed on the public API in late 2024; older
		// models omit them, which is fine — zero, and the paymaster math holds.
		var msg struct {
			Model string `json:"model"`
			Usage struct {
				PromptTokens        int64 `json:"prompt_tokens"`
				CompletionTokens    int64 `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`

				InputTokens        int64 `json:"input_tokens"`
				OutputTokens       int64 `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			return out
		}
		out.Model = msg.Model

		// Whichever family the body actually used. A response carries one or
		// the other, never both, so summing would be wrong and preferring a
		// fixed one would zero the other.
		in, outTok, cached := msg.Usage.PromptTokens, msg.Usage.CompletionTokens, msg.Usage.PromptTokensDetails.CachedTokens
		if in == 0 && outTok == 0 && cached == 0 {
			in, outTok, cached = msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.InputTokensDetails.CachedTokens
		}

		// Both families' input count INCLUDES cached tokens; subtract so the
		// fresh-input number we feed to paymaster.Estimate is what gets
		// billed at the input rate (cached gets the cached rate).
		fresh := in - cached
		if fresh < 0 {
			fresh = 0
		}
		out.InputTokens = fresh
		out.OutputTokens = outTok
		out.CachedInputTokens = cached
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

// g08FrozenParseSSE extracts the last/cumulative usage values from Server-Sent
// Events. The streaming observer calls it per event with reusable scratch;
// callers with a complete response can use it directly. Providers use
// three common event shapes: a normal response object in data:, OpenAI
// Responses' {response:{...}}, and Anthropic's {message:{...}} start event.
func g08FrozenParseSSE(codec, body string) LLMUsage {
	var lines []string
	return g08FrozenParseSSEWithScratch(codec, body, &lines)
}

func g08FrozenParseSSEWithScratch(codec, body string, scratch *[]string) LLMUsage {
	var out LLMUsage
	dataLines := (*scratch)[:0]
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		clear(dataLines)
		dataLines = dataLines[:0]
		if data == "[DONE]" {
			return
		}
		candidates := []string{data}
		var envelope struct {
			Response json.RawMessage `json:"response"`
			Message  json.RawMessage `json:"message"`
		}
		if json.Unmarshal([]byte(data), &envelope) == nil {
			if len(envelope.Response) > 0 {
				candidates = append(candidates, string(envelope.Response))
			}
			if len(envelope.Message) > 0 {
				candidates = append(candidates, string(envelope.Message))
			}
		}
		for _, candidate := range candidates {
			g08FrozenMerge(&out, g08FrozenParse(codec, candidate))
		}
	}

	// Iterate without allocating a slice entry for every SSE line.
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	*scratch = dataLines
	return out
}

func g08FrozenMerge(dst *LLMUsage, src LLMUsage) {
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.InputTokens > dst.InputTokens {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > dst.OutputTokens {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CachedInputTokens > dst.CachedInputTokens {
		dst.CachedInputTokens = src.CachedInputTokens
	}
	if src.CacheCreationTokens > dst.CacheCreationTokens {
		dst.CacheCreationTokens = src.CacheCreationTokens
	}
}

// g08FrozenObserver retains one event, never the entire response. This keeps
// final usage observable even when a stream exceeds the response-buffer cap.
// Oversized events are discarded through their delimiter without interrupting
// delivery; callers must report usage as unknown if any event was skipped.
type g08FrozenObserver struct {
	codec   string
	limit   int
	buf     []byte
	lines   []string
	usage   LLMUsage
	lineLen int // saturated at 2: only empty and CR-only lines delimit events
	lineCR  bool
	discard bool
	skipped int
}

func (s *g08FrozenObserver) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		end := len(p)
		if i >= 0 {
			end = i + 1
		}
		part := p[:end]
		s.append(part)
		content := part
		if i >= 0 {
			content = part[:len(part)-1]
		}
		if s.lineLen == 0 && len(content) > 0 {
			s.lineCR = content[0] == '\r'
		}
		s.lineLen = min(2, s.lineLen+len(content))
		if i >= 0 {
			if s.lineLen == 0 || (s.lineLen == 1 && s.lineCR) {
				s.flush()
			}
			s.lineLen, s.lineCR = 0, false
		}
		p = p[end:]
	}
	return n, nil
}

func (s *g08FrozenObserver) append(p []byte) {
	if s.discard {
		return
	}
	if len(p) > s.limit-len(s.buf) {
		s.discard = true
		s.buf = s.buf[:0]
		return
	}
	need := len(s.buf) + len(p)
	if need > cap(s.buf) {
		// append's implicit capacity growth may exceed the declared limit.
		capacity := min(s.limit, max(need, max(1024, cap(s.buf)*2)))
		buf := make([]byte, len(s.buf), capacity)
		copy(buf, s.buf)
		s.buf = buf
	}
	s.buf = append(s.buf, p...)
}

func (s *g08FrozenObserver) flush() {
	if s.discard {
		s.skipped++
	} else if len(s.buf) > 0 {
		g08FrozenMerge(&s.usage, g08FrozenParseSSEWithScratch(s.codec, string(s.buf), &s.lines))
	}
	s.buf = s.buf[:0]
	s.discard = false
}

func (s *g08FrozenObserver) finish() LLMUsage {
	// Preserve the previous parser's acceptance of an unterminated final event.
	s.flush()
	if s.skipped > 0 {
		// Partial observations must not masquerade as complete token totals.
		return LLMUsage{Model: s.usage.Model}
	}
	return s.usage
}
