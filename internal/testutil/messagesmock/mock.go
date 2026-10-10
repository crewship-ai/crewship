// Package messagesmock provides a deterministic, offline Anthropic transport for
// owned integration tests. It cannot delegate a request to a real provider.
package messagesmock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Config contains only synthetic credentials and independently scripted roles.
type Config struct {
	ExpectedKey string
	Model       string
	Script      []Step
}

// Step selects one role by a stable marker in user message text. Steps for a
// role execute in script order; independent roles may interleave concurrently.
// A tool response requires the exact advertised ToolName. The following request
// must carry that tool use and a successful tool result; RequireResult further
// requires a nonce in that result, rather than in the prompt or assistant text.
type Step struct {
	Role          string
	MatchMessage  string
	Path          string
	ToolName      string
	ToolUseID     string
	ToolArgs      map[string]any
	RequireResult string
	Text          string
	InputTokens   int // Required for an explicitly scripted count_tokens call.
}

// Event is deliberately limited: no credentials, message bodies or tool output.
type Event struct {
	Role      string
	Step      int
	Path      string
	ToolName  string
	ToolUseID string
	Stream    bool
	Accepted  bool
	Failure   string
}

type roleState struct {
	marker  string
	steps   []Step
	next    int
	pending *Step
}

// Transport implements http.RoundTripper with no underlying network transport.
// Any rejected request is sticky and prevents Complete from claiming success.
type Transport struct {
	mu       sync.Mutex
	key      string
	model    string
	roles    map[string]*roleState
	evidence []Event
	failed   bool
}

var _ http.RoundTripper = (*Transport)(nil)

func New(config Config) (*Transport, error) {
	if config.ExpectedKey == "" || config.Model == "" || len(config.Script) == 0 || len(config.Script) > 1024 {
		return nil, errors.New("messagesmock: key, model and nonempty script required")
	}
	// Copy nested tool arguments too: caller mutations cannot change the script.
	encoded, err := json.Marshal(config.Script)
	if err != nil {
		return nil, errors.New("messagesmock: script must be JSON-compatible")
	}
	var steps []Step
	if json.Unmarshal(encoded, &steps) != nil {
		return nil, errors.New("messagesmock: invalid script")
	}
	t := &Transport{key: config.ExpectedKey, model: config.Model, roles: map[string]*roleState{}}
	ids := map[string]bool{}
	for _, step := range steps {
		if step.Path == "" {
			step.Path = "/v1/messages"
		}
		if step.Role == "" || step.MatchMessage == "" || strings.Contains(step.MatchMessage, config.ExpectedKey) {
			return nil, errors.New("messagesmock: role and non-secret marker required")
		}
		for _, identifier := range []string{step.Role, step.ToolName, step.ToolUseID} {
			if strings.Contains(identifier, config.ExpectedKey) || len(identifier) > 256 || strings.IndexFunc(identifier, unicode.IsControl) >= 0 {
				return nil, errors.New("messagesmock: evidence identifiers must be bounded and non-secret")
			}
		}
		if strings.Contains(step.Text, config.ExpectedKey) || containsKey(step.ToolArgs, config.ExpectedKey) {
			return nil, errors.New("messagesmock: provider key cannot be returned to the agent")
		}
		if step.Path != "/v1/messages" && step.Path != "/v1/messages/count_tokens" {
			return nil, errors.New("messagesmock: unsupported scripted path")
		}
		if step.Path == "/v1/messages/count_tokens" {
			if step.InputTokens <= 0 || step.ToolName != "" || step.Text != "" {
				return nil, errors.New("messagesmock: count_tokens requires token count and no message response")
			}
		} else if step.ToolName != "" {
			if step.ToolUseID == "" || ids[step.ToolUseID] || step.ToolArgs == nil || step.Text != "" {
				return nil, errors.New("messagesmock: tool step requires unique ID, arguments and no text response")
			}
			ids[step.ToolUseID] = true
		} else if step.Text == "" || step.ToolUseID != "" || step.ToolArgs != nil {
			return nil, errors.New("messagesmock: final message requires text and no tool fields")
		}
		r := t.roles[step.Role]
		if r == nil {
			r = &roleState{marker: step.MatchMessage}
			t.roles[step.Role] = r
		} else if r.marker != step.MatchMessage {
			return nil, errors.New("messagesmock: each role requires a stable message marker")
		}
		if step.RequireResult != "" {
			previousTool := false
			for i := len(r.steps) - 1; i >= 0; i-- {
				if r.steps[i].Path == "/v1/messages" {
					previousTool = r.steps[i].ToolName != ""
					break
				}
			}
			if !previousTool {
				return nil, errors.New("messagesmock: result requirement must follow a tool step")
			}
		}
		r.steps = append(r.steps, step)
	}
	for name, role := range t.roles {
		for i := len(role.steps) - 1; i >= 0; i-- {
			if role.steps[i].Path == "/v1/messages" {
				if role.steps[i].ToolName != "" {
					return nil, errors.New("messagesmock: every role must consume its final tool result")
				}
				break
			}
		}
		for other, candidate := range t.roles {
			if name != other && strings.Contains(role.marker, candidate.marker) {
				return nil, errors.New("messagesmock: role markers must not overlap")
			}
		}
	}
	return t, nil
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
	Messages  []message `json:"messages"`
	Tools     []struct {
		Name        string         `json:"name"`
		InputSchema map[string]any `json:"input_schema"`
	} `json:"tools"`
}

