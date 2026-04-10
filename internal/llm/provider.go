package llm

import "context"

// Role constants for chat messages.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is a single chat message in a conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"` // JSON string
}

// ToolDef defines a tool the model can call.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"` // JSON Schema object
}

// Request holds a completion request.
type Request struct {
	Model       string    `json:"model"`
	System      string    `json:"system,omitempty"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
}

// StopReason indicates why the model stopped generating.
type StopReason string

const (
	StopEndTurn StopReason = "end_turn"
	StopToolUse StopReason = "tool_use"
	StopMaxToks StopReason = "max_tokens"
)

// Response holds a completion response.
type Response struct {
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	StopReason StopReason `json:"stop_reason"`
	InputToks  int        `json:"input_tokens,omitempty"`
	OutputToks int        `json:"output_tokens,omitempty"`
}

// StreamEvent is emitted during streaming.
type StreamEvent struct {
	Type     string    `json:"type"` // "text", "tool_call", "done", "error"
	Content  string    `json:"content,omitempty"`
	ToolCall *ToolCall `json:"tool_call,omitempty"`
	Response *Response `json:"response,omitempty"` // set when Type == "done"
}

// Provider is the model-agnostic LLM interface.
// Implementations exist for Anthropic, OpenAI, and Ollama.
type Provider interface {
	// Complete sends a request and returns the full response.
	Complete(ctx context.Context, req Request) (*Response, error)

	// Stream sends a request and streams events to the handler.
	// The handler is called for each event; return error to abort.
	Stream(ctx context.Context, req Request, handler func(StreamEvent) error) (*Response, error)

	// Name returns the provider identifier (e.g. "anthropic", "openai", "ollama").
	Name() string
}
