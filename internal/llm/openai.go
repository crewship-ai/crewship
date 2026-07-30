package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/llm/endpoint"
)

const openaiAPIURL = "https://api.openai.com/v1/chat/completions"

// OpenAI implements Provider for the OpenAI Chat Completions API.
type OpenAI struct {
	apiKey  string
	baseURL string
	// ep is baseURL reduced to its mount root. Callers historically had to pass
	// the full ".../v1/chat/completions" because the value was used verbatim as
	// the POST target — so an ENDPOINT_URL credential stored in the ".../v1"
	// shape our own docs recommend 404'd. Normalizing lets any shape work.
	ep endpoint.Endpoint
	// epErr is why normalization failed, kept so a base that is not a URL at all
	// surfaces as a parse error rather than as a confusing request-construction
	// failure further down. A base rejected on policy (embedded credentials, an
	// odd scheme) is NOT an error here: it may be a deployment that worked, so it
	// falls through to the raw value.
	epErr  error
	client *http.Client
}

// baseURLError returns a parse error when the configured base is not a URL at
// all. Policy rejections return nil — the raw value is attempted instead.
func (o *OpenAI) baseURLError() error {
	if o.epErr != nil && errors.Is(o.epErr, endpoint.ErrParse) {
		return fmt.Errorf("parse base url: %w", o.epErr)
	}
	return nil
}

// newOpenAIEndpoint normalizes a base URL onto the chat wire. A parse failure
// leaves the endpoint zero, and the URL builders fall back to the raw value, so
// normalization can only repair a base — never reject one that used to work.
func newOpenAIEndpoint(baseURL string) (endpoint.Endpoint, error) {
	ep, err := endpoint.Normalize(baseURL)
	return ep.WithWire(endpoint.WireOpenAIChat), err
}

// chatURL is the completion target.
func (o *OpenAI) chatURL() string {
	if o.ep.Root == nil {
		return o.baseURL
	}
	return o.ep.ChatURL()
}

// modelsURL is the discovery target. The mount prefix and any query string
// (Azure addresses by ?api-version=) survive normalization, which is what the
// previous hand-rolled suffix rewrite was protecting.
//
// The fallback has to DERIVE the models path, not hand back the raw base: the
// raw base is the chat target, so returning it verbatim would make ListModels
// query /chat/completions and parse whatever came back as a model list. That
// path is reachable — a base rejected on policy (embedded credentials, an odd
// scheme) deliberately keeps working via the raw value — so it is the same
// suffix rewrite this package replaced, kept for exactly those deployments.
// Ollama's tagsURL fallback does the equivalent.
func (o *OpenAI) modelsURL() string {
	if o.ep.Root == nil {
		return rawModelsURL(o.baseURL)
	}
	return o.ep.ModelsURL()
}

// rawModelsURL is the pre-normalization derivation: rewrite a trailing
// "/chat/completions" to "/models". Going through net/url preserves the query
// (Azure's ?api-version=), which a suffix trim on the whole string mangles. An
// unparseable base has nothing to rewrite, so it is returned as-is and fails at
// request time the way it always did.
func rawModelsURL(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") + "/models"
	return u.String()
}

// NewOpenAI creates a provider that calls the OpenAI Chat Completions API.
func NewOpenAI(apiKey string) *OpenAI {
	ep, err := newOpenAIEndpoint(openaiAPIURL)
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: openaiAPIURL,
		ep:      ep,
		epErr:   err,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// NewOpenAIWithBaseURL creates an OpenAI-compatible provider with a custom base URL.
// Useful for Azure OpenAI, local proxies, or other OpenAI-compatible APIs.
func NewOpenAIWithBaseURL(apiKey, baseURL string) *OpenAI {
	ep, err := newOpenAIEndpoint(baseURL)
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: baseURL,
		ep:      ep,
		epErr:   err,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// NewOpenAIWithClient creates an OpenAI-compatible provider with a custom base
// URL AND a caller-supplied http.Client. This is the SSRF seam for a
// tenant-configured (openai_compat) governance-model endpoint (M2a, #1001): the
// caller passes a client whose transport dials through the #988 httpsafe fence,
// so a workspace can't point the endpoint at a link-local / private address to
// pivot. A nil client falls back to the default 120s client (same as
// NewOpenAIWithBaseURL) — callers wiring an untrusted endpoint MUST pass a
// guarded client, never nil.
func NewOpenAIWithClient(apiKey, baseURL string, client *http.Client) *OpenAI {
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	ep, err := newOpenAIEndpoint(baseURL)
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: baseURL,
		ep:      ep,
		epErr:   err,
		client:  client,
	}
}

// Name returns "openai".
func (o *OpenAI) Name() string { return "openai" }

// ListModels implements ModelLister against the OpenAI-compatible
// GET {root}/v1/models. baseURL may be given in any shape — a bare root,
// ".../v1", or the full ".../v1/chat/completions" — because it is reduced to a
// mount root at construction and the models path is appended from there.
func (o *OpenAI) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if err := o.baseURLError(); err != nil {
		return nil, err
	}
	// The models path, the mount prefix and any query string (Azure addresses
	// by ?api-version=) all come from the normalized endpoint, so this no longer
	// has to hand-rewrite a suffix off the raw base.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, o.modelsURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, "OpenAI"); err != nil {
		return nil, err
	}

	var raw struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode openai models: %w", err)
	}

	out := make([]ModelInfo, 0, len(raw.Data))
	for _, m := range raw.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, ModelInfo{ID: m.ID, DisplayName: m.ID, Provider: "openai"})
	}
	return out, nil
}