func contentBlocks(content json.RawMessage) ([]block, error) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return []block{{Type: "text", Text: text}}, nil
	}
	var blocks []block
	if err := json.Unmarshal(content, &blocks); err != nil || len(blocks) == 0 {
		return nil, errors.New("invalid message content")
	}
	return blocks, nil
}

func resultText(content json.RawMessage) (string, error) {
	blocks, err := contentBlocks(content)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, b := range blocks {
		if b.Type != "text" {
			return "", errors.New("non-text tool result")
		}
		out.WriteString(b.Text)
	}
	return out.String(), nil
}

func (t *Transport) reject(event Event, reason string) (*http.Response, error) {
	t.failed = true
	event.Failure = reason
	// A client retry storm must not make diagnostic evidence unbounded.
	if len(t.evidence) < 1024 {
		t.evidence = append(t.evidence, event)
	}
	return nil, fmt.Errorf("messagesmock: %s", reason)
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	event := Event{}
	if req != nil && req.Body != nil {
		defer req.Body.Close()
	}
	if t.failed {
		return t.reject(event, "fixture already rejected a request")
	}
	if req == nil || req.URL == nil || req.Method != http.MethodPost || req.URL.Scheme != "https" ||
		req.URL.Host != "api.anthropic.com" || req.URL.User != nil || req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" ||
		(req.URL.Path != "/v1/messages" && req.URL.Path != "/v1/messages/count_tokens") || req.URL.RawPath != "" ||
		(req.Host != "" && req.Host != "api.anthropic.com") {
		return t.reject(event, "unsupported provider request")
	}
	event.Path = req.URL.Path
	if req.Context().Err() != nil {
		return t.reject(event, "request cancelled")
	}
	mediaType, _, mediaError := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if len(req.Header.Values("x-api-key")) != 1 || req.Header.Get("x-api-key") != t.key ||
		len(req.Header.Values("anthropic-version")) != 1 || req.Header.Get("anthropic-version") != "2023-06-01" ||
		len(req.Header.Values("Content-Type")) != 1 || mediaError != nil || mediaType != "application/json" || req.Header.Get("Authorization") != "" {
		return t.reject(event, "invalid provider authentication or protocol headers")
	}
	if req.Body == nil {
		return t.reject(event, "missing request body")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		return t.reject(event, "invalid or oversized request body")
	}
	var input request
	if json.Unmarshal(body, &input) != nil || input.Model != t.model || len(input.Messages) == 0 ||
		(req.URL.Path == "/v1/messages" && input.MaxTokens <= 0) {
		return t.reject(event, "invalid model or message envelope")
	}
	var userText strings.Builder
	for _, m := range input.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return t.reject(event, "invalid message role")
		}
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			return t.reject(event, "invalid message content")
		}
		if m.Role == "user" {
			for _, b := range blocks {
				if b.Type == "text" {
					userText.WriteString(b.Text)
				}
			}
		}
	}
	var role *roleState
	for name, candidate := range t.roles {
		if strings.Contains(userText.String(), candidate.marker) {
			if role != nil {
				return t.reject(event, "ambiguous role markers")
			}
			event.Role, role = name, candidate
		}
	}
	if role == nil || role.next >= len(role.steps) {
		return t.reject(event, "unknown role or exhausted script")
	}
	event.Step = role.next
	step := role.steps[role.next]
	if step.Path != req.URL.Path || (step.Path == "/v1/messages/count_tokens" && input.Stream) {
		return t.reject(event, "unexpected scripted request path")
	}
	if err := checkResult(input.Messages, role.pending, step.RequireResult); err != nil {
		return t.reject(event, err.Error())
	}
	if step.ToolName != "" {
		matches := 0
		validSchema := false
		for _, tool := range input.Tools {
			if tool.Name == step.ToolName {
				matches++
				validSchema = tool.InputSchema["type"] == "object"
			}
		}
		if matches != 1 || !validSchema {
			return t.reject(event, "scripted tool is not uniquely advertised with object schema")
		}
	}
	role.next++
	// Token counting does not acknowledge a tool result or change conversation
	// state: the next Messages request must still supply the actual result.
	if step.Path == "/v1/messages" {
		role.pending = nil
		if step.ToolName != "" {
			copy := step
			role.pending = &copy
		}
	}
	event.ToolName, event.ToolUseID, event.Stream, event.Accepted = step.ToolName, step.ToolUseID, input.Stream, true
	if len(t.evidence) >= 1024 {
		return t.reject(event, "fixture evidence limit exceeded")
	}
	t.evidence = append(t.evidence, event)
	if step.Path == "/v1/messages/count_tokens" {
		return response(req, "application/json", map[string]any{"input_tokens": step.InputTokens}), nil
	}
	return messageResponse(req, t.model, step, event.Step, input.Stream), nil
}

