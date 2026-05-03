package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// streamJSONMessage represents a line from Claude Code --output-format stream-json.
// The format varies: top-level messages have "type" like "assistant", "result", "system";
// stream events have type "stream_event" with nested "event" containing deltas.
type streamJSONMessage struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
	// For "assistant" type messages with content blocks at top level (legacy)
	Content []contentBlock `json:"content,omitempty"`
	// For "result" type
	Result       string          `json:"result,omitempty"`
	DurationMs   float64         `json:"duration_ms,omitempty"`
	DurationAPI  float64         `json:"duration_api_ms,omitempty"`
	TotalCostUSD float64         `json:"total_cost_usd,omitempty"`
	NumTurns     int             `json:"num_turns,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
	ModelUsage   json.RawMessage `json:"modelUsage,omitempty"`
	Errors       []string        `json:"errors,omitempty"`
	// For "system" type with subtype "init"
	Model    string          `json:"model,omitempty"`
	Tools    []string        `json:"tools,omitempty"`
	CWD      string          `json:"cwd,omitempty"`
	MCPSrvrs json.RawMessage `json:"mcp_servers,omitempty"`
	// For stream_event type (--include-partial-messages)
	Event *streamEvent `json:"event,omitempty"`
}

// nestedMessage extracts content blocks from the "message" field if present.
// Claude Code stream-json wraps assistant content in {"type":"assistant","message":{"content":[...]}}.
type nestedMessage struct {
	Content []contentBlock `json:"content,omitempty"`
}

type contentBlock struct {
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Name      string       `json:"name,omitempty"`
	ID        string       `json:"id,omitempty"`
	Input     any          `json:"input,omitempty"`
	ToolUseID string       `json:"tool_use_id,omitempty"`
	Source    *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type streamEvent struct {
	Type  string      `json:"type"`
	Delta *eventDelta `json:"delta,omitempty"`
}

type eventDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

func (o *Orchestrator) streamOutput(ctx context.Context, result *provider.ExecResult, req AgentRunRequest, handler EventHandler) {
	var closeOnce sync.Once
	closeReader := func() {
		closeOnce.Do(func() {
			result.Reader.Close()
		})
	}
	defer closeReader()

	go func() {
		<-ctx.Done()
		closeReader()
	}()

	scanner := bufio.NewScanner(result.Reader)
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)

	adapter := getAdapter(req.CLIAdapter)
	useStreamJSON := adapter.UseStreamJSON()

	// Crow's Nest: capture the first 16 KB of raw stdout+stderr so the live
	// terminal panel can show a replayable snapshot. We deliberately do NOT
	// emit per-line — at 50 lines/sec that would flood the journal. The live
	// WebSocket stream (handler → wsHub) already carries real-time output to
	// the UI; this journal entry is the persistence + replay layer, so a
	// single end-of-stream summary is the right grain. totalBytes records
	// the full byte count (un-truncated) so consumers know how much was
	// dropped from the snapshot.
	const captureCap = 16 * 1024 // 16 KB cap for in-memory buffer
	captureBuf := make([]byte, 0, captureCap)
	var totalBytes int64

	for scanner.Scan() {
		// scanner.Bytes() aliases the scanner's internal buffer, so it's
		// valid only until the next Scan(). handleStreamJSONLine consumes
		// the slice synchronously (json.Unmarshal copies strings out), and
		// the non-JSON fallback below converts to string immediately.
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Accumulate for the end-of-stream journal emit. Once the buffer is
		// full we still count totalBytes so the summary reflects the real
		// volume even though the sample is capped.
		totalBytes += int64(len(line)) + 1 // +1 for the newline the scanner strips
		if len(captureBuf) < captureCap {
			remaining := captureCap - len(captureBuf)
			if len(line) <= remaining {
				captureBuf = append(captureBuf, line...)
				if len(captureBuf) < captureCap {
					captureBuf = append(captureBuf, '\n')
				}
			} else {
				captureBuf = append(captureBuf, line[:remaining]...)
			}
		}

		if useStreamJSON {
			adapter.ParseStreamLine(line, handler)
		} else {
			if handler != nil {
				handler(AgentEvent{
					Type:      "text",
					Content:   string(line) + "\n",
					Timestamp: time.Now(),
				})
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		o.logger.Debug("scanner error", "error", err, "agent_id", req.AgentID)
	}

	// End-of-stream Crow's Nest emit. We run unconditionally (even when
	// totalBytes is 0) because an empty-output run is still interesting for
	// debugging — the UI can render "agent produced no stdout" explicitly
	// instead of showing a hanging block. Use a fresh context in case the
	// request context was cancelled (user pressed stop); we still want the
	// capture recorded for post-mortem. ExecID lives on the provider
	// ExecResult so we record it in the payload for correlation with the
	// exec.command end event.
	emitCtx := ctx
	if emitCtx.Err() != nil {
		emitCtx = context.Background()
	}
	payload := map[string]any{
		"output":      string(captureBuf),
		"total_bytes": totalBytes,
		"truncated":   totalBytes > int64(len(captureBuf)),
	}
	if result != nil && result.ExecID != "" {
		payload["exec_id"] = result.ExecID
	}
	_, _ = o.getJournal().Emit(emitCtx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        "exec.output_chunk",
		Severity:    "info",
		ActorType:   "sidecar",
		ActorID:     req.AgentID,
		Summary:     fmt.Sprintf("%s stdout+stderr capture (%d bytes)", req.AgentSlug, totalBytes),
		Payload:     payload,
		Refs:        map[string]any{"chat_id": req.ChatID},
	})
}

// emitToolResultBlock sends a tool_result event for the given content block.
func emitToolResultBlock(block contentBlock, handler EventHandler) {
	meta := map[string]interface{}{}
	if block.ToolUseID != "" {
		meta["tool_use_id"] = block.ToolUseID
	}
	handler(AgentEvent{
		Type:      "tool_result",
		Content:   block.Text,
		Metadata:  meta,
		Timestamp: time.Now(),
	})
}

// emitImageBlock sends an image event for the given content block.
func emitImageBlock(block contentBlock, handler EventHandler) {
	if block.Source != nil && block.Source.Data != "" {
		handler(AgentEvent{
			Type:    "image",
			Content: block.Source.Data,
			Metadata: map[string]interface{}{
				"media_type": block.Source.MediaType,
			},
			Timestamp: time.Now(),
		})
	}
}

// handleStreamJSONLine kept as a thin wrapper around parseClaudeCodeStreamJSON
// so existing tests in exec_test.go that call o.handleStreamJSONLine directly
// keep working unchanged. The actual per-adapter dispatch happens in
// streamOutput above via adapter.ParseStreamLine.
func (o *Orchestrator) handleStreamJSONLine(line []byte, handler EventHandler) {
	parseClaudeCodeStreamJSON(line, handler)
}