// Complete sends a non-streaming completion request to the OpenAI-compatible API.
func (o *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := o.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := o.doWithRetry(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, "OpenAI"); err != nil {
		return nil, err
	}

	var raw openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	return raw.toResponse(), nil
}

// Stream sends a streaming completion request, calling handler for each event.
func (o *OpenAI) Stream(ctx context.Context, req Request, handler func(StreamEvent) error) (*Response, error) {
	body, err := o.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}
	resp, err := o.doWithRetry(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, "OpenAI"); err != nil {
		return nil, err
	}

	return o.parseSSEStream(resp.Body, handler)
}

// doWithRetry executes an HTTP request with exponential backoff retry on transient errors.
// Shares the loop in httpretry.go with the Anthropic provider (max 3 attempts, 1s/2s/4s
// exponential backoff with jitter, Retry-After honoured, retryableStatusCodes) so policy
// stays consistent across LLM backends. Without this the caller saw raw 429/503 from the
// upstream the moment OpenAI rate-limited a burst -- which the orchestrator's own retry
// layer would then duplicate, amplifying spikes.
func (o *OpenAI) doWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	return doWithRetry(ctx, o.client, func(ctx context.Context) (*http.Request, error) {
		return o.newHTTPRequest(ctx, body)
	}, "openai", "OpenAI")
}

func (o *OpenAI) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	// Wrapped as a request-build failure because that is what it is from the
	// retry loop's point of view — it must not retry a base URL that will never
	// parse.
	if err := o.baseURLError(); err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.chatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	return req, nil
}

func (o *OpenAI) buildRequestBody(req Request, stream bool) ([]byte, error) {
	msgs := make([]openaiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, toOpenAIMessage(m))
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		body["tools"] = toFunctionTools(req.Tools)
	}
	if stream {
		body["stream"] = true
	}
	return json.Marshal(body)
}

// --- OpenAI wire types ---

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// functionToolDef is the OpenAI-style {"type":"function","function":{...}}
// tool wire format. Ollama adopted the same schema, so both providers share
// this struct and the toFunctionTools converter.
type functionToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openaiToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			// OpenAI auto-caches prompts ≥1024 tokens since Sept 2025;
			// cached_tokens is the read count (no separate "creation"
			// counter — caching is opaque on their side).
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (r *openaiResponse) toResponse() *Response {
	resp := &Response{
		InputToks:       r.Usage.PromptTokens,
		OutputToks:      r.Usage.CompletionTokens,
		CachedInputToks: r.Usage.PromptTokensDetails.CachedTokens,
	}
	if len(r.Choices) == 0 {
		resp.StopReason = StopEndTurn
		return resp
	}
	choice := r.Choices[0]
	switch choice.FinishReason {
	case "tool_calls":
		resp.StopReason = StopToolUse
	case "length":
		resp.StopReason = StopMaxToks
	default:
		resp.StopReason = StopEndTurn
	}
	resp.Content = choice.Message.Content
	for _, tc := range choice.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}
	return resp
}

