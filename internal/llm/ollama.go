package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/llm/endpoint"
)

// Ollama implements Provider for the Ollama /api/chat endpoint.
// Supports tool calling (Ollama 0.5+) and NDJSON streaming.
type Ollama struct {
	baseURL string // e.g. "http://localhost:11434"
	// ep is baseURL reduced to its mount root, so the API path is appended
	// exactly once no matter which shape the operator stored. Zero when baseURL
	// could not be parsed — see chatURL for the fallback.
	ep     endpoint.Endpoint
	model  string
	client *http.Client // bounded by Client.Timeout — used for Complete()
	stream *http.Client // no total deadline — used for Stream()
}

// chatURL is the completion endpoint. Going through endpoint.Endpoint is what
// makes a stored ".../v1" value work: concatenating onto the raw string produced
// ".../v1/api/chat", a 404, and — Keeper being fail-closed — a DENY on every
// credential request, with the credential's own Test button still green.
//
// An unparseable baseURL falls back to the historical concatenation. This
// package repairs shapes; it must not turn a working oddball deployment into a
// construction-time failure.
func (o *Ollama) chatURL() string {
	if o.ep.Root == nil {
		return o.baseURL + "/api/chat"
	}
	return o.ep.ChatURL()
}

// tagsURL is Ollama's model list, used by discovery and by set-time model
// validation. Same normalization, same reason.
func (o *Ollama) tagsURL() string {
	if o.ep.Root == nil {
		return o.baseURL + "/api/tags"
	}
	return o.ep.TagsURL()
}

// NewOllama creates a provider that calls a local or remote Ollama instance.
// The streaming path uses a separate http.Client with no total deadline:
// http.Client.Timeout cancels the entire request including body read, so
// applying it to a long-running NDJSON stream silently truncates a 10-min
// generation at 5 min. The streaming client still bounds dial/header time
// via the transport, leaving body read to caller ctx cancellation.
func NewOllama(baseURL, model string) *Ollama {
	// Clone DefaultTransport so remote deployments behind HTTP_PROXY / HTTPS_PROXY
	// keep working and TLS/dial defaults (TLSHandshakeTimeout, ExpectContinueTimeout)
	// aren't dropped — a zero-value http.Transport silently disables all of those.
	streamTransport := http.DefaultTransport.(*http.Transport).Clone()
	streamTransport.ResponseHeaderTimeout = 60 * time.Second
	streamTransport.IdleConnTimeout = 90 * time.Second
	// A parse failure is deliberately non-fatal: ep stays zero and the URL
	// builders fall back to raw concatenation, preserving pre-normalization
	// behaviour for a value we cannot interpret.
	ep, _ := endpoint.Normalize(baseURL)
	return &Ollama{
		baseURL: strings.TrimRight(baseURL, "/"),
		ep:      ep.WithWire(endpoint.WireOllama),
		model:   model,
		client:  &http.Client{Timeout: 300 * time.Second},
		stream:  &http.Client{Transport: streamTransport},
	}
}

// NewOllamaWithClient builds an Ollama provider whose non-streaming requests
// (Complete + ListModels) use the supplied http.Client. Used for a workspace
// (tenant-configured) endpoint where the dial must be SSRF-guarded — the
// caller passes a client with an SSRF-aware transport. The streaming client
// keeps the standard proxy-aware transport (streaming isn't used for the
// server-side discovery/validation path). A nil client falls back to the
// default 300s-timeout client.
func NewOllamaWithClient(baseURL, model string, client *http.Client) *Ollama {
	o := NewOllama(baseURL, model)
	if client != nil {
		o.client = client
	}
	return o
}

// Name returns "ollama".
func (o *Ollama) Name() string { return "ollama" }

// checkOllamaStatus maps a non-200 Ollama response to an error, reading at
// most 512 bytes of the body for context. Ollama has no auth or rate-limit
// distinction, so the shared checkStatus wording doesn't apply here.
func checkOllamaStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("ollama returned %d: %s", resp.StatusCode, errBody)
}