func containsKey(value any, key string) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, key)
	case map[string]any:
		for name, item := range v {
			if strings.Contains(name, key) || containsKey(item, key) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsKey(item, key) {
				return true
			}
		}
	}
	return false
}

func checkResult(messages []message, pending *Step, nonce string) error {
	last := messages[len(messages)-1]
	if last.Role != "user" {
		return errors.New("request must end with user content")
	}
	blocks, _ := contentBlocks(last.Content)
	results := []block{}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			results = append(results, b)
		}
	}
	if pending == nil {
		if len(results) != 0 {
			return errors.New("unsolicited tool result")
		}
		return nil
	}
	if len(messages) < 2 || messages[len(messages)-2].Role != "assistant" || len(results) != 1 ||
		results[0].ToolUseID != pending.ToolUseID || results[0].IsError {
		return errors.New("missing correlated successful tool result")
	}
	previous, _ := contentBlocks(messages[len(messages)-2].Content)
	uses := 0
	for _, b := range previous {
		if b.Type == "tool_use" && b.ID == pending.ToolUseID && b.Name == pending.ToolName && reflect.DeepEqual(b.Input, pending.ToolArgs) {
			uses++
		}
	}
	text, err := resultText(results[0].Content)
	if uses != 1 || err != nil || strings.TrimSpace(text) == "" || (nonce != "" && !strings.Contains(text, nonce)) {
		return errors.New("tool use or actual result payload does not match script")
	}
	return nil
}

func response(req *http.Request, contentType string, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {contentType}},
		Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: req}
}

func messageResponse(req *http.Request, model string, step Step, index int, stream bool) *http.Response {
	id := fmt.Sprintf("msg_mock_%s_%d", step.Role, index)
	stop := "end_turn"
	content := map[string]any{"type": "text", "text": step.Text}
	if step.ToolName != "" {
		stop = "tool_use"
		content = map[string]any{"type": "tool_use", "id": step.ToolUseID, "name": step.ToolName, "input": step.ToolArgs}
	}
	msg := map[string]any{"id": id, "type": "message", "role": "assistant", "model": model,
		"content": []any{content}, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 16, "output_tokens": 8, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}}
	if !stream {
		return response(req, "application/json", msg)
	}
	var wire bytes.Buffer
	emit := func(kind string, value map[string]any) {
		value["type"] = kind
		data, _ := json.Marshal(value)
		fmt.Fprintf(&wire, "event: %s\ndata: %s\n\n", kind, data)
	}
	msg["content"], msg["stop_reason"] = []any{}, nil
	msg["usage"] = map[string]any{"input_tokens": 16, "output_tokens": 0, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
	emit("message_start", map[string]any{"message": msg})
	var delta map[string]any
	if step.ToolName != "" {
		content["input"] = map[string]any{}
		encoded, _ := json.Marshal(step.ToolArgs)
		delta = map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}
	} else {
		content["text"] = ""
		delta = map[string]any{"type": "text_delta", "text": step.Text}
	}
	emit("content_block_start", map[string]any{"index": 0, "content_block": content})
	emit("content_block_delta", map[string]any{"index": 0, "delta": delta})
	emit("content_block_stop", map[string]any{"index": 0})
	emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 8}})
	emit("message_stop", map[string]any{})
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(bytes.NewReader(wire.Bytes())), ContentLength: int64(wire.Len()), Request: req}
}

func (t *Transport) Complete() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed {
		return errors.New("messagesmock: rejected requests prevent successful completion")
	}
	var incomplete []string
	for name, role := range t.roles {
		if role.next != len(role.steps) || role.pending != nil {
			incomplete = append(incomplete, name)
		}
	}
	sort.Strings(incomplete)
	if len(incomplete) != 0 {
		return fmt.Errorf("messagesmock: unconsumed role scripts: %s", strings.Join(incomplete, ", "))
	}
	return nil
}

func (t *Transport) Evidence() []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Event(nil), t.evidence...)
}
