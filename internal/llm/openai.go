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
)

const openaiAPIURL = "https://api.openai.com/v1/chat/completions"

// OpenAI implements Provider for the OpenAI Chat Completions API.
type OpenAI struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewOpenAI creates a provider that calls the OpenAI Chat Completions API.
func NewOpenAI(apiKey string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: openaiAPIURL,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// NewOpenAIWithBaseURL creates an OpenAI-compatible provider with a custom base URL.
// Useful for Azure OpenAI, local proxies, or other OpenAI-compatible APIs.
func NewOpenAIWithBaseURL(apiKey, baseURL string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// Name returns "openai".
func (o *OpenAI) Name() string { return "openai" }

// Complete sends a non-streaming completion request to the OpenAI-compatible API.
func (o *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := o.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := o.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkOpenAIStatus(resp); err != nil {
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
	httpReq, err := o.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkOpenAIStatus(resp); err != nil {
		return nil, err
	}

	return o.parseSSEStream(resp.Body, handler)
}

func (o *OpenAI) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL, bytes.NewReader(body))
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
		body["tools"] = toOpenAITools(req.Tools)
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

type openaiToolDef struct {
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

func toOpenAITools(tools []ToolDef) []openaiToolDef {
	out := make([]openaiToolDef, len(tools))
	for i, t := range tools {
		out[i] = openaiToolDef{Type: "function"}
		out[i].Function.Name = t.Name
		out[i].Function.Description = t.Description
		out[i].Function.Parameters = t.InputSchema
	}
	return out
}

func checkOpenAIStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("invalid OpenAI API key")
	case http.StatusTooManyRequests:
		return fmt.Errorf("OpenAI rate limit exceeded")
	default:
		return fmt.Errorf("OpenAI API returned %d: %s", resp.StatusCode, body)
	}
}

func (o *OpenAI) parseSSEStream(r io.Reader, handler func(StreamEvent) error) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	final := &Response{StopReason: StopEndTurn}
	var textParts []string

	type partialToolCall struct {
		ID   string
		Name string
		Args strings.Builder
	}
	toolMap := make(map[int]*partialToolCall)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}

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
			continue
		}
		if chunk.Usage != nil {
			final.InputToks = chunk.Usage.PromptTokens
			final.OutputToks = chunk.Usage.CompletionTokens
			final.CachedInputToks = chunk.Usage.PromptTokensDetails.CachedTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]

		if choice.Delta.Content != "" {
			textParts = append(textParts, choice.Delta.Content)
			if err := handler(StreamEvent{Type: "text", Content: choice.Delta.Content}); err != nil {
				return final, err
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

		switch choice.FinishReason {
		case "tool_calls":
			final.StopReason = StopToolUse
		case "length":
			final.StopReason = StopMaxToks
		case "stop":
			final.StopReason = StopEndTurn
		}
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

	if err := handler(StreamEvent{Type: "done", Response: final}); err != nil {
		return final, err
	}
	return final, scanner.Err()
}