// ListModels implements ModelLister against Ollama's GET /api/tags. There is
// no curated fallback for Ollama (the model set is whatever the local daemon
// has pulled), so a failure here is terminal for the caller — they get the
// error, not a static list.
func (o *Ollama) ListModels(ctx context.Context) ([]ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, o.tagsURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkOllamaStatus(resp); err != nil {
		return nil, err
	}

	var raw struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ollama tags: %w", err)
	}

	out := make([]ModelInfo, 0, len(raw.Models))
	for _, m := range raw.Models {
		if m.Name == "" {
			continue
		}
		out = append(out, ModelInfo{ID: m.Name, DisplayName: m.Name, Provider: "ollama"})
	}
	return out, nil
}

// Complete sends a non-streaming completion request to the Ollama API.
func (o *Ollama) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := o.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.chatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkOllamaStatus(resp); err != nil {
		return nil, err
	}

	var raw ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}
	return raw.toResponse(), nil
}

// Stream sends a streaming completion request, calling handler for each event.
func (o *Ollama) Stream(ctx context.Context, req Request, handler func(StreamEvent) error) (*Response, error) {
	body, err := o.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.chatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.stream.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkOllamaStatus(resp); err != nil {
		return nil, err
	}

	return o.parseNDJSONStream(resp.Body, handler)
}

func (o *Ollama) buildRequestBody(req Request, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}

	msgs := make([]ollamaMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, ollamaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, toOllamaMessage(m))
	}

	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   stream,
	}

	opts := map[string]any{}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	// Always pin num_predict. Ollama's default is -1 (generate until the
	// model emits a natural EOS) which routinely produces multi-thousand-
	// token replies for callers that just forgot to set MaxTokens. Mirror
	// the Anthropic provider's 4096 default so cost and latency stay
	// bounded by default; explicit opt-in still works because any caller
	// who sets MaxTokens themselves wins.
	maxToks := req.MaxTokens
	if maxToks <= 0 {
		maxToks = 4096
	}
	opts["num_predict"] = maxToks
	body["options"] = opts

	if len(req.Tools) > 0 {
		body["tools"] = toFunctionTools(req.Tools)
	}

	return json.Marshal(body)
}

// --- Ollama wire types ---

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Thinking is a reasoning model's chain of thought, which Ollama returns
	// SEPARATELY from content. Dropping it was actively dangerous: a reasoning
	// model (qwen3, deepseek-r1, gpt-oss…) that spends its whole token budget
	// thinking returns content:"" with thinking populated, so a caller reading
	// only content saw a successful, empty completion. For the Keeper judge —
	// which is fail-closed — that is a DENY on every credential request, with no
	// error logged anywhere to explain it.
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
	// DoneReason distinguishes a finished answer ("stop") from one cut off by
	// the token budget ("length"). Ollama always reported StopEndTurn before, so
	// a truncated verdict was indistinguishable from a complete one.
	DoneReason string `json:"done_reason,omitempty"`
	// Token counts (may be 0 on partial stream chunks)
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (r *ollamaChatResponse) toResponse() *Response {
	resp := &Response{
		Content:    r.Message.Content,
		Thinking:   r.Message.Thinking,
		InputToks:  r.PromptEvalCount,
		OutputToks: r.EvalCount,
	}
	switch {
	case len(r.Message.ToolCalls) > 0:
		resp.StopReason = StopToolUse
		// Complete has no error channel for a single bad tool call, so the
		// per-call "{}" fallback inside toToolCalls is the whole story here.
		resp.ToolCalls, _ = toToolCalls(r.Message.ToolCalls)
	case r.DoneReason == "length":
		// Surfaced so a caller can say "the model ran out of budget" instead of
		// treating an empty or half-written answer as a considered one.
		resp.StopReason = StopMaxToks
	default:
		resp.StopReason = StopEndTurn
	}
	return resp
}

func toOllamaMessage(m Message) ollamaMessage {
	if m.Role == RoleTool {
		return ollamaMessage{Role: "tool", Content: m.Content}
	}
	if len(m.ToolCalls) > 0 {
		tcs := make([]ollamaToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			var args map[string]any
			_ = json.Unmarshal([]byte(tc.Input), &args)
			tcs[i].Function.Name = tc.Name
			tcs[i].Function.Arguments = args
		}
		return ollamaMessage{Role: "assistant", Content: m.Content, ToolCalls: tcs}
	}
	return ollamaMessage{Role: m.Role, Content: m.Content}
}

