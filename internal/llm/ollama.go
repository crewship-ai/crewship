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

// Ollama implements Provider for the Ollama /api/chat endpoint.
// Supports tool calling (Ollama 0.5+) and NDJSON streaming.
type Ollama struct {
	baseURL string // e.g. "http://localhost:11434"
	model   string
	client  *http.Client
}

// NewOllama creates a provider that calls a local or remote Ollama instance.
func NewOllama(baseURL, model string) *Ollama {
	return &Ollama{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 300 * time.Second},
	}
}

func (o *Ollama) Name() string { return "ollama" }

func (o *Ollama) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := o.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, errBody)
	}

	var raw ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}
	return raw.toResponse(), nil
}

func (o *Ollama) Stream(ctx context.Context, req Request, handler func(StreamEvent) error) (*Response, error) {
	body, err := o.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, errBody)
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
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	if len(opts) > 0 {
		body["options"] = opts
	}

	if len(req.Tools) > 0 {
		body["tools"] = toOllamaTools(req.Tools)
	}

	return json.Marshal(body)
}

// --- Ollama wire types ---

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type ollamaToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
	// Token counts (may be 0 on partial stream chunks)
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (r *ollamaChatResponse) toResponse() *Response {
	resp := &Response{
		Content:    r.Message.Content,
		InputToks:  r.PromptEvalCount,
		OutputToks: r.EvalCount,
	}
	if len(r.Message.ToolCalls) > 0 {
		resp.StopReason = StopToolUse
		for i, tc := range r.Message.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Function.Arguments)
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:    fmt.Sprintf("tc_%d", i),
				Name:  tc.Function.Name,
				Input: string(argsJSON),
			})
		}
	} else {
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

func toOllamaTools(tools []ToolDef) []ollamaToolDef {
	out := make([]ollamaToolDef, len(tools))
	for i, t := range tools {
		out[i] = ollamaToolDef{Type: "function"}
		out[i].Function.Name = t.Name
		out[i].Function.Description = t.Description
		out[i].Function.Parameters = t.InputSchema
	}
	return out
}

// parseNDJSONStream reads Ollama's NDJSON stream and emits StreamEvents.
func (o *Ollama) parseNDJSONStream(r io.Reader, handler func(StreamEvent) error) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	final := &Response{StopReason: StopEndTurn}
	var textParts []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var chunk ollamaChatResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}

		if chunk.Message.Content != "" {
			textParts = append(textParts, chunk.Message.Content)
			if err := handler(StreamEvent{Type: "text", Content: chunk.Message.Content}); err != nil {
				return final, err
			}
		}

		if chunk.Done {
			final.InputToks = chunk.PromptEvalCount
			final.OutputToks = chunk.EvalCount
			final.Content = strings.Join(textParts, "")

			if len(chunk.Message.ToolCalls) > 0 {
				final.StopReason = StopToolUse
				for i, tc := range chunk.Message.ToolCalls {
					argsJSON, _ := json.Marshal(tc.Function.Arguments)
					toolCall := ToolCall{
						ID:    fmt.Sprintf("tc_%d", i),
						Name:  tc.Function.Name,
						Input: string(argsJSON),
					}
					final.ToolCalls = append(final.ToolCalls, toolCall)
					if err := handler(StreamEvent{Type: "tool_call", ToolCall: &toolCall}); err != nil {
						return final, err
					}
				}
			}
			break
		}
	}

	if err := handler(StreamEvent{Type: "done", Response: final}); err != nil {
		return final, err
	}
	return final, scanner.Err()
}
