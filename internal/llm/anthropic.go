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

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"

// Anthropic implements Provider for the Anthropic Messages API.
type Anthropic struct {
	apiKey string
	client *http.Client
}

// NewAnthropic creates a provider that calls the Anthropic Messages API.
func NewAnthropic(apiKey string) *Anthropic {
	return &Anthropic{
		apiKey: apiKey,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := a.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkAnthropicStatus(resp); err != nil {
		return nil, err
	}

	var raw anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}
	return raw.toResponse(), nil
}

func (a *Anthropic) Stream(ctx context.Context, req Request, handler func(StreamEvent) error) (*Response, error) {
	body, err := a.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic http: %w", err)
	}
	defer resp.Body.Close()

	if err := checkAnthropicStatus(resp); err != nil {
		return nil, err
	}

	return a.parseSSEStream(resp.Body, handler)
}

func (a *Anthropic) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", a.apiKey)
	return req, nil
}

func (a *Anthropic) buildRequestBody(req Request, stream bool) ([]byte, error) {
	msgs := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, toAnthropicMessage(m))
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
	}
	if req.System != "" {
		body["system"] = req.System
	}
	maxToks := req.MaxTokens
	if maxToks == 0 {
		maxToks = 4096
	}
	body["max_tokens"] = maxToks
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		body["tools"] = toAnthropicTools(req.Tools)
	}
	if stream {
		body["stream"] = true
	}
	return json.Marshal(body)
}

// --- Anthropic wire types ---

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicContentBlock
}

type anthropicContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                 `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (r *anthropicResponse) toResponse() *Response {
	resp := &Response{
		InputToks:  r.Usage.InputTokens,
		OutputToks: r.Usage.OutputTokens,
	}
	switch r.StopReason {
	case "tool_use":
		resp.StopReason = StopToolUse
	case "max_tokens":
		resp.StopReason = StopMaxToks
	default:
		resp.StopReason = StopEndTurn
	}
	var textParts []string
	for _, block := range r.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: string(inputJSON),
			})
		}
	}
	resp.Content = strings.Join(textParts, "")
	return resp
}

func toAnthropicMessage(m Message) anthropicMessage {
	if m.Role == RoleTool {
		return anthropicMessage{
			Role: "user",
			Content: []anthropicContentBlock{{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}},
		}
	}
	if len(m.ToolCalls) > 0 {
		blocks := make([]anthropicContentBlock, 0, len(m.ToolCalls)+1)
		if m.Content != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			var input any
			_ = json.Unmarshal([]byte(tc.Input), &input)
			blocks = append(blocks, anthropicContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
		return anthropicMessage{Role: "assistant", Content: blocks}
	}
	return anthropicMessage{Role: m.Role, Content: m.Content}
}

func toAnthropicTools(tools []ToolDef) []anthropicTool {
	out := make([]anthropicTool, len(tools))
	for i, t := range tools {
		out[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	return out
}

func checkAnthropicStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("invalid Anthropic API key")
	case http.StatusTooManyRequests:
		return fmt.Errorf("Anthropic rate limit exceeded")
	default:
		return fmt.Errorf("Anthropic API returned %d: %s", resp.StatusCode, body)
	}
}

// parseSSEStream reads Anthropic's SSE stream and emits StreamEvents.
func (a *Anthropic) parseSSEStream(r io.Reader, handler func(StreamEvent) error) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	final := &Response{StopReason: StopEndTurn}
	var textParts []string
	var toolCalls []ToolCall
	var currentToolID, currentToolName string
	var currentToolInput strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock *struct {
				Type  string `json:"type"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				Text  string `json:"text"`
				Input any    `json:"input"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message *anthropicResponse `json:"message"`
			Usage   *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				final.InputToks = event.Message.Usage.InputTokens
			}

		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				currentToolID = event.ContentBlock.ID
				currentToolName = event.ContentBlock.Name
				currentToolInput.Reset()
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				textParts = append(textParts, event.Delta.Text)
				if err := handler(StreamEvent{Type: "text", Content: event.Delta.Text}); err != nil {
					return final, err
				}
			case "input_json_delta":
				currentToolInput.WriteString(event.Delta.PartialJSON)
			}

		case "content_block_stop":
			if currentToolID != "" {
				tc := ToolCall{
					ID:    currentToolID,
					Name:  currentToolName,
					Input: currentToolInput.String(),
				}
				toolCalls = append(toolCalls, tc)
				if err := handler(StreamEvent{Type: "tool_call", ToolCall: &tc}); err != nil {
					return final, err
				}
				currentToolID = ""
				currentToolName = ""
			}

		case "message_delta":
			if event.Delta != nil {
				switch event.Delta.StopReason {
				case "tool_use":
					final.StopReason = StopToolUse
				case "max_tokens":
					final.StopReason = StopMaxToks
				default:
					final.StopReason = StopEndTurn
				}
			}
			if event.Usage != nil {
				final.OutputToks = event.Usage.OutputTokens
			}

		case "message_stop":
			// stream complete
		}
	}

	final.Content = strings.Join(textParts, "")
	final.ToolCalls = toolCalls

	if err := handler(StreamEvent{Type: "done", Response: final}); err != nil {
		return final, err
	}
	return final, scanner.Err()
}