// toToolCalls converts Ollama tool calls to the provider-neutral form,
// synthesising sequential IDs (Ollama assigns none). A marshal failure
// substitutes "{}" for that call's input and reports the first such error so
// the streaming path can surface it; the non-streaming path keeps the
// fallback and ignores the error.
func toToolCalls(tcs []ollamaToolCall) ([]ToolCall, error) {
	out := make([]ToolCall, len(tcs))
	var firstErr error
	for i, tc := range tcs {
		argsJSON, err := json.Marshal(tc.Function.Arguments)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("marshal tool call args: %w", err)
			}
			argsJSON = []byte("{}")
		}
		out[i] = ToolCall{
			ID:    fmt.Sprintf("tc_%d", i),
			Name:  tc.Function.Name,
			Input: string(argsJSON),
		}
	}
	return out, firstErr
}

// parseNDJSONStream reads Ollama's NDJSON stream and emits StreamEvents.
func (o *Ollama) parseNDJSONStream(r io.Reader, handler func(StreamEvent) error) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	final := &Response{StopReason: StopEndTurn}
	var textParts []string
	// Reasoning deltas are accumulated but deliberately NOT emitted as "text"
	// events: they are not the answer, and streaming them would put a model's
	// chain of thought into chat output. They are kept so the final response can
	// explain an empty completion the same way the non-streaming path does.
	var thinkingParts []string
	// sawDone tracks whether the terminal NDJSON line ("done":true) ever
	// arrived. A connection can close cleanly (plain io.EOF, scanner.Err() ==
	// nil) mid-generation — a network blip or load-shedding event — and
	// without this flag that looked identical to a normal completion, so the
	// caller got nil error with truncated (or, before this fix, even fully
	// dropped — see below) content. See the check after the scan loop.
	var sawDone bool

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var chunk ollamaChatResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}

		if chunk.Message.Thinking != "" {
			thinkingParts = append(thinkingParts, chunk.Message.Thinking)
		}

		if chunk.Message.Content != "" {
			textParts = append(textParts, chunk.Message.Content)
			if err := handler(StreamEvent{Type: "text", Content: chunk.Message.Content}); err != nil {
				return final, err
			}
		}

		if chunk.Done {
			sawDone = true
			final.InputToks = chunk.PromptEvalCount
			final.OutputToks = chunk.EvalCount

			switch {
			case len(chunk.Message.ToolCalls) > 0:
				final.StopReason = StopToolUse
				toolCalls, err := toToolCalls(chunk.Message.ToolCalls)
				if err != nil {
					return final, err
				}
				for _, toolCall := range toolCalls {
					final.ToolCalls = append(final.ToolCalls, toolCall)
					if err := handler(StreamEvent{Type: "tool_call", ToolCall: &toolCall}); err != nil {
						return final, err
					}
				}
			case chunk.DoneReason == "length":
				// Same mapping as toResponse: a streaming caller must be able to
				// tell a budget cut from a finished turn too.
				final.StopReason = StopMaxToks
			}
			break
		}
	}

	// Assembled unconditionally (not just inside the chunk.Done branch above)
	// so a caller that gets an error below still sees whatever partial
	// content did arrive, instead of the pre-fix behavior where a truncated
	// stream silently dropped Content/Thinking to "" on top of reporting
	// success.
	final.Content = strings.Join(textParts, "")
	final.Thinking = strings.Join(thinkingParts, "")

	scanErr := scanner.Err()
	// The scan loop ended. If it also ended without a real scanner error,
	// that normally means a clean io.EOF -- which is exactly what a
	// legitimate finish looks like AND exactly what a mid-generation
	// connection drop looks like. sawDone is what tells them apart: a real
	// completion always emits a final line with "done":true. Without this, a
	// cut stream silently returned nil error with truncated content as if it
	// were a full response.
	//
	// A genuine scanErr (including context.Canceled from a caller-initiated
	// cancellation) is left untouched here and returned as-is below, so
	// cancellation semantics are unchanged.
	if scanErr == nil && !sawDone {
		scanErr = fmt.Errorf("ollama stream ended without a done:true event (connection closed unexpectedly after %d bytes of content): %w", len(final.Content), io.ErrUnexpectedEOF)
	}

	if err := handler(StreamEvent{Type: "done", Response: final}); err != nil {
		return final, err
	}
	return final, scanErr
}
