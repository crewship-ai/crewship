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
	// Think opts a reasoning model's chain of thought in or out where the
	// provider exposes that as a switch. nil means "don't mention it", which is
	// the only safe default: the flag is not universal, and a model with no
	// thinking capability should never be sent a key it may reject.
	//
	// It exists for callers whose answer must fit a budget too small to reason
	// inside. The Keeper judge is the motivating one — 256 tokens, fail-closed —
	// where a model that reasons returns an empty verdict, which reads as DENY
	// (see internal/llm/ollama_thinking_test.go for the measured numbers).
	//
	// Only the Ollama provider acts on this today; Anthropic and OpenAI ignore
	// it, so setting it is never a portability hazard, just a no-op there.
	Think *bool `json:"think,omitempty"`
	// Format constrains the model's decoding to a shape — a JSON Schema object,
	// or the string "json" for schema-less JSON mode. nil means "don't mention
	// it", for the same reason Think is a pointer: the field is not universal,
	// and a model server that does not implement it must keep seeing the request
	// it saw before rather than a key it may reject.
	//
	// It exists for callers that PARSE the answer rather than show it. The Keeper
	// judge is the motivating one: it asks for a verdict object in prose and then
	// brace-scans the reply, so a model that opens with "Sure, here's my
	// assessment:" produces nothing parseable — which a fail-closed path turns
	// into DENY on every credential request. A schema removes that failure mode
	// at the decoder instead of asking the model more firmly.
	//
	// It is a hint, never a guarantee: only the Ollama provider acts on it today
	// (Anthropic and OpenAI ignore it, so setting it is a no-op there, not a
	// portability hazard), and a hosted judge therefore still returns whatever it
	// likes. Callers must keep their tolerant parser.
	Format any `json:"format,omitempty"`
}

// StopReason indicates why the model stopped generating.
type StopReason string

const (
	StopEndTurn StopReason = "end_turn"
	StopToolUse StopReason = "tool_use"
	StopMaxToks StopReason = "max_tokens"
)

// Response holds a completion response.
//
// Cache token fields carry provider-reported prompt-caching counts so the
// downstream telemetry + paymaster layers can report cache hit ratios and
// price cache reads correctly (Anthropic charges ~10% of base input cost
// for a cache read; OpenAI activates caching automatically on prompts
// ≥1024 tokens and reports cached_tokens). Providers that don't surface
// cache info leave both fields zero — dashboards can still compute
// "cached / input" without branching per provider.
//
// InputToks is FRESH input on every codec: exclusive of CachedInputToks and
// CacheCreationToks, never a total. Anthropic reports it that way on the wire
// and its codec passes the value through; OpenAI reports prompt_tokens
// INCLUSIVE of prompt_tokens_details.cached_tokens, so openai.go subtracts
// (clamped at zero) to meet this contract. A codec that leaves the inclusive
// wire value in place bills cache reads at the full input rate — the paymaster
// prices these three fields as separate channels and adds them.
type Response struct {
	Content string `json:"content,omitempty"`
	// Thinking is a reasoning model's chain of thought when the provider returns
	// it separately from Content (Ollama does). It is never the answer — but it
	// is the difference between "the model said nothing" and "the model reasoned
	// and never got to an answer", which is the diagnosis a caller needs when a
	// completion comes back empty.
	Thinking          string     `json:"thinking,omitempty"`
	ToolCalls         []ToolCall `json:"tool_calls,omitempty"`
	StopReason        StopReason `json:"stop_reason"`
	InputToks         int        `json:"input_tokens,omitempty"`
	OutputToks        int        `json:"output_tokens,omitempty"`
	CachedInputToks   int        `json:"cached_input_tokens,omitempty"`
	CacheCreationToks int        `json:"cache_creation_tokens,omitempty"`
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

// ModelInfo describes one model a provider can serve. ID is the wire
// identifier passed to Complete/Stream as Request.Model; DisplayName is a
// human-friendly label (may equal ID when the upstream gives none).
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Provider    string `json:"provider"`
}

// ModelLister is an OPTIONAL capability a Provider may implement to enumerate
// the models it can serve. It is intentionally separate from Provider so the
// core completion interface stays minimal — callers type-assert for it
// (`p, ok := prov.(ModelLister)`) and fall back to CuratedModels when the
// provider can't (or won't) list live.
type ModelLister interface {
	// ListModels returns the models the provider can serve right now. A
	// non-nil error means the live lookup failed; callers should fall back
	// to the curated set rather than surfacing the raw error.
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