func toOpenAIMessage(m Message) openaiMessage {
	if m.Role == RoleTool {
		return openaiMessage{
			Role:       "tool",
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
	}
	if len(m.ToolCalls) > 0 {
		tcs := make([]openaiToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			tcs[i] = openaiToolCall{
				ID:   tc.ID,
				Type: "function",
			}
			tcs[i].Function.Name = tc.Name
			tcs[i].Function.Arguments = tc.Input
		}
		return openaiMessage{
			Role:      "assistant",
			Content:   m.Content,
			ToolCalls: tcs,
		}
	}
	return openaiMessage{Role: m.Role, Content: m.Content}
}

func toFunctionTools(tools []ToolDef) []functionToolDef {
	out := make([]functionToolDef, len(tools))
	for i, t := range tools {
		out[i] = functionToolDef{Type: "function"}
		out[i].Function.Name = t.Name
		out[i].Function.Description = t.Description
		out[i].Function.Parameters = t.InputSchema
	}
	return out
}

func (o *OpenAI) parseSSEStream(r io.Reader, handler func(StreamEvent) error) (*Response, error) {
	final := &Response{StopReason: StopEndTurn}
	var textParts []string

	type partialToolCall struct {
		ID   string
		Name string
		Args strings.Builder
	}
	toolMap := make(map[int]*partialToolCall)

	// sawFinishReason tracks whether any choice ever carried a non-empty
	// finish_reason -- OpenAI's terminal signal, always sent on the last
	// chunk before "[DONE]". A connection can also close cleanly (plain
	// io.EOF, scanner.Err() == nil) mid-generation, which otherwise looked
	// identical to a normal completion; see the check after the scan loop.
	var sawFinishReason bool

	fnErr, scanErr := forEachSSEData(r, 64*1024, 1024*1024, func(data string) (bool, error) {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, nil
		}
		if chunk.Usage != nil {
			final.InputToks = chunk.Usage.PromptTokens
			final.OutputToks = chunk.Usage.CompletionTokens
			final.CachedInputToks = chunk.Usage.PromptTokensDetails.CachedTokens
		}

		if len(chunk.Choices) == 0 {
			return false, nil
		}
		choice := chunk.Choices[0]

		if choice.Delta.Content != "" {
			textParts = append(textParts, choice.Delta.Content)
			if err := handler(StreamEvent{Type: "text", Content: choice.Delta.Content}); err != nil {
				return false, err
			}
		}

		for _, tcDelta := range choice.Delta.ToolCalls {
			ptc, ok := toolMap[tcDelta.Index]
			if !ok {
				ptc = &partialToolCall{}
				toolMap[tcDelta.Index] = ptc
			}
			if tcDelta.ID != "" {
				ptc.ID = tcDelta.ID
			}
			if tcDelta.Function.Name != "" {
				ptc.Name = tcDelta.Function.Name
			}
			ptc.Args.WriteString(tcDelta.Function.Arguments)
		}

		if choice.FinishReason != "" {
			sawFinishReason = true
		}
		switch choice.FinishReason {
		case "tool_calls":
			final.StopReason = StopToolUse
		case "length":
			final.StopReason = StopMaxToks
		case "stop":
			final.StopReason = StopEndTurn
		}
		return false, nil
	})
	if fnErr != nil {
		return final, fnErr
	}

	final.Content = strings.Join(textParts, "")
	for i := 0; i < len(toolMap); i++ {
		ptc := toolMap[i]
		if ptc == nil {
			continue
		}
		tc := ToolCall{ID: ptc.ID, Name: ptc.Name, Input: ptc.Args.String()}
		final.ToolCalls = append(final.ToolCalls, tc)
		if err := handler(StreamEvent{Type: "tool_call", ToolCall: &tc}); err != nil {
			return final, err
		}
	}

	// The scan loop ended without an fnErr. If it also ended without a real
	// scanner error, that normally means a clean io.EOF -- which is exactly
	// what a legitimate finish looks like AND exactly what a mid-generation
	// connection drop looks like. sawFinishReason is what tells them apart: a
	// real completion always carries a finish_reason on its last chunk before
	// OpenAI sends "[DONE]" and closes. Without this, a cut stream silently
	// returned nil error with truncated content as if it were a full
	// response.
	//
	// A genuine scanErr (including context.Canceled from a caller-initiated
	// cancellation) is left untouched here and returned as-is below, so
	// cancellation semantics are unchanged.
	if scanErr == nil && !sawFinishReason {
		scanErr = fmt.Errorf("openai stream ended without a finish_reason (connection closed unexpectedly after %d bytes of content): %w", len(final.Content), io.ErrUnexpectedEOF)
	}

	if err := handler(StreamEvent{Type: "done", Response: final}); err != nil {
		return final, err
	}
	return final, scanErr
}
